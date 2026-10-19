package tdms

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func FuzzReader(f *testing.F) {
	f.Add([]byte("TDSm"))
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xff}, 64))
	addWritten(f, func(w *Writer) error { return nil })
	addWritten(f, func(w *Writer) error {
		ch, err := w.Group("g").Channel("c", TypeFloat64)
		if err != nil {
			return err
		}
		if err := ch.Write([]float64{1, 2, 3}); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return err
		}
		return ch.Write([]float64{4, 5})
	})
	addWritten(f, func(w *Writer) error {
		ch, err := w.Group("g").Channel("s", TypeString)
		if err != nil {
			return err
		}
		return ch.Write([]string{"ab", "", "c,d"})
	})
	addWritten(f, func(w *Writer) error {
		w.SetInterleaved(true)
		a, err := w.Group("g").Channel("a", TypeInt32)
		if err != nil {
			return err
		}
		b, err := w.Group("g").Channel("b", TypeFloat64)
		if err != nil {
			return err
		}
		if err := a.Write([]int32{1, 2, 3}); err != nil {
			return err
		}
		return b.Write([]float64{0.5, 1.5, 2.5})
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		file, err := NewReader(bytes.NewReader(data))
		if err != nil {
			return
		}
		defer file.Close()
		_ = file.Version()
		_ = file.Properties()
		_ = file.Status()
		for _, g := range file.Groups() {
			_ = g.Properties()
			for _, ch := range g.Channels() {
				if ch.Len() > 1<<20 {
					t.Fatalf("channel %s claims %d values in a %d byte input", ch.Path(), ch.Len(), len(data))
				}
				_, _ = ch.Values()
				_, _ = ch.Float64s()
				cur := ch.Cursor(3)
				for i := 0; cur.Next() && i < 8; i++ {
					_ = cur.Values()
				}
				_ = cur.Err()
			}
		}
	})
}

// FuzzSegmentIndex feeds the .gtdx decoder. A decoded sidecar is installed
// only after spans are checked against the file size the sidecar itself
// declares; a failure is a cache miss. The fuzzer must not panic.
func FuzzSegmentIndex(f *testing.F) {
	f.Add([]byte("GTDX"))
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xff}, segmentIndexHeader))
	dir := f.TempDir()
	path := filepath.Join(dir, "seed.tdms")
	w, err := Create(path)
	if err != nil {
		f.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeInt32)
	if err != nil {
		f.Fatal(err)
	}
	if err := ch.Write([]int32{1, 2, 3}); err != nil {
		f.Fatal(err)
	}
	if err := w.Close(); err != nil {
		f.Fatal(err)
	}
	side := SegmentIndexPath(path)
	file, err := Open(path, WithSegmentIndex(side))
	if err != nil {
		f.Fatal(err)
	}
	file.Close()
	blob, err := os.ReadFile(side)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(blob)

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			data = data[:1<<16]
		}
		_, _ = decodeSegmentIndex(data)

		dir := t.TempDir()
		path := filepath.Join(dir, "f.tdms")
		w, err := Create(path)
		if err != nil {
			t.Fatal(err)
		}
		ch, err := w.Group("g").Channel("c", TypeInt32)
		if err != nil {
			t.Fatal(err)
		}
		if err := ch.Write([]int32{7, 8, 9}); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		side := filepath.Join(dir, "crafted.gtdx")
		if err := os.WriteFile(side, data, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Open(path, WithSegmentIndex(side))
		if err != nil {
			t.Fatalf("sidecar must not fail the open: %v", err)
		}
		defer got.Close()
		g, ok := got.Group("g")
		if !ok {
			t.Fatal("group missing")
		}
		c, ok := g.Channel("c")
		if !ok {
			t.Fatal("channel missing")
		}
		vals, err := c.Values()
		if err != nil {
			t.Fatal(err)
		}
		want := []int32{7, 8, 9}
		gotv, ok := vals.([]int32)
		if !ok || len(gotv) != len(want) || gotv[0] != 7 || gotv[2] != 9 {
			t.Fatalf("sidecar changed the read: %#v", vals)
		}
	})
}

func addWritten(f *testing.F, build func(*Writer) error) {
	f.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		f.Fatal(err)
	}
	if err := build(w); err != nil {
		f.Fatal(err)
	}
	if err := w.Close(); err != nil {
		f.Fatal(err)
	}
	f.Add(buf.Bytes())
}
