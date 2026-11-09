package dataset

import (
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestRunVisitsEachIndexOnce(t *testing.T) {
	var mu sync.Mutex
	check := func(workers, n int) {
		t.Helper()
		got := make([]int, n)
		run(workers, n, func(i int) {
			mu.Lock()
			got[i]++
			mu.Unlock()
		})
		for i, c := range got {
			if c != 1 {
				t.Fatalf("workers %d n %d index %d count %d", workers, n, i, c)
			}
		}
	}
	check(3, 10)
	check(8, 2) // extra workers are capped at n
	run(4, 0, func(int) { t.Fatal("n == 0 should not call fn") })
}

func TestWorkerDefaultsAndNonPositive(t *testing.T) {
	prev := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(prev)

	if got := defaultOptions().workers; got != 4 {
		t.Fatalf("workers at 4 procs: %d", got)
	}
	runtime.GOMAXPROCS(9)
	if got := defaultOptions().workers; got != 8 {
		t.Fatalf("workers capped: %d", got)
	}
	runtime.GOMAXPROCS(1)
	if got := defaultOptions().workers; got != 1 {
		t.Fatalf("workers at 1 proc: %d", got)
	}
	runtime.GOMAXPROCS(4)

	o := options{workers: 7, readWorkers: 7}
	WithWorkers(0)(&o)
	WithWorkers(-1)(&o)
	if o.workers != 7 {
		t.Fatalf("non-positive WithWorkers stored %d", o.workers)
	}
	WithWorkers(3)(&o)
	if o.workers != 3 {
		t.Fatalf("WithWorkers(3) stored %d", o.workers)
	}
	WithReadWorkers(0)(&o)
	WithReadWorkers(-3)(&o)
	if o.readWorkers != 7 {
		t.Fatalf("non-positive WithReadWorkers stored %d", o.readWorkers)
	}
	WithReadWorkers(2)(&o)
	if o.readWorkers != 2 {
		t.Fatalf("WithReadWorkers(2) stored %d", o.readWorkers)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "a.tdms")
	writePair(t, path, "acq", "ai0", []float64{0}, []float64{0})
	ds, err := OpenFiles([]string{path}, WithWorkers(3), WithReadWorkers(2))
	if err != nil {
		t.Fatal(err)
	}
	if ds.opt.workers != 3 || ds.opt.readWorkers != 2 {
		t.Fatalf("open stored workers %d readWorkers %d", ds.opt.workers, ds.opt.readWorkers)
	}
}
