package tdms

import (
	"bytes"
	"fmt"
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
	f.Add(daqmxSeedFormatChanging())
	f.Add(daqmxSeedFormatChangingAlt())
	f.Add(daqmxSeedDigital())
	f.Add(daqmxSeedMultiBuffer())
	f.Add(daqmxSeedTyped())
	f.Add(daqmxSeedScaled())
	f.Add(multiChunkTwoBuffers())
	f.Add(widenedFile())
	f.Add(daqmxSeedPartialTwoBuffers())

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
				if info, ok := ch.DAQmx(); ok {
					for _, sc := range info.Scalers {
						_, _ = ch.DAQmxSlice(sc.ScaleID, 0, ch.Len())
					}
					_, _ = ch.DAQmxScaledFloat64s()
				}
			}
			_, _ = g.Values()
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
	// A sidecar with DAQmx blocks: two raw buffers, a digital line, and a
	// width change between segments.
	for i, file := range [][]byte{multiChunkTwoBuffers(), daqmxSeedDigital(), widenedFile()} {
		dpath := filepath.Join(dir, fmt.Sprintf("daq%d.tdms", i))
		if err := os.WriteFile(dpath, file, 0o644); err != nil {
			f.Fatal(err)
		}
		dside := SegmentIndexPath(dpath)
		df, err := Open(dpath, WithSegmentIndex(dside))
		if err != nil {
			f.Fatal(err)
		}
		df.Close()
		dblob, err := os.ReadFile(dside)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(dblob)
	}

	// The data file and the sidecar path are created once per fuzz worker.
	// Iterations run one at a time in a worker, so they can share them. The
	// data file never changes, so its size, mtime, and inode stay fixed and a
	// crafted sidecar can be a stable hit. withoutSegmentIndexWrite keeps a
	// miss from rewriting the sidecar (temp file, fsync, rename, directory
	// sync) on every iteration; Open reads the sidecar the same way as in
	// production.
	path = filepath.Join(dir, "f.tdms")
	w, err = Create(path)
	if err != nil {
		f.Fatal(err)
	}
	ch, err = w.Group("g").Channel("c", TypeInt32)
	if err != nil {
		f.Fatal(err)
	}
	if err := ch.Write([]int32{7, 8, 9}); err != nil {
		f.Fatal(err)
	}
	if err := w.Close(); err != nil {
		f.Fatal(err)
	}
	// A valid sidecar for this exact file, so the seed corpus includes a hit.
	fixedSide := filepath.Join(dir, "fixed.gtdx")
	fx, err := Open(path, WithSegmentIndex(fixedSide))
	if err != nil {
		f.Fatal(err)
	}
	fx.Close()
	fixedBlob, err := os.ReadFile(fixedSide)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(fixedBlob)
	side = filepath.Join(dir, "crafted.gtdx")

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			data = data[:1<<16]
		}
		_, _ = decodeSegmentIndex(data)

		if err := os.WriteFile(side, data, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Open(path, WithSegmentIndex(side), withoutSegmentIndexWrite())
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
