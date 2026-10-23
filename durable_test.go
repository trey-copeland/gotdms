package tdms

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// syncFile is an in-memory io.WriteSeeker with Sync. It logs each operation
// and keeps a copy of the bytes at every Sync: what a power loss right after
// that Sync would leave on disk.
type syncFile struct {
	buf     []byte
	pos     int64
	ops     []string
	durable [][]byte
	failAt  int // fail the n-th Sync (1-based); 0 never fails
	failErr error
	syncs   int
}

func (f *syncFile) Write(p []byte) (int, error) {
	end := f.pos + int64(len(p))
	if end > int64(len(f.buf)) {
		f.buf = append(f.buf, make([]byte, end-int64(len(f.buf)))...)
	}
	copy(f.buf[f.pos:], p)
	f.ops = append(f.ops, fmt.Sprintf("write %d+%d", f.pos, len(p)))
	f.pos = end
	return len(p), nil
}

func (f *syncFile) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.pos = off
	case io.SeekCurrent:
		f.pos += off
	case io.SeekEnd:
		f.pos = int64(len(f.buf)) + off
	}
	return f.pos, nil
}

func (f *syncFile) Sync() error {
	f.syncs++
	if f.failAt != 0 && f.syncs == f.failAt {
		f.ops = append(f.ops, "sync-fail")
		if f.failErr != nil {
			return f.failErr
		}
		return errors.New("injected sync failure")
	}
	f.ops = append(f.ops, "sync")
	f.durable = append(f.durable, append([]byte(nil), f.buf...))
	return nil
}

func (f *syncFile) count(op string) int {
	n := 0
	for _, o := range f.ops {
		if o == op {
			n++
		}
	}
	return n
}

// readFloats opens a snapshot and returns g/c, failing on any open error.
func readFloats(t *testing.T, snap []byte) ([]float64, FileStatus) {
	t.Helper()
	f, err := NewReader(bytes.NewReader(snap))
	if err != nil {
		t.Fatalf("durable snapshot of %d bytes does not open: %v", len(snap), err)
	}
	g, ok := f.Group("g")
	if !ok {
		return nil, f.Status()
	}
	c, ok := g.Channel("c")
	if !ok {
		return nil, f.Status()
	}
	v, err := c.Values()
	if err != nil {
		t.Fatal(err)
	}
	return v.([]float64), f.Status()
}

func patchOp(segStart int64) string { return fmt.Sprintf("write %d+8", segStart+12) }

func TestWithSyncOrdersDataThenLeadIn(t *testing.T) {
	f := &syncFile{}
	w, err := NewWriter(f, WithSync())
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	var segStarts []int64
	for seg := 0; seg < 3; seg++ {
		segStarts = append(segStarts, int64(len(f.buf)))
		if err := ch.Write([]float64{float64(seg), float64(seg) + 0.5}); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Each segment: data writes, sync, 8-byte patch, sync.
	for _, start := range segStarts {
		i := indexOf(f.ops, patchOp(start))
		if i < 1 || f.ops[i-1] != "sync" || i+1 >= len(f.ops) || f.ops[i+1] != "sync" {
			t.Fatalf("segment at %d: patch not between syncs: %v", start, f.ops)
		}
	}
	if got := f.count("sync"); got != 2*len(segStarts) {
		t.Fatalf("%d syncs for %d segments: %v", got, len(segStarts), f.ops)
	}
	// Every durable state opens, and keeps a prefix of the values.
	want := []float64{0, 0.5, 1, 1.5, 2, 2.5}
	for i, snap := range f.durable {
		vals, st := readFloats(t, snap)
		if len(vals) > len(want) || !equalF(vals, want[:len(vals)]) {
			t.Fatalf("snapshot %d: %v", i, vals)
		}
		if i%2 == 0 && !st.Incomplete {
			t.Fatalf("snapshot %d before the patch should end on the sentinel: %+v", i, st)
		}
		if i%2 == 1 && !st.Complete {
			t.Fatalf("snapshot %d after the patch should be complete: %+v", i, st)
		}
	}
	if vals, _ := readFloats(t, f.durable[len(f.durable)-1]); !equalF(vals, want) {
		t.Fatalf("final durable values %v", vals)
	}
}

func TestWithSyncStreamFinish(t *testing.T) {
	f := &syncFile{}
	w, err := NewWriter(f, WithSync())
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	st, err := ch.Stream(4)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ch, []float64{1, 2}); err != nil {
		t.Fatal(err)
	}
	// Writer.Sync mid-stream commits the values so far; the lead-in is
	// still the sentinel.
	if err := w.Sync(); err != nil {
		t.Fatal(err)
	}
	vals, status := readFloats(t, f.durable[len(f.durable)-1])
	if !equalF(vals, []float64{1, 2}) || !status.Incomplete {
		t.Fatalf("mid-stream sync: %v %+v", vals, status)
	}
	if err := st.Write(ch, []float64{3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := st.Finish(); err != nil {
		t.Fatal(err)
	}
	i := indexOf(f.ops, patchOp(0))
	if i < 1 || f.ops[i-1] != "sync" || i+1 >= len(f.ops) || f.ops[i+1] != "sync" {
		t.Fatalf("stream patch not between syncs: %v", f.ops)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	vals, status = readFloats(t, f.durable[len(f.durable)-1])
	if !equalF(vals, []float64{1, 2, 3, 4}) || !status.Complete {
		t.Fatalf("after finish: %v %+v", vals, status)
	}
}

func TestDefaultWriterNeverSyncs(t *testing.T) {
	f := &syncFile{}
	w, err := NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := w.Group("g").Channel("c", TypeFloat64)
	_ = ch.Write([]float64{1})
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if n := f.count("sync"); n != 0 {
		t.Fatalf("default writer synced %d times", n)
	}
}

func TestWriterSyncWithoutOption(t *testing.T) {
	f := &syncFile{}
	w, err := NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := w.Group("g").Channel("c", TypeFloat64)
	_ = ch.Write([]float64{7, 8})
	if err := w.Sync(); err != nil {
		t.Fatal(err)
	}
	if f.count("sync") != 1 {
		t.Fatalf("Sync did not fsync once: %v", f.ops)
	}
	vals, st := readFloats(t, f.durable[0])
	if !equalF(vals, []float64{7, 8}) || !st.Complete {
		t.Fatalf("Sync wrote %v %+v", vals, st)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Sync(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Sync after Close = %v", err)
	}
}

func TestWithSyncNeedsSyncer(t *testing.T) {
	if _, err := NewWriter(&bytes.Buffer{}, WithSync()); !errors.Is(err, ErrArgument) {
		t.Fatalf("WithSync on bytes.Buffer = %v", err)
	}
	// Writer.Sync on a writer without Sync only flushes.
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := w.Group("g").Channel("c", TypeFloat64)
	_ = ch.Write([]float64{1})
	if err := w.Sync(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("Sync did not flush to the buffer")
	}
}

func TestSyncFailureIsSticky(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		f := &syncFile{failAt: failAt}
		w, err := NewWriter(f, WithSync())
		if err != nil {
			t.Fatal(err)
		}
		ch, _ := w.Group("g").Channel("c", TypeFloat64)
		_ = ch.Write([]float64{1})
		if err := w.Flush(); err == nil {
			t.Fatalf("failAt %d: Flush succeeded", failAt)
		}
		if err := ch.Write([]float64{2}); err == nil {
			t.Fatalf("failAt %d: Write after a failed sync succeeded", failAt)
		}
		if err := w.Sync(); err == nil {
			t.Fatalf("failAt %d: Sync after a failed sync succeeded", failAt)
		}
		if err := w.Close(); err == nil {
			t.Fatalf("failAt %d: Close after a failed sync succeeded", failAt)
		}
		if failAt == 1 {
			// The patch must not be written when the data sync failed.
			if indexOf(f.ops, patchOp(0)) >= 0 {
				t.Fatalf("lead-in patched after a failed data sync: %v", f.ops)
			}
		}
	}
}

func TestCreateWithSyncRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.tdms")
	w, err := Create(path, WithSync(), WithWriteBuffer(4096))
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := ch.Write([]float64{float64(i)}); err != nil {
			t.Fatal(err)
		}
		if err := w.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	vals, st := readFloats(t, data)
	if !equalF(vals, []float64{0, 1, 2}) || !st.Complete {
		t.Fatalf("%v %+v", vals, st)
	}
	// Create without options still compiles and works.
	w2, err := Create(filepath.Join(t.TempDir(), "plain.tdms"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(data[4:8])&TocMetaData == 0 {
		t.Fatal("first segment has no metadata")
	}
}

func indexOf(ops []string, op string) int {
	for i, o := range ops {
		if o == op {
			return i
		}
	}
	return -1
}

func equalF(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Writer.Sync on a WithSync writer does not fsync a third time: Flush
// already synced the data and the patched lead-in.
func TestWriterSyncOnDurableWriterSyncsTwicePerSegment(t *testing.T) {
	f := &syncFile{}
	w, err := NewWriter(f, WithSync())
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := w.Group("g").Channel("c", TypeFloat64)
	_ = ch.Write([]float64{1, 2})
	if err := w.Sync(); err != nil {
		t.Fatal(err)
	}
	if n := f.count("sync"); n != 2 {
		t.Fatalf("WithSync + Sync made %d fsyncs, want 2: %v", n, f.ops)
	}
	if err := w.Sync(); err != nil {
		t.Fatal(err)
	}
	if n := f.count("sync"); n != 2 {
		t.Fatalf("idle Sync on a WithSync writer fsynced again: %v", f.ops)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// An fsync the file cannot do (EINVAL, as on a Unix pipe) is returned by
// Writer.Sync but does not poison a default writer.
func TestWriterSyncUnsupportedIsNotSticky(t *testing.T) {
	f := &syncFile{failAt: 1, failErr: &os.PathError{Op: "sync", Path: "pipe", Err: syscall.EINVAL}}
	w, err := NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := w.Group("g").Channel("c", TypeFloat64)
	_ = ch.Write([]float64{1})
	if err := w.Sync(); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("Sync = %v, want EINVAL", err)
	}
	_ = ch.Write([]float64{2})
	if err := w.Flush(); err != nil {
		t.Fatalf("writer poisoned by an unsupported fsync: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	vals, _ := readFloats(t, f.buf)
	if !equalF(vals, []float64{1, 2}) {
		t.Fatalf("values %v", vals)
	}
	// Any other failure is still sticky.
	g := &syncFile{failAt: 1}
	w2, _ := NewWriter(g)
	ch2, _ := w2.Group("g").Channel("c", TypeFloat64)
	_ = ch2.Write([]float64{1})
	if err := w2.Sync(); err == nil {
		t.Fatal("injected failure not returned")
	}
	if err := w2.Flush(); err == nil {
		t.Fatal("a failed fsync must leave the writer unusable")
	}
}
