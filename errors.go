package tdms

import "fmt"

// Sentinel errors. Use errors.Is. Public functions wrap these in *Error so the
// operation and, when there is one, the object path are included in the text.
var (
	// ErrBadTag means a segment does not start with the TDSm tag.
	ErrBadTag = errSentinel("segment tag is not TDSm")
	// ErrIndexFile means the file is a *.tdms_index (TDSh) companion, which
	// stores metadata only. Open the .tdms data file instead.
	ErrIndexFile = errSentinel("TDSh index file has no raw data; open the .tdms file")
	// ErrVersion means the segment version is neither 4712 (v1.0) nor 4713 (v2.0).
	ErrVersion = errSentinel("unsupported TDMS version")
	// ErrTruncated means the file ended before a segment, value, or length prefix finished.
	ErrTruncated = errSentinel("truncated TDMS data")
	// ErrCorrupt means a length, offset, or object path contradicts the TDMS rules.
	ErrCorrupt = errSentinel("corrupt TDMS structure")
	// ErrDAQmx means a DAQmx raw-data index uses a layout this package does not
	// decode. Supported format-changing scalers (index words 0x00001269 and
	// 0x00001369) and digital-line scalers (0x0000126A) are read. A non-zero
	// sample-format bitmap, an unknown DAQmx type code, a DAQmx index whose
	// data type is neither 0xFFFFFFFF nor its one scaler's type, or a segment
	// that mixes DAQmx and standard channels still returns ErrDAQmx.
	ErrDAQmx = errSentinel("unsupported DAQmx raw data")
	// ErrUnsupported means a data type or layout this package does not implement,
	// such as fixed-point (0x4F) or a multi-channel interleaved string segment.
	ErrUnsupported = errSentinel("unsupported TDMS feature")
	// ErrType means a Go value does not match the channel or property type.
	ErrType = errSentinel("data type mismatch")
	// ErrLayout means an interleaved segment is not well-defined for its channels.
	ErrLayout = errSentinel("invalid interleaved layout")
	// ErrArgument means an API argument is out of range.
	ErrArgument = errSentinel("invalid argument")
	// ErrClosed means the file or writer was already closed.
	ErrClosed = errSentinel("closed")
)

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// Error is a TDMS operation error. Unwrap yields the sentinel or the
// underlying I/O error.
type Error struct {
	Op   string // "open", "read", "write", "flush"
	Path string // object path, file path, or ""
	Err  error
}

func (e *Error) Error() string {
	if e.Path == "" {
		return "tdms: " + e.Op + ": " + e.Err.Error()
	}
	return "tdms: " + e.Op + " " + e.Path + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

func wrap(op, path string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Op: op, Path: path, Err: err}
}

func wrapf(op, path string, err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return &Error{Op: op, Path: path, Err: fmt.Errorf(format+": %w", append(args, err)...)}
}
