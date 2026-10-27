# Defragment

[Back to the docs index](README.md)

A TDMS file written incrementally holds one segment per append. Each segment has its own 28-byte lead-in, its own metadata, and a small raw-data chunk, and each channel's samples are scattered across every segment that carried them. A file logged over a long run can reach thousands of segments. Reading it then pays per-segment overhead and gathers each channel from many places.

Defragment rewrites such a file as a single segment whose raw data is contiguous per channel, so later reads are faster. This is the purpose of NI LabVIEW's TDMS Defragment function, and `gotdms` matches its intent: the channel tree, channel order, data types, every value, and each object's final property values are unchanged. Only the physical layout changes.

## Why a single segment is faithful

The reader already flattens a file to, per channel, one concatenated sample sequence plus the final value of each property, because TDMS property writes are last-wins and the format has no notion of a gap in a channel. So any file this package can open reads identically to a single segment that stores every channel's full sample run back to back with final properties. That single-segment form is the most defragmented a file can be, and it is what Defragment writes: one metadata block naming every object, followed by one contiguous raw-data section.

Channel order is first-appearance order, the order the reader assigns and the order the writer keeps. A channel that appeared only in later segments, or disappeared partway through, still ends up with exactly the samples the reader reports, written once. A channel that carries no samples (and a group with only properties) is preserved as a metadata object.

## API

```go
// Write a defragmented copy of src to dst.
func Defragment(dst io.Writer, src io.ReaderAt, opts ...ReaderOption) error

// Write a defragmented copy of srcPath to dstPath (created or truncated).
func DefragmentToFile(dstPath, srcPath string, opts ...ReaderOption) error

// Rewrite path in place, atomically.
func DefragmentFile(path string, opts ...ReaderOption) error
```

`src` must report its length the way `NewReader` requires (a `Size` method, an `*os.File`, or an `io.Seeker`). The `ReaderOption`s are passed to the open, so `WithLenientTail` defragments a file that ends in a recoverable torn tail (the torn bytes are dropped, the whole segments are kept) and `WithSegmentIndex` reuses a `.gtdx` sidecar to skip the metadata walk.

`DefragmentFile` writes the copy to a temporary file in the same directory and renames it over the original, so an interrupted run leaves either the old file or the new one, never a half-written file. If defragmentation fails the original is left untouched and the temporary file is removed. Output is always little-endian TDMS 2.0.

## Performance

For a file whose data channels are all fixed-size, Defragment copies each channel's bytes straight from the source to the destination without decoding or re-encoding a single value. It gathers each channel in read-buffer-sized batches (`WithReadBuffer`, default 256 KiB) and streams them through a declared-count segment (`Writer.Stream`), so resident memory stays within one batch per channel regardless of file size. This is the common case for logged measurement data and where defragmentation matters most.

A file that contains a string channel takes a buffered path instead: a string channel's offset table needs every string's length before its payload, so it cannot be streamed. Every channel's full data is assembled in memory for one segment. The result is the same single contiguous segment; only the memory cost is higher. (A future optimization could stream the numeric channels while buffering only the string column.)

## What is not rewritten

Two layouts this package reads but never writes are rejected, so defragmentation never silently changes the on-disk format. Both are checked before any byte is written, and `DefragmentToFile` / `DefragmentFile` leave the destination (and the original) untouched on rejection.

- **DAQmx raw data** returns `ErrDAQmx`. The reader decodes DAQmx to native values, but the writer does not produce the DAQmx layout, so a rewrite would turn DAQmx channels into plain channels.
- **A big-endian segment** returns `ErrUnsupported`. The writer only emits little-endian segments. (Big-endian input could be converted to little-endian in a future opt-in; the values would be identical.)

A channel that has a type on disk but zero samples is preserved as a metadata object with its properties, but its type is not carried, because a zero-sample channel has no raw-data index to hold one; it re-reads as a void channel. Channels with samples keep their exact data type, including the float-with-unit variants.

## CLI

```bash
gotdms defragment run.tdms                 # rewrite in place
gotdms defragment -o clean.tdms run.tdms   # write a copy, leave run.tdms alone
gotdms defragment --lenient run.tdms       # recover a torn tail first
```

See [the CLI page](cli.md#defragment).
