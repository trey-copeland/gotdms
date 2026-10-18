package dataset_test

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/trey-copeland/gotdms"
	"github.com/trey-copeland/gotdms/dataset"
)

func Example() {
	dir, err := os.MkdirTemp("", "gotdms-dataset-example")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(dir)

	epoch := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < 100; i++ {
		if err := writeSample(filepath.Join(dir, fmt.Sprintf("f%03d.tdms", i)), i); err != nil {
			fmt.Println(err)
			return
		}
	}

	ds, err := dataset.OpenGlob(dir, "*.tdms",
		dataset.WithTimestampChannelName("Time"),
		dataset.WithNumericEpoch(epoch),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	hits := ds.Find("ai0")
	fmt.Printf("files %d\n", len(ds.Files()))
	fmt.Printf("ai0 count %d group %s\n", len(hits), hits[0].Group)

	start := epoch.Add(10 * time.Second)
	end := epoch.Add(13 * time.Second)
	trigger := epoch.Add(10500 * time.Millisecond)
	series, err := ds.ReadMany([]dataset.Selector{
		{Name: "ai0"},
		{Name: "ai1"},
	}, start, end, dataset.WithReadBasis(trigger))
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, s := range series {
		fmt.Printf("%s %v\n", s.Name, s.Y)
		fmt.Printf("%s rel", s.Name)
		for _, r := range s.RelativeTimes() {
			fmt.Printf(" %.3f", r)
		}
		fmt.Println()
	}

	// Output:
	// files 100
	// ai0 count 100 group acquisition
	// ai0 [10 11 12]
	// ai0 rel -0.500 0.500 1.500
	// ai1 [100 110 120]
	// ai1 rel -0.500 0.500 1.500
}

func writeSample(path string, i int) error {
	w, err := tdms.Create(path)
	if err != nil {
		return err
	}
	sec := []float64{float64(i)}
	if err := writeNum(w, "acquisition", "Time", sec); err != nil {
		w.Close()
		return err
	}
	if err := writeNum(w, "acquisition", "ai0", sec); err != nil {
		w.Close()
		return err
	}
	if err := writeNum(w, "sensors", "Time", sec); err != nil {
		w.Close()
		return err
	}
	if err := writeNum(w, "sensors", "ai1", []float64{float64(i) * 10}); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func writeNum(w *tdms.Writer, group, name string, y []float64) error {
	ch, err := w.Group(group).Channel(name, tdms.TypeFloat64)
	if err != nil {
		return err
	}
	return ch.Write(y)
}
