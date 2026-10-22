package tdms

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildSegmentIndex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	writeIndexedFile(t, path)
	side, err := BuildSegmentIndexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if side != SegmentIndexPath(path) {
		t.Fatalf("sidecar %s", side)
	}
	ok, err := SegmentIndexValid(path)
	if err != nil || !ok {
		t.Fatalf("valid %v %v", ok, err)
	}
	if err := BuildSegmentIndex(path); err != nil {
		t.Fatal(err)
	}

	before := segmentParses.Load()
	f, err := Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !f.SegmentIndexUsed() {
		t.Fatal("built sidecar was not used")
	}
	if segmentParses.Load() != before {
		t.Fatal("open parsed after BuildSegmentIndex")
	}
	if indexedSnapshot(t, f) == "" {
		t.Fatal("snapshot")
	}
}

func TestBuildSegmentIndexUnwritableDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	writeIndexedFile(t, path)
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	// Unix mode bits make the directory unwritable. Windows keeps the
	// directory writable, so a create probe selects that host's failure.
	probe, probeErr := os.CreateTemp(dir, ".probe-*")
	if probeErr != nil {
		err := BuildSegmentIndex(path)
		if err == nil {
			t.Fatal("expected a write error")
		}
		if _, statErr := os.Stat(SegmentIndexPath(path)); !os.IsNotExist(statErr) {
			t.Fatalf("sidecar after failed write: %v", statErr)
		}
		return
	}
	probe.Close()
	os.Remove(probe.Name())
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory at the sidecar path fails the atomic rename.
	// os.Remove of that directory fails, so the write error is returned.
	side := SegmentIndexPath(path)
	if err := os.Mkdir(side, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(side, "keep"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := BuildSegmentIndex(path); err == nil {
		t.Fatal("expected a write error")
	}
	if _, err := os.Stat(filepath.Join(side, "keep")); err != nil {
		t.Fatalf("failed write removed the blocking sidecar path: %v", err)
	}
}

func TestSegmentIndexTrustIdentityHit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	writeIndexedFile(t, path)
	if err := BuildSegmentIndex(path); err != nil {
		t.Fatal(err)
	}
	before := segmentParses.Load()
	f, err := Open(path, WithSegmentIndex(SegmentIndexPath(path)), WithSegmentIndexTrustIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !f.SegmentIndexUsed() {
		t.Fatal("trust-identity missed a fresh sidecar")
	}
	if segmentParses.Load() != before {
		t.Fatal("trust-identity re-parsed")
	}
	if indexedSnapshot(t, f) == "" {
		t.Fatal("snapshot")
	}
}

func TestSegmentIndexTrustIdentityMissesSizeMtimeIdentity(t *testing.T) {
	openTrust := func(path string) (*File, error) {
		return Open(path, WithSegmentIndex(SegmentIndexPath(path)), WithSegmentIndexTrustIdentity())
	}
	t.Run("size", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "run.tdms")
		writeIndexedFile(t, path)
		if err := BuildSegmentIndex(path); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		grown := append(append([]byte{}, raw...), 0)
		if err := os.WriteFile(path, grown, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
			t.Fatal(err)
		}
		if _, err := openTrust(path); err == nil {
			t.Fatal("size change was trusted")
		}
	})
	t.Run("mtime", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "run.tdms")
		writeIndexedFile(t, path)
		if err := BuildSegmentIndex(path); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		later := fi.ModTime().Add(time.Hour)
		if err := os.Chtimes(path, later, later); err != nil {
			t.Fatal(err)
		}
		before := segmentParses.Load()
		f, err := openTrust(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if f.SegmentIndexUsed() || segmentParses.Load() == before {
			t.Fatal("mtime change was trusted")
		}
	})
	t.Run("identity", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "run.tdms")
		writeIndexedFile(t, path)
		if err := BuildSegmentIndex(path); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		side, err := os.ReadFile(SegmentIndexPath(path))
		if err != nil {
			t.Fatal(err)
		}
		origDev, origIno := openFileID(t, path)
		replaced := path + ".replaced"
		if err := os.WriteFile(replaced, raw, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(replaced, fi.ModTime(), fi.ModTime()); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replaced, path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(SegmentIndexPath(path), side, 0644); err != nil {
			t.Fatal(err)
		}
		fi2, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		dev, ino := openFileID(t, path)
		before := segmentParses.Load()
		f, err := openTrust(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if dev == origDev && ino == origIno {
			if origDev != 0 || origIno != 0 {
				t.Fatal("replaced file kept device and inode")
			}
			// fileID is 0,0 only where the platform has no device or inode.
			// Windows fills both from GetFileInformationByHandle, so a
			// replaced file there takes the miss path below. A same-size,
			// same-mtime replace stays a trust hit only when both are 0.
			same := fi2.Size() == fi.Size() && fi2.ModTime().UnixNano() == fi.ModTime().UnixNano()
			if same {
				if !f.SegmentIndexUsed() || segmentParses.Load() != before {
					t.Fatal("platform without device and inode should trust a same-size, same-mtime replace")
				}
				return
			}
			if f.SegmentIndexUsed() {
				t.Fatal("trusted a replace whose size or mtime did not match")
			}
			return
		}
		if fi2.Size() != fi.Size() || fi2.ModTime().UnixNano() != fi.ModTime().UnixNano() {
			t.Fatalf("size %d/%d mtime %d/%d", fi2.Size(), fi.Size(), fi2.ModTime().UnixNano(), fi.ModTime().UnixNano())
		}
		if f.SegmentIndexUsed() || segmentParses.Load() == before {
			t.Fatal("replaced file was trusted")
		}
	})
}

func openFileID(t *testing.T, path string) (dev, ino uint64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dev, ino, err = fileID(f)
	if err != nil {
		t.Fatal(err)
	}
	return dev, ino
}

func TestSegmentIndexDefaultCatchesMetadataSameSizeMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty(PropUnitString, "abcd"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := BuildSegmentIndex(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(raw, []byte("abcd"))
	if idx < 0 {
		t.Fatal("property bytes not found")
	}
	copy(raw[idx:], []byte("wxyz"))
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	side := SegmentIndexPath(path)
	sideBytes, err := os.ReadFile(side)
	if err != nil {
		t.Fatal(err)
	}

	before := segmentParses.Load()
	f, err := Open(path, WithSegmentIndex(side), WithSegmentIndexTrustIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if !f.SegmentIndexUsed() || segmentParses.Load() != before {
		f.Close()
		t.Fatal("trust-identity re-read a same-size, same-mtime edit")
	}
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	p, ok := c.Property(PropUnitString)
	f.Close()
	if !ok || p.Value != "abcd" {
		t.Fatalf("trust saw %+v ok %v", p, ok)
	}
	gotSide, err := os.ReadFile(side)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSide, sideBytes) {
		t.Fatal("trust hit rewrote the sidecar")
	}

	before = segmentParses.Load()
	f, err = Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.SegmentIndexUsed() || segmentParses.Load() == before {
		t.Fatal("default mode reused a sidecar after a metadata edit")
	}
	g, _ = f.Group("g")
	c, _ = g.Channel("c")
	p, ok = c.Property(PropUnitString)
	if !ok || p.Value != "wxyz" {
		t.Fatalf("default saw %+v ok %v", p, ok)
	}
}

func TestBuildSegmentIndexLenientTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cut.tdms")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cut := append([]byte(nil), raw[:len(raw)-3]...)
	if err := os.WriteFile(path, cut, 0644); err != nil {
		t.Fatal(err)
	}
	if err := BuildSegmentIndex(path); !errors.Is(err, ErrTruncated) {
		t.Fatalf("strict build: %v", err)
	}
	if err := BuildSegmentIndex(path, WithLenientTail()); err != nil {
		t.Fatal(err)
	}
	ok, err := SegmentIndexValid(path)
	if err != nil || ok {
		t.Fatalf("strict valid %v %v", ok, err)
	}
	ok, err = SegmentIndexValid(path, WithLenientTail())
	if err != nil || !ok {
		t.Fatalf("lenient valid %v %v", ok, err)
	}
	if _, err := Open(path, WithSegmentIndex(SegmentIndexPath(path))); !errors.Is(err, ErrTruncated) {
		t.Fatalf("strict open: %v", err)
	}
	before := segmentParses.Load()
	f, err := Open(path, WithLenientTail(), WithSegmentIndex(SegmentIndexPath(path)))
	if err != nil {
		t.Fatal(err)
	}
	if !f.SegmentIndexUsed() || segmentParses.Load() != before {
		f.Close()
		t.Fatal("lenient sidecar was not used")
	}
	if !f.Status().Truncated {
		t.Fatalf("status %#v", f.Status())
	}
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1, 2, 3})
	f.Close()

	before = segmentParses.Load()
	f, err = Open(path, WithLenientTail(), WithSegmentIndex(SegmentIndexPath(path)), WithSegmentIndexTrustIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !f.SegmentIndexUsed() || segmentParses.Load() != before {
		t.Fatal("lenient trust missed")
	}
	if !f.Status().Truncated {
		t.Fatalf("trust status %#v", f.Status())
	}
}

func TestSegmentIndexValidMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	writeIndexedFile(t, path)
	ok, err := SegmentIndexValid(path)
	if err != nil || ok {
		t.Fatalf("missing %v %v", ok, err)
	}
	if _, err := SegmentIndexValid(filepath.Join(dir, "nope.tdms")); err == nil {
		t.Fatal("missing data file")
	}
}
