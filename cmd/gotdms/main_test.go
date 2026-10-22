package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trey-copeland/gotdms"
)

func TestInfoAndConvert(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	when := time.Date(2020, 1, 2, 3, 4, 5, 123456789, time.UTC)
	w, err := tdms.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetProperty("title", "demo"); err != nil {
		t.Fatal(err)
	}
	g := w.Group("acquisition")
	if err := g.SetProperty("note", "lab"); err != nil {
		t.Fatal(err)
	}
	temp, err := g.Channel("temperature", tdms.TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := temp.SetProperty(tdms.PropUnitString, "degC"); err != nil {
		t.Fatal(err)
	}
	if err := temp.Write([]float64{21.5, 22}); err != nil {
		t.Fatal(err)
	}
	// Named Time, but the samples are float64. CSV must not treat the name as a timestamp.
	clock, err := g.Channel("Time", tdms.TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := clock.Write([]float64{1.25}); err != nil {
		t.Fatal(err)
	}
	label, err := g.Channel("label", tdms.TypeString)
	if err != nil {
		t.Fatal(err)
	}
	if err := label.Write([]string{"a", "b,c"}); err != nil {
		t.Fatal(err)
	}
	stamp, err := g.Channel("stamp", tdms.TypeTimestamp)
	if err != nil {
		t.Fatal(err)
	}
	if err := stamp.SetProperty(tdms.PropStartTime, tdms.NewTimestamp(when)); err != nil {
		t.Fatal(err)
	}
	if err := stamp.Write([]tdms.Timestamp{tdms.NewTimestamp(when)}); err != nil {
		t.Fatal(err)
	}
	other, err := w.Group("other").Channel("temperature", tdms.TypeInt32)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Write([]int32{7}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"info", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("info: %d %s", code, stderr.String())
	}
	text := stdout.String()
	for _, want := range []string{
		"file: " + path,
		"version: 4713",
		"status: complete",
		"title (tdsTypeString): demo",
		"acquisition:",
		"note (tdsTypeString): lab",
		"temperature (tdsTypeDoubleFloat, 2 samples):",
		"unit_string (tdsTypeString): degC",
		"label (tdsTypeString, 2 samples):",
		"stamp (tdsTypeTimeStamp, 1 samples):",
		when.Format(time.RFC3339Nano),
		"other:",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("info missing %q\n%s", want, text)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"info", "--json", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("json: %d %s", code, stderr.String())
	}
	var doc infoFile
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Path != path || doc.Version != 4713 || !doc.Status.Complete || doc.Status.Incomplete {
		t.Fatalf("%+v", doc)
	}
	if len(doc.Groups) != 2 || doc.Groups[0].Name != "acquisition" || doc.Groups[0].Channels[0].Samples != 2 {
		t.Fatalf("%+v", doc.Groups)
	}
	if doc.Properties[0].Name != "title" || doc.Properties[0].Value != "demo" {
		t.Fatalf("props %+v", doc.Properties)
	}

	stdout.Reset()
	if code := run([]string{"convert", "--group", "acquisition", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("convert: %d %s", code, stderr.String())
	}
	wantCSV := strings.Join([]string{
		"/'acquisition'/'temperature',/'acquisition'/'Time',/'acquisition'/'label',/'acquisition'/'stamp'",
		"21.5,1.25,a," + when.Format(time.RFC3339Nano),
		"22,#N/A,\"b,c\",#N/A",
		"",
	}, "\n")
	if stdout.String() != wantCSV {
		t.Fatalf("csv:\n%s\nwant:\n%s", stdout.String(), wantCSV)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"convert", "--channel", "temperature", path}, &stdout, &stderr); code == 0 {
		t.Fatal("ambiguous channel should fail")
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"convert", "--group", "other", "--channel", "temperature", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("one channel: %d %s", code, stderr.String())
	}
	if stdout.String() != "/'other'/'temperature'\n7\n" {
		t.Fatalf("one column:\n%s", stdout.String())
	}

	out := filepath.Join(dir, "out.csv")
	stdout.Reset()
	if code := run([]string{"convert", "-o", out, "--group", "other", "--channel", "temperature", path}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatal("wrote to stdout and -o")
	}
}

func TestConvertEmptyStringAndMissingSample(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gap.tdms")
	w, err := tdms.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	g := w.Group("g")
	s, err := g.Channel("s", tdms.TypeString)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]string{"", "x"}); err != nil {
		t.Fatal(err)
	}
	n, err := g.Channel("n", tdms.TypeInt32)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Write([]int32{1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"convert", "--group", "g", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("convert: %d %s", code, stderr.String())
	}
	want := "/'g'/'s',/'g'/'n'\n,1\nx,#N/A\n"
	if stdout.String() != want {
		t.Fatalf("csv:\n%s\nwant:\n%s", stdout.String(), want)
	}
}

func TestInfoConvertDAQmx(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hsai.tdms")
	if err := os.WriteFile(path, daqmxCLIFile(), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"info", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("info: %d %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "c (tdsTypeI16, 4 samples):") {
		t.Fatalf("info:\n%s", stdout.String())
	}
	stdout.Reset()
	if code := run([]string{"convert", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("convert: %d %s", code, stderr.String())
	}
	want := "/'g'/'c'\n1\n2\n-1\n-2\n"
	if stdout.String() != want {
		t.Fatalf("csv:\n%s\nwant:\n%s", stdout.String(), want)
	}
}

func daqmxCLIFile() []byte {
	var meta []byte
	put := func(v uint32) {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], v)
		meta = append(meta, buf[:]...)
	}
	put64 := func(v uint64) {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], v)
		meta = append(meta, buf[:]...)
	}
	put(1)
	path := "/'g'/'c'"
	put(uint32(len(path)))
	meta = append(meta, path...)
	put(0x00001269)
	put(0xFFFFFFFF)
	put(1)
	put64(4)
	put(1)
	for _, v := range []uint32{3, 0, 0, 0, 0} {
		put(v)
	}
	put(1)
	put(2)
	put(0)
	raw := []byte{0x01, 0x00, 0x02, 0x00, 0xFF, 0xFF, 0xFE, 0xFF}
	file := make([]byte, 28, 28+len(meta)+len(raw))
	copy(file[0:4], "TDSm")
	binary.LittleEndian.PutUint32(file[4:8], 1<<1|1<<2|1<<3|1<<7)
	binary.LittleEndian.PutUint32(file[8:12], 4713)
	binary.LittleEndian.PutUint64(file[12:20], uint64(len(meta)+len(raw)))
	binary.LittleEndian.PutUint64(file[20:28], uint64(len(meta)))
	file = append(file, meta...)
	return append(file, raw...)
}

func TestCLIUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "gotdms info") {
		t.Fatalf("usage: %d %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "convert") {
		t.Fatalf("help: %d %s", code, stdout.String())
	}
	stderr.Reset()
	if code := run([]string{"nope"}, &stdout, &stderr); code != 2 {
		t.Fatal(code)
	}
	stderr.Reset()
	if code := run([]string{"info", filepath.Join(t.TempDir(), "missing.tdms")}, &stdout, &stderr); code != 1 {
		t.Fatalf("missing: %d %s", code, stderr.String())
	}
}
