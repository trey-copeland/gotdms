package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/trey-copeland/gotdms"
)

func runInfo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print the file tree as JSON")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: gotdms info [--json] file.tdms\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	path := fs.Arg(0)
	f, err := tdms.Open(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer f.Close()
	var werr error
	if *jsonOut {
		werr = writeInfoJSON(stdout, f, path)
	} else {
		werr = writeInfo(stdout, f, path)
	}
	if werr != nil {
		fmt.Fprintln(stderr, werr)
		return 1
	}
	return 0
}

func writeInfo(w io.Writer, f *tdms.File, path string) error {
	st := f.Status()
	if _, err := fmt.Fprintf(w, "file: %s\nversion: %d\nsegments: %d\nstatus: %s\n", path, f.Version(), f.SegmentCount(), st); err != nil {
		return err
	}
	if st.Incomplete || st.Truncated {
		if _, err := fmt.Fprintf(w, "partial tail: %t\nunused tail bytes: %d\n", st.PartialTail, st.UnusedTail); err != nil {
			return err
		}
	}
	if err := writePropBlock(w, "properties:", f.Properties(), "  "); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "groups:\n"); err != nil {
		return err
	}
	if len(f.Groups()) == 0 {
		_, err := fmt.Fprintf(w, "  (none)\n")
		return err
	}
	for _, g := range f.Groups() {
		if _, err := fmt.Fprintf(w, "  %s:\n", g.Name()); err != nil {
			return err
		}
		if err := writePropBlock(w, "    properties:", g.Properties(), "      "); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "    channels:\n"); err != nil {
			return err
		}
		if len(g.Channels()) == 0 {
			if _, err := fmt.Fprintf(w, "      (none)\n"); err != nil {
				return err
			}
			continue
		}
		for _, ch := range g.Channels() {
			if _, err := fmt.Fprintf(w, "      %s (%s, %d samples):\n", ch.Name(), ch.DataType(), ch.Len()); err != nil {
				return err
			}
			if err := writePropBlock(w, "        properties:", ch.Properties(), "          "); err != nil {
				return err
			}
		}
	}
	return nil
}

func writePropBlock(w io.Writer, header string, props []tdms.Property, indent string) error {
	if _, err := fmt.Fprintf(w, "%s\n", header); err != nil {
		return err
	}
	if len(props) == 0 {
		_, err := fmt.Fprintf(w, "%s(none)\n", indent)
		return err
	}
	for _, p := range props {
		text, err := formatScalar(p.Value)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s%s (%s): %s\n", indent, p.Name, p.Type, text); err != nil {
			return err
		}
	}
	return nil
}

type infoFile struct {
	Path       string      `json:"path"`
	Version    uint32      `json:"version"`
	Segments   int         `json:"segments"`
	Status     infoStatus  `json:"status"`
	Properties []infoProp  `json:"properties"`
	Groups     []infoGroup `json:"groups"`
}

type infoStatus struct {
	Complete        bool  `json:"complete"`
	Incomplete      bool  `json:"incomplete"`
	Truncated       bool  `json:"truncated"`
	PartialTail     bool  `json:"partial_tail"`
	UnusedTailBytes int64 `json:"unused_tail_bytes"`
}

type infoProp struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value any    `json:"value"`
}

type infoGroup struct {
	Name       string        `json:"name"`
	Path       string        `json:"path"`
	Properties []infoProp    `json:"properties"`
	Channels   []infoChannel `json:"channels"`
}

type infoChannel struct {
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	Type       string     `json:"type"`
	Samples    int        `json:"samples"`
	Properties []infoProp `json:"properties"`
}

func writeInfoJSON(w io.Writer, f *tdms.File, path string) error {
	st := f.Status()
	doc := infoFile{
		Path:     path,
		Version:  f.Version(),
		Segments: f.SegmentCount(),
		Status: infoStatus{
			Complete:        st.Complete,
			Incomplete:      st.Incomplete,
			Truncated:       st.Truncated,
			PartialTail:     st.PartialTail,
			UnusedTailBytes: st.UnusedTail,
		},
		Properties: propsJSON(f.Properties()),
		Groups:     []infoGroup{},
	}
	for _, g := range f.Groups() {
		jg := infoGroup{
			Name:       g.Name(),
			Path:       g.Path(),
			Properties: propsJSON(g.Properties()),
			Channels:   []infoChannel{},
		}
		for _, ch := range g.Channels() {
			jg.Channels = append(jg.Channels, infoChannel{
				Name:       ch.Name(),
				Path:       ch.Path(),
				Type:       ch.DataType().String(),
				Samples:    ch.Len(),
				Properties: propsJSON(ch.Properties()),
			})
		}
		doc.Groups = append(doc.Groups, jg)
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func propsJSON(ps []tdms.Property) []infoProp {
	if len(ps) == 0 {
		return []infoProp{}
	}
	out := make([]infoProp, len(ps))
	for i, p := range ps {
		out[i] = infoProp{Name: p.Name, Type: p.Type.String(), Value: propJSONValue(p.Value)}
	}
	return out
}
