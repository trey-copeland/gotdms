package tdms

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"sync/atomic"
)

// segmentParses counts full segment-header walks. A sidecar hit does not
// increment it. Tests use the delta to show a later open skipped parsing.
var segmentParses atomic.Int64

// File is an open TDMS file. It is safe for concurrent value reads when the
// underlying io.ReaderAt is. Close it when finished.
type File struct {
	r       io.ReaderAt
	closer  io.Closer
	path    string
	size    int64
	version uint32
	segs    int
	readBuf int

	props  []Property
	groups []*Group
	gindex map[string]int
	byPath map[string]*Channel

	// End-of-file state. An incomplete final segment still contributes every
	// whole value it contains; these fields only describe that end.
	incomplete  bool
	partialTail bool
	unusedTail  int64
	truncated   bool // final next-segment offset ran past EOF; WithLenientTail
	lenient     bool
	indexUsed   bool
}

// Group is a named collection of channels.
type Group struct {
	file     *File
	name     string
	props    []Property
	channels []*Channel
	chIndex  map[string]int
}

// Channel is one raw-data series plus its properties.
//
// Values are concatenated across segments in file order. A channel that is
// absent from a later segment keeps the samples it already has; later segments
// append more samples when the channel reappears.
type Channel struct {
	file  *File
	group *Group
	name  string
	path  string
	dt    DataType
	hasDT bool
	props []Property
	idx   rawIndex
	spans []span
	// runs holds repeat records for spans whose runIdx is non-zero.
	// Index 0 is unused. A nil slice means every span is one segment,
	// which keeps the span itself free of pointers.
	runs  []spanRun
	total int64
	daqmx *daqmxState
}

type rawIndex struct {
	known bool
	none  bool
	dt    DataType
	n     int64
	total int64
	daqmx *daqmxSpec
}

// span is one segment's contribution to a channel.
//
// Fixed-size values are addressed as
//
//	fileOff = regionBase + col + valueIndex*stride
//
// Contiguous channels use stride == typeSize and col == offset of the channel's
// block. Interleaved channels use stride == sum of the sample widths and col ==
// the channel's offset inside the sample row.
type span struct {
	dataPos   int64
	chunkSize int64
	nChunks   int64
	nPerChunk int64
	col       int64
	stride    int64
	typeSize  int
	strSize   int64

	partialN      int64
	partialPos    int64
	partialCol    int64
	partialStride int64

	// big is the segment's endianness. str marks a length-prefixed string
	// block. digital extracts one bit from each stride. bit is that bit
	// inside the byte at col (0 is the least significant bit). solo keeps
	// the span out of the shared ReadValues window: col is an offset within
	// the whole chunk, which is how a raw buffer after the first one is
	// addressed.
	big     bool
	str     bool
	digital bool
	bit     uint8
	solo    bool

	// live is set on every span from a final segment that is not finished:
	// the NI incomplete sentinel, or a finite next-segment offset past EOF
	// recovered by WithLenientTail. Values, Slice, and Cursor still return
	// those samples. Follow skips them until a later open sees a finished
	// lead-in.
	live bool

	// runIdx is 0 when this span is one segment. Otherwise it indexes the
	// channel's run table. The index is not a pointer, so growing the span
	// slice does not take a write barrier.
	runIdx uint32
}

// spanRun is one merged run: count repeats of the span at dataPos+i*step.
type spanRun struct {
	count int64
	step  int64
}

// Open opens the TDMS file at path. Optional ReaderOptions configure the
// sequential buffer Cursor uses and, with WithSegmentIndex, a sidecar cache
// of the segment layout. Values keeps using ReadAt and ignores that buffer.
func Open(path string, opts ...ReaderOption) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, wrap("open", path, err)
	}
	tf, err := newReader(f, path, opts...)
	if err != nil {
		f.Close()
		return nil, err
	}
	tf.closer = f
	return tf, nil
}

// NewReader reads a TDMS file from r. r must be able to report its length,
// either with a Size method (bytes.Reader, io.SectionReader), as an *os.File,
// or as an io.Seeker. Metadata is parsed now; raw samples stay on r until a
// channel read. NewReader does not close r.
//
// Optional ReaderOptions configure the sequential buffer Cursor uses.
// Values keeps using ReadAt and ignores that buffer. WithSegmentIndex on a
// NewReader applies when path was recorded by Open or r is an *os.File whose
// Name is the data file; otherwise the sidecar is skipped and the file is parsed.
func NewReader(r io.ReaderAt, opts ...ReaderOption) (*File, error) {
	return newReader(r, "", opts...)
}

func newReader(r io.ReaderAt, path string, opts ...ReaderOption) (*File, error) {
	if r == nil {
		return nil, wrap("open", path, fmt.Errorf("%w: nil reader", ErrArgument))
	}
	cfg := readerConfig{bufSize: DefaultReadBuffer}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.bufSize <= 0 {
		cfg.bufSize = DefaultReadBuffer
	}
	sz, err := readerSize(r)
	if err != nil {
		return nil, wrap("open", path, err)
	}
	if sz < 0 {
		return nil, wrap("open", path, ErrCorrupt)
	}
	f := &File{
		r:       r,
		path:    path,
		size:    sz,
		readBuf: cfg.bufSize,
		lenient: cfg.lenientTail,
		gindex:  map[string]int{},
		byPath:  map[string]*Channel{},
	}
	var pre dataStat
	if cfg.indexSidecar != "" {
		// Stat the open handle, not the path, before any parse. The sidecar
		// must describe the bytes this File reads.
		pre, _ = f.statData()
		if f.trySegmentIndex(cfg.indexSidecar, cfg.indexTrustIdentity, pre) {
			return f, nil
		}
	}
	if err := f.parse(); err != nil {
		return nil, err
	}
	if cfg.indexSidecar != "" && !cfg.indexNoWrite {
		// A cache write failure leaves the open valid. The next open parses.
		_ = f.writeSegmentIndex(cfg.indexSidecar, pre)
	}
	return f, nil
}

// Close releases the file opened by Open. It is a no-op for NewReader.
// Reads after Close return ErrClosed.
func (f *File) Close() error {
	f.r = nil
	if f.closer == nil {
		return nil
	}
	err := f.closer.Close()
	f.closer = nil
	return err
}

// Version is the TDMS version of the first segment, 4712 or 4713.
func (f *File) Version() uint32 { return f.version }

// SegmentCount is the number of lead-ins parsed.
func (f *File) SegmentCount() int { return f.segs }

// Properties returns a copy of the file-level properties in file order.
func (f *File) Properties() []Property { return copyProps(f.props) }

// Property returns the file-level property name. The second result is false
// when the property was never written.
func (f *File) Property(name string) (Property, bool) { return findProp(f.props, name) }

// Groups returns groups in the order they first appear.
func (f *File) Groups() []*Group {
	out := make([]*Group, len(f.groups))
	copy(out, f.groups)
	return out
}

// Group looks up a group by its unquoted name.
func (f *File) Group(name string) (*Group, bool) {
	i, ok := f.gindex[name]
	if !ok {
		return nil, false
	}
	return f.groups[i], true
}

// Name is the group name without path quoting.
func (g *Group) Name() string { return g.name }

// Path is the TDMS object path, for example /'demo'.
func (g *Group) Path() string { return groupPath(g.name) }

// Properties returns a copy of the group's properties.
func (g *Group) Properties() []Property { return copyProps(g.props) }

// Property looks up a group property by name.
func (g *Group) Property(name string) (Property, bool) { return findProp(g.props, name) }

// Channels returns channels in the order they first appear.
func (g *Group) Channels() []*Channel {
	out := make([]*Channel, len(g.channels))
	copy(out, g.channels)
	return out
}

// Channel looks up a channel by its unquoted name.
func (g *Group) Channel(name string) (*Channel, bool) {
	i, ok := g.chIndex[name]
	if !ok {
		return nil, false
	}
	return g.channels[i], true
}

// Name is the channel name without path quoting.
func (c *Channel) Name() string { return c.name }

// GroupName is the unquoted name of the owning group.
func (c *Channel) GroupName() string { return c.group.name }

// Path is the TDMS object path, for example /'demo'/'voltage'.
func (c *Channel) Path() string { return c.path }

// DataType is the channel's raw-data type. It is TypeVoid until an index
// assigns one.
func (c *Channel) DataType() DataType { return c.dt }

// Len is the number of raw values across every segment.
func (c *Channel) Len() int { return int(c.total) }

// Properties returns a copy of the channel's properties.
func (c *Channel) Properties() []Property { return copyProps(c.props) }

// Property looks up a channel property by name.
func (c *Channel) Property(name string) (Property, bool) { return findProp(c.props, name) }

// Slice reads n raw values beginning at sample i.
//
// The concrete type matches Values. The returned slice is newly allocated.
// i and n are sample indexes, not bytes. i >= 0, n >= 0, and i+n <= Len.
//
// Contiguous channels are read with ReadAt at the byte offset of sample i.
// Interleaved channels start at the multiplexed window that contains sample
// i; the bytes before that window are not read. String channels read the
// offset-table entries that bound [i, i+n) and copy only those string bytes.
// Earlier chunks are not decoded. A string window does not re-check that the
// offset table consumes the whole chunk; Values still does, for a full chunk.
func (c *Channel) Slice(i, n int) (any, error) {
	if c == nil {
		return nil, wrap("read", "", fmt.Errorf("%w: nil channel", ErrArgument))
	}
	return c.readRange(i, n)
}

// Values reads every raw value. The concrete type is:
//
//	TypeInt8 []int8, TypeInt16 []int16, TypeInt32 []int32, TypeInt64 []int64,
//	TypeUint8 []uint8, TypeUint16 []uint16, TypeUint32 []uint32, TypeUint64 []uint64,
//	TypeFloat32 []float32, TypeFloat64 []float64,
//	TypeFloat32WithUnit []float32, TypeFloat64WithUnit []float64,
//	TypeExtendedFloat and TypeExtendedFloatWithUnit []Extended,
//	TypeBool []bool, TypeString []string, TypeTimestamp []Timestamp,
//	TypeComplex64 []complex64, TypeComplex128 []complex128.
//
// A channel that never received a raw-data index returns nil, nil.
// The returned slice is newly allocated.
func (c *Channel) Values() (any, error) {
	return c.readRange(0, c.Len())
}

// Cursor returns a batching reader over the whole channel. batch is the
// maximum number of values Next yields; non-positive batch selects 4096.
// The slices returned by Values are valid until the next Next call.
// Close returns the sequential buffer if the scan stops early.
func (c *Channel) Cursor(batch int) *Cursor {
	if batch <= 0 {
		batch = 4096
	}
	return &Cursor{ch: c, batch: batch, end: -1}
}

// CursorAt returns a Cursor over the half-open sample range [i, i+n).
// batch is the maximum number of values Next yields; non-positive batch
// selects 4096. Seeking matches Slice: contiguous channels ReadAt at sample
// i, and interleaved channels start at that sample's multiplexed window.
// A range that does not fit in the channel fails on the first Next.
func (c *Channel) CursorAt(i, n, batch int) *Cursor {
	cur := c.Cursor(batch)
	if c == nil {
		cur.err = wrap("read", "", fmt.Errorf("%w: nil channel", ErrArgument))
		cur.end = 0
		return cur
	}
	if i < 0 || n < 0 || int64(i) > c.total || int64(n) > c.total-int64(i) {
		cur.err = wrap("read", c.path, fmt.Errorf("%w: range %d:%d of %d", ErrArgument, i, n, c.total))
		cur.end = 0
		cur.pos = 0
		return cur
	}
	cur.pos = i
	cur.end = i + n
	return cur
}

// Cursor streams one channel in batches without retaining earlier batches.
//
// Next reads a contiguous fixed-size batch of at least 32 KiB with ReadAt
// straight into the reused slice. Smaller batches, and interleaved or
// non-native channels, go through a sequential buffer (see WithReadBuffer)
// so a scan does not issue a ReadAt per batch. The slice returned by Values
// is valid until the next Next call; the cursor reuses its backing array.
// Close returns that buffer early. Concurrent Next calls on one cursor are
// not safe. Concurrent cursors on one file are safe when the underlying
// ReaderAt is.
//
// To timestamp batches as a waveform, take WaveformHeader once and call
// TimeAt with the absolute sample index. Waveform loads the whole channel.
type Cursor struct {
	ch    *Channel
	batch int
	pos   int
	end   int // exclusive end; negative means the channel length
	cur   any
	err   error
	seq   *seqReader
}

// Next loads the next batch. It returns false at the end or after an error.
// Err distinguishes those two.
func (cur *Cursor) Next() bool {
	if cur.err != nil || cur.ch == nil {
		return false
	}
	lim := cur.limit()
	if cur.pos >= lim {
		cur.cur = nil
		cur.release()
		return false
	}
	n := cur.batch
	if cur.pos+n > lim {
		n = lim - cur.pos
	}
	r, err := cur.reader()
	if err != nil {
		cur.err = wrap("read", cur.ch.path, err)
		cur.cur = nil
		cur.release()
		return false
	}
	if err := cur.refill(r, n); err != nil {
		cur.err = wrap("read", cur.ch.path, err)
		cur.cur = nil
		cur.release()
		return false
	}
	cur.pos += n
	if cur.pos >= lim {
		cur.release()
	}
	return true
}

func (cur *Cursor) limit() int {
	if cur.end < 0 {
		if cur.ch == nil {
			return 0
		}
		return cur.ch.Len()
	}
	return cur.end
}

func (cur *Cursor) reader() (io.ReaderAt, error) {
	if cur.ch.file == nil || cur.ch.file.r == nil {
		return nil, ErrClosed
	}
	if cur.directRead() {
		return cur.ch.file.r, nil
	}
	if cur.seq == nil {
		cur.seq = newSeqReader(cur.ch.file.r, cur.ch.file.readBuf)
	}
	return cur.seq, nil
}

// directRead reports whether this cursor should ReadAt into the batch.
// The decision uses the configured batch, so a short final batch follows
// the same path as the rest of the scan.
func (cur *Cursor) directRead() bool {
	if contigDirect == 0 || cur.ch == nil || !cur.ch.contiguousBytes() || !fixedNativeDT(cur.ch.dt) {
		return false
	}
	if contigDirect == 2 {
		return true
	}
	sz, ok := cur.ch.dt.Size()
	if !ok || sz <= 0 || cur.batch <= 0 || cur.batch > math.MaxInt/sz {
		return false
	}
	nbytes := cur.batch * sz
	buf := DefaultReadBuffer
	if cur.ch.file != nil && cur.ch.file.readBuf > 0 {
		buf = cur.ch.file.readBuf
	}
	// A batch at least as large as the sequential buffer would skip it and
	// still pay for the allocation. Smaller batches won against the default
	// 256 KiB buffer once they reached 32 KiB; a larger custom buffer keeps
	// those batches on the buffered path.
	if nbytes >= buf {
		return true
	}
	return buf <= DefaultReadBuffer && nbytes >= cursorDirectMin
}

func (cur *Cursor) release() {
	if cur.seq != nil {
		cur.seq.release()
		cur.seq = nil
	}
}

// Close returns the cursor's sequential buffer to the pool. Next after Close
// reports the end of the scan. Close is safe to call more than once. A scan
// that runs to completion, and a Next that fails, also return the buffer.
// Dropping a cursor without Close still returns a pooled buffer when the
// cursor is garbage-collected.
func (cur *Cursor) Close() {
	if cur == nil {
		return
	}
	cur.release()
	if cur.ch != nil && cur.err == nil {
		cur.pos = cur.limit()
	}
	cur.cur = nil
}

// Err returns the error from the last Next, or nil.
func (cur *Cursor) Err() error { return cur.err }

// Values returns the current batch as documented on Channel.Values.
func (cur *Cursor) Values() any { return cur.cur }

// Len is the current batch length.
func (cur *Cursor) Len() int { return sliceLen(cur.cur) }

// Float64s converts the current batch with the same rules as Channel.Float64s.
//
// On a float64 channel the result is the cursor's reused batch. That slice
// aliases Values and is overwritten by the next Next, and cleared by Close.
// It is not safe to retain. Use Float64sCopy to keep an independent copy.
func (cur *Cursor) Float64s() ([]float64, error) {
	if cur.cur == nil {
		return nil, wrap("read", cur.ch.path, ErrArgument)
	}
	return toFloat64s(cur.cur)
}

// Float64sCopy returns an independent []float64 of the current batch.
// The result stays valid after Next and Close. On a float64 channel this
// copies the reused batch; other numeric channels already allocate in
// Float64s, and Copy returns that new slice (it does not alias the batch).
func (cur *Cursor) Float64sCopy() ([]float64, error) {
	if cur == nil || cur.cur == nil {
		path := ""
		if cur != nil && cur.ch != nil {
			path = cur.ch.path
		}
		return nil, wrap("read", path, ErrArgument)
	}
	if s, ok := cur.cur.([]float64); ok {
		out := make([]float64, len(s))
		copy(out, s)
		return out, nil
	}
	return toFloat64s(cur.cur)
}

func (c *Channel) readRange(start, n int) (any, error) {
	if c.file == nil || c.file.r == nil {
		return nil, wrap("read", c.path, ErrClosed)
	}
	return c.readRangeFrom(c.file.r, start, n, nil)
}

func (c *Channel) readRangeFrom(r io.ReaderAt, start, n int, reuse any) (any, error) {
	if start < 0 || n < 0 || int64(start) > c.total || int64(n) > c.total-int64(start) {
		return nil, wrap("read", c.path, fmt.Errorf("%w: range %d:%d of %d", ErrArgument, start, n, c.total))
	}
	if !c.hasDT {
		if n == 0 {
			return nil, nil
		}
		return nil, wrap("read", c.path, ErrCorrupt)
	}
	if c.dt == TypeString {
		if n == 0 {
			return []string{}, nil
		}
		s, err := c.readStrings(r, start, n)
		if err != nil {
			return nil, wrap("read", c.path, err)
		}
		return s, nil
	}
	return readTyped(r, c.path, c.dt, c.hasDT, c.spans, c.runs, c.total, start, n, reuse)
}

func readTyped(r io.ReaderAt, path string, dt DataType, hasDT bool, spans []span, runs []spanRun, total int64, start, n int, reuse any) (any, error) {
	if start < 0 || n < 0 || int64(start) > total || int64(n) > total-int64(start) {
		return nil, wrap("read", path, fmt.Errorf("%w: range %d:%d of %d", ErrArgument, start, n, total))
	}
	if !hasDT {
		if n == 0 {
			return nil, nil
		}
		return nil, wrap("read", path, ErrCorrupt)
	}
	if n == 0 {
		v, err := emptyOf(dt)
		return v, wrap("read", path, err)
	}
	if v, ok, err := readFastSpans(dt, spans, runs, r, start, n, reuse); ok || err != nil {
		if err != nil {
			return nil, wrap("read", path, err)
		}
		return v, nil
	}
	sz, ok := dt.Size()
	if !ok {
		return nil, wrap("read", path, fmt.Errorf("%w: %s", ErrUnsupported, dt))
	}
	buf := make([]byte, n*sz)
	if err := fillPackedSpans(spans, runs, dt, r, start, n, sz, buf); err != nil {
		return nil, wrap("read", path, err)
	}
	v, err := decodePacked(dt, buf)
	if err != nil {
		return nil, wrap("read", path, err)
	}
	return v, nil
}

func (f *File) parse() error {
	segmentParses.Add(1)
	if f.size == 0 {
		return wrap("read", f.path, ErrTruncated)
	}
	var order []string
	haveOrder := false
	pos := int64(0)
	for pos < f.size {
		if f.size-pos < leadInSize {
			return wrapf("read", f.path, ErrTruncated, "%d trailing bytes", f.size-pos)
		}
		var hdr [leadInSize]byte
		if err := readFull(f.r, hdr[:], pos); err != nil {
			return wrapf("read", f.path, err, "lead-in at %d", pos)
		}
		lead, err := decodeLeadIn(hdr[:])
		if err != nil {
			return wrapf("read", f.path, err, "segment at %d", pos)
		}
		if lead.version != Version10 && lead.version != Version20 {
			return wrapf("read", f.path, ErrVersion, "segment at %d version %d", pos, lead.version)
		}
		if f.segs == 0 {
			f.version = lead.version
		}
		segEnd, dataPos, truncated, err := segmentBounds(pos, f.size, lead, f.lenient)
		if err != nil {
			return wrapf("read", f.path, err, "segment at %d", pos)
		}
		hasMeta := lead.toc&TocMetaData != 0
		if !hasMeta && lead.raw != 0 {
			return wrapf("read", f.path, ErrCorrupt, "segment at %d has raw-data offset %d without kTocMetaData", pos, lead.raw)
		}
		if !hasMeta && !haveOrder {
			return wrapf("read", f.path, ErrCorrupt, "first segment has no metadata")
		}
		rawLen := segEnd - dataPos
		hasRaw := rawLen > 0
		if lead.toc&TocRawData == 0 && hasRaw {
			return wrapf("read", f.path, ErrCorrupt, "segment at %d has %d raw bytes without kTocRawData", pos, rawLen)
		}
		big := lead.toc&TocBigEndian != 0
		newList := lead.toc&TocNewObjList != 0
		if hasMeta {
			meta := make([]byte, lead.raw)
			if lead.raw > 0 {
				if err := readFull(f.r, meta, pos+leadInSize); err != nil {
					return wrapf("read", f.path, err, "metadata at %d", pos+leadInSize)
				}
			}
			order, err = f.readMeta(meta, order, haveOrder, newList, hasRaw, big)
			if err != nil {
				return err
			}
			haveOrder = true
		}
		if hasRaw {
			slots, err := f.slotsFrom(order)
			if err != nil {
				return wrapf("read", f.path, err, "segment at %d", pos)
			}
			interleaved := lead.toc&TocInterleavedData != 0
			live := lead.next == incompleteSegment || truncated
			if _, err := f.addSpans(slots, dataPos, rawLen, interleaved, big, live); err != nil {
				return wrapf("read", f.path, err, "segment at %d", pos)
			}
		}
		f.segs++
		if lead.next == incompleteSegment {
			f.incomplete = true
			break
		}
		if truncated {
			f.truncated = true
			break
		}
		pos = segEnd
	}
	// appendSpan already merged each channel as segments arrived. A second
	// walk here is a no-op on a uniform file and a full failed-merge scan
	// on a file whose sample counts never repeat.
	return nil
}

func segmentBounds(pos, size int64, lead leadIn, lenient bool) (segEnd, dataPos int64, truncated bool, err error) {
	if lead.raw > math.MaxInt64 {
		return 0, 0, false, ErrCorrupt
	}
	dataPos, err = add64(pos, leadInSize)
	if err != nil {
		return 0, 0, false, err
	}
	dataPos, err = add64(dataPos, int64(lead.raw))
	if err != nil {
		return 0, 0, false, err
	}
	if lead.next == incompleteSegment {
		if dataPos > size {
			return 0, 0, false, ErrTruncated
		}
		return size, dataPos, false, nil
	}
	if lead.next > math.MaxInt64 {
		return 0, 0, false, ErrCorrupt
	}
	segEnd, err = add64(pos, leadInSize)
	if err != nil {
		return 0, 0, false, err
	}
	segEnd, err = add64(segEnd, int64(lead.next))
	if err != nil {
		return 0, 0, false, err
	}
	if dataPos > segEnd {
		return 0, 0, false, ErrTruncated
	}
	if segEnd > size {
		// Metadata is in the file and the declared raw section runs past EOF.
		// WithLenientTail keeps earlier segments and the whole values that
		// fit. The default still rejects the file.
		if lenient && dataPos <= size {
			return size, dataPos, true, nil
		}
		return 0, 0, false, ErrTruncated
	}
	return segEnd, dataPos, false, nil
}

const (
	idxNone = iota
	idxMatch
	idxFull
	idxDAQmx
)

func (f *File) readMeta(meta []byte, order []string, haveOrder, newList, hasRaw, big bool) ([]string, error) {
	var bo binary.ByteOrder = binary.LittleEndian
	if big {
		bo = binary.BigEndian
	}
	d := decoder{b: meta, bo: bo}
	if len(meta) == 0 {
		if newList && hasRaw {
			return []string{}, nil
		}
		return order, nil
	}
	nobj, err := d.u32()
	if err != nil {
		return nil, wrap("read", f.path, err)
	}
	if uint64(nobj) > uint64(len(meta)) {
		return nil, wrap("read", f.path, ErrCorrupt)
	}
	// The raw order is updated whether or not this segment carries raw bytes.
	// A metadata-only segment that declares an index, or that drops a channel,
	// is the order a following raw-only segment reuses. npTDMS does the same.
	next := order
	if newList || !haveOrder {
		next = nil
	} else {
		next = append([]string(nil), order...)
	}
	removed := map[string]bool{}
	inNext := map[string]bool{}
	for _, p := range next {
		inNext[p] = true
	}
	seen := map[string]bool{}
	for i := uint32(0); i < nobj; i++ {
		opath, err := d.str()
		if err != nil {
			return nil, wrap("read", f.path, err)
		}
		if seen[opath] {
			return nil, wrapf("read", opath, ErrCorrupt, "duplicate object in segment")
		}
		seen[opath] = true
		mode, body, spec, err := d.rawHeader()
		if err != nil {
			return nil, wrap("read", opath, err)
		}
		kind, group, channel, err := parseObjectPath(opath)
		if err != nil {
			return nil, wrapf("read", opath, err, "object path")
		}
		var ch *Channel
		switch kind {
		case kindFile:
			if mode == idxFull {
				return nil, wrap("read", opath, fmt.Errorf("%w: file object has raw data", ErrCorrupt))
			}
		case kindGroup:
			if mode == idxFull {
				return nil, wrap("read", opath, fmt.Errorf("%w: group object has raw data", ErrCorrupt))
			}
			f.ensureGroup(group)
		case kindChannel:
			ch = f.ensureChannel(group, channel)
		}
		if mode == idxFull {
			dt, n, total, err := parseFullIndex(body, bo)
			if err != nil {
				return nil, wrap("read", opath, err)
			}
			if ch == nil {
				return nil, wrap("read", opath, fmt.Errorf("%w: raw data on non-channel", ErrCorrupt))
			}
			if ch.idx.daqmx != nil || ch.daqmx != nil {
				return nil, wrap("read", opath, fmt.Errorf("%w: channel changed from DAQmx raw data to a standard index", ErrCorrupt))
			}
			if ch.hasDT && ch.dt != dt {
				return nil, wrapf("read", opath, ErrCorrupt, "data type changed from %s to %s", ch.dt, dt)
			}
			ch.dt = dt
			ch.hasDT = true
			// The index persists even when this segment has no raw bytes, so a
			// later raw-only segment can use it.
			ch.idx = rawIndex{known: true, dt: dt, n: n, total: total}
		} else if mode == idxDAQmx {
			if ch == nil {
				return nil, wrap("read", opath, fmt.Errorf("%w: raw data on non-channel", ErrCorrupt))
			}
			if ch.idx.known && ch.idx.daqmx == nil && ch.hasDT {
				return nil, wrap("read", opath, fmt.Errorf("%w: channel changed from a standard index to DAQmx raw data", ErrCorrupt))
			}
			if ch.daqmx != nil && !sameDAQmxScalers(ch.daqmx.spec, spec) {
				return nil, wrap("read", opath, fmt.Errorf("%w: DAQmx scalers changed (count, scale id, or type)", ErrCorrupt))
			}
			dt := spec.scalers[spec.primary].dt
			if ch.hasDT && ch.dt != dt {
				return nil, wrapf("read", opath, ErrCorrupt, "data type changed from %s to %s", ch.dt, dt)
			}
			ch.dt = dt
			ch.hasDT = true
			ch.idx = rawIndex{known: true, dt: dt, n: spec.n, daqmx: spec}
			if ch.daqmx == nil {
				ch.daqmx = newDAQmxState(spec)
			} else {
				ch.daqmx.spec = spec
			}
		} else if mode == idxMatch {
			if ch != nil {
				if !ch.idx.known {
					return nil, wrap("read", opath, fmt.Errorf("%w: raw index matches previous segment but there is none", ErrCorrupt))
				}
				// Matches-previous resumes the stored index. A prior 0xFFFFFFFF
				// does not wipe that index.
				ch.idx.none = false
			}
		} else if ch != nil {
			// 0xFFFFFFFF: this channel has no data in the current raw order.
			// Keep dt/n/total so a later matches-previous can put it back.
			ch.idx.none = true
		}
		nprops, err := d.u32()
		if err != nil {
			return nil, wrap("read", opath, err)
		}
		for p := uint32(0); p < nprops; p++ {
			prop, err := decodeProperty(&d)
			if err != nil {
				return nil, wrap("read", opath, err)
			}
			switch kind {
			case kindFile:
				upsert(&f.props, prop)
			case kindGroup:
				g := f.ensureGroup(group)
				upsert(&g.props, prop)
			case kindChannel:
				upsert(&ch.props, prop)
			}
		}
		if ch == nil {
			continue
		}
		switch {
		case ch.idx.none || !ch.idx.known:
			removed[ch.path] = true
			delete(inNext, ch.path)
		default:
			delete(removed, ch.path)
			if !inNext[ch.path] {
				next = append(next, ch.path)
				inNext[ch.path] = true
			}
		}
	}
	// Bytes after the last declared object are not an error. The raw-data
	// offset in the lead-in, not the end of the object list, is where raw
	// data starts (NI's format article; npTDMS and other readers seek there
	// and never look at the gap). Captures from NI DAQ hardware carry a gap
	// of a few KiB here, which npTDMS reads cleanly. The gap is skipped
	// unread, whatever it holds.
	compact := next[:0]
	for _, p := range next {
		if !removed[p] {
			compact = append(compact, p)
		}
	}
	return compact, nil
}

func (d *decoder) rawHeader() (int, []byte, *daqmxSpec, error) {
	h, err := d.u32()
	if err != nil {
		return 0, nil, nil, err
	}
	switch h {
	case RawIndexNoData:
		return idxNone, nil, nil, nil
	case RawIndexMatchesPrevious:
		return idxMatch, nil, nil, nil
	case RawIndexDAQmxFormatChanging, RawIndexDAQmxDigitalLine, RawIndexDAQmxDigitalObserved:
		spec, err := d.daqmxIndex(h)
		if err != nil {
			return 0, nil, nil, err
		}
		return idxDAQmx, nil, spec, nil
	}
	if h < 20 {
		return 0, nil, nil, fmt.Errorf("%w: raw-data index length %d", ErrCorrupt, h)
	}
	body, err := d.bytes(int(h - 4))
	if err != nil {
		return 0, nil, nil, err
	}
	return idxFull, body, nil, nil
}

func parseFullIndex(body []byte, bo binary.ByteOrder) (DataType, int64, int64, error) {
	if len(body) < 16 {
		return 0, 0, 0, fmt.Errorf("%w: raw-data index shorter than 20 bytes", ErrCorrupt)
	}
	d := decoder{b: body, bo: bo}
	code, err := d.u32()
	if err != nil {
		return 0, 0, 0, err
	}
	dim, err := d.u32()
	if err != nil {
		return 0, 0, 0, err
	}
	if dim != 1 {
		return 0, 0, 0, fmt.Errorf("%w: array dimension %d, TDMS 2.0 requires 1", ErrUnsupported, dim)
	}
	count, err := d.u64()
	if err != nil {
		return 0, 0, 0, err
	}
	n, err := u64ToI64(count)
	if err != nil {
		return 0, 0, 0, err
	}
	dt := DataType(code)
	switch dt {
	case TypeDAQmxRaw:
		return 0, 0, 0, ErrDAQmx
	case TypeFixedPoint:
		return 0, 0, 0, fmt.Errorf("%w: %s", ErrUnsupported, dt)
	case TypeVoid:
		if n != 0 {
			return 0, 0, 0, fmt.Errorf("%w: void channel has %d values", ErrCorrupt, n)
		}
		return dt, 0, 0, nil
	case TypeString:
		if len(body) < 24 {
			return 0, 0, 0, fmt.Errorf("%w: string index missing total size", ErrCorrupt)
		}
		tot, err := d.u64()
		if err != nil {
			return 0, 0, 0, err
		}
		total, err := u64ToI64(tot)
		if err != nil {
			return 0, 0, 0, err
		}
		if n > 0 && uint64(total)/4 < uint64(n) {
			return 0, 0, 0, fmt.Errorf("%w: string total size %d too small for %d offsets", ErrCorrupt, total, n)
		}
		// Offsets are uint32. A table past 4 GiB, or a payload past 4 GiB,
		// cannot be addressed and is rejected before any buffer is allocated.
		if n > int64(math.MaxUint32/4) {
			return 0, 0, 0, fmt.Errorf("%w: string offset table longer than 4 GiB", ErrCorrupt)
		}
		table := uint64(n) * 4
		if uint64(total) >= table && uint64(total)-table > math.MaxUint32 {
			return 0, 0, 0, fmt.Errorf("%w: string block longer than 4 GiB", ErrCorrupt)
		}
		return dt, n, total, nil
	default:
		sz, ok := dt.Size()
		if !ok {
			return 0, 0, 0, fmt.Errorf("%w: %s", ErrUnsupported, dt)
		}
		if _, err := mul64(n, int64(sz)); err != nil {
			return 0, 0, 0, err
		}
		return dt, n, 0, nil
	}
}

func (f *File) slotsFrom(order []string) ([]rawSlot, error) {
	slots := make([]rawSlot, 0, len(order))
	for _, p := range order {
		ch := f.byPath[p]
		if ch == nil || !ch.idx.known || ch.idx.none {
			return nil, fmt.Errorf("%w: channel %s is in the raw order without an index", ErrCorrupt, p)
		}
		slots = append(slots, rawSlot{ch: ch, dt: ch.idx.dt, n: ch.idx.n, total: ch.idx.total, daqmx: ch.idx.daqmx})
	}
	return slots, nil
}

type rawSlot struct {
	ch    *Channel
	dt    DataType
	n     int64
	total int64
	daqmx *daqmxSpec
}

// addSpans lays out one segment's raw data. The raw-data index words decide
// the layout, not kTocDAQmxRawData: npTDMS reads standard channels in a
// segment that sets that bit the same way, and DAQmx indexes without it.
func (f *File) addSpans(slots []rawSlot, dataPos, rawLen int64, interleaved, big, incomplete bool) (unused int64, err error) {
	daqN := 0
	for _, s := range slots {
		if s.daqmx != nil {
			daqN++
		}
	}
	if daqN > 0 && daqN != len(slots) {
		return 0, fmt.Errorf("%w: segment mixes DAQmx and standard channels", ErrDAQmx)
	}
	if daqN > 0 {
		return f.addDAQmxSpans(slots, dataPos, rawLen, big, incomplete)
	}
	if len(slots) == 0 {
		if rawLen != 0 {
			return 0, fmt.Errorf("%w: %d raw bytes but no channels", ErrCorrupt, rawLen)
		}
		return 0, nil
	}
	interleaved, err = normalizeLayout(slots, interleaved)
	if err != nil {
		return 0, err
	}
	cols := make([]int64, len(slots))
	sizes := make([]int64, len(slots))
	var chunk int64
	var stride int64
	for i, s := range slots {
		if s.n > int64(math.MaxInt) {
			return 0, fmt.Errorf("%w: chunk has more values than int", ErrUnsupported)
		}
		ds, sz, err := slotBytes(s)
		if err != nil {
			return 0, err
		}
		sizes[i] = ds
		if interleaved {
			cols[i] = stride
			stride, err = add64(stride, int64(sz))
			if err != nil {
				return 0, err
			}
		} else {
			cols[i] = chunk
		}
		chunk, err = add64(chunk, ds)
		if err != nil {
			return 0, err
		}
	}
	if chunk == 0 {
		if rawLen != 0 {
			return 0, fmt.Errorf("%w: empty chunk size with %d raw bytes", ErrCorrupt, rawLen)
		}
		return 0, nil
	}
	if rawLen%chunk != 0 && !incomplete {
		return 0, fmt.Errorf("%w: raw length %d is not a multiple of chunk size %d", ErrCorrupt, rawLen, chunk)
	}
	nChunks := rawLen / chunk
	rem := rawLen % chunk
	partialN := make([]int64, len(slots))
	partialPos := make([]int64, len(slots))
	if rem > 0 {
		base, err := add64(dataPos, mustMul(nChunks, chunk))
		if err != nil {
			return 0, err
		}
		if interleaved {
			if stride <= 0 {
				return 0, ErrCorrupt
			}
			extra := rem / stride
			unused = rem % stride
			for i := range slots {
				partialN[i] = extra
				partialPos[i] = base
			}
		} else {
			left := rem
			at := base
			for i, s := range slots {
				if s.dt == TypeString {
					if sizes[i] == 0 || left < sizes[i] {
						break
					}
					partialN[i] = s.n
					partialPos[i] = at
					at += sizes[i]
					left -= sizes[i]
					continue
				}
				sz, ok := s.dt.Size()
				if !ok || sz == 0 {
					break
				}
				can := left / int64(sz)
				if can > s.n {
					can = s.n
				}
				partialN[i] = can
				partialPos[i] = at
				used := can * int64(sz)
				at += used
				left -= used
				if can < s.n {
					break
				}
			}
			unused = left
		}
	}
	for i, s := range slots {
		sp := span{
			dataPos:    dataPos,
			chunkSize:  chunk,
			nChunks:    nChunks,
			nPerChunk:  s.n,
			col:        cols[i],
			typeSize:   0,
			big:        big,
			str:        s.dt == TypeString,
			strSize:    sizes[i],
			partialN:   partialN[i],
			partialPos: partialPos[i],
			live:       incomplete,
		}
		if sp.str {
			// String blocks are contiguous per channel even when a single
			// string channel carries kTocInterleavedData.
		} else {
			sz, _ := s.dt.Size()
			sp.typeSize = sz
			if interleaved {
				sp.stride = stride
				sp.partialCol = cols[i]
				sp.partialStride = stride
			} else {
				sp.stride = int64(sz)
				sp.partialStride = int64(sz)
			}
		}
		full, err := mul64(nChunks, s.n)
		if err != nil {
			return 0, err
		}
		add, err := add64(full, partialN[i])
		if err != nil {
			return 0, err
		}
		s.ch.total, err = add64(s.ch.total, add)
		if err != nil {
			return 0, err
		}
		s.ch.spans, s.ch.runs = appendSpan(s.ch.spans, s.ch.runs, sp)
	}
	if incomplete {
		f.unusedTail = unused
		f.partialTail = rem > 0
	}
	return unused, nil
}

// mustMul is a*b for products the caller has already bounded, such as a
// chunk index times a chunk size inside a validated raw-data length. An
// overflow there is a bug in this package, not a bad file, so it panics
// instead of returning a value that would address the wrong bytes.
func mustMul(a, b int64) int64 {
	v, err := mul64(a, b)
	if err != nil {
		panic(fmt.Sprintf("tdms: internal error: %d * %d overflows int64", a, b))
	}
	return v
}

func slotBytes(s rawSlot) (int64, int, error) {
	if s.dt == TypeString {
		if s.n > 0 && uint64(s.total)/4 < uint64(s.n) {
			return 0, 0, ErrCorrupt
		}
		return s.total, 0, nil
	}
	if s.dt == TypeVoid || s.n == 0 {
		return 0, 0, nil
	}
	sz, ok := s.dt.Size()
	if !ok {
		return 0, 0, fmt.Errorf("%w: %s", ErrUnsupported, s.dt)
	}
	ds, err := mul64(s.n, int64(sz))
	return ds, sz, err
}

// normalizeLayout checks the interleaved flag. A single string channel written
// with the flag set is read as a contiguous offset table; the NI string layout
// has no per-sample interleave. Mixed string/fixed or several string channels
// with the flag set return ErrLayout.
func normalizeLayout(slots []rawSlot, interleaved bool) (bool, error) {
	if !interleaved {
		return false, nil
	}
	var nref int64 = -1
	unsized, sized := 0, 0
	for _, s := range slots {
		if s.n == 0 {
			continue
		}
		if nref < 0 {
			nref = s.n
		} else if s.n != nref {
			return false, fmt.Errorf("%w: interleaved channels have different lengths", ErrLayout)
		}
		if s.dt == TypeString {
			unsized++
		} else {
			sized++
		}
	}
	if unsized > 0 && (sized > 0 || unsized > 1) {
		return false, fmt.Errorf("%w: string channels are not byte-interleaved; the offset table is per channel", ErrLayout)
	}
	if unsized == 1 && sized == 0 {
		return false, nil
	}
	return true, nil
}

func (f *File) ensureGroup(name string) *Group {
	if i, ok := f.gindex[name]; ok {
		return f.groups[i]
	}
	g := &Group{file: f, name: name, chIndex: map[string]int{}}
	f.gindex[name] = len(f.groups)
	f.groups = append(f.groups, g)
	return g
}

func (f *File) ensureChannel(group, name string) *Channel {
	g := f.ensureGroup(group)
	if i, ok := g.chIndex[name]; ok {
		return g.channels[i]
	}
	ch := &Channel{
		file:  f,
		group: g,
		name:  name,
		path:  channelPath(group, name),
	}
	g.chIndex[name] = len(g.channels)
	g.channels = append(g.channels, ch)
	f.byPath[ch.path] = ch
	return ch
}

func (c *Channel) readPacked(r io.ReaderAt, start, n, size int) ([]byte, error) {
	dst := make([]byte, n*size)
	if err := c.fillPacked(r, start, n, size, dst); err != nil {
		return nil, err
	}
	return dst, nil
}

// copyRun reads n samples of a repeated span without expanding it into one
// span per segment. Repeat i starts at dataPos + i*runStep.
func (sp *span) copyRun(runs []spanRun, r io.ReaderAt, start, n int64, dst []byte) error {
	if n == 0 {
		return nil
	}
	if sp.runIdx == 0 || int(sp.runIdx) >= len(runs) || runs[sp.runIdx].count <= 1 || runs[sp.runIdx].step <= 0 || sp.partialN != 0 || sp.str || sp.nPerChunk <= 0 || sp.typeSize <= 0 {
		return ErrCorrupt
	}
	per, err := mul64(sp.nChunks, sp.nPerChunk)
	if err != nil || per <= 0 {
		return ErrCorrupt
	}
	one := *sp
	one.runIdx = 0
	left := n
	idx := start
	dstOff := 0
	for left > 0 {
		rep := idx / per
		within := idx % per
		if rep < 0 || rep >= runs[sp.runIdx].count {
			return ErrCorrupt
		}
		avail := per - within
		if avail > left {
			avail = left
		}
		shift, err := mul64(rep, runs[sp.runIdx].step)
		if err != nil {
			return err
		}
		pos, err := add64(sp.dataPos, shift)
		if err != nil {
			return err
		}
		one.dataPos = pos
		nbytes := int(avail) * sp.typeSize
		if nbytes < 0 || dstOff > len(dst) || nbytes > len(dst)-dstOff {
			return ErrCorrupt
		}
		if err := one.copyValues(nil, r, within, avail, dst[dstOff:dstOff+nbytes]); err != nil {
			return err
		}
		idx += avail
		left -= avail
		dstOff += nbytes
	}
	return nil
}

func (sp *span) copyValues(runs []spanRun, r io.ReaderAt, start, n int64, dst []byte) error {
	if sp.runIdx != 0 {
		return sp.copyRun(runs, r, start, n, dst)
	}
	full, err := mul64(sp.nChunks, sp.nPerChunk)
	if err != nil {
		return err
	}
	dstOff := 0
	if start < full {
		take := n
		if start+take > full {
			take = full - start
		}
		if err := sp.copyFull(r, start, take, dst[dstOff:]); err != nil {
			return err
		}
		dstOff += int(take) * sp.typeSize
		n -= take
		start += take
	}
	if n > 0 {
		within := start - full
		if err := copyStrided(r, sp.partialPos, sp.partialCol, sp.partialStride, within, n, dst[dstOff:], sp.typeSize); err != nil {
			return err
		}
		dstOff += int(n) * sp.typeSize
	}
	if dstOff < 0 || dstOff > len(dst) {
		return ErrCorrupt
	}
	return sp.maskDigital(dst[:dstOff])
}

func (sp *span) copyFull(r io.ReaderAt, start, n int64, dst []byte) error {
	if sp.nPerChunk <= 0 {
		return ErrCorrupt
	}
	left := n
	idx := start
	dstOff := 0
	for left > 0 {
		chunk := idx / sp.nPerChunk
		within := idx % sp.nPerChunk
		avail := sp.nPerChunk - within
		if avail > left {
			avail = left
		}
		base, err := add64(sp.dataPos, mustMul(chunk, sp.chunkSize))
		if err != nil {
			return err
		}
		if err := copyStrided(r, base, sp.col, sp.stride, within, avail, dst[dstOff:], sp.typeSize); err != nil {
			return err
		}
		idx += avail
		left -= avail
		dstOff += int(avail) * sp.typeSize
	}
	return nil
}

func copyStrided(r io.ReaderAt, region, col, stride, within, count int64, dst []byte, size int) error {
	if count == 0 {
		return nil
	}
	if size <= 0 || stride <= 0 {
		return ErrCorrupt
	}
	start, err := add64(region, col)
	if err != nil {
		return err
	}
	delta, err := mul64(within, stride)
	if err != nil {
		return err
	}
	start, err = add64(start, delta)
	if err != nil {
		return err
	}
	if stride == int64(size) {
		nbytes, err := mul64(count, int64(size))
		if err != nil {
			return err
		}
		return readFull(r, dst[:nbytes], start)
	}
	// De-interleave in windows so a channel read does not allocate the whole
	// multiplexed span. Sample i of this channel sits at byte i*stride.
	done := int64(0)
	for done < count {
		maxTake := int64(1)
		if int64(size) < strideChunk && stride > 0 {
			maxTake = (strideChunk-int64(size))/stride + 1
			if maxTake < 1 {
				maxTake = 1
			}
		}
		take := count - done
		if take > maxTake {
			take = maxTake
		}
		spanBytes, err := mul64(take-1, stride)
		if err != nil {
			return err
		}
		spanBytes, err = add64(spanBytes, int64(size))
		if err != nil {
			return err
		}
		buf := borrowScratch(int(spanBytes))
		err = readFull(r, buf[:spanBytes], start)
		if err != nil {
			releaseScratch(buf)
			return err
		}
		deinterleave(dst[int(done)*size:], buf, size, int(stride), int(take))
		releaseScratch(buf)
		step, err := mul64(take, stride)
		if err != nil {
			return err
		}
		start, err = add64(start, step)
		if err != nil {
			return err
		}
		done += take
	}
	return nil
}

func (c *Channel) readStrings(r io.ReaderAt, start, n int) ([]string, error) {
	out := make([]string, n)
	dst := 0
	need := n
	pos := start
	for i := range c.spans {
		if need == 0 {
			break
		}
		sp := &c.spans[i]
		if sp.runIdx != 0 {
			return nil, ErrCorrupt
		}
		spanN64, err := spanSamples(sp, nil)
		if err != nil {
			return nil, err
		}
		spanN := int(spanN64)
		if pos >= spanN {
			pos -= spanN
			continue
		}
		if sp.nPerChunk == 0 {
			return nil, ErrCorrupt
		}
		chunk := pos / int(sp.nPerChunk)
		local := pos % int(sp.nPerChunk)
		for need > 0 && int64(chunk) < sp.nChunks {
			avail := int(sp.nPerChunk) - local
			if avail < 0 {
				return nil, ErrCorrupt
			}
			take := avail
			if take > need {
				take = need
			}
			strs, err := sp.loadStringsAt(r, int64(chunk), local, take)
			if err != nil {
				return nil, err
			}
			copy(out[dst:], strs)
			dst += len(strs)
			need -= len(strs)
			local = 0
			chunk++
		}
		if need > 0 && sp.partialN > 0 && int64(chunk) >= sp.nChunks {
			strs, err := sp.loadBlock(r, sp.partialPos)
			if err != nil {
				return nil, err
			}
			// partialPos for a contiguous string is the block itself.
			if local > len(strs) {
				return nil, ErrCorrupt
			}
			take := len(strs) - local
			if take > need {
				take = need
			}
			copy(out[dst:], strs[local:local+take])
			dst += take
			need -= take
		}
		pos = 0
	}
	if need != 0 {
		return nil, ErrCorrupt
	}
	return out, nil
}

func (sp *span) loadStringsAt(r io.ReaderAt, chunk int64, local, take int) ([]string, error) {
	base, err := add64(sp.dataPos, mustMul(chunk, sp.chunkSize))
	if err != nil {
		return nil, err
	}
	off, err := add64(base, sp.col)
	if err != nil {
		return nil, err
	}
	// A full chunk keeps the strict "offsets consume the block" check.
	// A window reads only the offset entries and payload bytes it returns.
	if local == 0 && int64(take) == sp.nPerChunk {
		return sp.loadBlock(r, off)
	}
	return sp.loadStringWindow(r, off, int(sp.nPerChunk), local, take)
}

// loadStringWindow reads strings [local, local+take) from the block at off.
// It reads the uint32 end-offsets that bound the window and the payload
// between them, not the earlier string bytes.
func (sp *span) loadStringWindow(r io.ReaderAt, off int64, nAll, local, take int) ([]string, error) {
	if take == 0 {
		return []string{}, nil
	}
	if local < 0 || take < 0 || nAll < 0 || local > nAll || take > nAll-local {
		return nil, ErrCorrupt
	}
	if sp.strSize < 0 {
		return nil, ErrCorrupt
	}
	table := int64(nAll) * 4
	if table > sp.strSize {
		return nil, ErrCorrupt
	}
	var bo binary.ByteOrder = binary.LittleEndian
	if sp.big {
		bo = binary.BigEndian
	}
	var start uint32
	if local > 0 {
		var b [4]byte
		if err := readFull(r, b[:], off+int64(local-1)*4); err != nil {
			return nil, err
		}
		start = bo.Uint32(b[:])
	}
	offs := make([]byte, 4*take)
	if err := readFull(r, offs, off+int64(local)*4); err != nil {
		return nil, err
	}
	ends := make([]uint32, take)
	prev := start
	var end uint32
	for i := 0; i < take; i++ {
		end = bo.Uint32(offs[i*4:])
		if end < prev {
			return nil, ErrCorrupt
		}
		ends[i] = end
		prev = end
	}
	payload := sp.strSize - table
	if int64(end) > payload {
		return nil, ErrCorrupt
	}
	nbytes := int(end - start)
	buf := make([]byte, nbytes)
	if nbytes > 0 {
		dataAt, err := add64(off, table)
		if err != nil {
			return nil, err
		}
		dataAt, err = add64(dataAt, int64(start))
		if err != nil {
			return nil, err
		}
		if err := readFull(r, buf, dataAt); err != nil {
			return nil, err
		}
	}
	out := make([]string, take)
	prev = start
	for i := 0; i < take; i++ {
		a := int(prev - start)
		b := int(ends[i] - start)
		if a < 0 || b < a || b > len(buf) {
			return nil, ErrCorrupt
		}
		out[i] = ignoreTrailingNUL(buf[a:b])
		prev = ends[i]
	}
	return out, nil
}

func (sp *span) loadBlock(r io.ReaderAt, off int64) ([]string, error) {
	if sp.strSize < 0 || sp.strSize > int64(math.MaxInt) {
		return nil, ErrCorrupt
	}
	buf := make([]byte, sp.strSize)
	if err := readFull(r, buf, off); err != nil {
		return nil, err
	}
	var bo binary.ByteOrder = binary.LittleEndian
	if sp.big {
		bo = binary.BigEndian
	}
	return decodeStrings(buf, int(sp.nPerChunk), bo)
}

func copyProps(in []Property) []Property {
	if len(in) == 0 {
		return nil
	}
	out := make([]Property, len(in))
	copy(out, in)
	return out
}

func findProp(props []Property, name string) (Property, bool) {
	for i := len(props) - 1; i >= 0; i-- {
		if props[i].Name == name {
			return props[i], true
		}
	}
	return Property{}, false
}

func upsert(props *[]Property, p Property) {
	for i := range *props {
		if (*props)[i].Name == p.Name {
			(*props)[i] = p
			return
		}
	}
	*props = append(*props, p)
}

func emptyOf(dt DataType) (any, error) {
	if dt == TypeString {
		return []string{}, nil
	}
	if _, ok := dt.Size(); !ok {
		return nil, nil
	}
	return decodePacked(dt, nil)
}

func sliceLen(v any) int {
	switch s := v.(type) {
	case []int8:
		return len(s)
	case []int16:
		return len(s)
	case []int32:
		return len(s)
	case []int64:
		return len(s)
	case []uint8:
		return len(s)
	case []uint16:
		return len(s)
	case []uint32:
		return len(s)
	case []uint64:
		return len(s)
	case []float32:
		return len(s)
	case []float64:
		return len(s)
	case []bool:
		return len(s)
	case []string:
		return len(s)
	case []Timestamp:
		return len(s)
	case []Extended:
		return len(s)
	case []complex64:
		return len(s)
	case []complex128:
		return len(s)
	default:
		return 0
	}
}
