# Extracting this tree into gotdms-python

This directory is the whole future repository. Nothing here creates the GitHub repo or a PyPI project. Those steps need the owner's accounts.

## Lift

1. Confirm the GitHub name `gotdms-python` and the PyPI name `gotdms`. Both PyPI names `gotdms` and `gotdms-python` returned HTTP 404 on 18 Oct 2026, so both looked free. The recommended split is repository `gotdms-python`, PyPI distribution `gotdms`, import package `gotdms`.
2. Create an empty repository `github.com/trey-copeland/gotdms-python`. Do not add a license or README in the GitHub UI. This tree already has both.
3. Copy the contents of this directory to that repository's root. `bridge/.github/workflows/` becomes `.github/workflows/` because the directory itself is the root. Do not leave a `bridge/` prefix.
4. The Go module path is already `github.com/trey-copeland/gotdms-python`. `cmd/gotdms-bridge` imports that path. No import edit is required at extraction.

## Replace directive

`go.mod` still says:

```
replace github.com/trey-copeland/gotdms => ../
```

That line is what `go test` uses while this tree lives inside the gotdms checkout. `main` already contains the dataset API this binary imports. No core tag has been published, so the replace stays `../` until one is. Require a tag cut from that `main` (or later). A tag cut from the pre-dataset history would not build.

When a core tag does contain `github.com/trey-copeland/gotdms/dataset`, edit `go.mod` in the extracted repo to:

```
require github.com/trey-copeland/gotdms vX.Y.Z

require github.com/apache/arrow-go/v18 v18.0.0
```

and delete the `replace` line. Then:

```bash
go get github.com/trey-copeland/gotdms@vX.Y.Z
go mod tidy
go test ./...
```

Until that tag exists, source installs use the core snapshot the sdist vendors at `third_party/gotdms`. The sdist vendors the parent module when `../go.mod` is the core module, and otherwise vendors a local replace such as `./_gotdms` when that directory is the core module. A source install (`pip install` of the sdist, Go on `PATH`) rewrites `replace => ../` onto `./third_party/gotdms` and tags the wheel for the host. Set `GOTDMS_PLAT_TAG` only when cross-compiling. Building a wheel from this checkout still uses `../` and does not rewrite `go.mod`.

The CI workflow's `go mod edit -replace github.com/trey-copeland/gotdms=./_gotdms` stays valid after extraction. It checks out `trey-copeland/gotdms` (default branch). That default branch includes the dataset package.

## After the push

1. GitHub Actions on the new repo should run `.github/workflows/ci.yml` and `.github/workflows/wheels.yml`. `ci.yml` checks out gotdms `main`, which includes the dataset API. `wheels.yml` builds an sdist and a linux/amd64 wheel from the parent checkout only when this tree is still inside gotdms. On the extracted repo, the wheel build uses the replace onto that checkout, or a published core tag once one exists. The sdist produced from this monorepo already vendors the core.
2. `go test ./...` in the extracted repo.
3. `python -m build` and `twine check dist/*` (or the wheels workflow).
4. Install the wheel in a clean venv, with `GOTDMS_BRIDGE` unset, and import `gotdms`. `locate_binary()` must print the path inside `site-packages/gotdms/_bin/`.
5. Do not tag until the PyPI trusted publisher is configured. See `.github/workflows/release.yml`.

## Versions

The core module, this Go module, and the Python package version independently.

- Core tags (`v0.1.0` and later) are the gotdms reader. They are not this package's version.
- Python `0.1.0` in `pyproject.toml` is this wrapper. The first Git tag of the extracted repo should be `v0.1.0` so the Go module version and the Python version match for that first release. Later tags may diverge. Say so in that repo's changelog when they do.
- The compatibility contract with a running `gotdms-bridge` is protocol version 1 (`ProtocolVersion` in `frame.go`), not the core tag. A bridge and a client disagree when that number disagrees.
- The minimum core source this binary can compile is the dataset API (declared timestamp channels, basis, `ReadMany`). That API is on `main`. No released core tag provides it yet.
