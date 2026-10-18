# Changelog

Nothing here has been tagged or uploaded to PyPI.

## 0.1.0 (proposed)

First packaging of the Python client and the `gotdms-bridge` binary.

- Interactive `serve` protocol version 1 and `convert` bundles, Arrow IPC.
- Polars frames via `as_polars`. numpy arrays for raw samples.
- Wheels embed one platform binary. Source distributions vendor the core checkout they were built from so `go build` works before a core tag contains the dataset API.
- The core library's proposed `v0.1.0` does not include that dataset API. This package does not claim it does.
