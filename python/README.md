# gotdms Python client

This package is the MIT path off npTDMS for post-analysis. It does not import npTDMS. A small client launches the `gotdms-bridge` binary, which is a separate Go module in `bridge/` and links Apache Arrow. The core gotdms module stays dependency-free.

numpy is required. Decode needs the `arrow` extra (`pyarrow`). DataFrames need the `pandas` extra.

```bash
export GOTDMS_BRIDGE=/path/to/gotdms-bridge
```

`GOTDMS_BRIDGE` overrides the binary location. Otherwise the client looks on `PATH`.

Two modes:

- Interactive: `gotdms.open(...)` keeps one process open and reads channels or time windows on demand.
- Batch: `gotdms-bridge convert` writes `manifest.json` plus one Arrow IPC stream per channel. `gotdms.load_bundle` opens that directory in one call.

Scaling is explicit. gotdms returns raw samples. npTDMS returns scaled data unless you opt out. Pass `scaled=True` to apply an NI linear scale. A missing scale is an error, not a silent raw result. Non-linear scales and DAQmx scaling are not applied.

Relative time is seconds since the basis you set. It is not npTDMS `time_track(absolute_time=False)`, which is zero at the first sample when `wf_start_offset` is zero.

The mapping, both usage modes, and the remaining gaps are in [docs/nptdms-migration.md](../docs/nptdms-migration.md).
