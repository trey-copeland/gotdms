package main

import (
	"bytes"
	"encoding/json"
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
