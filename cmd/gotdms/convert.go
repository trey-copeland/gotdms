package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/trey-copeland/gotdms"
)

func runConvert(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("convert", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outPath := fs.String("o", "", "CSV output path; stdout when empty or -")
	group := fs.String("group", "", "export this group; with --channel, the channel's group")
	channel := fs.String("channel", "", "export this channel; the name must be unique when --group is empty")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: gotdms convert [-o out.csv] [--group name] [--channel name] file.tdms\n")
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
	chs, err := selectChannels(f, *group, *channel)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var out io.Writer = stdout
	if *outPath != "" && *outPath != "-" {
		of, err := os.Create(*outPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer of.Close()
		out = of
	}
	if err := writeChannelsCSV(out, chs); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func selectChannels(f *tdms.File, group, channel string) ([]*tdms.Channel, error) {
	if group != "" {
		g, ok := f.Group(group)
		if !ok {
			return nil, fmt.Errorf("group %q not found", group)
		}
		if channel == "" {
			chs := g.Channels()
			if len(chs) == 0 {
				return nil, fmt.Errorf("group %q has no channels", group)
			}
			return chs, nil
		}
		ch, ok := g.Channel(channel)
		if !ok {
			return nil, fmt.Errorf("channel %q not found in group %q", channel, group)
		}
		return []*tdms.Channel{ch}, nil
	}
	if channel == "" {
		var all []*tdms.Channel
		for _, g := range f.Groups() {
			all = append(all, g.Channels()...)
		}
		if len(all) == 0 {
			return nil, fmt.Errorf("file has no channels")
		}
		return all, nil
	}
	var found []*tdms.Channel
	for _, g := range f.Groups() {
		if ch, ok := g.Channel(channel); ok {
			found = append(found, ch)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("channel %q not found", channel)
	case 1:
		return found, nil
	default:
		return nil, fmt.Errorf("channel %q is in more than one group; set --group", channel)
	}
}

// csvMissing is the cell written when a channel has no sample at that row.
// A present empty string stays an empty field, so the two are not the same
// cell. A string whose value is these four characters is written the same
// way; CSV has no separate missing-value type.
const csvMissing = "#N/A"

// writeChannelsCSV streams one column per channel. Rows line up by sample
// index. A channel that ends early writes csvMissing for the rest of its
// column. A present empty string is an empty field. Each column is read
// with Cursor in batches, so the whole channel is not held at once.
func writeChannelsCSV(w io.Writer, chs []*tdms.Channel) error {
	cols := make([]csvCol, len(chs))
	header := make([]string, len(chs))
	for i, ch := range chs {
		header[i] = ch.Path()
		cols[i] = csvCol{cur: ch.Cursor(4096)}
		defer cols[i].cur.Close()
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return err
	}
	for {
		row := make([]string, len(cols))
		for i := range row {
			row[i] = csvMissing
		}
		any := false
		for i := range cols {
			s, ok, err := cols[i].next()
			if err != nil {
				return err
			}
			if ok {
				any = true
				row[i] = s
			}
		}
		if !any {
			break
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

type csvCol struct {
	cur   *tdms.Cursor
	batch any
	i     int
	done  bool
}

func (c *csvCol) next() (string, bool, error) {
	if c.done {
		return "", false, nil
	}
	if c.batch == nil || c.i >= batchLen(c.batch) {
		if !c.cur.Next() {
			c.done = true
			c.batch = nil
			return "", false, c.cur.Err()
		}
		c.batch = c.cur.Values()
		c.i = 0
		if batchLen(c.batch) == 0 {
			c.done = true
			return "", false, nil
		}
	}
	s, err := formatSample(c.batch, c.i)
	if err != nil {
		return "", false, err
	}
	c.i++
	return s, true, nil
}

func batchLen(v any) int {
	switch s := v.(type) {
	case []int8:
		return len(s)
	case []int16:
		return len(s)
	case []int32:
		return len(s)
	case []int64:
		return len(s)
	case []uint8:
		return len(s)
	case []uint16:
		return len(s)
	case []uint32:
		return len(s)
	case []uint64:
		return len(s)
	case []float32:
		return len(s)
	case []float64:
		return len(s)
	case []bool:
		return len(s)
	case []string:
		return len(s)
	case []tdms.Timestamp:
		return len(s)
	case []tdms.Extended:
		return len(s)
	case []complex64:
		return len(s)
	case []complex128:
		return len(s)
	default:
		return 0
	}
}
