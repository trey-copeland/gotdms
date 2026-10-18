package tdms

import (
	"bytes"
	"errors"
	"math"
	"testing"
	"time"
)

func TestWaveformRoundTrip(t *testing.T) {
	start := time.Date(2024, 6, 1, 12, 0, 0, 123456789, time.UTC)
	ts := NewTimestamp(start)
	in := Waveform[float64]{
		Start:     ts,
		Increment: 0.001,
		Y:         []float64{-1.5, 0, 2.25, math.Copysign(0, -1)},
		Unit:      "V",
	}
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "acq", "ai0", TypeFloat64)
	if err := ch.WriteWaveform(in); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	c := mustChannel(t, f, "acq", "ai0")
	got, err := ReadWaveform[float64](c)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasStart() || !got.HasIncrement() {
		t.Fatalf("presence start %v inc %v", got.HasStart(), got.HasIncrement())
	}
	if got.Start != ts {
		t.Fatalf("timestamp %+v want %+v", got.Start, ts)
	}
	if got.Increment != in.Increment || got.Unit != "V" {
		t.Fatalf("dt %v unit %q", got.Increment, got.Unit)
	}
	assertEqual(t, got.Y, in.Y)
	if got.Dt() != time.Millisecond {
		t.Fatalf("Dt %v", got.Dt())
	}
	viaMethod, err := c.Waveform()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, viaMethod.Y, in.Y)

	names := propNames(c.Properties())
	wantNames := []string{PropStartTime, PropIncrement, PropSamples, PropUnitString}
	if len(names) != len(wantNames) {
		t.Fatalf("props %v", names)
	}
	for i := range wantNames {
		if names[i] != wantNames[i] {
			t.Fatalf("prop %d %q want %q", i, names[i], wantNames[i])
		}
	}
	if PropStartTime != "wf_start_time" || PropIncrement != "wf_increment" || PropSamples != "wf_samples" || PropUnitString != "unit_string" {
		t.Fatal("property constants drifted from LabVIEW names")
	}
	sp, ok := c.Property(PropSamples)
	if !ok || sp.Type != TypeInt32 || sp.Value != int32(len(in.Y)) {
		t.Fatalf("wf_samples %+v", sp)
	}
	inc, ok := c.Increment()
	if !ok || inc != in.Increment {
		t.Fatalf("Increment %v %v", inc, ok)
	}
	when, ok := c.StartTime()
	if !ok || !when.Equal(start) {
		t.Fatalf("StartTime %v %v", when, ok)
	}
	unit, ok := c.Unit()
	if !ok || unit != "V" {
		t.Fatalf("Unit %q %v", unit, ok)
	}
	n, ok := c.SampleCount()
	if !ok || n != uint64(len(in.Y)) {
		t.Fatalf("SampleCount %d %v", n, ok)
	}
	t1, ok := got.TimeAt(1)
	if !ok || !t1.Equal(start.Add(time.Millisecond)) {
		t.Fatalf("TimeAt(1) %v %v", t1, ok)
	}
	if _, ok := got.TimeAt(-1); ok {
		t.Fatal("TimeAt(-1) ok")
	}
	if _, ok := got.TimeAt(len(in.Y)); ok {
		t.Fatal("TimeAt(len) ok")
	}
}

func TestWaveformReadFromProperties(t *testing.T) {
	start := time.Date(2019, 3, 4, 5, 6, 7, 8, time.UTC)
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeInt16)
	if err := ch.SetProperty(PropStartTime, start); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty(PropIncrement, 0.5); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty(PropSamples, 3); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty(PropUnitString, "A"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]int16{4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	c := mustChannel(t, f, "g", "c")
	got, err := ReadWaveform[int16](c)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got.Y, []int16{4, 5, 6})
	if !got.HasStart() || !got.HasIncrement() || got.Increment != 0.5 || got.Unit != "A" {
		t.Fatalf("%+v", got)
	}
	when, _ := got.Start.Time()
	if !when.Equal(start) {
		t.Fatalf("start %v", when)
	}
	t2, ok := got.TimeAt(2)
	if !ok || !t2.Equal(start.Add(time.Second)) {
		t.Fatalf("t2 %v %v", t2, ok)
	}
	if _, err := ReadWaveform[float64](c); !errors.Is(err, ErrType) {
		t.Fatalf("mismatch %v", err)
	}
}

func TestWaveformMissingProperties(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.Write([]float64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	c := mustChannel(t, openBytes(t, buf.Bytes()), "g", "c")
	got, err := ReadWaveform[float64](c)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasStart() || got.HasIncrement() || got.Unit != "" || got.Start != (Timestamp{}) || got.Increment != 0 {
		t.Fatalf("%+v", got)
	}
	assertEqual(t, got.Y, []float64{1, 2})
	if _, ok := got.TimeAt(0); ok {
		t.Fatal("missing start should not invent an epoch time")
	}
	h := c.WaveformHeader()
	if h.HasStart || h.HasIncrement || h.HasUnit || h.HasSamples {
		t.Fatalf("header %+v", h)
	}
}

func TestWaveformScaleIsExplicit(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat32)
	if err := ch.SetProperty("NI_Scale[1]_Scale_Type", "Linear"); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty("NI_Scale[1]_Linear_Slope", 2.0); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty("NI_Scale[1]_Linear_Y_Intercept", 0.5); err != nil {
		t.Fatal(err)
	}
	in := Waveform[float32]{Start: NewTimestamp(time.Unix(0, 0).UTC()), Increment: 1, Y: []float32{1, 2, 3}, Unit: "raw"}
	if err := ch.WriteWaveform(in); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	c := mustChannel(t, openBytes(t, buf.Bytes()), "g", "c")
	raw, err := ReadWaveform[float32](c)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, raw.Y, []float32{1, 2, 3})
	sc, ok := c.LinearScale()
	if !ok {
		t.Fatal("scale missing")
	}
	scaled := raw.ApplyScale(sc)
	assertEqual(t, scaled.Y, []float64{2.5, 4.5, 6.5})
	if scaled.Unit != "raw" || !scaled.HasStart() || scaled.Increment != 1 {
		t.Fatalf("scaled meta %+v", scaled)
	}
	sw, err := ScaledWaveform[float32](c)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, sw.Y, scaled.Y)

	// No scale: conversion only, still explicit.
	var plain bytes.Buffer
	w = newBufWriter(t, &plain)
	ch = mustCh(t, w, "g", "c", TypeInt32)
	if err := ch.Write([]int32{8}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	c = mustChannel(t, openBytes(t, plain.Bytes()), "g", "c")
	id, err := ScaledWaveform[int32](c)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, id.Y, []float64{8})
}

func TestWriteWaveformDoesNotInvertScale(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	wf := Waveform[float64]{Increment: 1, Y: []float64{10, 20}, Unit: "eu"}
	if err := ch.WriteWaveform(wf); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	c := mustChannel(t, openBytes(t, buf.Bytes()), "g", "c")
	got, err := c.Float64s()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, []float64{10, 20})
}

func TestWriteWaveformEmptyUnitKeepsExisting(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.SetProperty(PropUnitString, "keep"); err != nil {
		t.Fatal(err)
	}
	if err := ch.WriteWaveform(Waveform[float64]{Y: []float64{1}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	c := mustChannel(t, openBytes(t, buf.Bytes()), "g", "c")
	unit, ok := c.Unit()
	if !ok || unit != "keep" {
		t.Fatalf("unit %q %v", unit, ok)
	}
}

func TestWriteWaveformTypeMismatchSetsNothing(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeInt16)
	err := ch.WriteWaveform(Waveform[float64]{Increment: 1, Y: []float64{1}, Unit: "V"})
	if !errors.Is(err, ErrType) {
		t.Fatalf("%v", err)
	}
	if len(ch.ch.obj.props) != 0 || ch.ch.n != 0 {
		t.Fatalf("props %v n %d", ch.ch.obj.props, ch.ch.n)
	}
	if err := ch.WriteWaveform(nil); !errors.Is(err, ErrType) {
		t.Fatalf("nil %v", err)
	}
}

func TestWaveformHeaderTimestampsChunks(t *testing.T) {
	start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	y := []float64{0, 1, 2, 3, 4, 5, 6}
	if err := ch.WriteWaveform(Waveform[float64]{Start: NewTimestamp(start), Increment: 0.25, Y: y, Unit: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	f, err := NewReader(bytes.NewReader(raw), WithReadBuffer(8))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c := mustChannel(t, f, "g", "c")
	full, err := ReadWaveform[float64](c)
	if err != nil {
		t.Fatal(err)
	}
	h := c.WaveformHeader()
	if !h.HasStart || !h.HasIncrement || !h.HasUnit || !h.HasSamples || h.Samples != uint64(len(y)) || h.Unit != "s" || h.Increment != 0.25 {
		t.Fatalf("header %+v", h)
	}
	if h.Dt() != 250*time.Millisecond {
		t.Fatalf("header Dt %v", h.Dt())
	}
	cur := c.Cursor(3)
	idx := 0
	var got []float64
	for cur.Next() {
		batch := cur.Values().([]float64)
		for i := range batch {
			abs := idx + i
			ht, ok := h.TimeAt(abs)
			wt, ok2 := full.TimeAt(abs)
			if !ok || !ok2 || !ht.Equal(wt) {
				t.Fatalf("index %d header %v wf %v", abs, ht, wt)
			}
		}
		got = append(got, batch...)
		idx += len(batch)
	}
	if err := cur.Err(); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, y)
	if _, ok := h.TimeAt(-1); ok {
		t.Fatal("negative index")
	}
}

func TestWaveformTimeAtNotCumulative(t *testing.T) {
	const dt = 0.1
	var i int
	var cum float64
	directNS, cumNS := 0.0, 0.0
	for i = 1; i <= 5_000_000; i++ {
		cum += dt
		directNS = math.Round(float64(i) * dt * 1e9)
		cumNS = math.Round(cum * 1e9)
		if directNS != cumNS {
			break
		}
	}
	if directNS == cumNS {
		t.Fatal("expected i*dt and a running sum to diverge at nanosecond resolution")
	}
	start := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	h := WaveformHeader{Start: NewTimestamp(start), Increment: dt, HasStart: true, HasIncrement: true}
	got, ok := h.TimeAt(i)
	if !ok {
		t.Fatal("TimeAt")
	}
	want := start.Add(time.Duration(directNS))
	if !got.Equal(want) {
		t.Fatalf("TimeAt(%d) %v want %v (cumulative would be %v)", i, got, want, start.Add(time.Duration(cumNS)))
	}
}

func TestWaveformZeroIncrement(t *testing.T) {
	start := time.Date(1990, 5, 6, 0, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.WriteWaveform(Waveform[float64]{Start: NewTimestamp(start), Increment: 0, Y: []float64{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	c := mustChannel(t, openBytes(t, buf.Bytes()), "g", "c")
	wf, err := c.Waveform()
	if err != nil {
		t.Fatal(err)
	}
	if !wf.HasIncrement() || wf.Increment != 0 || wf.Dt() != 0 {
		t.Fatalf("%+v dt %v", wf, wf.Dt())
	}
	t1, ok := wf.TimeAt(1)
	if !ok || !t1.Equal(start) {
		t.Fatalf("t1 %v", t1)
	}
}

func TestUnitDescriptionFallback(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.SetProperty("NI_UnitDescription", "psi"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	c := mustChannel(t, openBytes(t, buf.Bytes()), "g", "c")
	wf, err := c.Waveform()
	if err != nil {
		t.Fatal(err)
	}
	if wf.Unit != "psi" || !c.WaveformHeader().HasUnit {
		t.Fatalf("unit %q", wf.Unit)
	}
}

func TestBufferOptionsRoundTrip(t *testing.T) {
	const n = 100000 // two float64 channels, 1.6 MiB raw, larger than the 1 MiB deinterleave window
	a := make([]float64, n)
	b := make([]int16, n)
	for i := range a {
		a[i] = float64(i) * 0.5
		b[i] = int16(i)
	}
	var buf bytes.Buffer
	w, err := NewWriter(&buf, WithWriteBuffer(64))
	if err != nil {
		t.Fatal(err)
	}
	w.SetInterleaved(true)
	ca := mustCh(t, w, "g", "a", TypeFloat64)
	cb := mustCh(t, w, "g", "b", TypeInt16)
	if err := ca.Write(a); err != nil || cb.Write(b) != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := NewReader(bytes.NewReader(buf.Bytes()), WithReadBuffer(32))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ga, _ := f.Group("g")
	cha, _ := ga.Channel("a")
	chb, _ := ga.Channel("b")
	gotA, err := cha.Float64s()
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := chb.Values()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, gotA, a)
	assertEqual(t, gotB, b)
	cur := cha.Cursor(1000)
	var streamed int
	var last float64
	for cur.Next() {
		batch := cur.Values().([]float64)
		if len(batch) == 0 {
			t.Fatal("empty batch")
		}
		last = batch[len(batch)-1]
		streamed += len(batch)
	}
	if err := cur.Err(); err != nil || streamed != n || last != a[n-1] {
		t.Fatalf("stream %d err %v last %v", streamed, cur.Err(), last)
	}
}

func TestBoolRewriteAfterFlush(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeBool)
	if err := ch.Write([]bool{true, true}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]bool{false, true, false}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	c := mustChannel(t, openBytes(t, buf.Bytes()), "g", "c")
	got, err := c.Bools()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, []bool{true, true, false, true, false})
}

func TestScalarPathsMatchBulk(t *testing.T) {
	f64 := []float64{-0, 1.25, math.SmallestNonzeroFloat64, math.MaxFloat64}
	c64 := []complex64{complex(1, -2), complex(3, 4)}
	i16 := []int16{-32768, -1, 0, 32767}
	check := func(dt DataType, raw []byte) {
		t.Helper()
		fast, err := decodePacked(dt, raw)
		if err != nil {
			t.Fatal(err)
		}
		slow, err := decodePackedScalar(dt, raw)
		if err != nil {
			t.Fatal(err)
		}
		assertEqual(t, fast, slow)
	}
	check(TypeFloat64, appendFixed(nil, f64))
	check(TypeComplex64, appendFixed(nil, c64))
	check(TypeInt16, appendFixed(nil, i16))

	scalar := make([]byte, len(f64)*8)
	appendFixedScalar(scalar, f64)
	if !bytes.Equal(scalar, appendFixed(nil, f64)) {
		t.Fatal("scalar float64 encoder diverged from bulk copy")
	}
	sc := make([]byte, len(c64)*8)
	appendFixedScalar(sc, c64)
	if !bytes.Equal(sc, appendFixed(nil, c64)) {
		t.Fatal("scalar complex64 encoder diverged from bulk copy")
	}

	strs := []string{"", "a", "hello", "", "ü"}
	var streamed bytes.Buffer
	if err := writeStringBlock(&streamed, strs); err != nil {
		t.Fatal(err)
	}
	built, err := appendStringBlock(nil, strs)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(streamed.Bytes(), built) {
		t.Fatalf("string block\n%q\n%q", streamed.Bytes(), built)
	}
}

func TestNegativeIncrement(t *testing.T) {
	start := time.Date(2022, 7, 8, 9, 0, 0, 0, time.UTC)
	h := WaveformHeader{Start: NewTimestamp(start), Increment: -0.5, HasStart: true, HasIncrement: true}
	got, ok := h.TimeAt(3)
	if !ok || !got.Equal(start.Add(-1500*time.Millisecond)) {
		t.Fatalf("%v %v", got, ok)
	}
}

func mustChannel(t *testing.T, f *File, group, name string) *Channel {
	t.Helper()
	g, ok := f.Group(group)
	if !ok {
		t.Fatalf("group %s", group)
	}
	c, ok := g.Channel(name)
	if !ok {
		t.Fatalf("channel %s", name)
	}
	return c
}

func propNames(ps []Property) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}
