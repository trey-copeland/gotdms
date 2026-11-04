package tdms

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeSrc(t *testing.T, path string, b []byte, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, b, perm); err != nil {
		t.Fatal(err)
	}
	// WriteFile applies the umask; set the bits the test means.
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
}

func assertDefragged(t *testing.T, path string, src []byte) {
	t.Helper()
	of, err := Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer of.Close()
	if of.SegmentCount() != 1 {
		t.Fatalf("%s: %d segments, want 1", path, of.SegmentCount())
	}
	assertSameFile(t, openBytes(t, src), of)
}

func assertOnlyFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	if len(entries) != len(names) {
		t.Fatalf("dir has %v, want %v", entries, names)
	}
	for _, e := range entries {
		if !want[e.Name()] {
			t.Fatalf("unexpected file %q (leftover temporary?)", e.Name())
		}
	}
}

// DefragmentToFile with the destination naming the source, by the same
// path, another spelling of it, a symlink, or a hard link, must not destroy
// the source. Before the temp-file rewrite, os.Create truncated the source
// while it was still open for reading.
func TestDefragmentToFileSameFile(t *testing.T) {
	src := fragmented(t)
	cases := []struct {
		name string
		dst  func(dir, path string) string
	}{
		{"same path", func(dir, path string) string { return path }},
		{"other spelling", func(dir, path string) string {
			return filepath.Join(dir, ".", "sub", "..", filepath.Base(path))
		}},
		{"symlink", func(dir, path string) string {
			link := filepath.Join(dir, "link.tdms")
			if err := os.Symlink(path, link); err != nil {
				t.Skipf("symlink: %v", err)
			}
			return link
		}},
		{"hard link", func(dir, path string) string {
			link := filepath.Join(dir, "hard.tdms")
			if err := os.Link(path, link); err != nil {
				t.Skipf("link: %v", err)
			}
			return link
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "run.tdms")
			writeSrc(t, path, src, 0o644)
			dst := c.dst(dir, path)
			if err := DefragmentToFile(dst, path); err != nil {
				t.Fatalf("defragment: %v", err)
			}
			assertDefragged(t, dst, src)
			switch c.name {
			case "symlink":
				// The link survives and its target is the rewritten file.
				fi, err := os.Lstat(dst)
				if err != nil || fi.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("symlink replaced by a file: %v %v", fi, err)
				}
				assertDefragged(t, path, src)
			case "hard link":
				// The hard link names the new file; the source name keeps
				// the old bytes (documented: the copy is a new file).
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, src) {
					t.Fatalf("source name changed through the hard link: %v", err)
				}
			default:
				assertDefragged(t, path, src)
			}
		})
	}
}

func TestDefragmentFileSymlinkKeepsLink(t *testing.T) {
	src := fragmented(t)
	dir := t.TempDir()
	real := filepath.Join(dir, "real.tdms")
	writeSrc(t, real, src, 0o644)
	link := filepath.Join(dir, "link.tdms")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := DefragmentFile(link); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced: %v %v", fi, err)
	}
	assertDefragged(t, real, src)
	assertOnlyFiles(t, dir, "real.tdms", "link.tdms")
}

func TestDefragmentKeepsPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits")
	}
	src := fragmented(t)
	dir := t.TempDir()

	// In place keeps the file's bits (os.CreateTemp alone gives 0600).
	path := filepath.Join(dir, "run.tdms")
	writeSrc(t, path, src, 0o644)
	if err := DefragmentFile(path); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
		t.Fatalf("in place: mode %v, want 0644", fi.Mode().Perm())
	}

	// A new destination takes the source's bits.
	srcPath := filepath.Join(dir, "in.tdms")
	writeSrc(t, srcPath, src, 0o640)
	newDst := filepath.Join(dir, "new.tdms")
	if err := DefragmentToFile(newDst, srcPath); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(newDst); fi.Mode().Perm() != 0o640 {
		t.Fatalf("new destination: mode %v, want 0640", fi.Mode().Perm())
	}

	// An existing destination keeps its own bits.
	oldDst := filepath.Join(dir, "old.tdms")
	writeSrc(t, oldDst, []byte("old"), 0o604)
	if err := DefragmentToFile(oldDst, srcPath); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(oldDst); fi.Mode().Perm() != 0o604 {
		t.Fatalf("existing destination: mode %v, want 0604", fi.Mode().Perm())
	}
	assertDefragged(t, oldDst, src)
}

// A rejected file leaves an existing destination byte for byte, and no
// temporary file.
func TestDefragmentToFileRejectLeavesDestination(t *testing.T) {
	for _, c := range []struct {
		name string
		file []byte
		want error
	}{
		{"big-endian", beChannelFile(t, TypeInt16, 2, []byte{0xFF, 0xFE, 0x01, 0x2C}), ErrUnsupported},
		{"daqmx", buildDAQmx(daqTOC, []dmObj{daqI16(RawIndexDAQmxFormatChanging, 4)}, i16Raw(1, 2, 3, 4)), ErrDAQmx},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			srcPath := filepath.Join(dir, "in.tdms")
			dstPath := filepath.Join(dir, "out.tdms")
			writeSrc(t, srcPath, c.file, 0o644)
			old := []byte("previous contents")
			writeSrc(t, dstPath, old, 0o644)
			if err := DefragmentToFile(dstPath, srcPath); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			got, err := os.ReadFile(dstPath)
			if err != nil || !bytes.Equal(got, old) {
				t.Fatalf("destination changed: %q %v", got, err)
			}
			assertOnlyFiles(t, dir, "in.tdms", "out.tdms")
		})
	}
}

func TestDefragmentToFileErrors(t *testing.T) {
	src := fragmented(t)
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "in.tdms")
	writeSrc(t, srcPath, src, 0o644)
	// A directory is not a destination.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := DefragmentToFile(sub, srcPath); !errors.Is(err, ErrArgument) {
		t.Fatalf("directory destination: %v", err)
	}
	// A missing destination directory fails before anything is written.
	if err := DefragmentToFile(filepath.Join(dir, "missing", "out.tdms"), srcPath); err == nil {
		t.Fatal("missing directory: want an error")
	}
	// A dangling symlink cannot be resolved.
	link := filepath.Join(dir, "dangling.tdms")
	if err := os.Symlink(filepath.Join(dir, "nowhere", "x.tdms"), link); err == nil {
		if err := DefragmentToFile(link, srcPath); err == nil {
			t.Fatal("dangling symlink: want an error")
		}
	}
	// A missing source.
	if err := DefragmentToFile(filepath.Join(dir, "out.tdms"), filepath.Join(dir, "none.tdms")); err == nil {
		t.Fatal("missing source: want an error")
	}
	assertOnlyFiles(t, dir, "in.tdms", "sub", "dangling.tdms")
}

// A typed channel with zero samples keeps its type: the copy names it with a
// raw-data index whose count is 0.
func TestDefragmentZeroSampleTypedChannel(t *testing.T) {
	file := buildDAQmx(TocMetaData|TocNewObjList|TocRawData, []dmObj{
		objStd("/'g'/'empty'", TypeFloat64, 0),
		objStd("/'g'/'data'", TypeInt16, 2),
	}, i16Raw(7, 8))
	in := openBytes(t, file)
	g, _ := in.Group("g")
	e, _ := g.Channel("empty")
	if e.DataType() != TypeFloat64 || e.Len() != 0 {
		t.Fatalf("fixture: empty is %s with %d values", e.DataType(), e.Len())
	}
	var out bytes.Buffer
	if err := Defragment(&out, bytes.NewReader(file)); err != nil {
		t.Fatal(err)
	}
	assertSameFile(t, in, openBytes(t, out.Bytes()))

	// Buffered path (a string channel with data), with an empty typed
	// string channel and an empty float channel. The fixture is written
	// with keepType, the same mechanism the copy uses.
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	label := mustCh(t, w, "g", "label", TypeString)
	es := mustCh(t, w, "g", "empty_str", TypeString)
	ef := mustCh(t, w, "g", "empty_f", TypeFloat64)
	es.ch.keepType, ef.ch.keepType = true, true
	if err := label.Write([]string{"a", "bc"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	src := buf.Bytes()
	sin := openBytes(t, src)
	sg, _ := sin.Group("g")
	for name, want := range map[string]DataType{"empty_str": TypeString, "empty_f": TypeFloat64} {
		c, _ := sg.Channel(name)
		if c.DataType() != want || c.Len() != 0 {
			t.Fatalf("keepType fixture %s: %s with %d values", name, c.DataType(), c.Len())
		}
	}
	out.Reset()
	if err := Defragment(&out, bytes.NewReader(src)); err != nil {
		t.Fatal(err)
	}
	assertSameFile(t, sin, openBytes(t, out.Bytes()))

	// Metadata only: no channel has samples.
	meta := buildDAQmx(TocMetaData|TocNewObjList, []dmObj{objStd("/'g'/'empty'", TypeUint32, 0)}, nil)
	out.Reset()
	if err := Defragment(&out, bytes.NewReader(meta)); err != nil {
		t.Fatal(err)
	}
	of := openBytes(t, out.Bytes())
	assertSameFile(t, openBytes(t, meta), of)
	og, _ := of.Group("g")
	oc, _ := og.Channel("empty")
	if oc.DataType() != TypeUint32 {
		t.Fatalf("metadata-only copy: type %s", oc.DataType())
	}
}

// failingWriter accepts n bytes and then fails every write, counting the calls
// made after the first failure.
type failingWriter struct {
	n, wrote, after int
	failed          bool
}

var errDiskFull = errors.New("disk full")

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.failed {
		f.after++
		return 0, errDiskFull
	}
	k := len(p)
	if f.wrote+k > f.n {
		k = f.n - f.wrote
	}
	f.wrote += k
	if k < len(p) {
		f.failed = true
		return k, errDiskFull
	}
	return k, nil
}

// After a write error Defragment returns it and stops writing: the writer is
// not closed, which would append more bytes after the failure.
func TestDefragmentStopsAfterWriteError(t *testing.T) {
	for _, src := range [][]byte{fragmented(t), stringFile(t)} {
		var full bytes.Buffer
		if err := Defragment(&full, bytes.NewReader(src)); err != nil {
			t.Fatal(err)
		}
		for _, n := range []int{0, 10, full.Len() / 2, full.Len() - 1} {
			w := &failingWriter{n: n}
			if err := Defragment(w, bytes.NewReader(src)); !errors.Is(err, errDiskFull) {
				t.Fatalf("n=%d: err = %v", n, err)
			}
			if w.after != 0 {
				t.Fatalf("n=%d: %d writes after the failure", n, w.after)
			}
		}
	}
}

func stringFile(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	num := mustCh(t, w, "g", "num", TypeInt32)
	str := mustCh(t, w, "g", "label", TypeString)
	if err := num.Write([]int32{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := str.Write([]string{"alpha", "beta"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
