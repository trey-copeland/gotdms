package dataset

import (
	"os"
	"path/filepath"
	"testing"
)

func fdCount(t *testing.T) (n int, counted bool) {
	t.Helper()
	n, counted, err := countOpenFDs()
	if err != nil {
		t.Fatal(err)
	}
	return n, counted
}

// assertNoFDLeak fails when Read or ReadMany left a file open.
// Where countOpenFDs works, the descriptor count must not rise.
// Elsewhere each path must rename aside and back. That is the Windows
// check: a handle from os.Open blocks the rename.
func assertNoFDLeak(t *testing.T, what string, paths []string, before int, counted bool) {
	t.Helper()
	if counted {
		after, ok, err := countOpenFDs()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatal("file-descriptor count unavailable after it succeeded")
		}
		if after > before {
			t.Fatalf("%s fds before %d after %d", what, before, after)
		}
		return
	}
	for _, p := range paths {
		if err := renameRoundTrip(p); err != nil {
			t.Fatalf("%s rename %s: %v", what, p, err)
		}
	}
}

// renameRoundTrip renames path aside and back. The file is unchanged
// when both renames succeed.
func renameRoundTrip(path string) error {
	dst := path + ".renamed"
	if err := os.Rename(path, dst); err != nil {
		return err
	}
	return os.Rename(dst, path)
}

func TestCountOpenFDsSeesFile(t *testing.T) {
	before, ok, err := countOpenFDs()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skip("no file-descriptor directory on this OS")
	}
	f, err := os.CreateTemp(t.TempDir(), "fd")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	after, ok, err := countOpenFDs()
	if err != nil || !ok {
		t.Fatalf("after count ok=%v err=%v", ok, err)
	}
	if after <= before {
		t.Fatalf("fds before %d after %d", before, after)
	}
}

func TestRenameDetectsHeldFile(t *testing.T) {
	if _, ok, err := countOpenFDs(); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Skip("descriptor count is the leak probe on this OS")
	}
	path := filepath.Join(t.TempDir(), "held.tdms")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := renameRoundTrip(path); err == nil {
		f.Close()
		t.Fatal("renamed an open file; the leak probe would miss a held handle")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := renameRoundTrip(path); err != nil {
		t.Fatal(err)
	}
}
