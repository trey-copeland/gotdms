package tdms

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// Segment-index sidecar, version 2. Little-endian. This is not an NI
// *.tdms_index (TDSh) file; the magic is the four bytes GTDX.
//
//	0   4   magic "GTDX"
//	4   4   uint32 version (2; version 1 is still accepted)
//	8   8   int64  data file size
//	16  8   int64  modification time, Unix nanoseconds
//	24  8   uint64 device id (0 when the platform has none)
//	32  8   uint64 inode (0 when the platform has none)
//	40  4   uint32 fingerprint, IEEE CRC-32 of each lead-in followed by that
//	        segment's metadata bytes, in file order. Raw samples are not hashed.
//	44  4   uint32 IEEE CRC-32 of the payload
//	48  4   uint32 TDMS version of the first segment (4712 or 4713)
//	52  4   uint32 segment count
//	56  4   uint32 flags: bit 0 incomplete final segment, bit 1 partial tail,
//	        bit 2 final segment recovered by WithLenientTail (truncated).
//	        Any other bit is rejected.
//	60  4   uint32 reserved, must be 0
//	64  8   int64  unused tail bytes
//	72      payload
//
// The payload is the file properties, then groups in first-seen order. Each
// group is a length-prefixed name, its properties, and its channels. Each
// channel is its name, TDMS path, data type, a has-type byte, properties,
// spans, and the total sample count. A property is a uint32 byte length and
// then the TDMS property encoding (name, type, value). A version 2 span is
// 96 bytes: six int64 layout fields (data position, chunk size, chunk count,
// values per chunk, column, stride), a uint32 type size, a uint32 flag word
// (bit 0 big endian, bit 1 string, bit 2 live/incomplete segment, bit 5 run),
// and five int64 fields (string block size, partial count, partial position,
// partial column, partial stride). Bit 5 appends int64 step and int64 count
// and the record is 112 bytes. Count greater than 1 repeats the layout at
// dataPos+i*step. A clear bit is one segment and omits those fields.
// Version 1 spans are 96 bytes, one segment each, and still decode. A
// v0.1.0 reader rejects any version other than 1, so it treats a version 2
// sidecar as a miss.
//
// Invalidation: size, modification time, device, inode, fingerprint, and the
// payload checksum must all match. Any miss, or any decode error, discards
// the sidecar and parses the file. By default a cache hit still reads every
// lead-in and the metadata bytes that follow it, in order to recompute that
// fingerprint; it does not decode object paths, raw-data indexes, or
// properties out of those bytes. WithSegmentIndexTrustIdentity skips that
// walk when size, modification time, device, and inode already match. An
// in-place edit that keeps those four values is then invisible. The object
// tree and the spans come from the sidecar. Sample bytes are read from the
// file either way, so an in-place change to raw samples is visible on a hit
// when the spans still address those bytes. A change to lead-ins or metadata
// changes the fingerprint and forces a parse, unless trust-identity was set
// and size, time, and identity were preserved. A span that would address
// bytes outside the file, or that claims more samples than the file can
// hold, is a decode error and therefore a cache miss, including when
// trust-identity is set.

const (
	segmentIndexMagic   = "GTDX"
	segmentIndexVersion = 2
	segmentIndexV1      = 1
	segmentIndexHeader  = 72
	segmentIndexMaxSize = 64 << 20

	indexFlagIncomplete = 1 << 0
	indexFlagPartial    = 1 << 1
	indexFlagTruncated  = 1 << 2

	spanFlagBig     = 1 << 0
	spanFlagStr     = 1 << 1
	spanFlagLive    = 1 << 2
	spanFlagDigital = 1 << 3
	spanFlagSolo    = 1 << 4
	// spanFlagRun marks a version 2 span that continues for more than one
	// segment. The 16-byte step and count follow the 96-byte record. A
	// single segment omits them.
	spanFlagRun  = 1 << 5
	spanBitShift = 8

	// spanRecord is a version 2 run: the 96-byte span plus step and count.
	// spanRecordV1 is one segment, which version 2 also uses when the run
	// flag is clear.
	spanRecord   = 112
	spanRecordV1 = 96
)

// segmentIndexLimit is segmentIndexMaxSize. Tests lower it to exercise the
// over-limit write without building a 64 MiB sidecar.
var segmentIndexLimit int64 = segmentIndexMaxSize

// SegmentIndexPath is the conventional sidecar for a TDMS file: path plus
// ".gtdx". Pass it to WithSegmentIndex. The file is not an NI *.tdms_index
// (TDSh) companion.
//
// The sidecar is little-endian, version 2, magic "GTDX", with a 72-byte header.
// Version 1 sidecars still decode. A v0.1.0 reader accepts only version 1, so
// a version 2 sidecar is a cache miss for that release.
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
//	56  uint32 flags (bit 0 incomplete final segment, bit 1 partial tail,
//	    bit 2 truncated final segment from WithLenientTail)
//	60  uint32 reserved, must be 0
//	64  int64  unused tail bytes
//	72  payload: file properties, groups, channels, and spans
//
// A property is a uint32 length and the ordinary TDMS property encoding.
// A version 2 span is 96 bytes, the same fields as version 1. Bit 5 of the
// flag word marks a run: step and count follow, and the record is 112 bytes.
// A single segment leaves the bit clear and omits those fields. Run count
// greater than 1 repeats the layout at dataPos+i*step. A run is not used
// for strings, live spans, or a partial tail. Version 1 spans are 96 bytes
// and describe one segment. The flag word is big-endian, string, live,
// digital line, and a raw buffer that does not start at the chunk (solo).
// A digital span stores its bit index in bits 8–15. A channel
// whose has-type byte is 2 is DAQmx: after the channel total, the sidecar
// stores the scaler vector and any non-primary scaler spans. The sum of a
// channel's sample bytes, and of each DAQmx scaler's sample bytes, must fit
// in the file. Invalidation is size,
// modification time, device, inode, the metadata fingerprint, and the
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
	truncated  bool
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

// dataStat is a stat of the open data file plus the device and file index
// taken from that same handle. Windows file indexes are not on FileInfo.
type dataStat struct {
	fi       os.FileInfo
	dev, ino uint64
}

// statData stats the data file through the open handle when the reader
// has one, so a file replaced at its path after the open is not mistaken for
// the bytes being read. Other readers fall back to the recorded path.
// Identity comes from the handle (fileID) when the reader is an *os.File.
// A failed identity call is returned rather than stored as 0, 0.
func (f *File) statData() (dataStat, error) {
	if of, ok := f.r.(*os.File); ok {
		fi, err := of.Stat()
		if err != nil {
			return dataStat{}, err
		}
		dev, ino, err := fileID(of)
		if err != nil {
			return dataStat{}, err
		}
		return dataStat{fi: fi, dev: dev, ino: ino}, nil
	}
	var fi os.FileInfo
	var err error
	if s, ok := f.r.(interface{ Stat() (os.FileInfo, error) }); ok {
		fi, err = s.Stat()
	} else {
		data := f.dataPath()
		if data == "" {
			return dataStat{}, os.ErrNotExist
		}
		fi, err = os.Stat(data)
	}
	if err != nil {
		return dataStat{}, err
	}
	dev, ino := fileIdentity(fi)
	return dataStat{fi: fi, dev: dev, ino: ino}, nil
}

// readFileLimited reads path, returning at most limit bytes. A file larger
// than limit (for example a cache file that grew between its stat and this
// read) yields a nil slice and no error, so the caller treats it as an
// unusable sidecar instead of loading it in full. This caps the read even
// when the on-disk size changed after the size check that gated the call.
func readFileLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	blob, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(blob)) > limit {
		return nil, nil
	}
	return blob, nil
}

func (f *File) trySegmentIndex(sidecar string, trustIdentity bool, pre dataStat) bool {
	data := f.dataPath()
	fi := pre.fi
	if data == "" || sidecar == "" || fi == nil {
		return false
	}
	if filepath.Clean(sidecar) == filepath.Clean(data) {
		return false
	}
	if !fi.Mode().IsRegular() || fi.Size() != f.size {
		return false
	}
	sf, err := os.Stat(sidecar)
	if err != nil || sf.Size() == 0 || sf.Size() > segmentIndexLimit {
		return false
	}
	blob, err := readFileLimited(sidecar, segmentIndexLimit)
	if err != nil {
		return false
	}
	img, err := decodeSegmentIndex(blob)
	if err != nil {
		return false
	}
	if img.size != fi.Size() || img.mtime != fi.ModTime().UnixNano() || img.dev != pre.dev || img.ino != pre.ino {
		return false
	}
	// A lenient build records a past-EOF tail. A strict open must still
	// reject that file, so the sidecar is a miss unless this open is lenient.
	if img.truncated && !f.lenient {
		return false
	}
	if !trustIdentity {
		fp, err := segmentFingerprint(f.r, f.size, f.lenient)
		if err != nil || fp != img.fp {
			return false
		}
	}
	f.installIndex(img)
	return true
}

func (f *File) installIndex(img *indexImage) {
	f.version = img.version
	f.segs = img.segs
	f.incomplete = img.incomplete
	f.partialTail = img.partial
	f.truncated = img.truncated
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

// writeSegmentIndex records pre, the handle's stat taken before the parse.
// The handle is stat'ed again here; a size, modification time, or identity
// change in between means the parsed tree may not match the bytes the
// sidecar would be keyed to, so nothing is written.
func (f *File) writeSegmentIndex(sidecar string, pre dataStat) error {
	data := f.dataPath()
	if data == "" || sidecar == "" || filepath.Clean(sidecar) == filepath.Clean(data) {
		return nil
	}
	if pre.fi == nil {
		return fmt.Errorf("segment index: no stat of the data file before parse")
	}
	if f.droppedTail > 0 {
		// The sidecar would describe a prefix of the file. A torn tail is a
		// repair to make (truncate the file), not a state to cache.
		return fmt.Errorf("%w: segment index: %d torn bytes after the last whole segment; truncate the file to %d bytes first",
			ErrTruncated, f.droppedTail, f.size)
	}
	now, err := f.statData()
	if err != nil {
		return err
	}
	if now.fi.Size() != f.size || pre.fi.Size() != f.size {
		return fmt.Errorf("segment index: file size changed during parse")
	}
	if now.fi.ModTime().UnixNano() != pre.fi.ModTime().UnixNano() || now.dev != pre.dev || now.ino != pre.ino {
		return fmt.Errorf("segment index: file changed during parse")
	}
	fp, err := segmentFingerprint(f.r, f.size, f.lenient)
	if err != nil {
		return err
	}
	blob, err := encodeSegmentIndex(f, now.fi.Size(), now.fi.ModTime().UnixNano(), now.dev, now.ino, fp)
	if err != nil {
		return err
	}
	// A sidecar over the read limit is never accepted. Writing it anyway
	// costs a large write on every open and reports success for a cache
	// that cannot hit.
	if int64(len(blob)) > segmentIndexLimit {
		return fmt.Errorf("segment index: sidecar would be %d bytes, over the %d-byte limit", len(blob), segmentIndexLimit)
	}
	return writeAtomic(sidecar, blob, sidecarPerm(now.fi))
}

// sidecarPerm gives the sidecar the data file's read and write bits, so a
// user who can read the TDMS file can also read its index. os.CreateTemp
// alone would leave it 0600.
func sidecarPerm(fi os.FileInfo) os.FileMode {
	perm := fi.Mode().Perm() & 0o666
	if perm == 0 {
		perm = 0o644
	}
	return perm | 0o200
}

// BuildSegmentIndex parses lead-ins and metadata of path and writes
// SegmentIndexPath(path) atomically. Raw samples are not read. A failure to
// create or replace the sidecar is returned. Open ignores that class of
// error; this function does not.
//
// opts are the same ReaderOptions as Open. WithLenientTail is honored and
// must be set on a later open that should hit a sidecar built from a
// past-EOF tail. WithSegmentIndex and WithSegmentIndexTrustIdentity do not
// change the path that is written or the bytes in it. The write is the
// conventional sidecar for path, not a sidecar named by WithSegmentIndex.
func BuildSegmentIndex(path string, opts ...ReaderOption) error {
	_, err := BuildSegmentIndexFile(path, opts...)
	return err
}

// BuildSegmentIndexFile is BuildSegmentIndex and returns the sidecar path
// after the write succeeds.
func BuildSegmentIndexFile(path string, opts ...ReaderOption) (string, error) {
	cfg := applyReaderOptions(opts)
	rf, err := os.Open(path)
	if err != nil {
		return "", wrap("index", path, err)
	}
	defer rf.Close()
	var ropts []ReaderOption
	if cfg.bufSize > 0 {
		ropts = append(ropts, WithReadBuffer(cfg.bufSize))
	}
	if cfg.lenientTail {
		ropts = append(ropts, WithLenientTail())
	}
	fi, err := rf.Stat()
	if err != nil {
		return "", wrap("index", path, err)
	}
	dev, ino, err := fileID(rf)
	if err != nil {
		return "", wrap("index", path, err)
	}
	pre := dataStat{fi: fi, dev: dev, ino: ino}
	tf, err := newReader(rf, path, ropts...)
	if err != nil {
		return "", err
	}
	side := SegmentIndexPath(path)
	werr := tf.writeSegmentIndex(side, pre)
	tf.Close()
	if werr != nil {
		return "", wrap("index", side, werr)
	}
	return side, nil
}

// SegmentIndexValid reports whether SegmentIndexPath(path) matches path.
// The check is the default one: size, modification time, device, inode, and
// the lead-in and metadata fingerprint. It does not write. WithSegmentIndex
// and WithSegmentIndexTrustIdentity are ignored, so a caller cannot weaken
// the check. WithLenientTail is honored and must match the open that built
// the sidecar when the file's final next-segment offset runs past EOF.
//
// A missing sidecar, a decode error, or an identity or fingerprint mismatch
// is false, nil. An error opening path, reading the sidecar, or walking
// lead-ins is returned.
func SegmentIndexValid(path string, opts ...ReaderOption) (bool, error) {
	cfg := applyReaderOptions(opts)
	rf, err := os.Open(path)
	if err != nil {
		return false, wrap("index", path, err)
	}
	defer rf.Close()
	fi, err := rf.Stat()
	if err != nil {
		return false, wrap("index", path, err)
	}
	if !fi.Mode().IsRegular() {
		return false, nil
	}
	side := SegmentIndexPath(path)
	sf, err := os.Stat(side)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, wrap("index", side, err)
	}
	if sf.Size() == 0 || sf.Size() > segmentIndexLimit {
		return false, nil
	}
	blob, err := readFileLimited(side, segmentIndexLimit)
	if err != nil {
		return false, wrap("index", side, err)
	}
	img, err := decodeSegmentIndex(blob)
	if err != nil {
		return false, nil
	}
	dev, ino, err := fileID(rf)
	if err != nil {
		return false, wrap("index", path, err)
	}
	if img.size != fi.Size() || img.mtime != fi.ModTime().UnixNano() || img.dev != dev || img.ino != ino {
		return false, nil
	}
	if img.truncated && !cfg.lenientTail {
		return false, nil
	}
	fp, err := segmentFingerprint(rf, fi.Size(), cfg.lenientTail)
	if err != nil {
		return false, wrap("index", path, err)
	}
	return fp == img.fp, nil
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
}, size int64, lenient bool) (uint32, error) {
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
		segEnd, _, _, err := segmentBounds(pos, size, lead, lenient)
		if err != nil {
			return 0, err
		}
		_, _ = h.Write(hdr[:])
		if lead.raw > 0 {
			// Bound the allocation by the bytes left after this lead-in, not
			// the whole file, so a corrupt metadata length near EOF cannot
			// force a file-sized buffer before readFull fails a few bytes in.
			if lead.raw > uint64(size-pos-leadInSize) {
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
	return encodeSegmentIndexVer(f, size, mtime, dev, ino, fp, segmentIndexVersion)
}

// encodeSegmentIndexVer writes a version 2 sidecar, or a version 1 sidecar
// of the spans as they stand (96 bytes each, no run fields). Version 1
// exists so tests can check that an older sidecar still hits. A test that
// wants one span per segment expands runs first. New writes use
// encodeSegmentIndex.
func encodeSegmentIndexVer(f *File, size, mtime int64, dev, ino uint64, fp uint32, version uint32) ([]byte, error) {
	if version != segmentIndexV1 && version != segmentIndexVersion {
		return nil, errIndex
	}
	payload, err := encodeIndexPayload(f, version == segmentIndexVersion)
	if err != nil {
		return nil, err
	}
	return finishSegmentIndex(f, payload, size, mtime, dev, ino, fp, version)
}

func finishSegmentIndex(f *File, payload []byte, size, mtime int64, dev, ino uint64, fp, version uint32) ([]byte, error) {
	buf := make([]byte, segmentIndexHeader+len(payload))
	copy(buf[0:4], segmentIndexMagic)
	binary.LittleEndian.PutUint32(buf[4:8], version)
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
	if f.truncated {
		flags |= indexFlagTruncated
	}
	binary.LittleEndian.PutUint32(buf[56:60], flags)
	binary.LittleEndian.PutUint64(buf[64:72], uint64(f.unusedTail))
	copy(buf[segmentIndexHeader:], payload)
	return buf, nil
}

func encodeIndexPayload(f *File, v2 bool) ([]byte, error) {
	w := &ixw{v2: v2}
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
			switch {
			case ch.daqmx != nil && ch.hasDT:
				w.u8(indexHasDAQmx)
			case ch.hasDT:
				w.u8(indexHasType)
			default:
				w.u8(0)
			}
			if err := w.props(ch.props); err != nil {
				return nil, err
			}
			w.u32(uint32(len(ch.spans)))
			for _, sp := range ch.spans {
				w.span(sp, ch.runs)
			}
			w.i64(ch.total)
			if ch.daqmx != nil && ch.hasDT {
				w.daqmxBlock(ch.daqmx)
			}
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
	ver := binary.LittleEndian.Uint32(b[4:8])
	var v2 bool
	switch ver {
	case segmentIndexV1:
	case segmentIndexVersion:
		v2 = true
	default:
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
	if flags&^(indexFlagIncomplete|indexFlagPartial|indexFlagTruncated) != 0 {
		return nil, errIndex
	}
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
		truncated:  flags&indexFlagTruncated != 0,
		unused:     int64(binary.LittleEndian.Uint64(b[64:72])),
	}
	if img.incomplete && img.truncated {
		return nil, errIndex
	}
	if img.version != Version10 && img.version != Version20 {
		return nil, errIndex
	}
	if img.segs < 1 {
		return nil, errIndex
	}
	if img.partial && !img.incomplete && !img.truncated {
		return nil, errIndex
	}
	if img.unused < 0 || (img.unused > 0 && !img.incomplete && !img.truncated) {
		return nil, errIndex
	}
	// Both versions are at least 96 bytes per span. A version 2 run is 16
	// bytes longer, so the count bound uses the shorter record.
	r := ixr{b: payload, spanSize: spanRecordV1, v2: v2}
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
			if has > indexHasDAQmx {
				return nil, errIndex
			}
			cprops, err := r.props()
			if err != nil {
				return nil, err
			}
			ns, err := r.count(r.spanSize)
			if err != nil {
				return nil, err
			}
			spans := make([]span, ns)
			var runs []spanRun
			for k := 0; k < ns; k++ {
				sp, err := r.span(&runs)
				if err != nil {
					return nil, err
				}
				if sp.live {
					anyLive = true
				}
				if sp.live && !img.incomplete && !img.truncated {
					return nil, errIndex
				}
				if err := spanTypeOK(DataType(dt), &sp, runs); err != nil {
					return nil, err
				}
				spans[k] = sp
			}
			total, err := r.i64()
			if err != nil {
				return nil, err
			}
			got, err := spanTotal(spans, runs)
			if err != nil || got != total {
				return nil, errIndex
			}
			var dx *daqmxState
			if has == indexHasDAQmx {
				dx, err = r.daqmxBlock(spans, runs, total, DataType(dt))
				if err != nil {
					return nil, err
				}
			}
			ch := &Channel{
				name:  cn,
				path:  cp,
				dt:    DataType(dt),
				hasDT: has == indexHasType || has == indexHasDAQmx,
				props: cprops,
				spans: spans,
				runs:  runs,
				total: total,
				daqmx: dx,
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
	if anyLive && !img.incomplete && !img.truncated {
		return nil, errIndex
	}
	// Version 2 is written already merged. Coalescing it again walks every
	// single-segment span on a file that cannot form a run. Version 1 is one
	// span per segment and still needs the fold.
	if !v2 {
		img.coalesce()
		for _, g := range img.groups {
			for _, ch := range g.channels {
				got, err := spanTotal(ch.spans, ch.runs)
				if err != nil || got != ch.total {
					return nil, errIndex
				}
				if ch.daqmx == nil {
					continue
				}
				for i, sps := range ch.daqmx.spans {
					got, err := spanTotal(sps, ch.daqmx.runs[i])
					if err != nil || got != ch.daqmx.totals[i] {
						return nil, errIndex
					}
				}
			}
		}
	}
	if err := spansInFile(img); err != nil {
		return nil, err
	}
	return img, nil
}

// spansInFile rejects a sidecar whose spans address bytes outside the file
// they claim to describe, or whose sample counts cannot fit in that file.
// Each channel, and each non-primary DAQmx scaler, is capped on its own:
// overlapping spans that individually fit can still sum past the file, and
// that sum is what Values would allocate. decodeSegmentIndex returns
// errIndex, and Open treats that as a cache miss.
func spansInFile(img *indexImage) error {
	if img.size < 0 {
		return errIndex
	}
	for _, g := range img.groups {
		for _, ch := range g.channels {
			if err := spanListInFile(ch.spans, ch.runs, img.size); err != nil {
				return err
			}
			if ch.daqmx == nil || ch.daqmx.spec == nil {
				continue
			}
			for i, sps := range ch.daqmx.spans {
				if i == ch.daqmx.spec.primary {
					continue
				}
				if err := spanListInFile(sps, ch.daqmx.runs[i], img.size); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// spanListInFile applies spanInFile to every span and then caps the list.
// Fixed-width and digital samples contribute samples*typeSize bytes. Digital
// lines are bit-packed, so several channels share each raw byte; the cap is
// per channel, and typeSize is 1, so each line may use up to the whole file.
// Interleaved channels count only their own type width, which is a subset of
// the stride already bounded by the chunk extent. Strings are not merged into
// runs. Each string sample has a 4-byte offset, so the sample count is capped
// at size/4. The resulting []string header is a constant multiple of that
// count, so the allocation stays proportional to the file.
func spanListInFile(spans []span, runs []spanRun, size int64) error {
	var samples int64
	var bytes int64
	strings := false
	for i := range spans {
		n, err := spanInFile(spans[i], runs, size)
		if err != nil {
			return err
		}
		samples, err = add64(samples, n)
		if err != nil {
			return errIndex
		}
		if spans[i].str {
			strings = true
			continue
		}
		if n == 0 {
			continue
		}
		sz := int64(spans[i].typeSize)
		if sz < 1 {
			return errIndex
		}
		b, err := mul64(n, sz)
		if err != nil {
			return errIndex
		}
		bytes, err = add64(bytes, b)
		if err != nil || bytes > size {
			return errIndex
		}
	}
	if strings && samples > size/4 {
		return errIndex
	}
	return nil
}

func spanInFile(sp span, runs []spanRun, size int64) (int64, error) {
	reps := sp.repeats(runs)
	if sp.runIdx != 0 && (int(sp.runIdx) >= len(runs) || runs[sp.runIdx].count < 2 || runs[sp.runIdx].step <= 0 || sp.partialN != 0 || sp.str || sp.live) {
		return 0, errIndex
	}
	if err := spanExtent(sp, sp.dataPos, size); err != nil {
		return 0, err
	}
	if reps > 1 {
		// Last repeat: start + step*(count-1), then the same chunk extent.
		// step > 0 and extent >= 0, so a last end inside the file puts every
		// earlier repeat inside it too. The product is overflow-checked.
		delta, err := mul64(reps-1, sp.repeatStep(runs))
		if err != nil {
			return 0, errIndex
		}
		last, err := add64(sp.dataPos, delta)
		if err != nil {
			return 0, errIndex
		}
		if err := spanExtent(sp, last, size); err != nil {
			return 0, err
		}
	}
	samples, err := spanSamples(&sp, runs)
	if err != nil || samples < 0 {
		return 0, errIndex
	}
	if sp.str {
		if reps > 1 {
			return 0, errIndex
		}
		if sp.nPerChunk > 0 {
			table, err := mul64(sp.nPerChunk, 4)
			if err != nil || sp.strSize < table {
				return 0, errIndex
			}
		}
		if sp.nChunks > 0 {
			bytes, err := mul64(sp.nChunks, sp.strSize)
			if err != nil || bytes > size {
				return 0, errIndex
			}
		}
		if sp.partialN > 0 && (sp.partialPos < 0 || sp.partialPos > size) {
			return 0, errIndex
		}
		return samples, nil
	}
	if samples > 0 {
		if sp.typeSize <= 0 || sp.typeSize > 16 {
			return 0, errIndex
		}
		b, err := mul64(samples, int64(sp.typeSize))
		if err != nil || b > size {
			return 0, errIndex
		}
		if sp.nPerChunk > 1 && sp.stride <= 0 {
			return 0, errIndex
		}
		// The per-chunk strided walk must stay inside one chunk. Otherwise a
		// read addresses the next chunk, or runs past EOF for the last chunk,
		// returning unrelated bytes on a large file. A fresh parse builds this
		// invariant from the real layout; a sidecar supplies col, stride, and
		// nPerChunk untrusted, so it is re-checked here.
		if sp.nChunks > 0 && sp.nPerChunk > 0 {
			end, err := strideExtent(sp.col, sp.stride, sp.nPerChunk, int64(sp.typeSize))
			if err != nil || end > sp.chunkSize {
				return 0, errIndex
			}
		}
	}
	if sp.partialN > 0 {
		if sp.partialPos < 0 || sp.partialPos > size {
			return 0, errIndex
		}
		// The partial tail is addressed from partialPos with its own column and
		// stride; its walk must end inside the file.
		end, err := strideExtent(sp.partialCol, sp.partialStride, sp.partialN, int64(sp.typeSize))
		if err != nil {
			return 0, errIndex
		}
		tail, err := add64(sp.partialPos, end)
		if err != nil || tail > size {
			return 0, errIndex
		}
	}
	return samples, nil
}

// strideExtent is the byte just past the last sample of a strided walk of n
// samples: col + (n-1)*stride + typeSize, overflow-checked. The walk fits a
// region of w bytes when the extent is <= w.
func strideExtent(col, stride, n, typeSize int64) (int64, error) {
	if n <= 0 {
		return 0, nil
	}
	reach, err := mul64(n-1, stride)
	if err != nil {
		return 0, err
	}
	end, err := add64(col, reach)
	if err != nil {
		return 0, err
	}
	return add64(end, typeSize)
}

// spanExtent reports whether the chunk region that starts at dataPos lies
// inside a file of the given size. Partial-tail positions are checked once
// by spanInFile; a run has no partial tail.
func spanExtent(sp span, dataPos, size int64) error {
	if dataPos < 0 || dataPos > size {
		return errIndex
	}
	if sp.nChunks > 0 {
		region, err := mul64(sp.nChunks, sp.chunkSize)
		if err != nil {
			return errIndex
		}
		end, err := add64(dataPos, region)
		if err != nil || end > size {
			return errIndex
		}
		if sp.chunkSize == 0 && (sp.nPerChunk > 0 || sp.strSize > 0) {
			return errIndex
		}
	}
	return nil
}

func spanTotal(spans []span, runs []spanRun) (int64, error) {
	var n int64
	for i := range spans {
		part, err := spanSamples(&spans[i], runs)
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

type ixw struct {
	b  []byte
	v2 bool
}

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

func (w *ixw) span(sp span, runs []spanRun) {
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
	if sp.digital {
		flags |= spanFlagDigital
		flags |= uint32(sp.bit) << spanBitShift
	}
	if sp.solo {
		flags |= spanFlagSolo
	}
	rc := int64(1)
	step := int64(0)
	if sp.runIdx != 0 && int(sp.runIdx) < len(runs) && runs[sp.runIdx].count > 1 {
		rc = runs[sp.runIdx].count
		step = runs[sp.runIdx].step
	}
	if w.v2 && rc > 1 {
		flags |= spanFlagRun
	}
	w.u32(flags)
	w.i64(sp.strSize)
	w.i64(sp.partialN)
	w.i64(sp.partialPos)
	w.i64(sp.partialCol)
	w.i64(sp.partialStride)
	if !w.v2 || rc <= 1 {
		return
	}
	w.i64(step)
	w.i64(rc)
}

type ixr struct {
	b        []byte
	off      int
	spanSize int
	v2       bool
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
	// n is bounded by the bytes left, not by memory: a Property is far
	// larger than its 4-byte minimum on disk. Grow as records decode.
	out := make([]Property, 0, min(n, 64))
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

func (r *ixr) span(runs *[]spanRun) (span, error) {
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
	if r.v2 && flags&spanFlagRun != 0 {
		step, err := r.i64()
		if err != nil {
			return span{}, err
		}
		count, err := r.i64()
		if err != nil {
			return span{}, err
		}
		if runs == nil || count < 2 || step <= 0 {
			return span{}, errIndex
		}
		*runs, sp.runIdx = allocRun(*runs, count, step)
	}
	if uint64(sz) > uint64(^uint(0)>>1) {
		return span{}, errIndex
	}
	sp.typeSize = int(sz)
	sp.big = flags&spanFlagBig != 0
	sp.str = flags&spanFlagStr != 0
	sp.live = flags&spanFlagLive != 0
	sp.digital = flags&spanFlagDigital != 0
	sp.solo = flags&spanFlagSolo != 0
	sp.bit = uint8((flags >> spanBitShift) & 0xFF)
	known := uint32(spanFlagBig | spanFlagStr | spanFlagLive | spanFlagDigital | spanFlagSolo | (0xFF << spanBitShift))
	if r.v2 {
		known |= spanFlagRun
	}
	if flags&^known != 0 {
		return span{}, errIndex
	}
	if sp.runIdx != 0 && (sp.partialN != 0 || sp.str || sp.live) {
		return span{}, errIndex
	}
	if sp.digital {
		if sp.bit > 7 {
			return span{}, errIndex
		}
	} else if sp.bit != 0 {
		return span{}, errIndex
	}
	if sp.dataPos < 0 || sp.chunkSize < 0 || sp.nChunks < 0 || sp.nPerChunk < 0 ||
		sp.col < 0 || sp.stride < 0 || sp.strSize < 0 || sp.partialN < 0 ||
		sp.partialPos < 0 || sp.partialCol < 0 || sp.partialStride < 0 {
		return span{}, errIndex
	}
	return sp, nil
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
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
	if err := tmp.Chmod(perm); err != nil && !isUnsupportedChmod(err) {
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
	syncDir(dir)
	return nil
}

// isUnsupportedChmod reports a chmod failure from a filesystem without mode
// bits (some network shares). The sidecar is still usable there.
func isUnsupportedChmod(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOTSUP)
}

// syncDir makes the rename durable where the platform allows opening a
// directory. It is best effort: a failure leaves a valid sidecar that may
// not survive a crash, and Open treats a missing sidecar as a miss.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		// Windows cannot flush a directory handle; the call would only fail.
		return
	}
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// replaceFile renames tmp over dst. os.Rename replaces an existing file on
// every supported platform, including Windows (MoveFileEx with
// MOVEFILE_REPLACE_EXISTING). The remove-and-retry fallback is limited to a
// regular file at dst, so it never deletes a directory or other object.
func replaceFile(tmp, dst string) error {
	err := os.Rename(tmp, dst)
	if err == nil {
		return nil
	}
	st, lerr := os.Lstat(dst)
	if lerr != nil || !st.Mode().IsRegular() {
		return err
	}
	if rmErr := os.Remove(dst); rmErr != nil && !os.IsNotExist(rmErr) {
		return err
	}
	return os.Rename(tmp, dst)
}

// spanTypeOK rejects a span whose sample width is not the channel data
// type's width. A fresh parse always sets typeSize from the data type, and
// readers size their output buffers from the data type, so a mismatch from
// a crafted sidecar would otherwise index past those buffers. A single
// segment with a positive chunk count is checked without a second sample
// multiply; the sample cap still overflow-checks that count.
func spanTypeOK(dt DataType, sp *span, runs []spanRun) error {
	if dt == TypeString {
		if !sp.str {
			return errIndex
		}
		return nil
	}
	if sp.str {
		return errIndex
	}
	if sp.runIdx == 0 && sp.partialN == 0 && (sp.nChunks == 0 || sp.nPerChunk == 0) {
		return nil
	}
	sz, ok := dt.Size()
	if sp.runIdx == 0 && sp.partialN == 0 {
		if !ok || sp.typeSize != sz {
			return errIndex
		}
		return nil
	}
	n, err := spanSamples(sp, runs)
	if err != nil {
		return errIndex
	}
	if n == 0 {
		return nil
	}
	if !ok || sp.typeSize != sz {
		return errIndex
	}
	return nil
}
