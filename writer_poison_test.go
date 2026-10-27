package tdms

import (
	"errors"
	"io"
	"testing"
)

// seekStartFailWS is a seekable in-memory writer whose SeekStart fails. The
// constructor's Seek(0, SeekCurrent) still succeeds, so the writer is seekable,
// but patchNext's Seek(at+12, SeekStart) fails after a segment's bytes have
// already been flushed to the underlying buffer.
type seekStartFailWS struct {
	b   []byte
	pos int
}

func (m *seekStartFailWS) Write(p []byte) (int, error) {
	end := m.pos + len(p)
	if end > len(m.b) {
		m.b = append(m.b, make([]byte, end-len(m.b))...)
	}
	copy(m.b[m.pos:], p)
	m.pos = end
	return len(p), nil
}

func (m *seekStartFailWS) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		m.pos += int(off)
	case io.SeekStart:
		return 0, errors.New("injected seek failure")
	case io.SeekEnd:
		m.pos = len(m.b) + int(off)
	default:
		return 0, errors.New("bad whence")
	}
	return int64(m.pos), nil
}

// TestFlushPatchFailurePoisonsWriter guards against a buffered (non-sync)
// segment write whose patchNext fails: the writer must become unusable so a
// later Flush or Close does not append a second copy of the segment and
// corrupt the file.
func TestFlushPatchFailurePoisonsWriter(t *testing.T) {
	ws := &seekStartFailWS{}
	w, err := NewWriter(ws)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	if err := ch.Write([]float64{1, 2, 3, 4}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Flush(); err == nil {
		t.Fatal("Flush: want error from failed patch, got nil")
	}
	wrote := len(ws.b)
	if wrote == 0 {
		t.Fatal("expected the first segment's bytes to be written before the patch failed")
	}

	// The writer must be poisoned: no further buffered writes succeed.
	if err := ch.Write([]float64{5}); err == nil {
		t.Fatal("Write after failed Flush: want error, got nil")
	}
	if err := w.Flush(); err == nil {
		t.Fatal("second Flush: want error, got nil")
	}

	// Close must report the failure and must not append a second segment.
	if err := w.Close(); err == nil {
		t.Fatal("Close: want the sticky error, got nil")
	}
	if len(ws.b) != wrote {
		t.Fatalf("Close appended %d bytes after the failed flush; want 0 (no duplicate segment)", len(ws.b)-wrote)
	}
}

// TestCloseNilWriter documents that Close on a nil *Writer returns an argument
// error instead of panicking, matching Sync, Flush, and busy.
func TestCloseNilWriter(t *testing.T) {
	var w *Writer
	if err := w.Close(); !errors.Is(err, ErrArgument) {
		t.Fatalf("nil Writer Close: want ErrArgument, got %v", err)
	}
}
