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
// WithSharedClock returns each timed series' times as a Clock of int64 Unix
// nanoseconds instead of Times, and series of one call read from the same
// clock share the *Clock. Series.AbsoluteTimes returns []time.Time from
// either form. Times that do not fit in int64 nanoseconds keep Times.
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
// axis is the sample values. Catalog does not prove that the axis is
// monotonic. When the first step is positive it reads only the first two
// samples, via Slice: Dt is that step, and Last is the time of sample n-1 on
// a uniform arithmetic axis. The last sample itself is not fetched. That span
// matches a well-formed clock and is
// provisional otherwise. A window that misses it does not open the file, so
// an irregular axis can be skipped. A first step that is not positive is
// scanned in full (true min/max, Dt only when every step matches). Read
// decodes the axis again and returns ErrMonotonic if a sample steps
// backwards. Series.Dt comes from that decode and is 0 when a later step
// disagrees with the first. ReadMany of several channels in one file opens
// that file once and decodes a shared clock once; each series then gets its
// own copy of the time vector so later mutation does not alias. Returned
// slices are owned by the Series. Waveform fallback does not load samples to
// build the span. A waveform window and a declared clock whose catalog step
// is positive read the sample range with CursorAt and Slice. The samples
// before the window are not decoded. A declared clock that does not match
// that step is decoded in full so a backwards step is still reported.
// Sample i is StartTime plus i times Increment, rounded to the nearest
// nanosecond in one multiply.
//
// Waveform properties are the latest wf_start_time and wf_increment on the
// channel. Segments that append under that same t0 and dt are correct. A later
// segment that rewrites those properties is not reconstructed.
//
// Resample is opt-in. It never extrapolates past a series, and it omits grid
// points that lie inside a reported gap. dt is rounded to nanoseconds. A step
// that rounds below 1ns returns ErrArgument instead of spinning.
//
// WithSegmentIndex opts catalog and reads into a tdms segment-index sidecar
// per file (path plus ".gtdx"). The cache is validated by tdms and ignored
// when it does not match the file. It is off unless the option is passed.
//
// Catalog and reads use a bounded worker pool (WithWorkers, WithReadWorkers).
// ReadMany's pool is the whole call: it does not start another pool per
// selector, so the worker count is also the cap on files open at once.
// The default cap is 4. A larger cap, including WithReadWorkers(100), opens
// that many files together and can exhaust the process file-descriptor limit.
// An error closes every file that call opened. Open does not keep file
// handles after it returns. Reads do not keep them either.
package dataset
