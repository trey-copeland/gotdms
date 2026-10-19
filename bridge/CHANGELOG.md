# Changelog

Nothing here has been tagged or uploaded to PyPI.

## 0.1.0 (proposed)

First packaging of the Python client and the `gotdms-bridge` binary.

- Interactive `serve` protocol version 1 and `convert` bundles, Arrow IPC.
- Polars frames via `as_polars`. numpy arrays for raw samples.
- Wheels embed one platform binary. Source distributions vendor the core checkout they were built from so `go build` works before a core tag is published.
- `main` includes the dataset API this binary imports. This package does not claim a core release tag exists.
