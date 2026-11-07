package tdms

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestEmptyInputIsTruncated(t *testing.T) {
	_, err := NewReader(bytes.NewReader(nil))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("%v", err)
	}
}

func TestCursorNonPositiveBatch(t *testing.T) {
	const n = 10
	data := make([]float64, n)
	for i := range data {
		data[i] = float64(i)
	}
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	for _, batch := range []int{0, -1} {
		cur := c.Cursor(batch)
		if !cur.Next() {
			t.Fatalf("batch %d: %v", batch, cur.Err())
		}
		got := cur.Values().([]float64)
		if len(got) != n {
			t.Fatalf("batch %d yielded %d values", batch, len(got))
		}
		if cur.Next() || cur.Err() != nil {
			t.Fatalf("batch %d second next err %v", batch, cur.Err())
		}
	}
	cur := c.Cursor(3)
	if !cur.Next() {
		t.Fatal(cur.Err())
	}
	if got := len(cur.Values().([]float64)); got != 3 {
		t.Fatalf("Cursor(3) yielded %d", got)
	}
}

func TestShortTailNamesItsByteCount(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.Write([]float64{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw := append(append([]byte(nil), buf.Bytes()...), []byte("TDSm")...)
	_, err := NewReader(bytes.NewReader(raw))
	if !errors.Is(err, ErrTruncated) || !strings.Contains(err.Error(), "4 trailing bytes") {
		t.Fatalf("%v", err)
	}
}

func TestStringWindowKeepsEmptyString(t *testing.T) {
	vals := []string{"a", "", "b"}
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "s", TypeString)
	if err := ch.Write(vals); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("s")
	got, err := c.Slice(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, []string{""})
	all, err := c.Strings()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, all, vals)
}
