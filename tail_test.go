package tdms

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFollowDeliversAppendedSegments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "live.tdms")
	writeFloatSegment(t, path, []float64{1, 2, 3}, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tail, err := Follow(ctx, path, "g", "c", WithTailInterval(5*time.Millisecond), WithTailBatch(2))
	if err != nil {
		t.Fatal(err)
	}
	defer tail.Close()

	errc := make(chan error, 1)
	go func() {
		time.Sleep(40 * time.Millisecond)
		if err := appendFloatSegment(path, []float64{4, 5}); err != nil {
			errc <- err
			cancel()
			return
		}
		time.Sleep(40 * time.Millisecond)
		if err := appendShortFloat(path, 4, []float64{9, 8}); err != nil {
			errc <- err
			cancel()
			return
		}
		time.Sleep(80 * time.Millisecond)
		errc <- nil
		cancel()
	}()

	var got []float64
	for tail.Next() {
		batch, ok := tail.Values().([]float64)
		if !ok {
			t.Fatalf("%T", tail.Values())
		}
		got = append(got, batch...)
	}
	if !errors.Is(tail.Err(), context.Canceled) {
		t.Fatalf("err %v", tail.Err())
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, []float64{1, 2, 3, 4, 5})

	// The short segment is on disk and a normal open still sees its whole values.
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, _ := f.Group("g")
	ch, _ := g.Channel("c")
	assertEqual(t, mustValues(t, ch), []float64{1, 2, 3, 4, 5, 9, 8})
	if ch.committed() != 5 {
		t.Fatalf("committed %d", ch.committed())
	}
	if !f.Status().Incomplete {
		t.Fatalf("%#v", f.Status())
	}
}

func TestFollowLenientTailStaysUncommitted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "live.tdms")
	writeFloatSegment(t, path, []float64{1, 2}, true)
	if err := appendFloatSegment(path, []float64{3, 4}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	missing := append([]byte(nil), raw[len(raw)-3:]...)
	if err := os.WriteFile(path, raw[:len(raw)-3], 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tail, err := Follow(ctx, path, "g", "c", WithTailInterval(5*time.Millisecond), WithTailBatch(8))
	if err != nil {
		t.Fatal(err)
	}
	defer tail.Close()

	errc := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			errc <- err
			cancel()
			return
		}
		if _, err := f.Write(missing); err != nil {
			f.Close()
			errc <- err
			cancel()
			return
		}
		errc <- f.Close()
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()

	var got []float64
	for tail.Next() {
		batch, ok := tail.Values().([]float64)
		if !ok {
			t.Fatalf("%T", tail.Values())
		}
		got = append(got, batch...)
	}
	if !errors.Is(tail.Err(), context.Canceled) {
		t.Fatalf("err %v", tail.Err())
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, []float64{1, 2, 3, 4})
}

func TestFollowClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "live.tdms")
	writeFloatSegment(t, path, []float64{1}, true)
	ctx := context.Background()
	tail, err := Follow(ctx, path, "g", "c", WithTailInterval(5*time.Millisecond), WithTailBatch(8))
	if err != nil {
		t.Fatal(err)
	}
	if !tail.Next() {
		t.Fatal(tail.Err())
	}
	assertEqual(t, tail.Values(), []float64{1})
	go func() {
		time.Sleep(20 * time.Millisecond)
		tail.Close()
	}()
	if tail.Next() {
		t.Fatalf("value after close: %v", tail.Values())
	}
	if tail.Err() != nil {
		t.Fatal(tail.Err())
	}
}

func TestFollowRequiresNames(t *testing.T) {
	_, err := Follow(context.Background(), "", "g", "c")
	if !errors.Is(err, ErrArgument) {
		t.Fatal(err)
	}
	_, err = Follow(nil, "x", "g", "c")
	if !errors.Is(err, ErrArgument) {
		t.Fatal(err)
	}
}

func writeFloatSegment(t *testing.T, path string, samples []float64, create bool) {
	t.Helper()
	if !create {
		t.Fatal("use appendFloatSegment")
	}
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Write(samples); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func appendFloatSegment(path string, samples []float64) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	w, err := NewWriter(f)
	if err != nil {
		return err
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		return err
	}
	if err := ch.Write(samples); err != nil {
		return err
	}
	return w.Flush()
}

func appendShortFloat(path string, declared int, samples []float64) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	w, err := NewWriter(f)
	if err != nil {
		return err
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		return err
	}
	s, err := ch.Stream(declared)
	if err != nil {
		return err
	}
	if err := s.Write(ch, samples); err != nil {
		return err
	}
	err = w.Close()
	if err == nil {
		return errors.New("short stream finished the segment")
	}
	return nil
}
