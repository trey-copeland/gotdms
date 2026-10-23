package dataset

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	tdms "github.com/trey-copeland/gotdms"
)

// A file with a torn tail fails OpenFiles by default. WithLenientTail
// keeps its whole segments for indexing and reads, and FileStatus says how
// many bytes were dropped.
func TestLenientTailTornFile(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", []float64{0, 1}, []float64{0, 1})
	torn := filepath.Join(dir, "b.tdms")
	writePair(t, torn, "acq", "ai0", []float64{2, 3}, []float64{2, 3})
	f, err := os.OpenFile(torn, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	base := []Option{WithTimestampChannelName("Time"), WithNumericEpoch(epoch)}
	if _, err := OpenGlob(dir, "*.tdms", base...); !errors.Is(err, tdms.ErrBadTag) {
		t.Fatalf("strict OpenGlob: %v", err)
	}
	for _, extra := range [][]Option{nil, {WithSegmentIndex(true)}} {
		ds, err := OpenGlob(dir, "*.tdms", append(append([]Option{WithLenientTail()}, base...), extra...)...)
		if err != nil {
			t.Fatal(err)
		}
		s, err := ds.Read(Selector{Name: "ai0"})
		if err != nil {
			t.Fatal(err)
		}
		if !sameFloats(s.Y, []float64{0, 1, 2, 3}) {
			t.Fatalf("Y = %v", s.Y)
		}
		files := ds.Files()
		st, ok := ds.FileStatus(files[1])
		if !ok || !st.Truncated || st.DroppedTail != 100 || !errors.Is(st.TailError, tdms.ErrBadTag) {
			t.Fatalf("torn status %#v %v", st, ok)
		}
		if st, ok := ds.FileStatus(files[0]); !ok || !st.Complete {
			t.Fatalf("clean status %#v %v", st, ok)
		}
		if _, ok := ds.FileStatus(filepath.Join(dir, "nope.tdms")); ok {
			t.Fatal("status for a file not in the dataset")
		}
		if _, err := os.Stat(tdms.SegmentIndexPath(torn)); !os.IsNotExist(err) {
			t.Fatalf("sidecar for the torn file: %v", err)
		}
	}
}
