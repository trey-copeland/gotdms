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
// the raw bytes. On a little-endian host, Write retains a fixed-size numeric
// or complex slice until Flush or Close; do not mutate it before then. Other
// value kinds are copied at Write. Flush streams that buffer and does not
// allocate a second full copy.
//
// Files are little-endian, version 4713 (TDMS 2.0). Big-endian segments are
// accepted on read. DAQmx raw data is detected and rejected with ErrDAQmx
// rather than decoded.
//
// Malformed input returns an error. The package does not panic on truncated or
// corrupt files.
package tdms
