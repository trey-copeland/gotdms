package tdms

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"
)

// DefaultTailInterval is the poll period Follow uses when WithTailInterval
// is omitted or non-positive.
const DefaultTailInterval = 100 * time.Millisecond

// Tail delivers new samples of one channel as later segments are appended
// to a TDMS file. It is the reader for a file another process is still
// writing, for example a LabVIEW acquisition that flushes segment by segment.
//
// Samples are returned in file order, in the channel's native Go type, the
// same concrete types as Channel.Values. Only samples from segments whose
// next-segment offset is a real length, and whose bytes are fully present,
// are delivered. A final segment that still carries the NI incomplete
// sentinel is skipped, including whole values already flushed into it.
// Channel.Len on a normal Open still includes those values; Tail does not.
//
// A segment whose lead-in names a length past EOF is opened with
// WithLenientTail. Samples that belong to that partial segment are not
// committed, so Next does not deliver them; earlier finished segments are.
// When a later poll sees the declared length, those samples become
// committed. A torn tail after the last whole segment whose error is
// ErrTruncated (a short trailing lead-in, or metadata that itself runs past
// EOF) is "the writer has not finished": the whole segments before it are
// delivered and the file is polled again. A file that does not exist yet,
// or that has no whole segment yet and returns ErrTruncated, is polled
// again too. Follow cannot tell a running writer from one that stopped, so
// such a file that never grows is polled until the context is cancelled.
// A torn tail of any other kind (a bad tag, zeros, garbage) and any other
// open or read error stop the tail. See WithLenientTail. Cancel the context
// to stop. Close cancels the wait without setting Err.
//
// The channel is chosen by its group and channel names. Nothing is inferred
// from the name: a channel named Time is tailed as whatever DataType it has.
// Next blocks until a new batch is available, the context is cancelled, or
// Close is called. Concurrent Next calls on one Tail are not safe. Close may
// run while Next is waiting.
type Tail struct {
	parent   context.Context
	ctx      context.Context
	stop     context.CancelFunc
	path     string
	group    string
	channel  string
	interval time.Duration
	batch    int
	pos      int64
	cur      any
	err      error
	done     bool
}

type tailConfig struct {
	interval time.Duration
	batch    int
}

// TailOption configures Follow.
type TailOption func(*tailConfig)

// WithTailInterval sets how often Follow polls the file. A non-positive
// interval selects DefaultTailInterval.
func WithTailInterval(d time.Duration) TailOption {
	return func(c *tailConfig) {
		if d > 0 {
			c.interval = d
		}
	}
}

// WithTailBatch sets the maximum number of samples Next returns at once.
// A non-positive batch selects 4096.
func WithTailBatch(n int) TailOption {
	return func(c *tailConfig) {
		if n > 0 {
			c.batch = n
		}
	}
}

// Follow tails channel in group of the TDMS file at path. The channel does
// not have to exist yet; Follow waits until it does or ctx is cancelled.
// See Tail.
func Follow(ctx context.Context, path, group, channel string, opts ...TailOption) (*Tail, error) {
	if ctx == nil {
		return nil, wrap("follow", path, fmt.Errorf("%w: nil context", ErrArgument))
	}
	if path == "" || group == "" || channel == "" {
		return nil, wrap("follow", path, fmt.Errorf("%w: path, group, and channel are required", ErrArgument))
	}
	cfg := tailConfig{interval: DefaultTailInterval, batch: 4096}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.interval <= 0 {
		cfg.interval = DefaultTailInterval
	}
	if cfg.batch <= 0 {
		cfg.batch = 4096
	}
	child, stop := context.WithCancel(ctx)
	return &Tail{
		parent:   ctx,
		ctx:      child,
		stop:     stop,
		path:     path,
		group:    group,
		channel:  channel,
		interval: cfg.interval,
		batch:    cfg.batch,
	}, nil
}

// Next returns the next batch. It returns false when the parent context is
// done, Close has been called, or a read fails. Err is nil after Close, and
// the parent context error after cancellation. The slice from Values is newly
// allocated and stays valid after later Next calls.
func (t *Tail) Next() bool {
	if t == nil || t.done || t.err != nil {
		return false
	}
	for {
		if err := t.parent.Err(); err != nil {
			t.finish(wrap("follow", t.path, err))
			return false
		}
		v, ok, err := t.pull()
		if err != nil {
			t.finish(wrap("follow", t.path, err))
			return false
		}
		if ok {
			t.cur = v
			return true
		}
		if !t.sleep() {
			t.cur = nil
			t.done = true
			if err := t.parent.Err(); err != nil {
				t.err = wrap("follow", t.path, err)
			}
			return false
		}
	}
}

func (t *Tail) finish(err error) {
	t.cur = nil
	t.done = true
	t.err = err
}

func (t *Tail) sleep() bool {
	timer := time.NewTimer(t.interval)
	defer timer.Stop()
	select {
	case <-t.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// pull reads the next committed batch. ok is false when the channel has no
// new finished samples yet. ErrTruncated and a missing file are reported as
// ok false so the caller polls again.
func (t *Tail) pull() (any, bool, error) {
	// Lenient tail keeps earlier finished segments when the last lead-in
	// names a length past EOF. Those tail samples are not committed.
	f, err := Open(t.path, WithLenientTail())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrTruncated) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer f.Close()
	if st := f.Status(); st.TailError != nil && !errors.Is(st.TailError, ErrTruncated) {
		// Zeros or garbage after the last whole segment. A writer that is
		// still running appends whole lead-ins, so this is not "wait".
		return nil, false, st.TailError
	}
	g, ok := f.Group(t.group)
	if !ok {
		return nil, false, nil
	}
	ch, ok := g.Channel(t.channel)
	if !ok {
		return nil, false, nil
	}
	n := ch.committed()
	if t.pos > n {
		return nil, false, fmt.Errorf("%w: file shrank below %d samples already delivered", ErrCorrupt, t.pos)
	}
	if t.pos == n {
		return nil, false, nil
	}
	if t.pos > int64(math.MaxInt) || n > int64(math.MaxInt) {
		return nil, false, fmt.Errorf("%w: sample index exceeds int", ErrUnsupported)
	}
	take := t.batch
	if int64(take) > n-t.pos {
		take = int(n - t.pos)
	}
	v, err := ch.Slice(int(t.pos), take)
	if err != nil {
		return nil, false, err
	}
	t.pos += int64(take)
	return v, true, nil
}

// Values returns the batch from the last successful Next.
func (t *Tail) Values() any {
	if t == nil {
		return nil
	}
	return t.cur
}

// Err returns the error from the last Next, or nil.
func (t *Tail) Err() error {
	if t == nil {
		return nil
	}
	return t.err
}

// Close unblocks Next. A Next that was waiting returns false and leaves Err
// nil, unless the parent context is already done. Close is safe to call more
// than once and from another goroutine.
func (t *Tail) Close() error {
	if t == nil || t.stop == nil {
		return nil
	}
	t.stop()
	return nil
}
