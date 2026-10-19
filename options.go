package tdms

// Default sequential buffer sizes. Cursor uses DefaultReadBuffer for streaming
// scans. The writer uses DefaultWriteBuffer so a flush streams in large writes
// without a second full-segment allocation. Values and other random reads stay
// on unbuffered ReadAt.
const (
	DefaultReadBuffer  = 256 << 10
	DefaultWriteBuffer = 1 << 20
)

type readerConfig struct {
	bufSize      int
	indexSidecar string
}

// ReaderOption configures NewReader.
type ReaderOption func(*readerConfig)

// WithReadBuffer sets the sequential buffer used by Cursor. A non-positive
// size selects DefaultReadBuffer. Values ignores this and reads through ReadAt.
func WithReadBuffer(size int) ReaderOption {
	return func(c *readerConfig) {
		if size > 0 {
			c.bufSize = size
		}
	}
}

// WithSegmentIndex reads and writes a sidecar segment index at sidecar.
// An empty sidecar disables the option. The cache records the parsed segment
// layout so a later open can skip decoding lead-ins, object paths, raw-data
// indexes, and properties. Raw samples are not stored.
//
// The sidecar is used only when it validates against the data file. Validation
// requires the file size, the modification time in nanoseconds, the device and
// inode when the platform reports them, and an IEEE CRC-32 of every lead-in
// and metadata section in order. Raw sample bytes are not part of that
// fingerprint: a cache hit still reads the samples that are on disk now.
// A missing file, a mismatched identity, a bad checksum, an unknown version,
// or a truncated sidecar is ignored and the file is parsed. Open does not
// fail because the cache is unusable. After a full parse, Open replaces the
// sidecar atomically (a temporary file in the same directory, then rename).
// A failure of that write is ignored.
//
// Open uses the path it opened as the data file. NewReader uses that path
// when it was given one, otherwise the name of an *os.File. Without a data
// path the sidecar is skipped. sidecar should not be the data file itself;
// that pairing is ignored so the TDMS file is not overwritten. The conventional
// path is SegmentIndexPath.
//
// The on-disk layout is little-endian, version 1, magic "GTDX". It is not an
// NI *.tdms_index (TDSh) file. See SegmentIndexPath for the field list.
func WithSegmentIndex(sidecar string) ReaderOption {
	return func(c *readerConfig) {
		c.indexSidecar = sidecar
	}
}

type writerConfig struct {
	bufSize int
}

// WriterOption configures NewWriter.
type WriterOption func(*writerConfig)

// WithWriteBuffer sets the buffered writer size. A non-positive size selects
// DefaultWriteBuffer. The lead-in of a seekable writer is still patched with
// the underlying seeker after the buffer is flushed.
func WithWriteBuffer(size int) WriterOption {
	return func(c *writerConfig) {
		if size > 0 {
			c.bufSize = size
		}
	}
}
