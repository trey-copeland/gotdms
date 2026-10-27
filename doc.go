// Package tdms reads and writes NI TDMS 2.0 files.
//
// TDMS (Technical Data Management Streaming) is National Instruments' binary
// format for measurement data. A file is a sequence of segments. Each segment
// has a 28-byte lead-in, an optional metadata section (object paths, raw-data
// indexes, and properties), and an optional raw-data section. Objects form a
// three-level tree: the file ("/"), groups ("/'Group'"), and channels
// ("/'Group'/'Channel'"). Only channels carry raw data.
//
// The on-disk layout implemented here follows "TDMS File Format Internal
// Structure" (NI, updated Nov 12, 2024):
//
//	https://www.ni.com/en/support/documentation/supplemental/07/tdms-file-format-internal-structure.html
//
// The package is pure Go with no cgo and no third-party dependencies. The
// package name is tdms; the module path is github.com/trey-copeland/gotdms.
//
// # Example
//
// Write one channel in two segments, then read it back whole and in batches:
//
//	w, err := tdms.Create("run.tdms")
//	if err != nil {
//		return err
//	}
//	ch, err := w.Group("acquisition").Channel("temperature", tdms.TypeFloat64)
//	if err != nil {
//		return err
//	}
//	if err := ch.Write([]float64{21.5, 21.7, 22.0, 22.4}); err != nil {
//		return err
//	}
//	if err := w.Flush(); err != nil { // ends the first segment
//		return err
//	}
//	if err := ch.Write([]float64{22.8}); err != nil {
//		return err
//	}
//	if err := w.Close(); err != nil {
//		return err
//	}
//
//	f, err := tdms.Open("run.tdms")
//	if err != nil {
//		return err
//	}
//	defer f.Close()
//	g, _ := f.Group("acquisition")
//	c, _ := g.Channel("temperature")
//	values, err := c.Float64s() // [21.5 21.7 22 22.4 22.8]
//
// ExampleCreate is the compile-checked version. The repository's docs
// directory has one page per topic; see the Documentation section below.
//
// # Reading
//
// Open or NewReader parses every segment's metadata up front and does not load
// raw data. Channel values are read on demand with Values, Slice (a sample
// range), the typed helpers (Float64s, Times, Strings, …), or a Cursor that
// yields batches. ReadValues and Group.Values read several channels at once
// and split a shared interleaved segment in one pass. Analog waveforms
// (t0, dt, Y, unit) are ReadWaveform / Channel.Waveform; they load every
// sample. Cursor plus WaveformHeader timestamps a channel in chunks.
// CursorAt streams a sample range. The underlying reader must implement
// io.ReaderAt (os.File and bytes.Reader do). Concurrent value reads are safe
// when the ReaderAt is. A Cursor over a contiguous fixed-size channel reads
// a batch of at least 32 KiB with ReadAt into the reused slice; smaller
// batches and interleaved channels use a sequential buffer. Values and
// Slice use ReadAt.
//
// Channel.ChunkStats reports count, min, max, mean, and RMS for each chunk of
// a numeric channel. The caller chooses the chunk size in samples. The walk
// uses Cursor and does not retain earlier chunks. String, boolean, timestamp,
// and complex channels return ErrType.
//
// File.Status reports whether the file ended on a finished segment, on the
// NI incomplete-segment sentinel, or (with WithLenientTail) on a final
// segment whose next-segment offset runs past EOF or a torn tail after the
// last whole segment, which is dropped. Reads of a file that opens still
// return whole values in a past-EOF or sentinel tail. Follow polls a file that is
// still being written and yields samples only from finished segments.
// WithSegmentIndex reuses a sidecar of the segment layout when the file's
// size, modification time, identity, and a fingerprint of the lead-ins and
// metadata still match. A cache hit still reads those lead-in and metadata
// bytes to recompute the fingerprint. A missing, stale, or corrupt sidecar
// is ignored.
//
// # Writing
//
// Create or NewWriter buffers one segment at a time. Flush emits that segment.
// Calling Write more than once on a channel before Flush appends to the same
// segment. The next segment reuses the previous raw-data index (and omits
// metadata entirely when nothing about the index or properties changed). A
// seekable writer publishes an incomplete-segment lead-in (next-segment offset
// 0xFFFFFFFFFFFFFFFF) and patches the real offset after the raw data is on
// disk, so a crash mid-segment still matches the rule in the NI article. A
// non-seekable writer writes the lead-in with the finished length, then streams
// the raw bytes. Write copies values into the channel buffer, so the caller
// may reuse the slice. WriteNoCopy skips that copy for a fixed-size numeric
// or complex slice; the caller must not mutate it until Flush or Close.
// Flush streams the channel buffer and does not allocate a second full copy.
//
// By default the writer never calls fsync, so the incomplete-segment rule
// protects against a process crash, not a power loss or OS crash. WithSync
// (Create(path, WithSync())) writes each segment with the sentinel, fsyncs,
// patches the length, and fsyncs again; Create also syncs the parent
// directory once (skipped on Windows). Writer.Sync writes the pending segment
// and fsyncs with or without the option. A power loss during a later segment
// can leave a torn tail, which WithLenientTail recovers on read.
//
// Stream declares each channel's value count, writes the segment header, and
// copies later chunks straight to the underlying writer. Memory stays within
// the write buffer plus the chunk being written. String channels stay on
// Write: the offset table needs every length first. Finish patches a seekable
// lead-in. A short stream is not patched into a finished segment.
//
// # Defragment
//
// Defragment, DefragmentToFile, and DefragmentFile rewrite a file that was
// written in many segments as one contiguous segment, so later reads are
// faster. This matches NI LabVIEW's TDMS Defragment: the channel tree, order,
// types, values, and final property values are unchanged. A file whose data
// channels are all fixed-size is copied byte for byte through a declared-count
// Stream, with no value decode and memory bounded by one read batch per
// channel; a file with a string channel is buffered. DAQmx raw data returns
// ErrDAQmx and a big-endian segment returns ErrUnsupported, because this
// package writes neither. DefragmentFile rewrites in place through a temporary
// file renamed over the original.
//
// Files are little-endian, version 4713 (TDMS 2.0). Big-endian segments are
// accepted on read. DAQmx raw data (kTocDAQmxRawData, index words 0x00001269,
// 0x00001369, and 0x0000126A) is decoded to the scaler's native type. Values
// stay raw; NI_Scale linear and polynomial chains are applied only by
// DAQmxScaledFloat64s and ApplyDAQmxScale. A layout this package does not
// decode returns ErrDAQmx.
//
// Sets of files are the dataset subpackage
// (github.com/trey-copeland/gotdms/dataset). The caller declares the timestamp
// channel, globally or per group. Waveform wf_start_time and wf_increment are
// a fallback when no timestamp channel was declared. This package stays a
// single-file reader and writer.
//
// Malformed input returns an error. The package does not panic on truncated or
// corrupt files. Errors wrap a sentinel such as ErrTruncated, ErrCorrupt, or
// ErrUnsupported; test them with errors.Is. Most also carry the operation
// and path in an *Error.
//
// # Documentation
//
// The README is a short tour. The reference pages in the repository's docs
// directory cover each topic in full. They track the main branch, so they
// can describe changes newer than the version shown here; CHANGELOG.md says
// what each release contains.
//
//   - [Reading]: Open, Values, Cursor, typed helpers, chunk statistics.
//   - [Writing]: Create, Flush, Write copy semantics, WithSync, Stream.
//   - [Defragment]: Defragment, DefragmentToFile, DefragmentFile.
//   - [Waveforms]: Waveform, ReadWaveform, WaveformHeader, scales.
//   - [File status]: File.Status, WithLenientTail, torn tails, Follow.
//   - [Datasets]: the dataset subpackage.
//   - [DAQmx]: DAQmx raw-data layouts and scaling.
//   - [API]: a table of the exported API, channel order, and errors.
//   - [Format and support]: the support matrix and format choices.
//   - [CLI]: the gotdms command.
//   - [Segment index]: the .gtdx sidecar.
//   - [Performance]: read and write paths and benchmarks.
//   - [Testing]: fuzzing, golden tests, and the CI-only npTDMS oracle.
//
// [Reading]: https://github.com/trey-copeland/gotdms/blob/main/docs/reading.md
// [Writing]: https://github.com/trey-copeland/gotdms/blob/main/docs/writing.md
// [Defragment]: https://github.com/trey-copeland/gotdms/blob/main/docs/defragment.md
// [Waveforms]: https://github.com/trey-copeland/gotdms/blob/main/docs/waveforms.md
// [File status]: https://github.com/trey-copeland/gotdms/blob/main/docs/file-status.md
// [Datasets]: https://github.com/trey-copeland/gotdms/blob/main/docs/dataset.md
// [DAQmx]: https://github.com/trey-copeland/gotdms/blob/main/docs/daqmx.md
// [API]: https://github.com/trey-copeland/gotdms/blob/main/docs/api.md
// [Format and support]: https://github.com/trey-copeland/gotdms/blob/main/docs/format.md
// [CLI]: https://github.com/trey-copeland/gotdms/blob/main/docs/cli.md
// [Segment index]: https://github.com/trey-copeland/gotdms/blob/main/docs/segment-index.md
// [Performance]: https://github.com/trey-copeland/gotdms/blob/main/docs/performance.md
// [Testing]: https://github.com/trey-copeland/gotdms/blob/main/docs/testing.md
package tdms
