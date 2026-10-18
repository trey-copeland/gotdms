// Package dataset reads sets of TDMS files as one catalog.
//
// You declare which channel carries time. WithTimestampChannelName names that
// channel for every group. WithTimestampChannel names it for one group and
// overrides the global name; an empty name opts that group out of the global
// name. Waveform timing (wf_start_time and wf_increment, read with StartTime
// and Increment) is only a fallback for a group that was not given a timestamp
// channel. Otherwise the series is untimed: Timed is false, the axis is the
// sample index, and a windowed read returns ErrUntimed. Nothing is inferred
// from sibling channel names.
//
// A declared timestamp channel is the source of truth. It wins even when the
// data channel also has waveform properties. It must sit in the same group and
// the same file as the data channel and have the same sample count. A missing
// channel, a length mismatch, or a type that is not a TDMS timestamp or a
// numeric value leaves that data channel untimed. That is not a catalog error,
// so one group can lack the global name while the others are timed. The
// declared channel is marked IsTimestamp. It is not given another channel as
// its own time source.
//
// Timestamp-typed channels are absolute times. Numeric timestamp channels
// (integers and floats) are seconds since the instant passed to
// WithNumericEpoch. There is no default epoch. Open returns ErrArgument if a
// declared timestamp channel is numeric and WithNumericEpoch was not set,
// because a bare 0.0 is not the Unix epoch unless the caller says so. Pass
// time.Unix(0, 0).UTC() when the numbers are Unix seconds.
//
// Relative time is sample time minus a basis, in seconds. It is zero only at
// the basis, positive after it, and negative before it. WithBasisTime sets
// the dataset basis; if omitted, the basis is the earliest timed sample in
// the catalog, fixed at Open. A window does not move it, so the first sample
// of a cropped read is not forced to zero. WithReadBasis overrides the basis
// for one Read, ReadWindow, or ReadMany, and ReadMany uses that one value for
// every series. Series.Basis is the basis that call stored. RelativeTimes
// uses it. RelativeTimesFrom computes against any other time without changing
// the series. An untimed series ignores the basis and returns sample indexes.
//
// Timestamp values must be non-decreasing inside a file. A step backwards
// returns ErrMonotonic from Read, ReadWindow, and ReadMany. Equal timestamps
// are kept in file order. Across files they follow the overlap policy, since
// a repeated time is not a positive increment. Gaps are recorded only when the
// previous file has a positive Dt and the next kept sample falls after last+dt.
// Y is not filled with NaN.
//
// # Names
//
// The same channel name in several groups or files is normal. Find, Search,
// and Match return every occurrence. A read with an empty group requires the
// name to appear in exactly one group; otherwise it returns *AmbiguousError.
// Pass Selector.Group to choose. Several files in that one group are merged.
//
// # Merge
//
// Occurrences are read in time order (first kept sample, then file path). The
// window is half-open: start <= t < end. Samples inside one file stay in file
// order. Overlap and gap policy applies where a new file meets samples already
// kept. OverlapKeepFirst (the default) drops an incoming sample that lies
// inside the previous sample's span: before last+dt when dt is positive, after
// last+dt when dt is negative, and at or before last when dt is 0.
// OverlapKeepLast drops kept samples that the incoming sample replaces.
// OverlapError returns ErrOverlap. Times are compared at nanosecond resolution:
// a sample within 1ns of last+dt is the next tick, not an overlap and not a
// gap. A single-sample file has Dt 0, so the space before the next file is
// visible in Times and is not turned into a Gap. Untimed channels that span
// files are concatenated in file-path order. A selector whose files do not
// share one time source returns ErrArgument.
//
// # Reads and memory
//
// Files whose span misses the window are not opened. A declared timestamp
// axis is the sample values, so catalog and read both decode that channel.
// Catalog keeps only the span (earliest, latest, uniform dt) and does not
// retain the samples. It still decodes every sample: the core reader has no
// range read, and a non-uniform or non-monotonic axis cannot be summarized
// from the endpoints alone. The decoded buffer is discarded before the next
// file. Read decodes the axis again. ReadMany of several channels in one file
// opens that file once and decodes a shared clock once; each series then gets
// its own copy of the time vector so later mutation does not alias. The paired
// data channel is decoded in full. Waveform fallback does not load samples to
// build the span. Inside a waveform file the core reader has no sample seek:
// Cursor starts at the first sample, batches before the window are discarded,
// and the walk stops at the window. A window at the end of a long channel
// still decodes the prefix. Sample i is StartTime plus i times Increment,
// rounded to the nearest nanosecond in one multiply.
//
// Waveform properties are the latest wf_start_time and wf_increment on the
// channel. Segments that append under that same t0 and dt are correct. A later
// segment that rewrites those properties is not reconstructed.
//
// Resample is opt-in. It never extrapolates past a series, and it omits grid
// points that lie inside a reported gap.
//
// Catalog and reads use a bounded worker pool (WithWorkers, WithReadWorkers).
// ReadMany's pool is the whole call: it does not start another pool per
// selector, so the worker count is also the cap on files open at once.
// Open does not keep file handles after it returns. Reads do not keep them
// either.
package dataset
