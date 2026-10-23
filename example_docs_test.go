package tdms_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/trey-copeland/gotdms"
)

// ExampleCreate is the write and read example from the README: one channel,
// two segments, then a whole-channel read and a Cursor walk.
func ExampleCreate() {
	dir, err := os.MkdirTemp("", "gotdms-example")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "run.tdms")

	if err := writeRun(path); err != nil {
		fmt.Println(err)
		return
	}
	if err := readRun(path); err != nil {
		fmt.Println(err)
		return
	}
	// Output:
	// values: [21.5 21.7 22 22.4 22.8]
	// batch: [21.5 21.7 22 22.4]
	// batch: [22.8]
}

func writeRun(path string) error {
	w, err := tdms.Create(path)
	if err != nil {
		return err
	}
	defer w.Close() // a second Close is a no-op

	ch, err := w.Group("acquisition").Channel("temperature", tdms.TypeFloat64)
	if err != nil {
		return err
	}
	if err := ch.SetProperty(tdms.PropUnitString, "degC"); err != nil {
		return err
	}
	if err := ch.Write([]float64{21.5, 21.7, 22.0, 22.4}); err != nil {
		return err
	}
	// Flush ends a segment. Call it again as more samples arrive.
	if err := w.Flush(); err != nil {
		return err
	}
	if err := ch.Write([]float64{22.8}); err != nil {
		return err
	}
	return w.Close()
}

func readRun(path string) error {
	f, err := tdms.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	g, ok := f.Group("acquisition")
	if !ok {
		return fmt.Errorf("group missing")
	}
	ch, ok := g.Channel("temperature")
	if !ok {
		return fmt.Errorf("channel missing")
	}
	values, err := ch.Float64s()
	if err != nil {
		return err
	}
	fmt.Println("values:", values)

	// Stream without holding the whole channel.
	cur := ch.Cursor(4)
	for cur.Next() {
		fmt.Println("batch:", cur.Values().([]float64))
	}
	return cur.Err()
}

// ExampleWithSync writes a file whose segments are fsynced around the
// lead-in patch, so a power loss leaves either the incomplete-segment
// sentinel or a length that covers bytes already on disk.
func ExampleWithSync() {
	dir, err := os.MkdirTemp("", "gotdms-example")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "durable.tdms")

	w, err := tdms.Create(path, tdms.WithSync())
	if err != nil {
		fmt.Println(err)
		return
	}
	ch, err := w.Group("acquisition").Channel("pressure", tdms.TypeFloat64)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := ch.Write([]float64{101.3, 101.4}); err != nil {
		fmt.Println(err)
		return
	}
	// Sync writes the pending segment and fsyncs it.
	if err := w.Sync(); err != nil {
		fmt.Println(err)
		return
	}
	if err := w.Close(); err != nil {
		fmt.Println(err)
		return
	}

	f, err := tdms.Open(path)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	fmt.Println(f.SegmentCount(), f.Status())
	// Output:
	// 1 complete
}

// ExampleWithLenientTail opens a file that ends in a torn tail, here eight
// zero bytes after the last whole segment (shorter than a lead-in), then
// truncates the torn bytes so the file opens strictly again.
func ExampleWithLenientTail() {
	dir, err := os.MkdirTemp("", "gotdms-example")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "torn.tdms")
	if err := writeRun(path); err != nil {
		fmt.Println(err)
		return
	}
	out, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		fmt.Println(err)
		return
	}
	if _, err := out.Write(make([]byte, 8)); err != nil {
		fmt.Println(err)
		return
	}
	if err := out.Close(); err != nil {
		fmt.Println(err)
		return
	}

	if _, err := tdms.Open(path); err != nil {
		fmt.Println("strict open is ErrTruncated:", errors.Is(err, tdms.ErrTruncated))
	}

	f, err := tdms.Open(path, tdms.WithLenientTail())
	if err != nil {
		fmt.Println(err)
		return
	}
	st := f.Status()
	f.Close()
	fmt.Println("status:", st, "dropped:", st.DroppedTail)

	info, err := os.Stat(path)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := os.Truncate(path, info.Size()-st.DroppedTail); err != nil {
		fmt.Println(err)
		return
	}
	f, err = tdms.Open(path)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	fmt.Println("after truncate:", f.Status())
	// Output:
	// strict open is ErrTruncated: true
	// status: truncated dropped: 8
	// after truncate: complete
}
