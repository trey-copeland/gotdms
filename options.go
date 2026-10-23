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
	bufSize            int
	indexSidecar       string
	indexTrustIdentity bool
	lenientTail        bool
	// indexNoWrite keeps Open from replacing the sidecar after a miss. It is
	// set only by withoutSegmentIndexWrite in options_test.go, so it exists
	// only in test builds; fuzz tests use it so each iteration does not pay
	// for a durable rewrite. Production code never sets it.
	indexNoWrite bool
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

// WithLenientTail opens a file whose final segment declares a next-segment
// offset past EOF. Earlier segments are kept, and whole values that fit in
// the partial tail are readable. File.Status reports Truncated, and those
// tail samples are not committed, so Follow does not deliver them until the
// declared length is actually present. Without this option Open returns
// ErrTruncated and the earlier segments are not available. A trailing
// fragment shorter than a lead-in, or a final segment whose metadata itself
// runs past EOF, is still ErrTruncated.
func WithLenientTail() ReaderOption {
	return func(c *readerConfig) {
		c.lenientTail = true
	}
}

// WithSegmentIndex reads and writes a sidecar segment index at sidecar.
// An empty sidecar disables the option. The cache records the parsed segment
// layout (object paths, raw-data indexes, properties, and the spans Values
// uses). Raw samples are not stored. A cache hit still reads every lead-in
// and metadata section to recompute the fingerprint; it does not rebuild the
// object tree from those bytes.
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
// The on-disk layout is little-endian, version 2, magic "GTDX". Version 1
// sidecars still open. It is not an NI *.tdms_index (TDSh) file. See
// SegmentIndexPath for the field list.
//
// WithSegmentIndexTrustIdentity skips the fingerprint walk on a later open.
// The default, with that option unset, still recomputes the fingerprint.
func WithSegmentIndex(sidecar string) ReaderOption {
	return func(c *readerConfig) {
		c.indexSidecar = sidecar
	}
}

// WithSegmentIndexTrustIdentity makes WithSegmentIndex accept a sidecar when
// the data file's size, modification time in nanoseconds, device, and inode
// match the header. The lead-in and metadata fingerprint is not recomputed.
//
// The default stays the full fingerprint check. Trust this only for files
// whose bytes you know were not edited in place. An edit that preserves size
// and modification time, and that does not replace the inode, is missed: the
// object tree and spans come from the sidecar, not from the bytes now on
// disk. Restoring the modification time with os.Chtimes after such an edit
// is the usual way that happens. Changing the size or the modification time
// is still a cache miss. Where the platform reports a device and inode,
// replacing the file is a miss even when size and modification time were
// copied onto the new file.
//
// On Windows the device and inode are the volume serial number and file
// index from GetFileInformationByHandle, so a replaced file is a miss even
// when size and modification time were copied onto it. On a platform that
// still reports no device and inode, trust mode checks only size and
// modification time, and both identity fields are stored as 0. FAT records
// modification time in steps of 2 seconds, and exFAT in steps of 10
// milliseconds, so two writes inside one step share a timestamp. An edit
// that lands in the same step is missed, just as an edit whose timestamp
// was restored is missed.
//
// Decode bounds checks stay in force. A span that addresses bytes outside
// the file, or that claims more samples than the file can hold, is a decode
// error and a cache miss. It is not an out-of-bounds read. Sample bytes are
// still read from the file, so a raw-sample edit is visible when the cached
// spans still address those bytes.
//
// The option does nothing unless WithSegmentIndex names a sidecar. It does
// not change what BuildSegmentIndex writes. A sidecar whose final segment
// was recovered with WithLenientTail is still a miss when this open is not
// lenient, so a strict Open keeps returning ErrTruncated.
func WithSegmentIndexTrustIdentity() ReaderOption {
	return func(c *readerConfig) {
		c.indexTrustIdentity = true
	}
}

func applyReaderOptions(opts []ReaderOption) readerConfig {
	cfg := readerConfig{bufSize: DefaultReadBuffer}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.bufSize <= 0 {
		cfg.bufSize = DefaultReadBuffer
	}
	return cfg
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
