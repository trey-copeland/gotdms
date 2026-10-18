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
	bufSize int
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
