// Command roundtrip writes a fixed set of TDMS files and reads a directory
// back as JSON. testdata/nptdms/harness.py drives it. It is not part of
// go test ./... (the testdata tree is ignored) and it is not a library.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/trey-copeland/gotdms"
)

type fileDoc struct {
	Properties map[string]any `json:"properties"`
	Channels   []chDoc        `json:"channels"`
}

type chDoc struct {
	Group  string `json:"group"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Values []any  `json:"values"`
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: roundtrip write|read <dir>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "write":
		err = writeAll(os.Args[2])
	case "read":
		err = readAll(os.Args[2])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeAll(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	when := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	type built struct {
		name string
		doc  fileDoc
	}
	files := []built{}

	add := func(name string, fn func(*tdms.Writer) (fileDoc, error)) error {
		path := filepath.Join(dir, name)
		w, err := tdms.Create(path)
		if err != nil {
			return err
		}
		doc, err := fn(w)
		if err != nil {
			w.Close()
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		files = append(files, built{name: name, doc: doc})
		return nil
	}

	if err := add("scalars.tdms", func(w *tdms.Writer) (fileDoc, error) {
		if err := w.SetProperty("title", "scalars"); err != nil {
			return fileDoc{}, err
		}
		g := w.Group("g")
		pairs := []struct {
			name string
			dt   tdms.DataType
			v    any
			out  []any
		}{
			{"i32", tdms.TypeInt32, []int32{-1, 2, 3}, []any{float64(-1), float64(2), float64(3)}},
			{"i16", tdms.TypeInt16, []int16{4, -5}, []any{float64(4), float64(-5)}},
			{"u8", tdms.TypeUint8, []uint8{0, 255}, []any{float64(0), float64(255)}},
			{"f64", tdms.TypeFloat64, []float64{1.5, -2.25}, []any{1.5, -2.25}},
			{"f32", tdms.TypeFloat32, []float32{0.5}, []any{float64(float32(0.5))}},
			{"b", tdms.TypeBool, []bool{true, false}, []any{true, false}},
			{"s", tdms.TypeString, []string{"ab", "", "c,d"}, []any{"ab", "", "c,d"}},
		}
		doc := fileDoc{Properties: map[string]any{"title": "scalars"}}
		for _, p := range pairs {
			ch, err := g.Channel(p.name, p.dt)
			if err != nil {
				return fileDoc{}, err
			}
			if err := ch.Write(p.v); err != nil {
				return fileDoc{}, err
			}
			doc.Channels = append(doc.Channels, chDoc{Group: "g", Name: p.name, Type: p.dt.String(), Values: p.out})
		}
		return doc, nil
	}); err != nil {
		return err
	}

	if err := add("segments.tdms", func(w *tdms.Writer) (fileDoc, error) {
		ch, err := w.Group("g").Channel("c", tdms.TypeFloat64)
		if err != nil {
			return fileDoc{}, err
		}
		if err := ch.Write([]float64{1, 2}); err != nil {
			return fileDoc{}, err
		}
		if err := w.Flush(); err != nil {
			return fileDoc{}, err
		}
		if err := ch.Write([]float64{3}); err != nil {
			return fileDoc{}, err
		}
		return fileDoc{
			Properties: map[string]any{},
			Channels:   []chDoc{{Group: "g", Name: "c", Type: tdms.TypeFloat64.String(), Values: []any{1.0, 2.0, 3.0}}},
		}, nil
	}); err != nil {
		return err
	}

	if err := add("interleaved.tdms", func(w *tdms.Writer) (fileDoc, error) {
		w.SetInterleaved(true)
		a, err := w.Group("g").Channel("a", tdms.TypeFloat64)
		if err != nil {
			return fileDoc{}, err
		}
		b, err := w.Group("g").Channel("b", tdms.TypeInt32)
		if err != nil {
			return fileDoc{}, err
		}
		if err := a.Write([]float64{1.25, 2.5}); err != nil {
			return fileDoc{}, err
		}
		if err := b.Write([]int32{10, 20}); err != nil {
			return fileDoc{}, err
		}
		return fileDoc{
			Properties: map[string]any{},
			Channels: []chDoc{
				{Group: "g", Name: "a", Type: tdms.TypeFloat64.String(), Values: []any{1.25, 2.5}},
				{Group: "g", Name: "b", Type: tdms.TypeInt32.String(), Values: []any{float64(10), float64(20)}},
			},
		}, nil
	}); err != nil {
		return err
	}

	if err := add("time.tdms", func(w *tdms.Writer) (fileDoc, error) {
		ch, err := w.Group("g").Channel("t", tdms.TypeTimestamp)
		if err != nil {
			return fileDoc{}, err
		}
		if err := ch.Write([]tdms.Timestamp{tdms.NewTimestamp(when)}); err != nil {
			return fileDoc{}, err
		}
		return fileDoc{
			Properties: map[string]any{},
			Channels: []chDoc{{
				Group: "g", Name: "t", Type: tdms.TypeTimestamp.String(),
				Values: []any{when.Format(time.RFC3339Nano)},
			}},
		}, nil
	}); err != nil {
		return err
	}

	out := map[string]fileDoc{}
	for _, f := range files {
		out[f.name] = f.doc
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), append(raw, '\n'), 0o644)
}

func readAll(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".tdms" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := map[string]fileDoc{}
	for _, name := range names {
		doc, err := readOne(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		out[name] = doc
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func readOne(path string) (fileDoc, error) {
	f, err := tdms.Open(path)
	if err != nil {
		return fileDoc{}, err
	}
	defer f.Close()
	doc := fileDoc{Properties: map[string]any{}}
	for _, p := range f.Properties() {
		doc.Properties[p.Name] = propValue(p.Value)
	}
	for _, g := range f.Groups() {
		for _, ch := range g.Channels() {
			vals, err := channelValues(ch)
			if err != nil {
				return fileDoc{}, err
			}
			doc.Channels = append(doc.Channels, chDoc{
				Group: g.Name(), Name: ch.Name(), Type: ch.DataType().String(), Values: vals,
			})
		}
	}
	if doc.Channels == nil {
		doc.Channels = []chDoc{}
	}
	return doc, nil
}

func channelValues(ch *tdms.Channel) ([]any, error) {
	switch ch.DataType() {
	case tdms.TypeTimestamp:
		ts, err := ch.Times()
		if err != nil {
			return nil, err
		}
		out := make([]any, len(ts))
		for i, v := range ts {
			out[i] = v.UTC().Format(time.RFC3339Nano)
		}
		return out, nil
	case tdms.TypeBool:
		b, err := ch.Bools()
		if err != nil {
			return nil, err
		}
		out := make([]any, len(b))
		for i, v := range b {
			out[i] = v
		}
		return out, nil
	case tdms.TypeString:
		s, err := ch.Strings()
		if err != nil {
			return nil, err
		}
		out := make([]any, len(s))
		for i, v := range s {
			out[i] = v
		}
		return out, nil
	default:
		f, err := ch.Float64s()
		if err != nil {
			return nil, err
		}
		out := make([]any, len(f))
		for i, v := range f {
			out[i] = v
		}
		return out, nil
	}
}

func propValue(v any) any {
	switch x := v.(type) {
	case string, bool, float64, float32, int32, int64, uint32, uint64:
		return x
	default:
		return fmt.Sprint(v)
	}
}
