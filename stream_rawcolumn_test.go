package tdms

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func rawI32(vals ...int32) []byte {
	b := make([]byte, 4*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint32(b[4*i:], uint32(v))
	}
	return b
}

// writeRawColumn is only called by Defragment with valid input, so its guard
// branches are exercised here directly.
func TestStreamWriteRawColumnGuards(t *testing.T) {
	newStream := func(t *testing.T) (*bytes.Buffer, *Writer, *ChannelWriter, *ChannelWriter, *Stream) {
		t.Helper()
		var buf bytes.Buffer
		w := newBufWriter(t, &buf)
		a := mustCh(t, w, "g", "a", TypeInt32)
		b := mustCh(t, w, "g", "b", TypeInt32)
		s, err := w.Stream([]*ChannelWriter{a, b}, []int{2, 1})
		if err != nil {
			t.Fatal(err)
		}
		return &buf, w, a, b, s
	}

	t.Run("valid", func(t *testing.T) {
		buf, w, a, b, s := newStream(t)
		if err := s.writeRawColumn(a, nil, 0); err != nil {
			t.Fatalf("zero values: %v", err)
		}
		if err := s.writeRawColumn(a, rawI32(1), 1); err != nil {
			t.Fatal(err)
		}
		if err := s.writeRawColumn(a, rawI32(2), 1); err != nil {
			t.Fatal(err)
		}
		if err := s.writeRawColumn(b, rawI32(3), 1); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		f := openBytes(t, buf.Bytes())
		g, _ := f.Group("g")
		ca, _ := g.Channel("a")
		cb, _ := g.Channel("b")
		assertEqual(t, mustValues(t, ca), []int32{1, 2})
		assertEqual(t, mustValues(t, cb), []int32{3})
	})

	t.Run("nil and foreign channels", func(t *testing.T) {
		_, _, _, _, s := newStream(t)
		if err := s.writeRawColumn(nil, rawI32(1), 1); !errors.Is(err, ErrArgument) {
			t.Fatalf("nil channel: %v", err)
		}
		if err := s.writeRawColumn(&ChannelWriter{}, rawI32(1), 1); !errors.Is(err, ErrArgument) {
			t.Fatalf("empty handle: %v", err)
		}
		var other bytes.Buffer
		ow := newBufWriter(t, &other)
		oc := mustCh(t, ow, "g", "a", TypeInt32)
		if err := s.writeRawColumn(oc, rawI32(1), 1); !errors.Is(err, ErrArgument) {
			t.Fatalf("other writer's channel: %v", err)
		}
	})

	t.Run("channel of this writer not in the stream", func(t *testing.T) {
		var buf bytes.Buffer
		w := newBufWriter(t, &buf)
		a := mustCh(t, w, "g", "a", TypeInt32)
		c := mustCh(t, w, "g", "c", TypeInt32)
		s, err := w.Stream([]*ChannelWriter{a}, []int{1})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.writeRawColumn(c, rawI32(1), 1); !errors.Is(err, ErrArgument) {
			t.Fatalf("channel outside the stream: %v", err)
		}
	})

	t.Run("order", func(t *testing.T) {
		_, _, a, b, s := newStream(t)
		if err := s.writeRawColumn(b, rawI32(3), 1); !errors.Is(err, ErrLayout) {
			t.Fatalf("b before a: %v", err)
		}
		if err := s.writeRawColumn(a, rawI32(1, 2), 2); err != nil {
			t.Fatal(err)
		}
		// a is full; writing it again is an argument error.
		if err := s.writeRawColumn(a, rawI32(9), 1); !errors.Is(err, ErrArgument) {
			t.Fatalf("a after it is full: %v", err)
		}
		if err := s.writeRawColumn(b, rawI32(3), 1); err != nil {
			t.Fatal(err)
		}
		// Every channel is full.
		if err := s.writeRawColumn(b, rawI32(4), 1); !errors.Is(err, ErrArgument) {
			t.Fatalf("b after the stream is full: %v", err)
		}
	})

	t.Run("overrun and byte length", func(t *testing.T) {
		_, _, a, _, s := newStream(t)
		if err := s.writeRawColumn(a, rawI32(1, 2, 3), 3); !errors.Is(err, ErrArgument) {
			t.Fatalf("overrun: %v", err)
		}
		if err := s.writeRawColumn(a, rawI32(1), 1); err != nil {
			t.Fatal(err)
		}
		if err := s.writeRawColumn(a, rawI32(2, 3), 2); !errors.Is(err, ErrArgument) {
			t.Fatalf("overrun after a partial write: %v", err)
		}
		// The byte length must be exactly nValues*size: one byte more or
		// less is rejected, and then the exact remaining count is accepted.
		if err := s.writeRawColumn(a, append(rawI32(2), 0), 1); !errors.Is(err, ErrArgument) {
			t.Fatalf("one byte too many: %v", err)
		}
		if err := s.writeRawColumn(a, rawI32(2)[:3], 1); !errors.Is(err, ErrArgument) {
			t.Fatalf("one byte short: %v", err)
		}
		if err := s.writeRawColumn(a, rawI32(2), 1); err != nil {
			t.Fatalf("remaining count: %v", err)
		}
	})

	t.Run("interleaved stream", func(t *testing.T) {
		var buf bytes.Buffer
		w := newBufWriter(t, &buf)
		w.SetInterleaved(true)
		a := mustCh(t, w, "g", "a", TypeInt32)
		s, err := w.Stream([]*ChannelWriter{a}, []int{1})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.writeRawColumn(a, rawI32(1), 1); !errors.Is(err, ErrLayout) {
			t.Fatalf("interleaved: %v", err)
		}
	})

	t.Run("finished and nil stream", func(t *testing.T) {
		_, _, a, b, s := newStream(t)
		if err := s.writeRawColumn(a, rawI32(1, 2), 2); err != nil {
			t.Fatal(err)
		}
		if err := s.writeRawColumn(b, rawI32(3), 1); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(); err != nil {
			t.Fatal(err)
		}
		if err := s.writeRawColumn(a, rawI32(1), 1); !errors.Is(err, ErrClosed) {
			t.Fatalf("after Finish: %v", err)
		}
		var nilStream *Stream
		if err := nilStream.writeRawColumn(a, rawI32(1), 1); !errors.Is(err, ErrArgument) {
			t.Fatalf("nil stream: %v", err)
		}
	})

	t.Run("write failure is sticky", func(t *testing.T) {
		// Find a byte budget that lets the lead-in and metadata through and
		// fails inside the raw column.
		reached := false
		for n := 0; n < 1024 && !reached; n++ {
			fw := &failingWriter{n: n}
			w, err := NewWriter(fw, WithWriteBuffer(1))
			if err != nil {
				t.Fatal(err)
			}
			a := mustCh(t, w, "g", "a", TypeInt32)
			s, err := w.Stream([]*ChannelWriter{a}, []int{4})
			if err != nil {
				continue
			}
			err = s.writeRawColumn(a, rawI32(1, 2), 2)
			if err == nil {
				continue
			}
			reached = true
			if !errors.Is(err, errDiskFull) {
				t.Fatalf("n=%d: write failure: %v", n, err)
			}
			if err := s.writeRawColumn(a, rawI32(3, 4), 2); !errors.Is(err, errDiskFull) {
				t.Fatalf("n=%d: write after a failure: %v", n, err)
			}
			if fw.after != 0 {
				t.Fatalf("n=%d: %d writes after the failure", n, fw.after)
			}
		}
		if !reached {
			t.Fatal("no budget failed inside the raw column")
		}
	})
}
