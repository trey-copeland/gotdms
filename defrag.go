package tdms

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Defragment reads the TDMS file from src and writes a defragmented copy to
// dst. A file that was written incrementally holds one segment per append,
// each with its own lead-in, metadata, and a small raw-data chunk; reads then
// pay per-segment overhead and gather each channel's samples from scattered
// places. Defragment rewrites the file as a single segment whose raw data is
// contiguous per channel, so later reads are faster. This matches the intent
// of NI LabVIEW's TDMS Defragment.
//
// The copy preserves the channel tree, channel order (first appearance, the
// order the reader assigns), data types, every value, and each object's final
// property values. A typed channel with zero samples keeps its type through a
// raw-data index with a count of 0; a channel that never had a type stays
// void. Output is little-endian TDMS 2.0.
//
// src must report its length the same way NewReader requires (a Size method,
// an *os.File, or an io.Seeker). ReaderOptions are passed to the internal
// open, so WithLenientTail defragments a file with a recoverable torn tail
// (the torn bytes are dropped) and WithSegmentIndex reuses a sidecar.
//
// A file that contains DAQmx raw data returns ErrDAQmx and a file with a
// big-endian segment returns ErrUnsupported; this package does not write
// either, so rewriting them would silently change the on-disk format. That
// check runs before the first byte is written, so a rejected file writes
// nothing to dst. Any later error (a read or write failure) can leave a
// partial copy in dst; DefragmentToFile and DefragmentFile never expose one.
func Defragment(dst io.Writer, src io.ReaderAt, opts ...ReaderOption) error {
	f, err := NewReader(src, opts...)
	if err != nil {
		return err
	}
	return defragment(dst, f)
}

// DefragmentToFile reads the TDMS file at srcPath and writes a defragmented
// copy to dstPath. See Defragment.
//
// The copy is written to a temporary file in dstPath's directory, fsynced,
// and renamed over dstPath, and the directory is then synced (best effort;
// not on Windows). dstPath is therefore never truncated or half-written: on
// any error, including a rejected DAQmx or big-endian file, an existing
// dstPath is left as it was and the temporary file is removed. Because the
// data is on stable storage before the rename, a crash or power loss leaves
// either the old dstPath or the complete copy, on file systems that make a
// rename durable after the data it names (common local file systems; not
// guaranteed on network file systems, or on Windows, whose directory cannot
// be synced).
//
// dstPath may name srcPath, including through a different relative path, a
// symbolic link, or a hard link; that is an in-place rewrite, as with
// DefragmentFile. A symbolic link at dstPath is followed, so its target is
// replaced and the link stays. The replacement is a new file: hard links to
// the old one and handles already open on it keep the old content, and the
// owner is not copied. The new file takes the permission bits of an existing
// dstPath, or of srcPath when dstPath does not exist.
//
// On Windows the rename cannot replace a dstPath that is read-only or that
// another process holds open without delete sharing; the call then returns
// the rename error, leaves dstPath as it was, and removes the temporary file.
func DefragmentToFile(dstPath, srcPath string, opts ...ReaderOption) error {
	return defragmentPath(dstPath, srcPath, opts)
}

// DefragmentFile rewrites the TDMS file at path in place. It is
// DefragmentToFile(path, path, opts...): the copy goes to a temporary file in
// the same directory and is renamed over the original, so on any error the
// original is left untouched. See DefragmentToFile for what a crash or power
// loss leaves, and for links and permissions.
func DefragmentFile(path string, opts ...ReaderOption) error {
	return defragmentPath(path, path, opts)
}

func defragmentPath(dstPath, srcPath string, opts []ReaderOption) error {
	f, err := Open(srcPath, opts...)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := defragReject(f); err != nil {
		return err
	}
	target, perm, err := defragTarget(dstPath, srcPath)
	if err != nil {
		return wrap("defragment", dstPath, err)
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, filepath.Base(target)+".defrag-*")
	if err != nil {
		return wrap("defragment", dstPath, err)
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			tmp.Close() // a second Close after a failed one is harmless
			os.Remove(tmpName)
		}
	}()
	if err := defragment(tmp, f); err != nil {
		return err
	}
	if err := tmp.Chmod(perm); err != nil && !isUnsupportedChmod(err) {
		return wrap("defragment", dstPath, err)
	}
	if err := tmp.Sync(); err != nil {
		return wrap("defragment", dstPath, err)
	}
	if err := tmp.Close(); err != nil {
		return wrap("defragment", dstPath, err)
	}
	// Close the source before the rename: Windows refuses to replace a file
	// that is open, and the source may be the destination.
	f.Close()
	// os.Rename replaces an existing file on every supported platform. This
	// does not use replaceFile: its remove-and-retry fallback could delete
	// the original and then fail.
	if err := os.Rename(tmpName, target); err != nil {
		return wrap("defragment", dstPath, err)
	}
	done = true
	syncDir(dir)
	return nil
}

// defragTarget resolves the path the copy replaces and the permission bits it
// gets. A symbolic link at dstPath is followed so the link survives the
// rename. An existing destination keeps its permission bits; a new one takes
// the source's.
func defragTarget(dstPath, srcPath string) (string, os.FileMode, error) {
	target := dstPath
	if fi, err := os.Lstat(dstPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(dstPath)
		if err != nil {
			return "", 0, err
		}
		target = resolved
	}
	fi, err := os.Stat(target)
	if err == nil {
		if !fi.Mode().IsRegular() {
			return "", 0, fmt.Errorf("%w: destination is not a regular file", ErrArgument)
		}
		return target, fi.Mode().Perm(), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", 0, err
	}
	sfi, err := os.Stat(srcPath)
	if err != nil {
		return "", 0, err
	}
	return target, sfi.Mode().Perm(), nil
}

// defragment writes a single-segment copy of f to dst. f stays open for
// reading throughout.
func defragment(dst io.Writer, f *File) error {
	if err := defragReject(f); err != nil {
		return err
	}

	w, err := NewWriter(dst)
	if err != nil {
		return err
	}

	// Rebuild the object tree with final property values, in reader order.
	for _, p := range f.Properties() {
		w.root.setProp(p)
	}
	var (
		dataChans []*Channel       // channels with samples, in order
		dataWrite []*ChannelWriter // their writers, same order
		anyString bool
	)
	for _, g := range f.Groups() {
		gw := w.Group(g.Name())
		for _, p := range g.Properties() {
			gw.g.obj.setProp(p)
		}
		for _, c := range g.Channels() {
			cw, err := gw.Channel(c.Name(), c.DataType())
			if err != nil {
				return err
			}
			for _, p := range c.Properties() {
				cw.ch.obj.setProp(p)
			}
			if c.hasDT && c.total == 0 && c.dt != TypeVoid {
				cw.ch.keepType = true
			}
			if c.hasDT && c.total > 0 {
				dataChans = append(dataChans, c)
				dataWrite = append(dataWrite, cw)
				if isStringType(c.dt) {
					anyString = true
				}
			}
		}
	}

	if len(dataChans) == 0 {
		// Metadata only: no raw data. Close flushes a single metadata segment
		// that names every object, with no raw-data index.
		return w.Close()
	}

	// On an error the writer is abandoned, not closed: Close would write a
	// metadata segment, or the rest of a stream, after the failure. The
	// writer holds no file of its own (dst belongs to the caller).
	if anyString {
		if err := defragBuffered(w, dataChans, dataWrite); err != nil {
			return err
		}
		return w.Close()
	}

	if err := defragStream(w, f, dataChans, dataWrite); err != nil {
		return err
	}
	return w.Close()
}

// defragReject returns an error for a file whose layout this package reads but
// cannot write: DAQmx raw data, or a big-endian segment. Checked before any
// byte is written so the destination is not left partly populated.
func defragReject(f *File) error {
	for _, g := range f.Groups() {
		for _, c := range g.Channels() {
			if c.daqmx != nil {
				return wrap("defragment", c.path, fmt.Errorf("%w: DAQmx raw data is not rewritten", ErrDAQmx))
			}
			for i := range c.spans {
				if c.spans[i].big {
					return wrap("defragment", c.path, fmt.Errorf("%w: big-endian segments are not rewritten", ErrUnsupported))
				}
			}
		}
	}
	return nil
}

// defragStream writes the fixed-size channels as one contiguous segment,
// copying each channel's bytes straight from the source into the destination
// without decoding or re-encoding values. Memory stays within one read batch.
func defragStream(w *Writer, f *File, chans []*Channel, writers []*ChannelWriter) error {
	counts := make([]int, len(chans))
	for i, c := range chans {
		counts[i] = int(c.total)
	}
	s, err := w.Stream(writers, counts)
	if err != nil {
		return err
	}
	rb := f.readBuf
	if rb <= 0 {
		rb = DefaultReadBuffer
	}
	for i, c := range chans {
		size, ok := c.dt.Size()
		if !ok {
			return wrap("defragment", c.path, fmt.Errorf("%w: %s", ErrUnsupported, c.dt))
		}
		batch := rb / size
		if batch < 1 {
			batch = 1
		}
		buf := make([]byte, batch*size)
		total := int(c.total)
		for off := 0; off < total; {
			n := batch
			if off+n > total {
				n = total - off
			}
			region := buf[:n*size]
			if err := c.fillPacked(f.r, off, n, size, region); err != nil {
				return wrap("defragment", c.path, err)
			}
			if err := s.writeRawColumn(writers[i], region, n); err != nil {
				return err
			}
			off += n
		}
	}
	return s.Finish()
}

// defragBuffered writes every channel with the buffered Write path and a single
// Flush, producing one contiguous segment. It is used when the file has a
// string channel: a string channel's offset table needs every length before
// its payload, so Stream cannot carry it. This path holds each channel's full
// data in memory while the segment is assembled.
func defragBuffered(w *Writer, chans []*Channel, writers []*ChannelWriter) error {
	for i, c := range chans {
		vals, err := c.Values()
		if err != nil {
			return err
		}
		if err := writers[i].Write(vals); err != nil {
			return err
		}
	}
	return w.Flush()
}

func isStringType(dt DataType) bool {
	return dt == TypeString || dt.storage() == TypeString
}
