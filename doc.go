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
// raw data. Channel values are read on demand with Values, the typed helpers
// (Float64s, Times, Strings, …), or a Cursor that yields batches. The
// underlying reader must implement io.ReaderAt (os.File and bytes.Reader do).
// Concurrent value reads are safe when the ReaderAt is.
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
// non-seekable writer buffers the finished segment and writes it once.
//
// Files are little-endian, version 4713 (TDMS 2.0). Big-endian segments are
// accepted on read. DAQmx raw data is detected and rejected with ErrDAQmx
// rather than decoded.
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
