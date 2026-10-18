package dataset

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trey-copeland/gotdms"
)

// OverlapPolicy decides what happens when a later file contains samples at
// times the merged series already covers.
type OverlapPolicy int

const (
	// OverlapKeepFirst drops the incoming sample. This is the default.
	OverlapKeepFirst OverlapPolicy = iota
	// OverlapKeepLast drops already kept samples that the incoming sample
	// replaces (their time is not strictly before the incoming time).
	OverlapKeepLast
	// OverlapError makes the read return ErrOverlap.
	OverlapError
)

// TimeSource is how an occurrence got its time axis.
type TimeSource int

const (
	// TimeNone means the occurrence is untimed. The axis is the sample index.
	TimeNone TimeSource = iota
	// TimeTimestamp means a declared timestamp channel. The channel itself
	// uses its own values. A data channel uses the channel named for its group.
	TimeTimestamp
	// TimeWaveform means wf_start_time and wf_increment. This is the fallback
	// used only when the group was not given a timestamp channel.
	TimeWaveform
)

func (s TimeSource) String() string {
	switch s {
	case TimeNone:
		return "none"
	case TimeTimestamp:
		return "timestamp"
	case TimeWaveform:
		return "waveform"
	default:
		return fmt.Sprintf("TimeSource(%d)", int(s))
	}
}

// Sentinel errors. *AmbiguousError unwraps to ErrAmbiguous.
var (
	ErrNoFiles   = errSentinel("no TDMS files")
	ErrNotFound  = errSentinel("channel not found")
	ErrAmbiguous = errSentinel("ambiguous channel name")
	ErrUntimed   = errSentinel("channel has no time axis")
	ErrOverlap   = errSentinel("overlapping samples")
	ErrMonotonic = errSentinel("timestamp channel is not monotonic")
	ErrArgument  = errSentinel("invalid argument")
)

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// AmbiguousError is returned when a channel name occurs in more than one
// group and Selector.Group is empty. Candidates lists every occurrence.
// Nothing is chosen automatically.
type AmbiguousError struct {
	Name       string
	Candidates []Occurrence
}

func (e *AmbiguousError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ambiguous channel %q:", e.Name)
	for _, c := range e.Candidates {
		fmt.Fprintf(&b, " group %q file %s;", c.Group, c.File)
	}
	return b.String()
}

func (e *AmbiguousError) Unwrap() error { return ErrAmbiguous }

// Occurrence is one channel in one file. The same name in another group or
// file is a separate occurrence.
type Occurrence struct {
	Name  string
	Group string
	File  string
	// IsTimestamp is true when this channel is the declared timestamp channel
	// for its group. Its values are the time axis. It does not reference
	// another channel.
	IsTimestamp bool
	// Samples is Channel.Len. The core total is an int64 narrowed to int by
	// that method; index math inside this package stays int64.
	Samples int
	Timed   bool
	Source  TimeSource
	// Start and Last bound the samples catalog will open for a window.
	// Both are zero when Timed is false or Samples is 0. Waveform fallback
	// sets them from wf_start_time and wf_increment with no sample reads.
	//
	// A declared clock whose first step is positive is not scanned. Start is
	// the first sample, Dt is that step in seconds, and Last is the instant
	// of sample Samples-1 on a uniform arithmetic axis (first + (n-1) steps).
	// That matches a well-formed clock. It is provisional: later steps are
	// checked at read, not here, and a window that misses this span does not
	// open the file. An irregular axis can be skipped. A first step that is
	// not positive is scanned in full, so Start/Last are the true min/max.
	Start time.Time
	Last  time.Time
	// Dt is the step in seconds. It is 0 for untimed channels, a single
	// sample, and a declared clock whose first step is not positive and whose
	// full scan is not uniform. For a declared clock with a positive first
	// step, Dt is that step and is provisional until read; Series.Dt is the
	// value uniformDt computes from the decoded axis, or 0 when a later step
	// disagrees. Waveform fallback stores wf_increment, which may be negative
	// or zero.
	Dt float64
	// Timestamp is the declared timestamp channel name when this data channel
	// took its axis from that channel. It is empty on the timestamp channel
	// itself and when Source is not TimeTimestamp.
	Timestamp string
}

// Selector names a channel. Group empty means the name must belong to one
// group across the dataset. Several files in that group are merged.
type Selector struct {
	Name  string
	Group string
}

// Gap is a hole between merged pieces. From is the exclusive end of the
// previous sample (last + dt). To is the next kept sample. Y has no filler.
// From is after To when dt is negative.
type Gap struct {
	From time.Time
	To   time.Time
}

// Series is a merged channel read. Times is nil when Timed is false.
// Basis is the time RelativeTimes subtracts. Dt is the shared positive
// increment when every contributing sample has that increment, else 0.
type Series struct {
	Name        string
	Group       string
	Files       []string
	Timed       bool
	IsTimestamp bool
	Source      TimeSource
	Dt          float64
	// Y and Times are owned by this Series. They do not alias another Series,
	// a shared clock retained for a second channel, or a tdms.Cursor batch.
	// Cursor batches are valid only until the next Next, so any Series built
	// from a cursor copies the values out. RelativeTimes and RelativeTimesFrom
	// allocate a new vector on every call.
	Y     []float64
	Times []time.Time
	Gaps  []Gap
	// Basis is the anchor for RelativeTimes: sample time minus Basis, in
	// seconds. It is the dataset basis, or the WithReadBasis value for the
	// call that returned this series. It is not the first sample of the
	// series or of the window. Resample copies it. RelativeTimesFrom does
	// not change it.
	Basis time.Time
}

// RelativeTimes returns seconds since Basis for a timed series.
// A sample before Basis is negative. Nothing is clamped or shifted so that
// the first sample is zero.
//
// An untimed series returns the sample indexes 0, 1, 2, … as float64.
// Basis does not apply. Timed distinguishes the two.
func (s Series) RelativeTimes() []float64 {
	return s.RelativeTimesFrom(s.Basis)
}

// RelativeTimesFrom returns seconds since basis without changing s.
// For a timed series each value is sample_time.Sub(basis).Seconds(): zero
// only at basis, positive after, negative before. basis need not be a sample.
// An untimed series ignores basis and returns sample indexes, the same as
// RelativeTimes.
func (s Series) RelativeTimesFrom(basis time.Time) []float64 {
	if !s.Timed {
		out := make([]float64, len(s.Y))
		for i := range out {
			out[i] = float64(i)
		}
		return out
	}
	out := make([]float64, len(s.Times))
	for i, ts := range s.Times {
		out[i] = ts.Sub(basis).Seconds()
	}
	return out
}

// Interp chooses Resample's opt-in interpolation. Reads never resample unless
// Resample is called.
type Interp int

const (
	// InterpLinear interpolates between adjacent native samples.
	InterpLinear Interp = iota
	// InterpPrevious holds the latest native sample at or before the grid time.
	InterpPrevious
)

// Dataset is an indexed set of TDMS files. It is safe for concurrent reads
// after Open returns. Open closes every file it scans.
type Dataset struct {
	files []string
	occ   []Occurrence
	basis time.Time
	opt   options
}

type options struct {
	workers     int
	readWorkers int
	basis       time.Time
	basisSet    bool
	epoch       time.Time
	epochSet    bool
	overlap     OverlapPolicy
	tsName      string
	tsNameSet   bool
	ts          map[string]string
}

type clockMode int

const (
	clockNone clockMode = iota
	clockNamed
	clockOptOut
)

// Option configures OpenFiles and OpenGlob.
type Option func(*options)

// WithWorkers sets the catalog pool size. Non-positive selects the default,
// which is GOMAXPROCS capped at 8.
func WithWorkers(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.workers = n
		}
	}
}

// WithReadWorkers sets how many files Read and ReadMany open at once.
// Non-positive selects the default of 4. The value is a cap, not a target.
// WithReadWorkers(100) holds up to 100 files open for the duration of the
// call and fails when the process is out of file descriptors. An error
// closes every file that call opened.
func WithReadWorkers(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.readWorkers = n
		}
	}
}

// WithBasisTime sets the basis subtracted by RelativeTimes. Without it, the
// basis is the earliest Start among timed occurrences in the catalog, chosen
// once at Open. A window does not move it. A sample before the basis has a
// negative relative time; a sample after it is positive. The zero Time is a
// real basis when this option is passed. This is not the epoch of numeric
// timestamp values; that is WithNumericEpoch. WithReadBasis overrides it for
// one call.
func WithBasisTime(t time.Time) Option {
	return func(o *options) {
		o.basis = t
		o.basisSet = true
	}
}

// WithNumericEpoch sets the absolute time of numeric timestamp value 0.
// Integers and floats in a declared timestamp channel are seconds since this
// instant. Timestamp-typed channels ignore it. Without this option, Open
// returns ErrArgument when a declared timestamp channel is numeric: the
// numbers are not assumed to be Unix seconds. Use time.Unix(0, 0).UTC() for
// seconds since the Unix epoch. The zero Time is a real epoch only when this
// option is passed.
func WithNumericEpoch(t time.Time) Option {
	return func(o *options) {
		o.epoch = t
		o.epochSet = true
	}
}

// WithOverlap sets the merge policy. The default is OverlapKeepFirst.
func WithOverlap(p OverlapPolicy) Option {
	return func(o *options) {
		o.overlap = p
	}
}

// WithTimestampChannelName declares that every group uses a timestamp channel
// with this name, except groups named by WithTimestampChannel. name must be
// non-empty. A group that has no channel of that name leaves its data channels
// untimed; catalog still succeeds. A later call replaces the name.
func WithTimestampChannelName(name string) Option {
	return func(o *options) {
		o.tsName = name
		o.tsNameSet = true
	}
}

// WithTimestampChannel declares the timestamp channel for one group. It
// overrides WithTimestampChannelName for that group. A later call for the same
// group replaces the earlier one. group must be non-empty. An empty name opts
// the group out of the global name: no channel in the group is a timestamp
// source, and waveform properties may still supply the fallback axis.
func WithTimestampChannel(group, name string) Option {
	return func(o *options) {
		if o.ts == nil {
			o.ts = map[string]string{}
		}
		o.ts[group] = name
	}
}

func (o options) declared(group string) (string, clockMode) {
	if n, ok := o.ts[group]; ok {
		if n == "" {
			return "", clockOptOut
		}
		return n, clockNamed
	}
	if o.tsNameSet {
		return o.tsName, clockNamed
	}
	return "", clockNone
}

func defaultOptions() options {
	n := runtime.GOMAXPROCS(0)
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return options{workers: n, readWorkers: 4, overlap: OverlapKeepFirst}
}

// OpenFiles indexes paths. Paths are cleaned to absolute paths and
// de-duplicated. Metadata is read in parallel. Declared timestamp channels
// are read so their span is known. Waveform fallback spans come from
// StartTime, Increment, and Len, without loading samples.
func OpenFiles(paths []string, opts ...Option) (*Dataset, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w", ErrNoFiles)
	}
	opt := defaultOptions()
	for _, o := range opts {
		if o != nil {
			o(&opt)
		}
	}
	if opt.workers < 1 {
		opt.workers = 1
	}
	if opt.readWorkers < 1 {
		opt.readWorkers = 1
	}
	if opt.tsNameSet && opt.tsName == "" {
		return nil, fmt.Errorf("%w: timestamp channel name must be non-empty", ErrArgument)
	}
	for g := range opt.ts {
		if g == "" {
			return nil, fmt.Errorf("%w: timestamp channel group must be non-empty", ErrArgument)
		}
	}
	abs, err := uniqueAbs(paths)
	if err != nil {
		return nil, err
	}
	if len(abs) == 0 {
		return nil, fmt.Errorf("%w", ErrNoFiles)
	}
	parts := make([][]Occurrence, len(abs))
	errs := make([]error, len(abs))
	run(opt.workers, len(abs), func(i int) {
		parts[i], errs[i] = indexFile(abs[i], opt)
	})
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("dataset: index %s: %w", abs[i], err)
		}
	}
	var occ []Occurrence
	for _, p := range parts {
		occ = append(occ, p...)
	}
	sort.Slice(occ, func(i, j int) bool {
		if occ[i].File != occ[j].File {
			return occ[i].File < occ[j].File
		}
		if occ[i].Group != occ[j].Group {
			return occ[i].Group < occ[j].Group
		}
		return occ[i].Name < occ[j].Name
	})
	ds := &Dataset{files: abs, occ: occ, opt: opt}
	ds.basis = opt.basis
	if !opt.basisSet {
		ds.basis = earliest(occ)
	}
	return ds, nil
}

// OpenGlob indexes files matched by filepath.Glob(filepath.Join(dir, pattern)).
// pattern is a filepath.Glob pattern relative to dir, such as "*.tdms".
// An absolute pattern replaces dir, which is how filepath.Join works.
// No matches returns ErrNoFiles. Recursive "**" is not supported.
func OpenGlob(dir, pattern string, opts ...Option) (*Dataset, error) {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrArgument, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoFiles, filepath.Join(dir, pattern))
	}
	return OpenFiles(matches, opts...)
}

// Basis is the default anchor for RelativeTimes. A read uses it unless
// WithReadBasis overrides that call. It does not change when a window crops
// the samples.
func (d *Dataset) Basis() time.Time { return d.basis }

// ReadOption configures one Read, ReadWindow, or ReadMany call.
// It does not change the dataset.
type ReadOption func(*readOptions)

type readOptions struct {
	basis    time.Time
	basisSet bool
}

// WithReadBasis sets the basis for this call only. ReadMany applies t to
// every returned series. A later WithReadBasis on the same call replaces the
// earlier one. The dataset basis is unchanged. Relative time is the sample
// time minus t, in seconds, and is negative before t. The zero Time is a real
// basis when this option is passed.
func WithReadBasis(t time.Time) ReadOption {
	return func(o *readOptions) {
		o.basis = t
		o.basisSet = true
	}
}

func (d *Dataset) basisFor(opts []ReadOption) time.Time {
	var cfg readOptions
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if cfg.basisSet {
		return cfg.basis
	}
	return d.basis
}

// Files returns the absolute paths that were indexed, in input order after
// de-duplication. Glob results are sorted by filepath.Glob.
func (d *Dataset) Files() []string {
	out := make([]string, len(d.files))
	copy(out, d.files)
	return out
}

// Occurrences returns every channel occurrence. The slice is a copy.
func (d *Dataset) Occurrences() []Occurrence { return cloneOcc(d.occ) }

// Find returns occurrences whose channel name equals name. Every group and
// file is included. The slice is a copy.
func (d *Dataset) Find(name string) []Occurrence {
	var out []Occurrence
	for _, o := range d.occ {
		if o.Name == name {
			out = append(out, o)
		}
	}
	return out
}

// Search returns occurrences whose channel name contains substr.
// The match is case-sensitive. An empty substr matches every occurrence.
func (d *Dataset) Search(substr string) []Occurrence {
	var out []Occurrence
	for _, o := range d.occ {
		if strings.Contains(o.Name, substr) {
			out = append(out, o)
		}
	}
	return out
}

// Match returns occurrences whose channel name matches pattern.
// pattern uses filepath.Match syntax ('*', '?', character classes).
// A bad pattern returns ErrArgument.
func (d *Dataset) Match(pattern string) ([]Occurrence, error) {
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrArgument, err)
	}
	var out []Occurrence
	for _, o := range d.occ {
		ok, err := filepath.Match(pattern, o.Name)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrArgument, err)
		}
		if ok {
			out = append(out, o)
		}
	}
	return out, nil
}

// Read merges every file of the channel and returns all samples.
// Untimed channels are concatenated in file-path order. opts may include
// WithReadBasis; the dataset basis is used otherwise.
func (d *Dataset) Read(sel Selector, opts ...ReadOption) (Series, error) {
	return d.read(sel, time.Time{}, time.Time{}, false, d.basisFor(opts))
}

// ReadWindow merges the channel and keeps samples with start <= t < end.
// Untimed channels return ErrUntimed. Files whose span misses the window are
// not opened. A timestamp axis that steps backwards returns ErrMonotonic.
// The basis is not moved to the first sample inside the window.
func (d *Dataset) ReadWindow(sel Selector, start, end time.Time, opts ...ReadOption) (Series, error) {
	if !end.After(start) {
		return Series{}, fmt.Errorf("%w: window end must be after start", ErrArgument)
	}
	return d.read(sel, start, end, true, d.basisFor(opts))
}

// ReadMany reads each selector over the same half-open window [start, end).
// Every series uses one basis: WithReadBasis if passed, otherwise the dataset
// basis. The result order matches sels. One untimed selector fails the call
// with ErrUntimed.
//
// Channels that share a file are read from one open. A timestamp channel used
// by several of those channels is decoded once per call. Concurrency is
// WithReadWorkers across that work, not one pool per selector.
func (d *Dataset) ReadMany(sels []Selector, start, end time.Time, opts ...ReadOption) ([]Series, error) {
	if !end.After(start) {
		return nil, fmt.Errorf("%w: window end must be after start", ErrArgument)
	}
	return d.readShared(sels, start, end, d.basisFor(opts))
}

func (d *Dataset) read(sel Selector, start, end time.Time, window bool, basis time.Time) (Series, error) {
	hits, err := d.resolve(sel)
	if err != nil {
		return Series{}, err
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].Timed != hits[0].Timed || hits[i].Source != hits[0].Source || hits[i].IsTimestamp != hits[0].IsTimestamp {
			return Series{}, fmt.Errorf("%w: %q group %q has mixed time axes", ErrArgument, hits[0].Name, hits[0].Group)
		}
	}
	if err := validateWindow(hits, window); err != nil {
		return Series{}, err
	}
	type job struct {
		o Occurrence
	}
	var jobs []job
	for _, o := range hits {
		if window && o.Timed && !hitsWindow(o, start, end) {
			continue
		}
		jobs = append(jobs, job{o: o})
	}
	pieces := make([]piece, len(jobs))
	errs := make([]error, len(jobs))
	run(d.opt.readWorkers, len(jobs), func(i int) {
		pieces[i], errs[i] = readOccurrence(jobs[i].o, d.opt, start, end, window)
	})
	for i, err := range errs {
		if err != nil {
			return Series{}, fmt.Errorf("dataset: read %s %q/%q: %w", jobs[i].o.File, jobs[i].o.Group, jobs[i].o.Name, err)
		}
	}
	return d.finish(sel, hits, pieces, basis)
}

func validateWindow(hits []Occurrence, window bool) error {
	if !window {
		return nil
	}
	for _, h := range hits {
		if !h.Timed {
			return fmt.Errorf("%w: %q group %q", ErrUntimed, h.Name, h.Group)
		}
	}
	return nil
}

func (d *Dataset) finish(sel Selector, hits []Occurrence, pieces []piece, basis time.Time) (Series, error) {
	sort.Slice(pieces, func(i, j int) bool {
		if !pieces[i].start.Equal(pieces[j].start) {
			if pieces[i].timed != pieces[j].timed {
				return pieces[i].timed
			}
			return pieces[i].start.Before(pieces[j].start)
		}
		return pieces[i].file < pieces[j].file
	})
	merged, err := mergePieces(pieces, d.opt.overlap)
	if err != nil {
		return Series{}, err
	}
	merged.Name = sel.Name
	if sel.Group != "" {
		merged.Group = sel.Group
	} else if len(hits) > 0 {
		merged.Group = hits[0].Group
	}
	merged.Basis = basis
	if len(hits) > 0 {
		merged.Source = hits[0].Source
		merged.Timed = hits[0].Timed
		merged.IsTimestamp = hits[0].IsTimestamp
	}
	if !merged.Timed {
		merged.Times = nil
		merged.Dt = 0
		merged.Gaps = nil
	}
	return merged, nil
}

func (d *Dataset) resolve(sel Selector) ([]Occurrence, error) {
	if sel.Name == "" {
		return nil, fmt.Errorf("%w: empty channel name", ErrArgument)
	}
	var hits []Occurrence
	groups := map[string]struct{}{}
	for _, o := range d.occ {
		if o.Name != sel.Name {
			continue
		}
		if sel.Group != "" && o.Group != sel.Group {
			continue
		}
		hits = append(hits, o)
		groups[o.Group] = struct{}{}
	}
	if len(hits) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, sel.Name)
	}
	if sel.Group == "" && len(groups) > 1 {
		return nil, &AmbiguousError{Name: sel.Name, Candidates: cloneOcc(hits)}
	}
	return hits, nil
}

func hitsWindow(o Occurrence, start, end time.Time) bool {
	if !o.Timed || o.Samples == 0 {
		return false
	}
	return o.Start.Before(end) && !o.Last.Before(start)
}

func earliest(occ []Occurrence) time.Time {
	var origin time.Time
	set := false
	for _, o := range occ {
		if !o.Timed || o.Samples == 0 {
			continue
		}
		if !set || o.Start.Before(origin) {
			origin = o.Start
			set = true
		}
	}
	return origin
}

func uniqueAbs(paths []string) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	for _, p := range paths {
		if p == "" {
			return nil, fmt.Errorf("%w: empty path", ErrArgument)
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, err
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}
	return out, nil
}

func cloneOcc(in []Occurrence) []Occurrence {
	if len(in) == 0 {
		return nil
	}
	out := make([]Occurrence, len(in))
	copy(out, in)
	return out
}

func run(workers, n int, fn func(i int)) {
	if n <= 0 {
		return
	}
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}
	var wg sync.WaitGroup
	jobs := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				fn(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

func indexFile(path string, opt options) ([]Occurrence, error) {
	f, err := tdms.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Occurrence
	for _, g := range f.Groups() {
		name, mode := opt.declared(g.Name())
		byName := map[string]*tdms.Channel{}
		var chans []*tdms.Channel
		for _, ch := range g.Channels() {
			chans = append(chans, ch)
			byName[ch.Name()] = ch
		}
		var tsCh *tdms.Channel
		var tsStart, tsLast time.Time
		var tsDt float64
		tsHasDt := false
		tsUsable := false
		if mode == clockNamed {
			tsCh = byName[name]
			if tsCh != nil {
				// Span is computed once per clock. Walking it again for every
				// data channel is the same answer and dominates catalog time
				// when many channels share a long axis.
				tsStart, tsLast, tsDt, tsHasDt, tsUsable, err = spanDeclared(tsCh, opt)
				if err != nil {
					return nil, err
				}
			}
		}
		for _, ch := range chans {
			o := Occurrence{
				Name:    ch.Name(),
				Group:   ch.GroupName(),
				File:    path,
				Samples: ch.Len(),
			}
			switch mode {
			case clockNamed:
				if ch.Name() == name {
					o.IsTimestamp = true
					if tsUsable {
						o.Timed = true
						o.Source = TimeTimestamp
						stampSpan(&o, tsCh.Len(), tsStart, tsLast, tsDt, tsHasDt)
					}
					break
				}
				if tsCh != nil && tsUsable && tsCh.Len() == ch.Len() {
					o.Timed = true
					o.Source = TimeTimestamp
					o.Timestamp = name
					stampSpan(&o, ch.Len(), tsStart, tsLast, tsDt, tsHasDt)
					break
				}
			case clockOptOut, clockNone:
				if err := applyWaveform(&o, ch); err != nil {
					return nil, err
				}
			}
			out = append(out, o)
		}
	}
	return out, nil
}

// spanDeclaredCalls counts catalog clock inspections. A shared clock is one
// call per file, not one call per data channel. Tests read the counter.
var spanDeclaredCalls atomic.Uint64

// spanDeclared reports the catalog span of a declared clock.
//
// A positive first step does not decode the channel. The core reader has no
// range read, so the last sample cannot be fetched without the prefix. Last
// is derived from the first two samples and the length: it equals the last
// sample when the stored axis is a uniform arithmetic sequence. Monotonicity
// is not proved here. Read, ReadWindow, and ReadMany decode the axis and
// return ErrMonotonic on a backwards step. Dt is that first step, which is
// what uniformDt returns for a uniform axis; a later irregular step is caught
// when the axis is decoded, and Series.Dt is then 0.
//
// A first step that is not positive cannot be extrapolated. Those clocks are
// scanned in full so Start/Last stay the true min/max.
func spanDeclared(ch *tdms.Channel, opt options) (start, last time.Time, dt float64, hasDt, usable bool, err error) {
	spanDeclaredCalls.Add(1)
	if ch.DataType() == tdms.TypeTimestamp {
		return spanTimestamp(ch)
	}
	if !numericTimeType(ch.DataType()) {
		return time.Time{}, time.Time{}, 0, false, false, nil
	}
	if ch.Len() == 0 {
		return time.Time{}, time.Time{}, 0, false, true, nil
	}
	if !opt.epochSet {
		return time.Time{}, time.Time{}, 0, false, true, fmt.Errorf("%w: numeric timestamp channel %q in group %q requires WithNumericEpoch", ErrArgument, ch.Name(), ch.GroupName())
	}
	return spanNumeric(ch, opt)
}

func spanNumeric(ch *tdms.Channel, opt options) (start, last time.Time, dt float64, hasDt, usable bool, err error) {
	n := int64(ch.Len())
	want := 2
	if n < 2 {
		want = int(n)
	}
	head, err := headFloats(ch, want)
	if err != nil {
		return time.Time{}, time.Time{}, 0, false, true, err
	}
	t0, err := secondsToTime(opt.epoch, head[0])
	if err != nil {
		return time.Time{}, time.Time{}, 0, false, true, fmt.Errorf("channel %q: %w", ch.Name(), err)
	}
	if n == 1 {
		return t0, t0, 0, false, true, nil
	}
	t1, err := secondsToTime(opt.epoch, head[1])
	if err != nil {
		return time.Time{}, time.Time{}, 0, false, true, fmt.Errorf("channel %q: %w", ch.Name(), err)
	}
	if !t1.After(t0) {
		return spanNumericScan(ch, opt)
	}
	delta := head[1] - head[0]
	lastSec := head[0] + float64(n-1)*delta
	tLast, err := secondsToTime(opt.epoch, lastSec)
	if err != nil {
		return time.Time{}, time.Time{}, 0, false, true, fmt.Errorf("channel %q: %w", ch.Name(), err)
	}
	return t0, tLast, t1.Sub(t0).Seconds(), true, true, nil
}

func spanNumericScan(ch *tdms.Channel, opt options) (start, last time.Time, dt float64, hasDt, usable bool, err error) {
	ys, err := ch.Float64s()
	if err != nil {
		return time.Time{}, time.Time{}, 0, false, true, err
	}
	var acc timeAcc
	for _, v := range ys {
		tm, err := secondsToTime(opt.epoch, v)
		if err != nil {
			return time.Time{}, time.Time{}, 0, false, true, fmt.Errorf("channel %q: %w", ch.Name(), err)
		}
		acc.add(tm)
	}
	return acc.start, acc.last, acc.dt, acc.hasDt, true, nil
}

func spanTimestamp(ch *tdms.Channel) (start, last time.Time, dt float64, hasDt, usable bool, err error) {
	n := int64(ch.Len())
	if n == 0 {
		return time.Time{}, time.Time{}, 0, false, true, nil
	}
	want := 2
	if n < 2 {
		want = 1
	}
	head, err := headTimestamps(ch, want)
	if err != nil {
		return time.Time{}, time.Time{}, 0, false, true, err
	}
	t0, ok := head[0].Time()
	if !ok {
		return time.Time{}, time.Time{}, 0, false, true, fmt.Errorf("%w: channel %q timestamp 0 (%d s) does not fit in time.Time", ErrArgument, ch.Name(), head[0].Seconds)
	}
	if n == 1 {
		return t0, t0, 0, false, true, nil
	}
	t1, ok := head[1].Time()
	if !ok {
		return time.Time{}, time.Time{}, 0, false, true, fmt.Errorf("%w: channel %q timestamp 1 (%d s) does not fit in time.Time", ErrArgument, ch.Name(), head[1].Seconds)
	}
	if !t1.After(t0) {
		return spanTimestampScan(ch)
	}
	step := t1.Sub(t0)
	tLast, err := addSteps(t0, step, n-1)
	if err != nil {
		return time.Time{}, time.Time{}, 0, false, true, fmt.Errorf("channel %q: %w", ch.Name(), err)
	}
	return t0, tLast, step.Seconds(), true, true, nil
}

func spanTimestampScan(ch *tdms.Channel) (start, last time.Time, dt float64, hasDt, usable bool, err error) {
	raw, err := ch.Timestamps()
	if err != nil {
		return time.Time{}, time.Time{}, 0, false, true, err
	}
	var acc timeAcc
	for i, t := range raw {
		tm, ok := t.Time()
		if !ok {
			return time.Time{}, time.Time{}, 0, false, true, fmt.Errorf("%w: channel %q timestamp %d (%d s) does not fit in time.Time", ErrArgument, ch.Name(), i, t.Seconds)
		}
		acc.add(tm)
	}
	return acc.start, acc.last, acc.dt, acc.hasDt, true, nil
}

// headFloats copies the first n numeric samples out of a cursor batch.
// The batch is not kept: Cursor may reuse it on the next Next.
func headFloats(ch *tdms.Channel, n int) ([]float64, error) {
	if n <= 0 {
		return nil, nil
	}
	if n > ch.Len() {
		n = ch.Len()
	}
	cur := ch.Cursor(n)
	out := make([]float64, 0, n)
	for cur.Next() && len(out) < n {
		batch, err := cur.Float64s()
		if err != nil {
			return nil, err
		}
		need := n - len(out)
		if len(batch) > need {
			batch = batch[:need]
		}
		out = append(out, batch...)
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	if len(out) != n {
		return nil, fmt.Errorf("%w: channel %q: read %d of %d timestamp samples", ErrArgument, ch.Name(), len(out), n)
	}
	return out, nil
}

func headTimestamps(ch *tdms.Channel, n int) ([]tdms.Timestamp, error) {
	if n <= 0 {
		return nil, nil
	}
	if n > ch.Len() {
		n = ch.Len()
	}
	cur := ch.Cursor(n)
	out := make([]tdms.Timestamp, 0, n)
	for cur.Next() && len(out) < n {
		batch, ok := cur.Values().([]tdms.Timestamp)
		if !ok || batch == nil {
			return nil, fmt.Errorf("%w: channel %q is not a timestamp axis", ErrUntimed, ch.Name())
		}
		need := n - len(out)
		if len(batch) > need {
			batch = batch[:need]
		}
		out = append(out, batch...)
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	if len(out) != n {
		return nil, fmt.Errorf("%w: channel %q: read %d of %d timestamp samples", ErrArgument, ch.Name(), len(out), n)
	}
	return out, nil
}

// addSteps returns t + n*step, or ErrArgument when the product overflows.
func addSteps(t time.Time, step time.Duration, n int64) (time.Time, error) {
	if n <= 0 || step == 0 {
		return t, nil
	}
	if step == time.Duration(math.MinInt64) {
		if n != 1 {
			return time.Time{}, fmt.Errorf("%w: timestamp span does not fit in time.Time", ErrArgument)
		}
		return t.Add(step), nil
	}
	mag := int64(step)
	if mag < 0 {
		mag = -mag
	}
	if n > int64(math.MaxInt64)/mag {
		return time.Time{}, fmt.Errorf("%w: timestamp span does not fit in time.Time", ErrArgument)
	}
	return t.Add(step * time.Duration(n)), nil
}

// loadAxis decodes a declared clock once. y is the channel's float64 values
// when wantY is set (the caller is reading the clock itself). Numeric clocks
// keep the Float64s buffer as y; timestamp clocks convert Timestamps in one pass.
func loadAxis(ch *tdms.Channel, opt options, wantY bool) (times []time.Time, y []float64, err error) {
	if ch.DataType() == tdms.TypeTimestamp {
		if ch.Len() == 0 {
			return nil, nil, nil
		}
		raw, err := ch.Timestamps()
		if err != nil {
			return nil, nil, err
		}
		times = make([]time.Time, len(raw))
		if wantY {
			y = make([]float64, len(raw))
		}
		for i, t := range raw {
			tm, ok := t.Time()
			if !ok {
				return nil, nil, fmt.Errorf("%w: channel %q timestamp %d (%d s) does not fit in time.Time", ErrArgument, ch.Name(), i, t.Seconds)
			}
			times[i] = tm
			if wantY {
				y[i] = float64(tm.UnixNano()) / 1e9
			}
		}
		return times, y, nil
	}
	if !numericTimeType(ch.DataType()) {
		return nil, nil, fmt.Errorf("%w: channel %q is not a timestamp axis", ErrUntimed, ch.Name())
	}
	if ch.Len() == 0 {
		return nil, nil, nil
	}
	if !opt.epochSet {
		return nil, nil, fmt.Errorf("%w: numeric timestamp channel %q in group %q requires WithNumericEpoch", ErrArgument, ch.Name(), ch.GroupName())
	}
	ys, err := ch.Float64s()
	if err != nil {
		return nil, nil, err
	}
	times = make([]time.Time, len(ys))
	for i, v := range ys {
		tm, err := secondsToTime(opt.epoch, v)
		if err != nil {
			return nil, nil, fmt.Errorf("channel %q: %w", ch.Name(), err)
		}
		times[i] = tm
	}
	if wantY {
		y = ys
	}
	return times, y, nil
}

// timeAcc records the earliest and latest sample in add, including when the
// axis steps backwards, and sets Dt only when every positive step matches the
// first within 1ns. That is the same Dt rule as uniformDt. spanDeclared uses
// it only for a clock whose first step is not positive.
type timeAcc struct {
	start, last, prev time.Time
	dt                float64
	hasDt             bool
	n                 int
}

func (a *timeAcc) add(tm time.Time) {
	if a.n == 0 {
		a.start, a.last, a.prev = tm, tm, tm
		a.n = 1
		return
	}
	if tm.Before(a.start) {
		a.start = tm
	}
	if tm.After(a.last) {
		a.last = tm
	}
	step := tm.Sub(a.prev).Seconds()
	if a.n == 1 {
		if step > 0 {
			a.dt = step
			a.hasDt = true
		}
	} else if a.hasDt && math.Abs(step-a.dt) > 1e-9 {
		a.hasDt = false
	}
	a.prev = tm
	a.n++
}

func stampSpan(o *Occurrence, n int, start, last time.Time, dt float64, hasDt bool) {
	if n <= 0 {
		return
	}
	o.Start, o.Last = start, last
	if hasDt {
		o.Dt = dt
	}
}

func numericTimeType(dt tdms.DataType) bool {
	switch dt {
	case tdms.TypeInt8, tdms.TypeInt16, tdms.TypeInt32, tdms.TypeInt64,
		tdms.TypeUint8, tdms.TypeUint16, tdms.TypeUint32, tdms.TypeUint64,
		tdms.TypeFloat32, tdms.TypeFloat64,
		tdms.TypeFloat32WithUnit, tdms.TypeFloat64WithUnit:
		return true
	default:
		return false
	}
}

func applyWaveform(o *Occurrence, ch *tdms.Channel) error {
	if ch == nil {
		return nil
	}
	start, ok := ch.StartTime()
	if !ok {
		return nil
	}
	inc := 0.0
	if v, has := ch.Increment(); has {
		inc = v
	}
	if math.IsNaN(inc) || math.IsInf(inc, 0) {
		return fmt.Errorf("%w: wf_increment of %s", ErrArgument, ch.Path())
	}
	o.Timed = true
	o.Source = TimeWaveform
	o.Dt = inc
	if ch.Len() == 0 {
		return nil
	}
	lo, hi, err := waveformSpan(start, inc, int64(ch.Len()))
	if err != nil {
		return fmt.Errorf("%w: waveform time of %s", ErrArgument, ch.Path())
	}
	o.Start, o.Last = lo, hi
	return nil
}

func uniformDt(times []time.Time) (float64, bool) {
	if len(times) < 2 {
		return 0, false
	}
	dt := times[1].Sub(times[0]).Seconds()
	if dt <= 0 {
		return 0, false
	}
	for i := 2; i < len(times); i++ {
		step := times[i].Sub(times[i-1]).Seconds()
		if math.Abs(step-dt) > 1e-9 {
			return 0, false
		}
	}
	return dt, true
}

func waveformSpan(start time.Time, inc float64, n int64) (time.Time, time.Time, error) {
	t0, err := waveformAt(start, inc, 0)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if n <= 1 {
		return t0, t0, nil
	}
	tN, err := waveformAt(start, inc, n-1)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if tN.Before(t0) {
		return tN, t0, nil
	}
	return t0, tN, nil
}

func waveformAt(start time.Time, inc float64, i int64) (time.Time, error) {
	if i == 0 || inc == 0 {
		return start, nil
	}
	return secondsToTime(start, float64(i)*inc)
}

func secondsToTime(epoch time.Time, sec float64) (time.Time, error) {
	if math.IsNaN(sec) || math.IsInf(sec, 0) {
		return time.Time{}, fmt.Errorf("%w: timestamp seconds are not finite", ErrArgument)
	}
	ns := sec * 1e9
	if ns >= float64(math.MaxInt64) || ns <= float64(math.MinInt64) {
		return time.Time{}, fmt.Errorf("%w: timestamp seconds do not fit in time.Time", ErrArgument)
	}
	return epoch.Add(time.Duration(math.Round(ns))), nil
}
