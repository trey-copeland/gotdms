package tdms

// withoutSegmentIndexWrite reads a WithSegmentIndex sidecar but never
// rewrites it after a miss. It is defined in a _test.go file, so it is not
// compiled into the package that callers import: they always get the
// documented atomic, fsynced replacement.
func withoutSegmentIndexWrite() ReaderOption {
	return func(c *readerConfig) {
		c.indexNoWrite = true
	}
}
