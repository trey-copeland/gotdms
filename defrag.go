package tdms

import (
	"fmt"
	"io"
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
// property values. Output is little-endian TDMS 2.0.
//
// src must report its length the same way NewReader requires (a Size method,
// an *os.File, or an io.Seeker). ReaderOptions are passed to the internal
// open, so WithLenientTail defragments a file with a recoverable torn tail
// (the torn bytes are dropped) and WithSegmentIndex reuses a sidecar.
//
// A file that contains DAQmx raw data returns ErrDAQmx and a file with a
// big-endian segment returns ErrUnsupported; this package does not write
// either, so rewriting them would silently change the on-disk format. Nothing
// is written to dst in those cases beyond what had already streamed; use
// DefragmentFile or DefragmentToFile for a clean all-or-nothing result.
func Defragment(dst io.Writer, src io.ReaderAt, opts ...ReaderOption) error {
	f, err := NewReader(src, opts...)
	if err != nil {
		return err
	}
	return defragment(dst, f)
}

// DefragmentToFile reads the TDMS file at srcPath and writes a defragmented
// copy to dstPath. dstPath is created or truncated. See Defragment.
func DefragmentToFile(dstPath, srcPath string, opts ...ReaderOption) error {
	f, err := Open(srcPath, opts...)
	if err != nil {
		return err
	}
	defer f.Close()
	out, err := os.Create(dstPath)
	if err != nil {
		return wrap("defragment", dstPath, err)
	}
	if err := defragment(out, f); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return wrap("defragment", dstPath, err)
	}
	return nil
}

// DefragmentFile rewrites the TDMS file at path in place. The defragmented
// copy is written to a temporary file in the same directory and renamed over
// the original, so an interrupted run leaves either the old file or the new
// one, never a half-written file. If defragmentation fails the original is
// left untouched. See Defragment.
func DefragmentFile(path string, opts ...ReaderOption) error {
	f, err := Open(path, opts...)
	if err != nil {
		return err
	}
	defer f.Close()

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".defrag-*")
	if err != nil {
		return wrap("defragment", path, err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if err := defragment(tmp, f); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return wrap("defragment", path, err)
	}
	// The reader still has the original open; close it before the rename so
	// Windows, which refuses to replace an open file, can complete it.
	f.Close()
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return wrap("defragment", path, err)
	}
	return nil
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
				w.Close()
				return err
			}
			for _, p := range c.Properties() {
				cw.ch.obj.setProp(p)
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

	if anyString {
		if err := defragBuffered(w, dataChans, dataWrite); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	}

	if err := defragStream(w, f, dataChans, dataWrite); err != nil {
		w.Close()
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
