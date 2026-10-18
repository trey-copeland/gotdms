# gotdms-python

MIT bridge from the [gotdms](https://github.com/trey-copeland/gotdms) TDMS library to Python. The analysis tool stays in Python. It uses numpy arrays and Polars frames. It does not import npTDMS.

This directory is self-contained so it can be lifted out as its own repository. While it sits inside the gotdms checkout, `go.mod` replaces the core module with `../`. After extraction, that parent is gone. The workflow under `.github/workflows/` checks out gotdms and rewrites the replace. See the comment at the top of that file: inside gotdms the workflow path is `bridge/.github/workflows`, and GitHub does not run it until this directory is the repository root.

## Layout

- `cmd/gotdms-bridge` is the binary: `serve` for a long-lived session, `convert` for a batch bundle.
- `python/` is the client (`pyproject.toml`, package `gotdms`).
- `docs/nptdms-migration.md` maps npTDMS post-analysis calls onto this client.
- `LICENSE` is MIT.

The core gotdms module stays dependency-free. Apache Arrow Go is linked only here.

## Build

```bash
go build -o gotdms-bridge ./cmd/gotdms-bridge
export GOTDMS_BRIDGE="$PWD/gotdms-bridge"
```

numpy is required and is BSD-3-Clause. Decode needs pyarrow (`gotdms[arrow]`). Frames need Polars (`gotdms[polars]`). pyarrow is the interchange; Polars speaks Arrow natively. This project does not depend on pandas. `frame.to_pandas()` is Polars' own conversion when a caller wants it.

## Two modes

Interactive: `gotdms.open(...)` keeps one process open and reads channels or time windows on demand. One `read_many` is one response, so a shared timestamp channel is decoded once.

Batch: `gotdms-bridge convert` writes `manifest.json` plus one Arrow IPC stream per channel. `gotdms.load_bundle` opens that directory, and `as_polars` builds a Polars frame.

Samples are raw unless `scaled=True` or `--scaled`. A missing linear scale is an error. Non-linear scales and DAQmx scaling are not applied.

Polars has no row index. npTDMS `as_dataframe(time_index=True)` becomes a `time` column (`Datetime(ns, UTC)`), not an index. `align="strict"` requires equal time vectors. `align="nptdms"` is a full outer join on that column and inserts nulls where npTDMS inserted NaNs. Relative time is seconds since the basis, not npTDMS's zero-at-first-sample track.

The call-site map, both modes, and the remaining gaps are in [docs/nptdms-migration.md](docs/nptdms-migration.md).
