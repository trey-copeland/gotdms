package tdms

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
	"time"
)

// Stream is one TDMS segment whose sample counts are fixed when it starts.
// Each Write copies that chunk to the underlying writer. The segment is not
// accumulated in memory; resident bytes stay within the write buffer
// (WithWriteBuffer, default 1 MiB) plus the chunk passed to the call in
// progress, and, for an interleaved chunk, one mix window of at most 1 MiB.
//
// The raw-data index, including each channel's value count, is written
// before any sample. A seekable writer (os.File, any io.WriteSeeker) publishes
// the NI incomplete-segment sentinel in the lead-in and Finish patches the
// real next-segment offset, the same rule Flush uses. A non-seekable writer
// is accepted for fixed-size channels because the declared counts already
// determine the segment length: the lead-in is written finished, then the
// samples. String channels are rejected: their offset table needs every
// string's length before the payload, so they stay on buffered Write.
//
// Contiguous channels are stored in creation order, one channel after
// another, including when the caller lists them differently. Finish a
// channel's count before writing the next. Interleaved channels (see
// SetInterleaved) must share one count; WriteInterleaved appends one
// equal-length slice per channel and mixes that chunk immediately. A
// channel that runs ahead of the others is ErrLayout, and no bytes from
// that call are written.
//
// Properties are part of the segment metadata, so they must be set before
// Stream. SetProperty, Write, Flush, and a second Stream fail while this
// stream is open. SetInterleaved during a stream does not change it; the
// flag is captured at Stream and a later change applies to the next segment.
// After Finish, buffered Write and Flush append further segments as usual.
//
// Finish returns an error if any channel has fewer or more values than
// declared, or if an earlier Write failed. On that path a seekable file is
// left as an incomplete segment (the reader keeps whole values that are
// present, per the NI rule) and a non-seekable file is shorter than its
// lead-in (the reader returns ErrTruncated). Neither path patches the
// segment into a finished file whose counts do not match the bytes.
// Close finishes a stream that has already received every declared value.
// Close on a short or failed stream returns the error and does not patch.
//
// An error from the underlying writer marks the Writer unusable. Later
// Write, Flush, Stream, and Close return that error. The segment is not
// patched closed.
type Stream struct {
	w           *Writer
	channels    []*wChannel
	counts      []int
	got         []int
	sizes       []int
	interleaved bool
	rawExpect   int64
	rawGot      int64
	segStart    int64
	metaLen     int
	order       []string
	indexes     map[string]wIndex
	scratch     []byte
	win         []byte
	cols        [][]byte
	err         error
	done        bool
}

// Stream starts a segment that will contain exactly counts[i] values of
// channels[i]. See Stream. The channels must belong to w. A count must be
// positive. String channels, a channel with no fixed size, pending buffered
// samples, and an underlying writer that already failed return an error.
// Interleaved mode requires equal counts.
func (w *Writer) Stream(channels []*ChannelWriter, counts []int) (*Stream, error) {
	if err := w.busy(); err != nil {
		return nil, wrap("write", w.path, err)
	}
	if w.pendingData() {
		return nil, wrap("write", w.path, fmt.Errorf("%w: flush pending samples before Stream", ErrArgument))
	}
	ordered, ns, err := orderStream(w, channels, counts)
	if err != nil {
		return nil, wrap("write", w.path, err)
	}
	for _, c := range ordered {
		if c.ch.dt == TypeString || c.ch.dt.storage() == TypeString {
			return nil, wrap("write", c.ch.path, fmt.Errorf("%w: string channels have no streaming offset table; use Write", ErrUnsupported))
		}
		if _, ok := c.ch.dt.Size(); !ok {
			return nil, wrap("write", c.ch.path, fmt.Errorf("%w: %s", ErrUnsupported, c.ch.dt))
		}
	}
	rawSize, sizes, err := declaredRawSize(ordered, ns, w.interleaved)
	if err != nil {
		return nil, wrap("write", w.path, err)
	}
	for i, c := range ordered {
		c.ch.n = ns[i]
	}
	defer func() {
		for _, c := range ordered {
			c.ch.n = 0
		}
	}()
	data := make([]*wChannel, len(ordered))
	for i, c := range ordered {
		data[i] = c.ch
	}
	if w.interleaved {
		if err := checkInterleaved(data); err != nil {
			return nil, wrap("write", w.path, err)
		}
	}
	meta, toc, order, indexes, err := w.buildMeta(data, true)
	if err != nil {
		return nil, wrap("write", w.path, err)
	}
	toc |= TocRawData
	if w.interleaved {
		toc |= TocInterleavedData
	}
	segStart := w.pos
	if err := w.writeStreamHeader(toc, meta, rawSize); err != nil {
		w.sticky = err
		return nil, wrap("write", w.path, err)
	}
	chs := make([]*wChannel, len(ordered))
	for i, c := range ordered {
		chs[i] = c.ch
	}
	s := &Stream{
		w:           w,
		channels:    chs,
		counts:      append([]int(nil), ns...),
		got:         make([]int, len(ns)),
		sizes:       sizes,
		interleaved: w.interleaved,
		rawExpect:   rawSize,
		segStart:    segStart,
		metaLen:     len(meta),
		order:       order,
		indexes:     indexes,
	}
	w.stream = s
	return s, nil
}

// Stream starts a one-channel stream of n values. See Writer.Stream.
func (c *ChannelWriter) Stream(n int) (*Stream, error) {
	if c == nil || c.w == nil {
		return nil, wrap("write", "", fmt.Errorf("%w: nil channel", ErrArgument))
	}
	return c.w.Stream([]*ChannelWriter{c}, []int{n})
}

// StreamWaveform sets wf_start_time, wf_increment, wf_samples (n), and, when
// unit is non-empty, unit_string, in that order, then starts a one-channel
// stream of n samples. Y is written afterward with Stream.Write. This is the
// streaming counterpart of WriteWaveform, which still buffers its whole Y
// slice until Flush. n is the waveform's wf_samples value.
func (c *ChannelWriter) StreamWaveform(start Timestamp, increment float64, unit string, n int) (*Stream, error) {
	if c == nil || c.w == nil || c.ch == nil {
		return nil, wrap("write", "", fmt.Errorf("%w: nil channel", ErrArgument))
	}
	if err := c.w.busy(); err != nil {
		return nil, wrap("write", c.ch.path, err)
	}
	switch c.ch.dt.storage() {
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64,
		TypeUint8, TypeUint16, TypeUint32, TypeUint64,
		TypeFloat32, TypeFloat64:
	default:
		return nil, wrap("write", c.ch.path, fmt.Errorf("%w: StreamWaveform wants a numeric channel", ErrType))
	}
	if n <= 0 {
		return nil, wrap("write", c.ch.path, fmt.Errorf("%w: count must be positive", ErrArgument))
	}
	if err := c.SetProperty(PropStartTime, start); err != nil {
		return nil, err
	}
	if err := c.SetProperty(PropIncrement, increment); err != nil {
		return nil, err
	}
	if err := c.SetProperty(PropSamples, n); err != nil {
		return nil, err
	}
	if unit != "" {
		if err := c.SetProperty(PropUnitString, unit); err != nil {
			return nil, err
		}
	}
	return c.Stream(n)
}

// Write appends values to ch. values uses the same slice types as
// ChannelWriter.Write. On a contiguous stream, ch must be the channel that
// still has room and whose earlier channels have already reached their
// counts. A multi-channel interleaved stream returns ErrLayout; use
// WriteInterleaved. The call copies bytes out before it returns, so the
// caller may reuse or mutate the slice afterward.
func (s *Stream) Write(ch *ChannelWriter, values any) error {
	if err := s.ready(); err != nil {
		return err
	}
	if s.interleaved && len(s.channels) > 1 {
		return wrap("write", s.channels[0].path, fmt.Errorf("%w: interleaved stream uses WriteInterleaved", ErrLayout))
	}
	if ch == nil || ch.ch == nil || ch.w != s.w {
		return wrap("write", "", fmt.Errorf("%w: channel is not in this stream", ErrArgument))
	}
	idx := s.index(ch.ch)
	if idx < 0 {
		return wrap("write", ch.ch.path, fmt.Errorf("%w: channel is not in this stream", ErrArgument))
	}
	if active := s.active(); active != idx {
		if active < 0 || idx < active {
			return wrap("write", ch.ch.path, fmt.Errorf("%w: channel already has %d values", ErrArgument, s.counts[idx]))
		}
		return wrap("write", ch.ch.path, fmt.Errorf("%w: write %s before %s", ErrLayout, s.channels[active].path, ch.ch.path))
	}
	n, err := valueCount(s.channels[idx].dt, values)
	if err != nil {
		return wrap("write", ch.ch.path, err)
	}
	if n == 0 {
		return nil
	}
	if n > s.counts[idx]-s.got[idx] {
		return wrap("write", ch.ch.path, fmt.Errorf("%w: %d values would pass the declared %d", ErrArgument, s.got[idx]+n, s.counts[idx]))
	}
	if err := s.emit(s.channels[idx].dt, values); err != nil {
		return wrap("write", ch.ch.path, s.fail(err))
	}
	s.got[idx] += n
	return nil
}

// WriteInterleaved appends one slice per channel of this stream, in creation
// order. Every slice must contain the same number of values. The chunk is
// mixed into the segment before the call returns. A contiguous stream
// returns ErrLayout.
func (s *Stream) WriteInterleaved(values ...any) error {
	if err := s.ready(); err != nil {
		return err
	}
	if !s.interleaved {
		return wrap("write", s.w.path, fmt.Errorf("%w: contiguous stream uses Write", ErrLayout))
	}
	if len(values) != len(s.channels) {
		return wrap("write", s.w.path, fmt.Errorf("%w: %d slices for %d channels", ErrArgument, len(values), len(s.channels)))
	}
	n := -1
	for i, ch := range s.channels {
		cn, err := valueCount(ch.dt, values[i])
		if err != nil {
			return wrap("write", ch.path, err)
		}
		if n < 0 {
			n = cn
		} else if cn != n {
			return wrap("write", ch.path, fmt.Errorf("%w: channel %s has %d values, channel %s has %d", ErrLayout, s.channels[0].path, n, ch.path, cn))
		}
	}
	if n == 0 {
		return nil
	}
	for i, ch := range s.channels {
		if n > s.counts[i]-s.got[i] {
			return wrap("write", ch.path, fmt.Errorf("%w: %d values would pass the declared %d", ErrArgument, s.got[i]+n, s.counts[i]))
		}
	}
	if cap(s.cols) < len(s.channels) {
		s.cols = make([][]byte, len(s.channels))
	}
	s.cols = s.cols[:len(s.channels)]
	s.scratch = s.scratch[:0]
	for i, ch := range s.channels {
		b, err := s.column(ch.dt, values[i])
		if err != nil {
			s.scratch = s.scratch[:0]
			return wrap("write", ch.path, err)
		}
		s.cols[i] = b
	}
	if err := s.writeMixed(s.cols, n); err != nil {
		return wrap("write", s.channels[0].path, s.fail(err))
	}
	for i := range s.got {
		s.got[i] += n
	}
	return nil
}

// Finish completes the segment. Every channel must have received its
// declared count. A seekable writer patches the lead-in. A second Finish
// after success is a no-op.
func (s *Stream) Finish() error {
	if s == nil || s.w == nil {
		return wrap("write", "", fmt.Errorf("%w: nil stream", ErrArgument))
	}
	if s.done && s.err == nil && s.w.stream == nil {
		return nil
	}
	return s.shutdown()
}

func (s *Stream) shutdown() error {
	if s.done && s.err == nil && s.w.stream == nil {
		return nil
	}
	if s.err != nil {
		return s.abandon(s.err)
	}
	for i, ch := range s.channels {
		if s.got[i] != s.counts[i] {
			return s.abandon(fmt.Errorf("%w: channel %s has %d of %d values", ErrArgument, ch.path, s.got[i], s.counts[i]))
		}
	}
	if s.rawGot != s.rawExpect {
		return s.abandon(fmt.Errorf("%w: stream wrote %d of %d bytes", ErrCorrupt, s.rawGot, s.rawExpect))
	}
	if err := s.w.bw.Flush(); err != nil {
		return s.abandon(err)
	}
	if err := s.w.syncDurable(); err != nil {
		return s.abandon(err)
	}
	if s.w.seekable {
		next := uint64(s.metaLen) + uint64(s.rawExpect)
		if err := s.w.patchNext(s.segStart, next); err != nil {
			return s.abandon(err)
		}
		if err := s.w.syncDurable(); err != nil {
			return s.abandon(err)
		}
	} else {
		s.w.pos += int64(leadInSize) + int64(s.metaLen) + s.rawExpect
	}
	s.w.started = true
	s.w.markEmitted(true)
	s.w.prevOrder = s.order
	s.w.prevIndex = s.indexes
	s.w.stream = nil
	s.done = true
	return nil
}

func (s *Stream) abandon(cause error) error {
	if cause == nil {
		cause = ErrArgument
	}
	if s.w.bw != nil {
		_ = s.w.bw.Flush()
	}
	if s.w.sticky == nil {
		s.w.sticky = cause
	}
	s.err = s.w.sticky
	s.done = true
	s.w.stream = nil
	return wrap("write", s.w.path, s.err)
}

func (s *Stream) fail(err error) error {
	if err == nil {
		return nil
	}
	if s.err == nil {
		s.err = err
	}
	if s.w.sticky == nil {
		s.w.sticky = err
	}
	return s.err
}

func (s *Stream) ready() error {
	if s == nil || s.w == nil {
		return wrap("write", "", fmt.Errorf("%w: nil stream", ErrArgument))
	}
	if s.w.closed {
		return wrap("write", s.w.path, ErrClosed)
	}
	if s.done || s.err != nil || s.w.sticky != nil {
		err := s.err
		if err == nil {
			err = s.w.sticky
		}
		if err == nil {
			err = fmt.Errorf("%w: stream finished", ErrClosed)
		}
		return wrap("write", s.w.path, err)
	}
	if s.w.stream != s {
		return wrap("write", s.w.path, fmt.Errorf("%w: stream is not current", ErrArgument))
	}
	return nil
}

func (s *Stream) active() int {
	for i, g := range s.got {
		if g < s.counts[i] {
			return i
		}
	}
	return -1
}

func (s *Stream) index(ch *wChannel) int {
	for i, c := range s.channels {
		if c == ch {
			return i
		}
	}
	return -1
}

func (s *Stream) emit(dt DataType, values any) error {
	if view, n, ok := borrowedView(dt, values); ok {
		if n == 0 {
			return nil
		}
		return s.writeRaw(view)
	}
	s.scratch = s.scratch[:0]
	raw, strs, n, err := appendEncoded(s.scratch, dt, values)
	if err != nil {
		return err
	}
	if len(strs) > 0 {
		return fmt.Errorf("%w: string channels have no streaming offset table; use Write", ErrUnsupported)
	}
	s.scratch = raw
	if n == 0 {
		return nil
	}
	return s.writeRaw(raw)
}

// column returns the little-endian bytes of one interleaved channel's chunk.
// Borrowed numeric views alias the caller for the rest of this call.
// Encoded kinds are copied into scratch, which this call may reuse, so the
// caller of writeMixed must finish before the next column() that encodes.
func (s *Stream) column(dt DataType, values any) ([]byte, error) {
	if view, _, ok := borrowedView(dt, values); ok {
		return view, nil
	}
	// Encode into a fresh region of scratch so earlier encoded columns stay.
	off := len(s.scratch)
	raw, strs, _, err := appendEncoded(s.scratch, dt, values)
	if err != nil {
		return nil, err
	}
	if len(strs) > 0 {
		return nil, fmt.Errorf("%w: string channels have no streaming offset table; use Write", ErrUnsupported)
	}
	s.scratch = raw
	return s.scratch[off:], nil
}

func (s *Stream) writeMixed(cols [][]byte, n int) error {
	stride := 0
	for _, sz := range s.sizes {
		stride += sz
	}
	if stride <= 0 || n == 0 {
		return nil
	}
	rows := 1
	if stride < strideChunk {
		rows = strideChunk / stride
		if rows < 1 {
			rows = 1
		}
	}
	if rows > n {
		rows = n
	}
	need := rows * stride
	if cap(s.win) < need {
		s.win = make([]byte, need)
	}
	for done := 0; done < n; {
		take := rows
		if done+take > n {
			take = n - done
		}
		dst := 0
		buf := s.win[:take*stride]
		for i := 0; i < take; i++ {
			sample := done + i
			for c := range cols {
				sz := s.sizes[c]
				src := sample * sz
				copy(buf[dst:dst+sz], cols[c][src:src+sz])
				dst += sz
			}
		}
		if err := s.writeRaw(buf); err != nil {
			return err
		}
		done += take
	}
	s.scratch = s.scratch[:0]
	return nil
}

func (s *Stream) writeRaw(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if int64(len(p)) > s.rawExpect-s.rawGot {
		return fmt.Errorf("%w: stream wrote past the declared segment", ErrCorrupt)
	}
	n, err := s.w.bw.Write(p)
	s.rawGot += int64(n)
	if err != nil {
		return err
	}
	if n != len(p) {
		return io.ErrShortWrite
	}
	return nil
}

func (w *Writer) writeStreamHeader(toc uint32, meta []byte, rawSize int64) error {
	if rawSize < 0 {
		return ErrCorrupt
	}
	if uint64(rawSize) > ^uint64(0)-uint64(len(meta)) {
		return fmt.Errorf("%w: segment larger than uint64", ErrArgument)
	}
	var next uint64
	if w.seekable {
		next = incompleteSegment
	} else {
		next = uint64(len(meta)) + uint64(rawSize)
	}
	lead := encodeLeadIn(toc, next, uint64(len(meta)))
	if _, err := w.bw.Write(lead); err != nil {
		return err
	}
	if _, err := w.bw.Write(meta); err != nil {
		return err
	}
	return nil
}

func (w *Writer) patchNext(at int64, next uint64) error {
	if _, err := w.ws.Seek(at+12, io.SeekStart); err != nil {
		return err
	}
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], next)
	if _, err := w.ws.Write(buf[:]); err != nil {
		return err
	}
	end := at + int64(leadInSize) + int64(next)
	if _, err := w.ws.Seek(end, io.SeekStart); err != nil {
		return err
	}
	w.pos = end
	return nil
}

func (w *Writer) pendingData() bool {
	for _, g := range w.groups {
		for _, ch := range g.channels {
			if ch.n > 0 || len(ch.raw) > 0 || len(ch.strs) > 0 {
				return true
			}
		}
	}
	return false
}

func orderStream(w *Writer, channels []*ChannelWriter, counts []int) ([]*ChannelWriter, []int, error) {
	if len(channels) == 0 {
		return nil, nil, fmt.Errorf("%w: stream has no channels", ErrArgument)
	}
	if len(channels) != len(counts) {
		return nil, nil, fmt.Errorf("%w: %d channels and %d counts", ErrArgument, len(channels), len(counts))
	}
	type item struct {
		c   *ChannelWriter
		n   int
		pos int
	}
	items := make([]item, len(channels))
	seen := map[*wChannel]bool{}
	for i, c := range channels {
		if c == nil || c.ch == nil || c.w != w {
			return nil, nil, fmt.Errorf("%w: channel is not from this writer", ErrArgument)
		}
		if seen[c.ch] {
			return nil, nil, fmt.Errorf("%w: duplicate channel %s", ErrArgument, c.ch.path)
		}
		seen[c.ch] = true
		if counts[i] <= 0 {
			return nil, nil, fmt.Errorf("%w: count for %s must be positive", ErrArgument, c.ch.path)
		}
		pos, ok := channelOrd(w, c.ch)
		if !ok {
			return nil, nil, fmt.Errorf("%w: unknown channel %s", ErrArgument, c.ch.path)
		}
		items[i] = item{c: c, n: counts[i], pos: pos}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].pos < items[j].pos })
	out := make([]*ChannelWriter, len(items))
	ns := make([]int, len(items))
	for i, it := range items {
		out[i] = it.c
		ns[i] = it.n
	}
	return out, ns, nil
}

func channelOrd(w *Writer, ch *wChannel) (int, bool) {
	ord := 0
	for _, g := range w.groups {
		for _, c := range g.channels {
			if c == ch {
				return ord, true
			}
			ord++
		}
	}
	return 0, false
}

func declaredRawSize(channels []*ChannelWriter, counts []int, interleaved bool) (int64, []int, error) {
	sizes := make([]int, len(channels))
	for i, c := range channels {
		sz, ok := c.ch.dt.Size()
		if !ok || sz <= 0 {
			return 0, nil, fmt.Errorf("%w: %s", ErrUnsupported, c.ch.dt)
		}
		if counts[i] > math.MaxInt/sz || int64(counts[i]) > math.MaxInt64/int64(sz) {
			return 0, nil, fmt.Errorf("%w: segment larger than int", ErrArgument)
		}
		sizes[i] = sz
	}
	if interleaved && len(channels) > 1 {
		if counts[0] <= 0 {
			return 0, nil, fmt.Errorf("%w: count must be positive", ErrArgument)
		}
		stride := 0
		for i, sz := range sizes {
			if counts[i] != counts[0] {
				return 0, nil, fmt.Errorf("%w: channel %s has %d values, channel %s has %d", ErrLayout, channels[0].ch.path, counts[0], channels[i].ch.path, counts[i])
			}
			stride += sz
		}
		if counts[0] > math.MaxInt/stride {
			return 0, nil, fmt.Errorf("%w: segment larger than int", ErrArgument)
		}
		return int64(counts[0]) * int64(stride), sizes, nil
	}
	var total int64
	for i, sz := range sizes {
		n := int64(counts[i]) * int64(sz)
		if n > math.MaxInt64-total {
			return 0, nil, fmt.Errorf("%w: segment larger than int64", ErrArgument)
		}
		total += n
	}
	return total, sizes, nil
}

func valueCount(dt DataType, values any) (int, error) {
	if values == nil {
		return 0, nil
	}
	switch dt.storage() {
	case TypeInt8:
		s, ok := values.([]int8)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeUint8:
		s, ok := values.([]uint8)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeInt16:
		s, ok := values.([]int16)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeUint16:
		s, ok := values.([]uint16)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeInt32:
		s, ok := values.([]int32)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeUint32:
		s, ok := values.([]uint32)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeFloat32:
		s, ok := values.([]float32)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeInt64:
		s, ok := values.([]int64)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeUint64:
		s, ok := values.([]uint64)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeFloat64:
		s, ok := values.([]float64)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeComplex64:
		s, ok := values.([]complex64)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeComplex128:
		s, ok := values.([]complex128)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeBool:
		s, ok := values.([]bool)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeTimestamp:
		switch s := values.(type) {
		case []Timestamp:
			return len(s), nil
		case []time.Time:
			return len(s), nil
		default:
			return 0, typeErr(dt, values)
		}
	case TypeExtendedFloat:
		s, ok := values.([]Extended)
		if !ok {
			return 0, typeErr(dt, values)
		}
		return len(s), nil
	case TypeString:
		return 0, fmt.Errorf("%w: string channels have no streaming offset table; use Write", ErrUnsupported)
	default:
		return 0, fmt.Errorf("%w: %s", ErrUnsupported, dt)
	}
}
