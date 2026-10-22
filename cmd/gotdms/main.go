// Command gotdms prints the structure of an NI TDMS file and exports channel
// samples to CSV.
//
//	gotdms info [--json] file.tdms
//	gotdms convert [-o out.csv] [--group name] [--channel name] file.tdms
//	gotdms index [--force] [--check] file.tdms...
//
// info lists groups, channels (data type and sample count), and properties
// at file, group, and channel level. --json writes the same tree as JSON.
//
// convert writes one CSV column per selected channel, with a header row.
// The header cell is the channel's TDMS object path. Omit both --group and
// --channel to export every channel. Timestamps are UTC RFC3339Nano because
// the channel's data type is tdsTypeTimeStamp; a channel is not treated as
// time because of its name. A present empty string is an empty field. A
// missing sample (the channel is shorter than the row) is the cell #N/A.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usageText = `gotdms reads NI TDMS files.

Usage:
  gotdms info [--json] file.tdms
  gotdms convert [-o out.csv] [--group name] [--channel name] file.tdms
  gotdms index [--force] [--check] file.tdms...

info prints groups, channels (data type and sample count), and properties
at file, group, and channel level. --json prints that tree as JSON.

convert writes channel samples as CSV with a header row. The header cell is
the channel's TDMS path. With no --group and no --channel, every channel is
a column. --group alone writes that group. --channel selects one channel and
requires --group when the name occurs in more than one group. Timestamps are
UTC RFC3339Nano. The format follows the channel data type, not the name.
A present empty string is an empty field. A row where that channel has no
sample is the cell #N/A. A string value that is itself #N/A looks the same.

index writes a .gtdx segment-index sidecar beside each TDMS file. It reads
lead-ins and metadata only. One command accepts several files. A directory
argument indexes the .tdms files in that directory, and a pattern indexes
the .tdms matches. Without --force, a sidecar that already matches is left
in place. --force rebuilds every sidecar. --check reports whether each
existing sidecar matches and does not write. --check and --force cannot be
combined. The exit status is non-zero when any file fails.
`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "info":
		return runInfo(args[1:], stdout, stderr)
	case "convert":
		return runConvert(args[1:], stdout, stderr)
	case "index":
		return runIndex(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
}
