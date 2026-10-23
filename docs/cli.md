# The gotdms command

[Back to the docs index](README.md)

The `gotdms` command reads a file and writes CSV. From a checkout:

```bash
go run ./cmd/gotdms info run.tdms
go run ./cmd/gotdms info --json run.tdms
go run ./cmd/gotdms convert -o run.csv run.tdms
go run ./cmd/gotdms convert --group acquisition --channel temperature -o temp.csv run.tdms
```

`go install github.com/trey-copeland/gotdms/cmd/gotdms@latest` builds the latest tagged release into `$(go env GOPATH)/bin` (or `GOBIN` when set); put that directory on `PATH`. Flags added since that tag (see the Unreleased section of [CHANGELOG.md](../CHANGELOG.md), such as `info --lenient`) need a checkout until the next tag. `gotdms help` prints the usage.

## info and convert

`info` prints the file, each group, and each channel with its data type, sample count, and properties at every level. `--json` prints that tree as JSON. The status object has `complete`, `incomplete`, `truncated`, `partial_tail`, and `unused_tail_bytes`, plus `dropped_tail_bytes`, `whole_bytes`, and `tail_error` when a torn tail was dropped. By default the command calls `Open` without `WithLenientTail`, so a next-segment offset past EOF or a torn tail is an error and prints no JSON; when a lenient open would recover the file, `info` says so and names the size to truncate to. `--lenient` opens with `WithLenientTail` and prints the dropped tail bytes, the byte where the whole segments end, and the strict error. `convert` writes one CSV column per channel and a header row. The header cell is the channel's TDMS path (`/'group'/'channel'`). With no `--group` and no `--channel`, every channel is a column. `--group` alone writes that group. `--channel` selects one channel and needs `--group` when the name is used in more than one group. Timestamp values are UTC RFC3339Nano because the data type is `tdsTypeTimeStamp`. A channel named `Time` that stores floats is written as numbers. A present empty string is an empty field. A row past the end of a shorter channel is the cell `#N/A`. A string whose value is the characters `#N/A` is written the same way; CSV has no separate missing-value type.

## index

`gotdms index [--force] [--check] file.tdms...` writes one sidecar per file. Several file arguments, a directory of `.tdms` files, or a pattern are accepted. An argument that already exists is that path, including a name that contains `*`, `?`, or `[`. A directory skips entries that are not regular files. The same path reached twice is indexed once. Without `--force`, a sidecar that already matches is left in place. `--check` reports whether each existing sidecar matches and does not write. The process exits non-zero if any file fails.

The sidecar itself is described in [Segment index](segment-index.md). Torn-tail recovery behind `--lenient` is in [File status, torn tails, and Follow](file-status.md).
