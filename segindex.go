package tdms

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
)

// Segment-index sidecar, version 1. Little-endian. This is not an NI
// *.tdms_index (TDSh) file; the magic is the four bytes GTDX.
//
//	0   4   magic "GTDX"
//	4   4   uint32 version (1)
//	8   8   int64  data file size
//	16  8   int64  modification time, Unix nanoseconds
//	24  8   uint64 device id (0 when the platform has none)
//	32  8   uint64 inode (0 when the platform has none)
//	40  4   uint32 fingerprint, IEEE CRC-32 of each lead-in followed by that
//	        segment's metadata bytes, in file order. Raw samples are not hashed.
//	44  4   uint32 IEEE CRC-32 of the payload
//	48  4   uint32 TDMS version of the first segment (4712 or 4713)
//	52  4   uint32 segment count
//	56  4   uint32 flags: bit 0 incomplete final segment, bit 1 partial tail
//	60  4   uint32 reserved, must be 0
//	64  8   int64  unused tail bytes
//	72      payload
//
// The payload is the file properties, then groups in first-seen order. Each
// group is a length-prefixed name, its properties, and its channels. Each
// channel is its name, TDMS path, data type, a has-type byte, properties,
// spans, and the total sample count. A property is a uint32 byte length and
// then the TDMS property encoding (name, type, value). A span is 96 bytes:
// six int64 layout fields (data position, chunk size, chunk count, values per
// chunk, column, stride), a uint32 type size, a uint32 flag word (bit 0 big
// endian, bit 1 string, bit 2 live/incomplete segment), then five int64 fields
// (string block size, partial count, partial position, partial column,
// partial stride).
//
// Invalidation: size, modification time, device, inode, fingerprint, and the
// payload checksum must all match. Any miss, or any decode error, discards
// the sidecar and parses the file. A cache hit still reads every lead-in and
// the metadata bytes that follow it, in order to recompute that fingerprint;
// it does not decode object paths, raw-data indexes, or properties out of
// those bytes. The object tree and the spans come from the sidecar. Sample
// bytes are read from the file either way, so an in-place change to raw
// samples is visible on a hit. A change to lead-ins or metadata changes the
// fingerprint and forces a parse. A span that would address bytes outside
// the file, or that claims more samples than the file can hold, is a decode
// error and therefore a cache miss.

const (
	segmentIndexMagic   = "GTDX"
	segmentIndexVersion = 1
	segmentIndexHeader  = 72
	segmentIndexMaxSize = 64 << 20

	indexFlagIncomplete = 1 << 0
	indexFlagPartial    = 1 << 1

	spanFlagBig  = 1 << 0
	spanFlagStr  = 1 << 1
	spanFlagLive = 1 << 2

	// spanRecord is the on-disk size of one span. See SegmentIndexPath.
	spanRecord = 96
)

// SegmentIndexPath is the conventional sidecar for a TDMS file: path plus
// ".gtdx". Pass it to WithSegmentIndex. The file is not an NI *.tdms_index
// (TDSh) companion.
//
// The sidecar is little-endian, version 1, magic "GTDX", with a 72-byte header:
//
//	0   magic
//	4   uint32 version
//	8   int64  data file size
//	16  int64  modification time, Unix nanoseconds
//	24  uint64 device (0 if the platform has none)
//	32  uint64 inode (0 if the platform has none)
//	40  uint32 IEEE CRC-32 of each lead-in followed by that segment's metadata
//	44  uint32 IEEE CRC-32 of the payload
//	48  uint32 TDMS version (4712 or 4713)
//	52  uint32 segment count
//	56  uint32 flags (bit 0 incomplete final segment, bit 1 partial tail)
//	60  uint32 reserved, must be 0
//	64  int64  unused tail bytes
//	72  payload: file properties, groups, channels, and spans
//
// A property is a uint32 length and the ordinary TDMS property encoding.
// A span is 96 bytes: data position, chunk size, chunk count, values per
// chunk, column, stride, type size, a flag word (big-endian, string, live),
// string block size, and the partial-chunk position fields. Invalidation is
// size, modification time, device, inode, the metadata fingerprint, and the
// payload checksum. Raw sample bytes are not in the sidecar.
func SegmentIndexPath(path string) string {
	return path + ".gtdx"
}

type indexImage struct {
	size       int64
	mtime      int64
	dev        uint64
	ino        uint64
	fp         uint32
	version    uint32
	segs       int
	incomplete bool
	partial    bool
	unused     int64
	props      []Property
	groups     []*Group
}

func (f *File) dataPath() string {
	if f.path != "" {
		return f.path
	}
	if of, ok := f.r.(*os.File); ok {
		return of.Name()
	}
	return ""
}

func (f *File) trySegmentIndex(sidecar string) bool {
	data := f.dataPath()
	if data == "" || sidecar == "" {
		return false
	}
	if filepath.Clean(sidecar) == filepath.Clean(data) {
		return false
	}
	fi, err := os.Stat(data)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != f.size {
		return false
	}
	sf, err := os.Stat(sidecar)
	if err != nil || sf.Size() == 0 || sf.Size() > segmentIndexMaxSize {
		return false
	}
	blob, err := os.ReadFile(sidecar)
	if err != nil {
		return false
	}
	img, err := decodeSegmentIndex(blob)
	if err != nil {
		return false
	}
	dev, ino := fileIdentity(fi)
	if img.size != fi.Size() || img.mtime != fi.ModTime().UnixNano() || img.dev != dev || img.ino != ino {
		return false
	}
	fp, err := segmentFingerprint(f.r, f.size)
	if err != nil || fp != img.fp {
		return false
	}
	f.installIndex(img)
	return true
}

func (f *File) installIndex(img *indexImage) {
	f.version = img.version
	f.segs = img.segs
	f.incomplete = img.incomplete
	f.partialTail = img.partial
	f.unusedTail = img.unused
	f.props = img.props
	f.groups = img.groups
	f.gindex = map[string]int{}
	f.byPath = map[string]*Channel{}
	for i, g := range f.groups {
		g.file = f
		if g.chIndex == nil {
			g.chIndex = map[string]int{}
		}
		f.gindex[g.name] = i
		for j, ch := range g.channels {
			ch.file = f
			ch.group = g
			g.chIndex[ch.name] = j
			f.byPath[ch.path] = ch
		}
	}
	f.indexUsed = true
}

func (f *File) writeSegmentIndex(sidecar string) error {
	data := f.dataPath()
	if data == "" || sidecar == "" || filepath.Clean(sidecar) == filepath.Clean(data) {
		return nil
	}
	fi, err := os.Stat(data)
	if err != nil || fi.Size() != f.size {
		return err
	}
	fp, err := segmentFingerprint(f.r, f.size)
	if err != nil {
		return err
	}
	dev, ino := fileIdentity(fi)
	blob, err := encodeSegmentIndex(f, fi.Size(), fi.ModTime().UnixNano(), dev, ino, fp)
	if err != nil {
		return err
	}
	return writeAtomic(sidecar, blob)
}

// SegmentIndexUsed reports whether this File was restored from a segment-index
// sidecar. A missing, stale, or corrupt sidecar leaves it false: the file was
// parsed, and when a sidecar was requested a fresh one was written after the
// parse. The method is the deterministic check that a later open used the cache.
func (f *File) SegmentIndexUsed() bool {
	if f == nil {
		return false
	}
	return f.indexUsed
}

// segmentFingerprint hashes lead-in and metadata bytes in file order.
// It walks the same segment boundaries as parse and does not decode them.
// Raw sample bytes are not included.
func segmentFingerprint(r interface {
	ReadAt(p []byte, off int64) (int, error)
}, size int64) (uint32, error) {
	if size == 0 {
		return 0, ErrTruncated
	}
	h := crc32.NewIEEE()
	pos := int64(0)
	for pos < size {
		if size-pos < leadInSize {
			return 0, ErrTruncated
		}
		var hdr [leadInSize]byte
		if err := readFull(r, hdr[:], pos); err != nil {
			return 0, err
		}
		lead, err := decodeLeadIn(hdr[:])
		if err != nil {
			return 0, err
		}
		if lead.version != Version10 && lead.version != Version20 {
			return 0, ErrVersion
		}
		if lead.toc&TocDAQmxRawData != 0 {
			return 0, ErrDAQmx
		}
		segEnd, _, _, err := segmentBounds(pos, size, lead, false)
		if err != nil {
			return 0, err
		}
		_, _ = h.Write(hdr[:])
		if lead.raw > 0 {
			if lead.raw > uint64(size) {
				return 0, ErrCorrupt
			}
			meta := make([]byte, lead.raw)
			if err := readFull(r, meta, pos+leadInSize); err != nil {
				return 0, err
			}
			_, _ = h.Write(meta)
		}
		if lead.next == incompleteSegment {
			break
		}
		pos = segEnd
	}
	return h.Sum32(), nil
}

func encodeSegmentIndex(f *File, size, mtime int64, dev, ino uint64, fp uint32) ([]byte, error) {
	payload, err := encodeIndexPayload(f)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, segmentIndexHeader+len(payload))
	copy(buf[0:4], segmentIndexMagic)
	binary.LittleEndian.PutUint32(buf[4:8], segmentIndexVersion)
	binary.LittleEndian.PutUint64(buf[8:16], uint64(size))
	binary.LittleEndian.PutUint64(buf[16:24], uint64(mtime))
	binary.LittleEndian.PutUint64(buf[24:32], dev)
	binary.LittleEndian.PutUint64(buf[32:40], ino)
	binary.LittleEndian.PutUint32(buf[40:44], fp)
	binary.LittleEndian.PutUint32(buf[44:48], crc32.ChecksumIEEE(payload))
	binary.LittleEndian.PutUint32(buf[48:52], f.version)
	if f.segs < 0 || uint64(f.segs) > uint64(^uint32(0)) {
		return nil, ErrCorrupt
	}
	binary.LittleEndian.PutUint32(buf[52:56], uint32(f.segs))
	var flags uint32
	if f.incomplete {
		flags |= indexFlagIncomplete
	}
	if f.partialTail {
		flags |= indexFlagPartial
	}
	binary.LittleEndian.PutUint32(buf[56:60], flags)
	binary.LittleEndian.PutUint64(buf[64:72], uint64(f.unusedTail))
	copy(buf[segmentIndexHeader:], payload)
	return buf, nil
}

func encodeIndexPayload(f *File) ([]byte, error) {
	w := &ixw{}
	if err := w.props(f.props); err != nil {
		return nil, err
	}
	w.u32(uint32(len(f.groups)))
	for _, g := range f.groups {
		if err := w.str(g.name); err != nil {
			return nil, err
		}
		if err := w.props(g.props); err != nil {
			return nil, err
		}
		w.u32(uint32(len(g.channels)))
		for _, ch := range g.channels {
			if err := w.str(ch.name); err != nil {
				return nil, err
			}
			if err := w.str(ch.path); err != nil {
				return nil, err
			}
			w.u32(uint32(ch.dt))
			if ch.hasDT {
				w.u8(1)
			} else {
				w.u8(0)
			}
			if err := w.props(ch.props); err != nil {
				return nil, err
			}
			w.u32(uint32(len(ch.spans)))
			for _, sp := range ch.spans {
				w.span(sp)
			}
			w.i64(ch.total)
		}
	}
	return w.b, nil
}

func decodeSegmentIndex(b []byte) (*indexImage, error) {
	if len(b) < segmentIndexHeader {
		return nil, errIndex
	}
	if string(b[0:4]) != segmentIndexMagic {
		return nil, errIndex
	}
	if binary.LittleEndian.Uint32(b[4:8]) != segmentIndexVersion {
		return nil, errIndex
	}
	payload := b[segmentIndexHeader:]
	if binary.LittleEndian.Uint32(b[44:48]) != crc32.ChecksumIEEE(payload) {
		return nil, errIndex
	}
	if binary.LittleEndian.Uint32(b[60:64]) != 0 {
		return nil, errIndex
	}
	size := int64(binary.LittleEndian.Uint64(b[8:16]))
	mtime := int64(binary.LittleEndian.Uint64(b[16:24]))
	if size < 0 {
		return nil, errIndex
	}
	flags := binary.LittleEndian.Uint32(b[56:60])
	img := &indexImage{
		size:       size,
		mtime:      mtime,
		dev:        binary.LittleEndian.Uint64(b[24:32]),
		ino:        binary.LittleEndian.Uint64(b[32:40]),
		fp:         binary.LittleEndian.Uint32(b[40:44]),
		version:    binary.LittleEndian.Uint32(b[48:52]),
		segs:       int(binary.LittleEndian.Uint32(b[52:56])),
		incomplete: flags&indexFlagIncomplete != 0,
		partial:    flags&indexFlagPartial != 0,
		unused:     int64(binary.LittleEndian.Uint64(b[64:72])),
	}
	if img.version != Version10 && img.version != Version20 {
		return nil, errIndex
	}
	if img.segs < 1 {
		return nil, errIndex
	}
	if img.partial && !img.incomplete {
		return nil, errIndex
	}
	if img.unused < 0 || (img.unused > 0 && !img.incomplete) {
		return nil, errIndex
	}
	r := ixr{b: payload}
	props, err := r.props()
	if err != nil {
		return nil, err
	}
	img.props = props
	ng, err := r.count(1)
	if err != nil {
		return nil, err
	}
	seenG := map[string]bool{}
	var anyLive bool
	for i := 0; i < ng; i++ {
		name, err := r.str()
		if err != nil {
			return nil, err
		}
		if name == "" || seenG[name] {
			return nil, errIndex
		}
		seenG[name] = true
		gp, err := r.props()
		if err != nil {
			return nil, err
		}
		nc, err := r.count(1)
		if err != nil {
			return nil, err
		}
		g := &Group{name: name, props: gp, chIndex: map[string]int{}}
		seenC := map[string]bool{}
		for j := 0; j < nc; j++ {
			cn, err := r.str()
			if err != nil {
				return nil, err
			}
			cp, err := r.str()
			if err != nil {
				return nil, err
			}
			if cn == "" || seenC[cn] || cp != channelPath(name, cn) {
				return nil, errIndex
			}
			seenC[cn] = true
			dt, err := r.u32()
			if err != nil {
				return nil, err
			}
			has, err := r.u8()
			if err != nil {
				return nil, err
			}
			if has > 1 {
				return nil, errIndex
			}
			cprops, err := r.props()
			if err != nil {
				return nil, err
			}
			ns, err := r.count(spanRecord)
			if err != nil {
				return nil, err
			}
			spans := make([]span, ns)
			for k := 0; k < ns; k++ {
				sp, err := r.span()
				if err != nil {
					return nil, err
				}
				if sp.live {
					anyLive = true
				}
				if sp.live && !img.incomplete {
					return nil, errIndex
				}
				spans[k] = sp
			}
			total, err := r.i64()
			if err != nil {
				return nil, err
			}
			got, err := spanTotal(spans)
			if err != nil || got != total {
				return nil, errIndex
			}
			ch := &Channel{
				name:  cn,
				path:  cp,
				dt:    DataType(dt),
				hasDT: has == 1,
				props: cprops,
				spans: spans,
				total: total,
			}
			if !ch.hasDT && (ch.dt != TypeVoid || len(ch.spans) != 0 || ch.total != 0) {
				return nil, errIndex
			}
			g.channels = append(g.channels, ch)
		}
		img.groups = append(img.groups, g)
	}
	if r.remain() != 0 {
		return nil, errIndex
	}
	if anyLive && !img.incomplete {
		return nil, errIndex
	}
	if err := spansInFile(img); err != nil {
		return nil, err
	}
	return img, nil
}

// spansInFile rejects a sidecar whose spans address bytes outside the file
// they claim to describe, or whose sample counts cannot fit in that file.
// decodeSegmentIndex returns errIndex, and Open treats that as a cache miss.
func spansInFile(img *indexImage) error {
	if img.size < 0 {
		return errIndex
	}
	for _, g := range img.groups {
		for _, ch := range g.channels {
			for _, sp := range ch.spans {
				if err := spanInFile(sp, img.size); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func spanInFile(sp span, size int64) error {
	if sp.dataPos < 0 || sp.dataPos > size {
		return errIndex
	}
	if sp.nChunks > 0 {
		region, err := mul64(sp.nChunks, sp.chunkSize)
		if err != nil {
			return errIndex
		}
		end, err := add64(sp.dataPos, region)
		if err != nil || end > size {
			return errIndex
		}
		if sp.chunkSize == 0 && (sp.nPerChunk > 0 || sp.strSize > 0) {
			return errIndex
		}
	}
	samples, err := spanTotal([]span{sp})
	if err != nil || samples < 0 {
		return errIndex
	}
	if sp.str {
		if sp.nPerChunk > 0 {
			table, err := mul64(sp.nPerChunk, 4)
			if err != nil || sp.strSize < table {
				return errIndex
			}
		}
		if sp.nChunks > 0 {
			bytes, err := mul64(sp.nChunks, sp.strSize)
			if err != nil || bytes > size {
				return errIndex
			}
		}
		if sp.partialN > 0 && (sp.partialPos < 0 || sp.partialPos > size) {
			return errIndex
		}
		return nil
	}
	if samples > 0 {
		if sp.typeSize <= 0 || sp.typeSize > 16 {
			return errIndex
		}
		bytes, err := mul64(samples, int64(sp.typeSize))
		if err != nil || bytes > size {
			return errIndex
		}
		if sp.nPerChunk > 1 && sp.stride <= 0 {
			return errIndex
		}
	}
	if sp.partialN > 0 && (sp.partialPos < 0 || sp.partialPos > size) {
		return errIndex
	}
	return nil
}

func spanTotal(spans []span) (int64, error) {
	var n int64
	for _, sp := range spans {
		full, err := mul64(sp.nChunks, sp.nPerChunk)
		if err != nil {
			return 0, err
		}
		part, err := add64(full, sp.partialN)
		if err != nil {
			return 0, err
		}
		n, err = add64(n, part)
		if err != nil {
			return 0, err
		}
	}
	return n, nil
}

var errIndex = fmt.Errorf("segment index")

type ixw struct{ b []byte }

func (w *ixw) u8(v byte) { w.b = append(w.b, v) }

func (w *ixw) u32(v uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	w.b = append(w.b, buf[:]...)
}

func (w *ixw) u64(v uint64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	w.b = append(w.b, buf[:]...)
}

func (w *ixw) i64(v int64) { w.u64(uint64(v)) }

func (w *ixw) str(s string) error {
	if len(s) > int(^uint32(0)) {
		return errIndex
	}
	w.u32(uint32(len(s)))
	w.b = append(w.b, s...)
	return nil
}

func (w *ixw) props(ps []Property) error {
	w.u32(uint32(len(ps)))
	for _, p := range ps {
		raw, err := encodeProperty(p)
		if err != nil {
			return err
		}
		w.u32(uint32(len(raw)))
		w.b = append(w.b, raw...)
	}
	return nil
}

func (w *ixw) span(sp span) {
	w.i64(sp.dataPos)
	w.i64(sp.chunkSize)
	w.i64(sp.nChunks)
	w.i64(sp.nPerChunk)
	w.i64(sp.col)
	w.i64(sp.stride)
	if sp.typeSize < 0 {
		sp.typeSize = 0
	}
	w.u32(uint32(sp.typeSize))
	var flags uint32
	if sp.big {
		flags |= spanFlagBig
	}
	if sp.str {
		flags |= spanFlagStr
	}
	if sp.live {
		flags |= spanFlagLive
	}
	w.u32(flags)
	w.i64(sp.strSize)
	w.i64(sp.partialN)
	w.i64(sp.partialPos)
	w.i64(sp.partialCol)
	w.i64(sp.partialStride)
}

type ixr struct {
	b   []byte
	off int
}

func (r *ixr) remain() int { return len(r.b) - r.off }

func (r *ixr) u8() (byte, error) {
	if r.remain() < 1 {
		return 0, errIndex
	}
	v := r.b[r.off]
	r.off++
	return v, nil
}

func (r *ixr) u32() (uint32, error) {
	if r.remain() < 4 {
		return 0, errIndex
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v, nil
}

func (r *ixr) u64() (uint64, error) {
	if r.remain() < 8 {
		return 0, errIndex
	}
	v := binary.LittleEndian.Uint64(r.b[r.off:])
	r.off += 8
	return v, nil
}

func (r *ixr) i64() (int64, error) {
	v, err := r.u64()
	if err != nil {
		return 0, err
	}
	return int64(v), nil
}

func (r *ixr) bytes(n int) ([]byte, error) {
	if n < 0 || r.remain() < n {
		return nil, errIndex
	}
	b := r.b[r.off : r.off+n]
	r.off += n
	return b, nil
}

func (r *ixr) str() (string, error) {
	n, err := r.u32()
	if err != nil {
		return "", err
	}
	b, err := r.bytes(int(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (r *ixr) count(elemMin int) (int, error) {
	n, err := r.u32()
	if err != nil {
		return 0, err
	}
	if elemMin < 1 {
		elemMin = 1
	}
	if uint64(n) > uint64(r.remain())/uint64(elemMin) {
		return 0, errIndex
	}
	return int(n), nil
}

func (r *ixr) props() ([]Property, error) {
	n, err := r.count(4)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	out := make([]Property, 0, n)
	for i := 0; i < n; i++ {
		ln, err := r.u32()
		if err != nil {
			return nil, err
		}
		raw, err := r.bytes(int(ln))
		if err != nil {
			return nil, err
		}
		d := decoder{b: raw, bo: binary.LittleEndian}
		p, err := decodeProperty(&d)
		if err != nil || d.remain() != 0 {
			return nil, errIndex
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *ixr) span() (span, error) {
	var sp span
	var err error
	if sp.dataPos, err = r.i64(); err != nil {
		return span{}, err
	}
	if sp.chunkSize, err = r.i64(); err != nil {
		return span{}, err
	}
	if sp.nChunks, err = r.i64(); err != nil {
		return span{}, err
	}
	if sp.nPerChunk, err = r.i64(); err != nil {
		return span{}, err
	}
	if sp.col, err = r.i64(); err != nil {
		return span{}, err
	}
	if sp.stride, err = r.i64(); err != nil {
		return span{}, err
	}
	sz, err := r.u32()
	if err != nil {
		return span{}, err
	}
	flags, err := r.u32()
	if err != nil {
		return span{}, err
	}
	if sp.strSize, err = r.i64(); err != nil {
		return span{}, err
	}
	if sp.partialN, err = r.i64(); err != nil {
		return span{}, err
	}
	if sp.partialPos, err = r.i64(); err != nil {
		return span{}, err
	}
	if sp.partialCol, err = r.i64(); err != nil {
		return span{}, err
	}
	if sp.partialStride, err = r.i64(); err != nil {
		return span{}, err
	}
	if uint64(sz) > uint64(^uint(0)>>1) {
		return span{}, errIndex
	}
	sp.typeSize = int(sz)
	sp.big = flags&spanFlagBig != 0
	sp.str = flags&spanFlagStr != 0
	sp.live = flags&spanFlagLive != 0
	if flags&^(spanFlagBig|spanFlagStr|spanFlagLive) != 0 {
		return span{}, errIndex
	}
	if sp.dataPos < 0 || sp.chunkSize < 0 || sp.nChunks < 0 || sp.nPerChunk < 0 ||
		sp.col < 0 || sp.stride < 0 || sp.strSize < 0 || sp.partialN < 0 ||
		sp.partialPos < 0 || sp.partialCol < 0 || sp.partialStride < 0 {
		return span{}, errIndex
	}
	return sp, nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".gtdx-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	success := false
	defer func() {
		if !success {
			os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFile(name, path); err != nil {
		return err
	}
	success = true
	return nil
}

func replaceFile(tmp, dst string) error {
	err := os.Rename(tmp, dst)
	if err == nil {
		return nil
	}
	if rmErr := os.Remove(dst); rmErr != nil && !os.IsNotExist(rmErr) {
		return err
	}
	return os.Rename(tmp, dst)
}
