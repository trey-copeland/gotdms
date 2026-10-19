package tdms

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"sync/atomic"
	"time"
)

// metaBuilds counts metadata encodings. A repeat Flush of an unchanged
// channel shape reuses the previous encoding and does not increment this.
var metaBuilds atomic.Int64

// metaReuse gates that cache. Benchmarks turn it off to measure the rebuild.
var metaReuse = true

// Writer appends TDMS segments to an io.Writer.
//
// Data and property changes are buffered until Flush or Close. Each flush
// emits one segment containing one chunk: every Write on a channel since the
// previous flush is concatenated into that chunk. Channels are laid out in
// the order they were created, not the order of the Write calls.
//
// A seekable writer (os.File, any io.WriteSeeker) first publishes a lead-in
// whose next-segment offset is 0xFFFFFFFFFFFFFFFF — the NI incomplete-segment
// sentinel — then patches the real length after the raw bytes are written. A
// crash in between still leaves a segment the reader can consume up to EOF.
// A writer that cannot seek writes the lead-in with the finished length, then
// the bytes, because the length is known before the raw section is streamed.
//
// Write copies values into the channel buffer. Mutating the caller's slice
// after Write returns does not change the segment. WriteNoCopy is the
// opt-in that skips that copy for a fixed-size numeric or complex slice on
// a little-endian host; the caller must not mutate that slice until Flush
// or Close returns. Bool, timestamp, and extended values are encoded into
// the channel buffer at Write. String headers are copied; string bytes are
// immutable. A big-endian host copies numeric slices at Write so they can
// be stored little-endian.
//
// Flush streams the channel buffer through a large bufio.Writer (see
// WithWriteBuffer). It does not allocate a second copy of a contiguous
// segment. Interleaved output is mixed in windows of about 1 MiB. The
// samples for a segment stay reachable until Flush returns. Stream writes
// a segment whose sample counts are declared up front, so those bytes can
// reach the underlying writer before Finish.
//
// Segments are little-endian version 4713. SetInterleaved selects the layout
// of later flushes. The writer is not safe for concurrent use.
type Writer struct {
	w        io.Writer
	bw       *bufio.Writer
	ws       io.WriteSeeker
	pos      int64
	seekable bool
	own      io.Closer
	path     string
	bufSize  int

	interleaved bool
	closed      bool
	started     bool
	stream      *Stream
	sticky      error

	metaValid bool
	metaIn    metaInput
	metaOut   []byte
	metaToc   uint32
	metaOrder []string
	metaIdx   map[string]wIndex

	root      *wObj
	groups    []*wGroup
	gindex    map[string]*wGroup
	prevOrder []string
	prevIndex map[string]wIndex
}

type wIndex struct {
	dt    DataType
	n     uint64
	total uint64
}

type wObj struct {
	path    string
	props   []Property
	dirty   bool
	emitted bool
	rev     uint64
}

type wGroup struct {
	name     string
	obj      *wObj
	channels []*wChannel
	cindex   map[string]*wChannel
}

type wChannel struct {
	group   string
	name    string
	path    string
	dt      DataType
	obj     *wObj
	raw     []byte
	aliased bool // raw aliases a caller slice; do not reslice it
	strs    []string
	n       int
}

// GroupWriter is the write handle for one group.
type GroupWriter struct {
	w *Writer
	g *wGroup
}

// ChannelWriter is the write handle for one channel.
type ChannelWriter struct {
	w  *Writer
	ch *wChannel
}

// Create creates or truncates path and returns a writer. Close flushes the
// last segment and closes the file.
func Create(path string) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, wrap("create", path, err)
	}
	w, err := NewWriter(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	w.own = f
	w.path = path
	return w, nil
}

// NewWriter writes a TDMS file to w. If w implements io.WriteSeeker the
// lead-in is patched in place; otherwise the lead-in carries the finished
// length and the raw bytes are streamed after it. Output goes through a
// buffer of DefaultWriteBuffer unless WithWriteBuffer says otherwise.
// NewWriter does not close w.
func NewWriter(w io.Writer, opts ...WriterOption) (*Writer, error) {
	if w == nil {
		return nil, wrap("write", "", fmt.Errorf("%w: nil writer", ErrArgument))
	}
	cfg := writerConfig{bufSize: DefaultWriteBuffer}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.bufSize <= 0 {
		cfg.bufSize = DefaultWriteBuffer
	}
	wr := &Writer{
		w:         w,
		bw:        bufio.NewWriterSize(w, cfg.bufSize),
		bufSize:   cfg.bufSize,
		root:      &wObj{path: "/"},
		gindex:    map[string]*wGroup{},
		prevIndex: map[string]wIndex{},
	}
	if ws, ok := w.(io.WriteSeeker); ok {
		pos, err := ws.Seek(0, io.SeekCurrent)
		if err == nil {
			wr.ws = ws
			wr.pos = pos
			wr.seekable = true
		}
	}
	return wr, nil
}

// SetInterleaved selects the raw layout used by subsequent Flush calls.
// Interleaved segments require every channel in the chunk to have the same
// number of values and a fixed-size type. A chunk that is only a single
// string channel may be interleaved; the bytes are the ordinary string
// offset table, because that is the only layout the format defines for
// strings. Several string channels, or a string mixed with numeric channels,
// return ErrLayout.
func (w *Writer) SetInterleaved(on bool) { w.interleaved = on }

// busy reports whether a buffered write or flush may proceed.
func (w *Writer) busy() error {
	if w == nil {
		return fmt.Errorf("%w: nil writer", ErrArgument)
	}
	if w.closed {
		return ErrClosed
	}
	if w.sticky != nil {
		return w.sticky
	}
	if w.stream != nil {
		return fmt.Errorf("%w: finish the open stream first", ErrArgument)
	}
	return nil
}

// SetProperty sets a file-level property. Integers that fit in int32 are
// stored as int32; larger ints are int64. time.Time is stored as a TDMS
// timestamp. The value is written on the next flush that emits metadata.
func (w *Writer) SetProperty(name string, value any) error {
	if err := w.busy(); err != nil {
		return wrap("write", "/", err)
	}
	return w.root.set(name, value)
}

// Group returns the group, creating it if needed.
func (w *Writer) Group(name string) *GroupWriter {
	g, ok := w.gindex[name]
	if !ok {
		g = &wGroup{
			name:   name,
			obj:    &wObj{path: groupPath(name)},
			cindex: map[string]*wChannel{},
		}
		w.gindex[name] = g
		w.groups = append(w.groups, g)
	}
	return &GroupWriter{w: w, g: g}
}

// Name is the group name.
func (g *GroupWriter) Name() string { return g.g.name }

// SetProperty sets a group property.
func (g *GroupWriter) SetProperty(name string, value any) error {
	if err := g.w.busy(); err != nil {
		return wrap("write", g.g.obj.path, err)
	}
	return g.g.obj.set(name, value)
}

// Channel returns the channel, creating it on first use. A second call with
// a different data type returns ErrType.
func (g *GroupWriter) Channel(name string, dt DataType) (*ChannelWriter, error) {
	if err := g.w.busy(); err != nil {
		return nil, wrap("write", channelPath(g.g.name, name), err)
	}
	if dt != TypeString && dt != TypeVoid {
		if _, ok := dt.Size(); !ok || dt == TypeFixedPoint || dt == TypeDAQmxRaw {
			return nil, wrap("write", channelPath(g.g.name, name), fmt.Errorf("%w: %s", ErrUnsupported, dt))
		}
	}
	if ch, ok := g.g.cindex[name]; ok {
		if ch.dt != dt {
			return nil, wrapf("write", ch.path, ErrType, "channel already has type %s", ch.dt)
		}
		return &ChannelWriter{w: g.w, ch: ch}, nil
	}
	ch := &wChannel{
		group: g.g.name,
		name:  name,
		path:  channelPath(g.g.name, name),
		dt:    dt,
		obj:   &wObj{path: channelPath(g.g.name, name)},
	}
	g.g.cindex[name] = ch
	g.g.channels = append(g.g.channels, ch)
	return &ChannelWriter{w: g.w, ch: ch}, nil
}

// Name is the channel name.
func (c *ChannelWriter) Name() string { return c.ch.name }

// DataType is the type given when the channel was created.
func (c *ChannelWriter) DataType() DataType { return c.ch.dt }

// SetProperty sets a channel property.
func (c *ChannelWriter) SetProperty(name string, value any) error {
	if err := c.w.busy(); err != nil {
		return wrap("write", c.ch.path, err)
	}
	return c.ch.obj.set(name, value)
}

// Write appends values to the current segment. values is a slice whose
// element type matches the channel: []int8, []int16, []int32, []int64,
// []uint8, []uint16, []uint32, []uint64, []float32, []float64, []bool,
// []string, []Timestamp or []time.Time, []Extended, []complex64, []complex128.
// Float-with-unit channels take the same slices as the underlying float.
// Repeated calls before Flush belong to one chunk.
//
// Write copies the values. Refilling the caller's slice and writing it
// again does not change bytes already buffered. Use WriteNoCopy to skip
// the copy for a fixed-size numeric or complex slice.
func (c *ChannelWriter) Write(values any) error {
	return c.writeValues(values, false)
}

// WriteNoCopy appends values without copying a fixed-size numeric or complex
// slice on a little-endian host (and without copying a one-byte slice on any
// host). The caller must not mutate that slice until Flush or Close returns.
// A later Write or WriteNoCopy copies any slice still retained so the new
// values can be appended. Bool, timestamp, extended, and string values are
// copied at the call, the same as Write. A big-endian host copies multi-byte
// numeric slices here too, so they can be stored little-endian.
func (c *ChannelWriter) WriteNoCopy(values any) error {
	return c.writeValues(values, true)
}

func (c *ChannelWriter) writeValues(values any, noCopy bool) error {
	if err := c.w.busy(); err != nil {
		return wrap("write", c.ch.path, err)
	}
	if noCopy {
		if view, n, ok := borrowedView(c.ch.dt, values); ok {
			c.ch.retainView(view, n)
			return nil
		}
	}
	if c.ch.aliased {
		owned := append([]byte(nil), c.ch.raw...)
		c.ch.raw = owned
		c.ch.aliased = false
	}
	raw, strs, n, err := appendEncoded(c.ch.raw, c.ch.dt, values)
	if err != nil {
		return wrap("write", c.ch.path, err)
	}
	c.ch.raw = raw
	c.ch.aliased = false
	c.ch.strs = append(c.ch.strs, strs...)
	c.ch.n += n
	return nil
}

// retainView keeps view as the channel's raw bytes. The first slice aliases
// the caller. A second slice is copied into an owned buffer so the previous
// view can be appended to.
func (ch *wChannel) retainView(view []byte, n int) {
	if n == 0 {
		return
	}
	if len(ch.raw) == 0 {
		ch.raw = view
		ch.aliased = true
		ch.n += n
		return
	}
	if ch.aliased {
		owned := make([]byte, len(ch.raw)+len(view))
		copy(owned, ch.raw)
		copy(owned[len(ch.raw):], view)
		ch.raw = owned
		ch.aliased = false
		ch.n += n
		return
	}
	ch.raw = append(ch.raw, view...)
	ch.n += n
}

// Flush writes the current segment. It is a no-op when nothing has changed
// and a segment has already been written.
func (w *Writer) Flush() error {
	if err := w.busy(); err != nil {
		return wrap("flush", w.path, err)
	}
	data := w.dataChannels()
	if len(data) == 0 && !w.pendingMeta() {
		if !w.started {
			return w.writeEmpty()
		}
		return nil
	}
	if w.interleaved && len(data) > 0 {
		if err := checkInterleaved(data); err != nil {
			return wrap("flush", w.path, err)
		}
	}
	rawSize, err := rawByteSize(data, w.interleaved)
	if err != nil {
		return wrap("flush", w.path, err)
	}
	meta, toc, order, indexes, err := w.buildMeta(data, rawSize > 0)
	if err != nil {
		return wrap("flush", w.path, err)
	}
	if rawSize > 0 {
		toc |= TocRawData
		if w.interleaved {
			toc |= TocInterleavedData
		}
	}
	if err := w.writeSegment(toc, meta, data, rawSize); err != nil {
		return wrap("flush", w.path, err)
	}
	w.started = true
	w.markEmitted(meta != nil || rawSize > 0)
	if rawSize > 0 {
		w.prevOrder = order
		w.prevIndex = indexes
	}
	w.resetData()
	return nil
}

// Close flushes pending data and, when Create was used, closes the file.
// An empty writer still produces a valid one-segment file that contains the
// root object.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	var err error
	if w.stream != nil {
		err = w.stream.shutdown()
	} else if w.sticky != nil {
		err = w.sticky
	} else {
		err = w.Flush()
	}
	w.closed = true
	w.stream = nil
	if w.own != nil {
		if cerr := w.own.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		return err
	}
	return nil
}

func (w *Writer) writeEmpty() error {
	meta, err := encodeObjects([]objRec{{
		path: "/",
		mode: idxNone,
	}})
	if err != nil {
		return err
	}
	toc := TocMetaData | TocNewObjList
	if err := w.writeSegment(toc, meta, nil, 0); err != nil {
		return wrap("flush", w.path, err)
	}
	w.started = true
	w.root.emitted = true
	w.root.dirty = false
	return nil
}

func (w *Writer) writeSegment(toc uint32, meta []byte, data []*wChannel, rawSize int64) error {
	if rawSize < 0 {
		return ErrCorrupt
	}
	if uint64(rawSize) > ^uint64(0)-uint64(len(meta)) {
		return fmt.Errorf("%w: segment larger than uint64", ErrArgument)
	}
	rawLen := uint64(len(meta)) + uint64(rawSize)
	if w.seekable {
		// Publish the incomplete-segment sentinel first so a crash before the
		// patch still satisfies the NI rule for the last segment. The buffer
		// is flushed before the seek so the patch lands on bytes already in
		// the underlying writer.
		lead := encodeLeadIn(toc, incompleteSegment, uint64(len(meta)))
		if _, err := w.bw.Write(lead); err != nil {
			return err
		}
		if _, err := w.bw.Write(meta); err != nil {
			return err
		}
		if err := writeRaw(w.bw, data, w.interleaved); err != nil {
			return err
		}
		if err := w.bw.Flush(); err != nil {
			return err
		}
		if _, err := w.ws.Seek(w.pos+12, io.SeekStart); err != nil {
			return err
		}
		var next [8]byte
		binary.LittleEndian.PutUint64(next[:], rawLen)
		if _, err := w.ws.Write(next[:]); err != nil {
			return err
		}
		end := w.pos + int64(leadInSize) + int64(len(meta)) + rawSize
		if _, err := w.ws.Seek(end, io.SeekStart); err != nil {
			return err
		}
		w.pos = end
		return nil
	}
	lead := encodeLeadIn(toc, rawLen, uint64(len(meta)))
	if _, err := w.bw.Write(lead); err != nil {
		return err
	}
	if _, err := w.bw.Write(meta); err != nil {
		return err
	}
	if err := writeRaw(w.bw, data, w.interleaved); err != nil {
		return err
	}
	if err := w.bw.Flush(); err != nil {
		return err
	}
	w.pos += int64(leadInSize) + int64(len(meta)) + rawSize
	return nil
}

func (w *Writer) dataChannels() []*wChannel {
	var out []*wChannel
	for _, g := range w.groups {
		for _, ch := range g.channels {
			if ch.n > 0 {
				out = append(out, ch)
			}
		}
	}
	return out
}

func (w *Writer) pendingMeta() bool {
	if w.root.dirty || (!w.root.emitted && !w.started) {
		return true
	}
	for _, g := range w.groups {
		if g.obj.dirty || !g.obj.emitted {
			return true
		}
		for _, ch := range g.channels {
			if ch.obj.dirty || !ch.obj.emitted {
				return true
			}
		}
	}
	return false
}

func (w *Writer) buildMeta(data []*wChannel, hasRaw bool) ([]byte, uint32, []string, map[string]wIndex, error) {
	if metaReuse && w.metaMatch(data, hasRaw) {
		return w.metaOut, w.metaToc, w.metaOrder, w.metaIdx, nil
	}
	snap, err := w.snapshotMeta(data, hasRaw)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	meta, toc, order, indexes, err := w.buildMetaBody(data, hasRaw)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	w.metaValid = true
	w.metaIn = snap
	w.metaOut = meta
	w.metaToc = toc
	w.metaOrder = order
	w.metaIdx = indexes
	return meta, toc, order, indexes, nil
}

func (w *Writer) buildMetaBody(data []*wChannel, hasRaw bool) ([]byte, uint32, []string, map[string]wIndex, error) {
	metaBuilds.Add(1)
	order := make([]string, len(data))
	for i, ch := range data {
		order[i] = ch.path
	}
	first := !w.started
	newList := first || !prefixExtend(w.prevOrder, order)
	// This writer does not reorder channels in a flush that has no raw
	// bytes, so that flush does not set kTocNewObjList after the file has
	// started. A file from another writer can still change the order in a
	// metadata-only segment; the reader honors that.
	if !hasRaw && !first {
		newList = false
	}
	var recs []objRec
	if first || w.root.dirty || !w.root.emitted {
		recs = append(recs, objRec{path: "/", mode: idxNone, props: w.root.props})
	}
	for _, g := range w.groups {
		if first || g.obj.dirty || !g.obj.emitted {
			recs = append(recs, objRec{path: g.obj.path, mode: idxNone, props: g.obj.props})
		}
	}
	indexes := map[string]wIndex{}
	for k, v := range w.prevIndex {
		indexes[k] = v
	}
	dataSet := map[string]*wChannel{}
	for _, ch := range data {
		dataSet[ch.path] = ch
	}
	var chans []*wChannel
	if first {
		for _, g := range w.groups {
			chans = append(chans, g.channels...)
		}
	} else if newList {
		chans = append(chans, data...)
		for _, g := range w.groups {
			for _, ch := range g.channels {
				if _, ok := dataSet[ch.path]; ok {
					continue
				}
				if ch.obj.dirty || !ch.obj.emitted {
					chans = append(chans, ch)
				}
			}
		}
	} else {
		for _, g := range w.groups {
			for _, ch := range g.channels {
				_, inData := dataSet[ch.path]
				_, had := w.prevIndex[ch.path]
				total, err := channelTotal(ch)
				if err != nil {
					return nil, 0, nil, nil, err
				}
				changed := inData && (!had || w.prevIndex[ch.path] != (wIndex{dt: ch.dt, n: uint64(ch.n), total: total}))
				if ch.obj.dirty || !ch.obj.emitted || changed || (inData && !containsStr(w.prevOrder, ch.path)) {
					chans = append(chans, ch)
				}
			}
		}
	}
	for _, ch := range chans {
		mode := idxNone
		var total uint64
		if ch.n > 0 {
			var err error
			total, err = channelTotal(ch)
			if err != nil {
				return nil, 0, nil, nil, err
			}
			prev, had := w.prevIndex[ch.path]
			cur := wIndex{dt: ch.dt, n: uint64(ch.n), total: total}
			if had && prev == cur {
				mode = idxMatch
			} else {
				mode = idxFull
			}
			indexes[ch.path] = cur
		} else if !hasRaw {
			if _, had := w.prevIndex[ch.path]; had {
				mode = idxMatch
			}
		} else {
			delete(indexes, ch.path)
		}
		props := []Property(nil)
		if ch.obj.dirty || !ch.obj.emitted {
			props = ch.obj.props
		}
		// newList must name every data channel even when the index matches
		// and the properties are unchanged, so the reader can rebuild order.
		if mode == idxMatch && len(props) == 0 && !newList {
			continue
		}
		recs = append(recs, objRec{
			path:  ch.path,
			mode:  mode,
			dt:    ch.dt,
			n:     uint64(ch.n),
			total: total,
			props: props,
		})
	}
	if hasRaw {
		// Drop indexes for channels that are no longer in the raw order.
		keep := map[string]bool{}
		for _, p := range order {
			keep[p] = true
		}
		for p := range indexes {
			if !keep[p] {
				delete(indexes, p)
			}
		}
	} else {
		indexes = nil
	}
	toc := uint32(0)
	if len(recs) > 0 {
		toc |= TocMetaData
	}
	if newList {
		toc |= TocNewObjList
	}
	if len(recs) == 0 {
		return nil, toc, order, indexes, nil
	}
	meta, err := encodeObjects(recs)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	return meta, toc, order, indexes, nil
}

func (w *Writer) markEmitted(wrote bool) {
	if !wrote {
		return
	}
	w.root.emitted = true
	w.root.dirty = false
	for _, g := range w.groups {
		g.obj.emitted = true
		g.obj.dirty = false
		for _, ch := range g.channels {
			ch.obj.emitted = true
			ch.obj.dirty = false
		}
	}
}

func (w *Writer) resetData() {
	for _, g := range w.groups {
		for _, ch := range g.channels {
			if ch.aliased {
				ch.raw = nil
				ch.aliased = false
			} else if ch.raw != nil {
				ch.raw = ch.raw[:0]
			}
			ch.strs = nil
			ch.n = 0
		}
	}
}

type objRec struct {
	path  string
	mode  int
	dt    DataType
	n     uint64
	total uint64
	props []Property
}

func encodeObjects(recs []objRec) ([]byte, error) {
	var b []byte
	b = appendU32(b, uint32(len(recs)))
	for _, r := range recs {
		b = appendU32(b, uint32(len(r.path)))
		b = append(b, r.path...)
		switch r.mode {
		case idxNone:
			b = appendU32(b, RawIndexNoData)
		case idxMatch:
			b = appendU32(b, RawIndexMatchesPrevious)
		default:
			if r.dt == TypeString {
				b = appendU32(b, 28)
				b = appendU32(b, uint32(r.dt))
				b = appendU32(b, 1)
				b = appendU64(b, r.n)
				b = appendU64(b, r.total)
			} else {
				b = appendU32(b, 20)
				b = appendU32(b, uint32(r.dt))
				b = appendU32(b, 1)
				b = appendU64(b, r.n)
			}
		}
		b = appendU32(b, uint32(len(r.props)))
		for _, p := range r.props {
			enc, err := encodeProperty(p)
			if err != nil {
				return nil, err
			}
			b = append(b, enc...)
		}
	}
	return b, nil
}

func channelTotal(ch *wChannel) (uint64, error) {
	if ch.dt != TypeString {
		return 0, nil
	}
	return stringBlockSize(ch.strs)
}

func checkInterleaved(data []*wChannel) error {
	n := data[0].n
	strings := 0
	for _, ch := range data {
		if ch.n != n {
			return fmt.Errorf("%w: channel %s has %d values, channel %s has %d", ErrLayout, data[0].path, n, ch.path, ch.n)
		}
		if ch.dt == TypeString {
			strings++
			continue
		}
		if _, ok := ch.dt.Size(); !ok {
			return fmt.Errorf("%w: %s has no fixed size", ErrLayout, ch.dt)
		}
	}
	if strings > 0 && (strings != len(data) || strings > 1) {
		return fmt.Errorf("%w: string data uses a per-channel offset table and cannot be multiplexed with other channels", ErrLayout)
	}
	return nil
}

func prefixExtend(prev, cur []string) bool {
	if len(cur) < len(prev) {
		return false
	}
	for i := range prev {
		if prev[i] != cur[i] {
			return false
		}
	}
	return true
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func (o *wObj) set(name string, value any) error {
	p, err := normalizeProp(name, value)
	if err != nil {
		return wrap("write", o.path, err)
	}
	upsert(&o.props, p)
	o.dirty = true
	o.rev++
	return nil
}

func normalizeProp(name string, value any) (Property, error) {
	switch v := value.(type) {
	case int:
		if int64(v) >= math.MinInt32 && int64(v) <= math.MaxInt32 {
			return Property{Name: name, Type: TypeInt32, Value: int32(v)}, nil
		}
		return Property{Name: name, Type: TypeInt64, Value: int64(v)}, nil
	case uint:
		if uint64(v) <= math.MaxUint32 {
			return Property{Name: name, Type: TypeUint32, Value: uint32(v)}, nil
		}
		return Property{Name: name, Type: TypeUint64, Value: uint64(v)}, nil
	case int8:
		return Property{Name: name, Type: TypeInt8, Value: v}, nil
	case int16:
		return Property{Name: name, Type: TypeInt16, Value: v}, nil
	case int32:
		return Property{Name: name, Type: TypeInt32, Value: v}, nil
	case int64:
		return Property{Name: name, Type: TypeInt64, Value: v}, nil
	case uint8:
		return Property{Name: name, Type: TypeUint8, Value: v}, nil
	case uint16:
		return Property{Name: name, Type: TypeUint16, Value: v}, nil
	case uint32:
		return Property{Name: name, Type: TypeUint32, Value: v}, nil
	case uint64:
		return Property{Name: name, Type: TypeUint64, Value: v}, nil
	case float32:
		return Property{Name: name, Type: TypeFloat32, Value: v}, nil
	case float64:
		return Property{Name: name, Type: TypeFloat64, Value: v}, nil
	case bool:
		return Property{Name: name, Type: TypeBool, Value: v}, nil
	case string:
		return Property{Name: name, Type: TypeString, Value: v}, nil
	case time.Time:
		return Property{Name: name, Type: TypeTimestamp, Value: NewTimestamp(v)}, nil
	case Timestamp:
		return Property{Name: name, Type: TypeTimestamp, Value: v}, nil
	case Extended:
		return Property{Name: name, Type: TypeExtendedFloat, Value: v}, nil
	case complex64:
		return Property{Name: name, Type: TypeComplex64, Value: v}, nil
	case complex128:
		return Property{Name: name, Type: TypeComplex128, Value: v}, nil
	default:
		return Property{}, fmt.Errorf("%w: property %q has Go type %T", ErrType, name, value)
	}
}

// metaInput is the buildMeta input that changes the encoded bytes. A repeat
// flush with the same counts, dirty flags, property revisions, and previous
// index reuses the last encoding. A property set bumps rev, so a mutation
// cannot return a stale blob. Sample-count changes miss the cache because
// the raw index contains the count.
type metaInput struct {
	hasRaw      bool
	started     bool
	rootDirty   bool
	rootEmitted bool
	rootRev     uint64
	groups      []metaGroup
	data        []metaData
	prevOrder   []string
	prev        []metaPrev
}

type metaGroup struct {
	dirty   bool
	emitted bool
	rev     uint64
	chans   []metaCh
}

type metaCh struct {
	dirty   bool
	emitted bool
	rev     uint64
	dt      DataType
}

type metaData struct {
	path  string
	dt    DataType
	n     int
	total uint64
}

type metaPrev struct {
	path string
	idx  wIndex
}

func (w *Writer) metaMatch(data []*wChannel, hasRaw bool) bool {
	if !w.metaValid {
		return false
	}
	in := &w.metaIn
	if in.hasRaw != hasRaw || in.started != w.started || in.rootDirty != w.root.dirty || in.rootEmitted != w.root.emitted || in.rootRev != w.root.rev {
		return false
	}
	if len(in.groups) != len(w.groups) || len(in.data) != len(data) || len(in.prevOrder) != len(w.prevOrder) || len(in.prev) != len(w.prevIndex) {
		return false
	}
	for i, g := range w.groups {
		mg := &in.groups[i]
		if mg.dirty != g.obj.dirty || mg.emitted != g.obj.emitted || mg.rev != g.obj.rev || len(mg.chans) != len(g.channels) {
			return false
		}
		for j, ch := range g.channels {
			c := mg.chans[j]
			if c.dirty != ch.obj.dirty || c.emitted != ch.obj.emitted || c.rev != ch.obj.rev || c.dt != ch.dt {
				return false
			}
		}
	}
	for i, ch := range data {
		total, err := channelTotal(ch)
		if err != nil || in.data[i] != (metaData{path: ch.path, dt: ch.dt, n: ch.n, total: total}) {
			return false
		}
	}
	for i := range in.prevOrder {
		if in.prevOrder[i] != w.prevOrder[i] {
			return false
		}
	}
	for _, p := range in.prev {
		v, ok := w.prevIndex[p.path]
		if !ok || v != p.idx {
			return false
		}
	}
	return true
}

func (w *Writer) snapshotMeta(data []*wChannel, hasRaw bool) (metaInput, error) {
	in := metaInput{
		hasRaw: hasRaw, started: w.started,
		rootDirty: w.root.dirty, rootEmitted: w.root.emitted, rootRev: w.root.rev,
	}
	for _, g := range w.groups {
		mg := metaGroup{dirty: g.obj.dirty, emitted: g.obj.emitted, rev: g.obj.rev}
		for _, ch := range g.channels {
			mg.chans = append(mg.chans, metaCh{dirty: ch.obj.dirty, emitted: ch.obj.emitted, rev: ch.obj.rev, dt: ch.dt})
		}
		in.groups = append(in.groups, mg)
	}
	for _, ch := range data {
		total, err := channelTotal(ch)
		if err != nil {
			return metaInput{}, err
		}
		in.data = append(in.data, metaData{path: ch.path, dt: ch.dt, n: ch.n, total: total})
	}
	in.prevOrder = append([]string(nil), w.prevOrder...)
	if len(w.prevIndex) > 0 {
		keys := make([]string, 0, len(w.prevIndex))
		for k := range w.prevIndex {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			in.prev = append(in.prev, metaPrev{path: k, idx: w.prevIndex[k]})
		}
	}
	return in, nil
}
