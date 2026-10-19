package tdms_test

import (
	"bytes"
	"fmt"
	"time"

	"github.com/trey-copeland/gotdms"
)

func Example_roundTrip() {
	start := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	buf := &bytes.Buffer{}
	w, err := tdms.NewWriter(buf)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := w.SetProperty("title", "demo"); err != nil {
		fmt.Println(err)
		return
	}
	ch, err := w.Group("demo").Channel("voltage", tdms.TypeFloat64)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := ch.SetProperty(tdms.PropUnitString, "V"); err != nil {
		fmt.Println(err)
		return
	}
	if err := ch.SetProperty(tdms.PropIncrement, 0.001); err != nil {
		fmt.Println(err)
		return
	}
	if err := ch.SetProperty(tdms.PropStartTime, start); err != nil {
		fmt.Println(err)
		return
	}
	if err := ch.Write([]float64{1.5, 2.5, 3.5}); err != nil {
		fmt.Println(err)
		return
	}
	if err := w.Close(); err != nil {
		fmt.Println(err)
		return
	}

	f, err := tdms.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	g, _ := f.Group("demo")
	c, _ := g.Channel("voltage")
	values, err := c.Float64s()
	if err != nil {
		fmt.Println(err)
		return
	}
	unit, _ := c.Unit()
	inc, _ := c.Increment()
	when, _ := c.StartTime()
	fmt.Println("group:", g.Name())
	fmt.Println("channel:", c.Name())
	fmt.Println("values:", values)
	fmt.Println("unit:", unit)
	fmt.Printf("increment: %.3f\n", inc)
	fmt.Println("start:", when.Format(time.RFC3339))
	// Output:
	// group: demo
	// channel: voltage
	// values: [1.5 2.5 3.5]
	// unit: V
	// increment: 0.001
	// start: 2020-01-02T03:04:05Z
}

func ExampleReadWaveform() {
	start := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	buf := &bytes.Buffer{}
	w, err := tdms.NewWriter(buf)
	if err != nil {
		fmt.Println(err)
		return
	}
	ch, err := w.Group("acq").Channel("ai0", tdms.TypeFloat64)
	if err != nil {
		fmt.Println(err)
		return
	}
	err = ch.WriteWaveform(tdms.Waveform[float64]{
		Start:     tdms.NewTimestamp(start),
		Increment: 0.001,
		Y:         []float64{0.1, 0.2, 0.3},
		Unit:      "V",
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := w.Close(); err != nil {
		fmt.Println(err)
		return
	}

	f, err := tdms.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	g, _ := f.Group("acq")
	c, _ := g.Channel("ai0")
	wf, err := tdms.ReadWaveform[float64](c)
	if err != nil {
		fmt.Println(err)
		return
	}
	t0, _ := wf.Start.Time()
	t1, _ := wf.TimeAt(1)
	n, _ := c.SampleCount()
	fmt.Println("samples:", wf.Y)
	fmt.Println("unit:", wf.Unit)
	fmt.Println("dt:", wf.Dt())
	fmt.Println("t0:", t0.Format(time.RFC3339Nano))
	fmt.Println("t1:", t1.Format(time.RFC3339Nano))
	fmt.Println("wf_samples:", n)
	// Output:
	// samples: [0.1 0.2 0.3]
	// unit: V
	// dt: 1ms
	// t0: 2024-06-01T12:00:00Z
	// t1: 2024-06-01T12:00:00.001Z
	// wf_samples: 3
}

func Example_stream() {
	buf := &bytes.Buffer{}
	w, err := tdms.NewWriter(buf)
	if err != nil {
		fmt.Println(err)
		return
	}
	ch, err := w.Group("acq").Channel("ai0", tdms.TypeFloat64)
	if err != nil {
		fmt.Println(err)
		return
	}
	s, err := ch.Stream(4)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, chunk := range [][]float64{{1, 2}, {3, 4}} {
		if err := s.Write(ch, chunk); err != nil {
			fmt.Println(err)
			return
		}
	}
	if err := w.Close(); err != nil {
		fmt.Println(err)
		return
	}
	f, err := tdms.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	g, _ := f.Group("acq")
	c, _ := g.Channel("ai0")
	values, err := c.Float64s()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(values)
	// Output:
	// [1 2 3 4]
}

func ExampleChannel_ChunkStats() {
	buf := &bytes.Buffer{}
	w, err := tdms.NewWriter(buf)
	if err != nil {
		fmt.Println(err)
		return
	}
	ch, err := w.Group("acq").Channel("ai0", tdms.TypeFloat64)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := ch.Write([]float64{1, 2, 3, 4}); err != nil {
		fmt.Println(err)
		return
	}
	if err := w.Close(); err != nil {
		fmt.Println(err)
		return
	}
	f, err := tdms.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	g, _ := f.Group("acq")
	c, _ := g.Channel("ai0")
	cur, err := c.ChunkStats(2)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer cur.Close()
	for cur.Next() {
		st := cur.Stat()
		fmt.Printf("%d %d %.1f %.1f %.1f\n", st.Index, st.Count, st.Min, st.Max, st.Mean)
	}
	if err := cur.Err(); err != nil {
		fmt.Println(err)
	}
	// Output:
	// 0 2 1.0 2.0 1.5
	// 1 2 3.0 4.0 3.5
}
