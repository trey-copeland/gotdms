package dataset_test

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/trey-copeland/gotdms"
	"github.com/trey-copeland/gotdms/dataset"
)

// writeWaveforms writes two waveform channels that share one clock:
// the same wf_start_time, wf_increment, and sample count.
func writeWaveforms(path string, start time.Time) error {
	w, err := tdms.Create(path)
	if err != nil {
		return err
	}
	defer w.Close()
	g := w.Group("acquisition")
	for i, name := range []string{"ai0", "ai1"} {
		ch, err := g.Channel(name, tdms.TypeFloat64)
		if err != nil {
			return err
		}
		y := []float64{0, 1, 2, 3}
		for j := range y {
			y[j] += float64(10 * i)
		}
		if err := ch.WriteWaveform(tdms.Waveform[float64]{
			Start:     tdms.NewTimestamp(start),
			Increment: 0.5,
			Y:         y,
		}); err != nil {
			return err
		}
	}
	return w.Close()
}

// ExampleWithSharedClock reads two waveform channels from one clock. Both
// series point to the same *Clock of int64 Unix nanoseconds instead of
// carrying their own []time.Time.
func ExampleWithSharedClock() {
	dir, err := os.MkdirTemp("", "gotdms-dataset-example")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "run.tdms")
	start := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := writeWaveforms(path, start); err != nil {
		fmt.Println(err)
		return
	}

	ds, err := dataset.OpenFiles([]string{path})
	if err != nil {
		fmt.Println(err)
		return
	}
	series, err := ds.ReadMany(
		[]dataset.Selector{{Name: "ai0"}, {Name: "ai1"}},
		start, start.Add(time.Hour),
		dataset.WithSharedClock(),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	a, b := series[0], series[1]
	fmt.Println("shared clock:", a.Clock != nil && a.Clock == b.Clock)
	fmt.Println("times nil:", a.Times == nil)
	fmt.Println("relative:", a.RelativeTimes())
	fmt.Println("last:", a.Clock.Time(a.Clock.Len()-1).Format(time.RFC3339Nano))
	// Output:
	// shared clock: true
	// times nil: true
	// relative: [0 0.5 1 1.5]
	// last: 2024-01-02T03:04:06.5Z
}

// ExampleWithoutTimes reads sample values only. The series is still timed
// and keeps Dt and Basis, but no time axis is built.
func ExampleWithoutTimes() {
	dir, err := os.MkdirTemp("", "gotdms-dataset-example")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "run.tdms")
	start := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := writeWaveforms(path, start); err != nil {
		fmt.Println(err)
		return
	}

	ds, err := dataset.OpenFiles([]string{path})
	if err != nil {
		fmt.Println(err)
		return
	}
	s, err := ds.Read(dataset.Selector{Name: "ai1"}, dataset.WithoutTimes())
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("y:", s.Y)
	fmt.Println("timed:", s.Timed, "dt:", s.Dt)
	fmt.Println("times nil:", s.Times == nil && s.Clock == nil)
	fmt.Println("relative nil:", s.RelativeTimes() == nil)
	// Output:
	// y: [10 11 12 13]
	// timed: true dt: 0.5
	// times nil: true
	// relative nil: true
}
