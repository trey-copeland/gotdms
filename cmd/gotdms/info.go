package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/trey-copeland/gotdms"
)

func runInfo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print the file tree as JSON")
	lenient := fs.Bool("lenient", false, "keep the whole segments of a file with a truncated or torn tail (tdms.WithLenientTail)")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: gotdms info [--json] [--lenient] file.tdms\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	path := fs.Arg(0)
	var opts []tdms.ReaderOption
	if *lenient {
		opts = append(opts, tdms.WithLenientTail())
	}
	before, _ := os.Stat(path)
	f, err := tdms.Open(path, opts...)
	if err != nil {
		fmt.Fprintln(stderr, err)
		if !*lenient {
			tornHint(stderr, path)
		}
		return 1
	}
	defer f.Close()
	whole := wholeBytes(path, f.Status(), before)
	var werr error
	if *jsonOut {
		werr = writeInfoJSON(stdout, f, path, whole)
	} else {
		werr = writeInfo(stdout, f, path, whole)
	}
	if werr != nil {
		fmt.Fprintln(stderr, werr)
		return 1
	}
	return 0
}

// tornHint runs after a strict open failed. When a lenient open of the
// same file drops a torn tail, it says how many bytes and where the whole
// segments end, so the file can be inspected with --lenient or truncated.
func tornHint(w io.Writer, path string) {
	before, _ := os.Stat(path)
	f, err := tdms.Open(path, tdms.WithLenientTail())
	if err != nil {
		return
	}
	st := f.Status()
	f.Close()
	if st.DroppedTail == 0 {
		if st.Truncated {
			fmt.Fprintf(w, "%s: the final segment runs past EOF; gotdms info --lenient shows the readable values\n", path)
		}
		return
	}
	whole := wholeBytes(path, st, before)
	if whole < 0 {
		fmt.Fprintf(w, "%s: torn tail: %d bytes after the last whole segment (the file changed while it was read). "+
			"gotdms info --lenient shows the whole segments\n", path, st.DroppedTail)
		return
	}
	fmt.Fprintf(w, "%s: torn tail: %d bytes after the last whole segment, which ends at byte %d. "+
		"gotdms info --lenient shows the whole segments. If those bytes are only what the writer had not finished "+
		"(for example after a power loss), truncating the file to %d bytes makes it open without --lenient\n",
		path, st.DroppedTail, whole, whole)
}

// wholeBytes is the file size minus a dropped torn tail: the byte where the
// whole segments end. It is -1 when nothing was dropped, or when the file's
// size or modification time differs from before, the stat taken before the
// open, so the size the open saw is not known. A truncate size computed
// from a file that changed under the open would cut the wrong bytes.
func wholeBytes(path string, st tdms.FileStatus, before os.FileInfo) int64 {
	if st.DroppedTail == 0 || before == nil {
		return -1
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Size() != before.Size() || !fi.ModTime().Equal(before.ModTime()) {
		return -1
	}
	return fi.Size() - st.DroppedTail
}

func writeInfo(w io.Writer, f *tdms.File, path string, whole int64) error {
	st := f.Status()
	if _, err := fmt.Fprintf(w, "file: %s\nversion: %d\nsegments: %d\nstatus: %s\n", path, f.Version(), f.SegmentCount(), st); err != nil {
		return err
	}
	if (st.Incomplete || st.Truncated) && st.DroppedTail == 0 {
		if _, err := fmt.Fprintf(w, "partial tail: %t\nunused tail bytes: %d\n", st.PartialTail, st.UnusedTail); err != nil {
			return err
		}
	}
	if st.DroppedTail > 0 {
		end := "unknown (the file changed while it was read)"
		if whole >= 0 {
			end = fmt.Sprint(whole)
		}
		if _, err := fmt.Fprintf(w, "dropped torn tail bytes: %d\nwhole segments end at byte: %s\ntail error: %v\n", st.DroppedTail, end, st.TailError); err != nil {
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
	Complete        bool   `json:"complete"`
	Incomplete      bool   `json:"incomplete"`
	Truncated       bool   `json:"truncated"`
	PartialTail     bool   `json:"partial_tail"`
	UnusedTailBytes int64  `json:"unused_tail_bytes"`
	DroppedTail     int64  `json:"dropped_tail_bytes,omitempty"`
	WholeBytes      int64  `json:"whole_bytes,omitempty"`
	TailError       string `json:"tail_error,omitempty"`
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

func writeInfoJSON(w io.Writer, f *tdms.File, path string, whole int64) error {
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
	if st.DroppedTail > 0 {
		doc.Status.DroppedTail = st.DroppedTail
		if whole >= 0 {
			doc.Status.WholeBytes = whole
		}
		doc.Status.TailError = st.TailError.Error()
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
