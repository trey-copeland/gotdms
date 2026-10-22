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
// segment whose next-segment offset runs past EOF. Reads of a file that
// opens still return whole values in that tail. Follow polls a file that is
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
// Stream declares each channel's value count, writes the segment header, and
// copies later chunks straight to the underlying writer. Memory stays within
// the write buffer plus the chunk being written. String channels stay on
// Write: the offset table needs every length first. Finish patches a seekable
// lead-in. A short stream is not patched into a finished segment.
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
// corrupt files.
package tdms
