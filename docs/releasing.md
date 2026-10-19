# Releasing gotdms

A Go library is distributed by a version tag. The module proxy (`proxy.golang.org`) fetches the tag from GitHub. This repository does not publish binaries for the library.

Nothing here has been tagged or published. `v0.1.0` is the proposed first tag. [CHANGELOG.md](../CHANGELOG.md) describes the tree on `main` at `795c8b1`: the reader and writer, analog waveforms and range reads, the dataset package, CI, and the in-tree `bridge/` module. The bridge is still pending extraction and is not a published Python package. Do not describe it as one in the release notes.

`.github/workflows/ci.yml` is already on `main`. This checklist requires that workflow to be green for the commit you tag. This document does not add a second copy of that CI workflow. `.github/workflows/release.yml` is separate: it runs only on a `v*` tag, re-checks the modules, and opens a GitHub Release. It does not build binaries, it does not publish to PyPI, and it does not talk to the module proxy. It does nothing until it has been merged to `main` and someone pushes a tag.

## Before the tag

1. Confirm `main` is the commit you mean. `git log -1` and the proposed `0.1.0` section in the changelog must still describe that commit.
2. Confirm the latest CI run on that commit is green. CI is `.github/workflows/ci.yml`.
3. From a clean checkout of that commit:

   ```bash
   gofmt -l .
   go vet ./...
   go test ./...
   go test -race .
   go test -race ./dataset/
   go test -run Example -count=1
   ```

   `gofmt -l` must print nothing. It also walks `bridge/`, because that directory is in the tree. `go test ./...` at the root does not enter `bridge/`; `bridge/` is its own module.

   On `795c8b1`, `dataset.TestSharedClockDecodedOnce` fails: catalog allocates about 279KB against that test's 64KB budget. The same failure is on untouched `main`. Do not tag until `go test ./...` is green. This checklist does not change that test.

4. In `bridge/`:

   ```bash
   go vet ./...
   go test ./...
   ```

   The Python client tests are `python3 -m unittest python/tests/test_session.py` from `bridge/`, and they need numpy, polars, and pyarrow. They are not part of the Go release workflow.

5. Confirm the core `go.mod` is still only:

   ```
   module github.com/trey-copeland/gotdms

   go 1.22
   ```

   There must be no `require` block. `go 1.22` is the minimum the module declares. CI also builds with Go 1.27.
6. Update `CHANGELOG.md`: move the proposed `0.1.0` section out of "not tagged" wording once you are about to tag. Commit that on `main`. The tag points at the commit that contains the notes.

## Tag and GitHub release

The tag is a semver tag with a leading `v`. For the first release that is `v0.1.0`.

```bash
git checkout main
git pull
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

Pushing the tag is what arms `.github/workflows/release.yml`, and only after that file is on the tagged commit. The workflow runs `gofmt`, `go vet`, `go test ./...`, `go test -race .`, `go test -race ./dataset/`, and `go test ./...` in `bridge/`, with the Go version in the core `go.mod`, then:

```bash
gh release create "$GITHUB_REF_NAME" --title "$GITHUB_REF_NAME" --generate-notes
```

Generated notes are commit subjects. After the workflow finishes, edit the GitHub release so the body leads with the `0.1.0` section of `CHANGELOG.md`. Do not attach library binaries. Do not treat that GitHub release as a PyPI upload.

If the workflow is not on `main` yet, create the release by hand with the same `gh release create` command, and still run the tests above first.

## Prove the proxy

Wait until `https://proxy.golang.org/github.com/trey-copeland/gotdms/@v/v0.1.0.info` returns the tag. The first request can take a minute. Then, in an empty directory, outside this checkout:

```bash
mkdir /tmp/gotdms-mod && cd /tmp/gotdms-mod
go mod init example.com/consume
go get github.com/trey-copeland/gotdms@v0.1.0
```

`go get` must select `v0.1.0`. A short program that imports `github.com/trey-copeland/gotdms` and calls `tdms.Create` on a `bytes.Buffer` must compile. The dataset package is `github.com/trey-copeland/gotdms/dataset` in the same module. `bridge/` is not part of that module.

pkg.go.dev (`https://pkg.go.dev/github.com/trey-copeland/gotdms@v0.1.0`) should show the package comment from `doc.go` and `Example_roundTrip`. The site lags the proxy. The README at the repository root is the page it renders above the package index.

## License

`LICENSE` is the MIT text. The core module vendors nothing and has no third-party Go dependencies, so there is no third-party notices file for that module. Test vectors in `golden_test.go` quote hex from NI's public TDMS format article. That is format documentation, not copied source. Comments mention npTDMS only to describe on-disk behavior. No npTDMS source is included.

`bridge/` links Apache Arrow and other Go modules, and the Python package depends on numpy, polars, and pyarrow. Those notices live in `bridge/THIRD_PARTY.md` and `bridge/licenses/go/`. They are not dependencies of the core module.

## What not to do

- Do not tag while `go test ./...` is failing.
- Do not publish the Python package, create `gotdms-python`, or push a tag inside `bridge/` from this checklist. Extraction and PyPI trusted publishing are separate.
- Do not upload the module anywhere except by pushing the tag. The proxy pulls from the tag.
