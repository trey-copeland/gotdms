# Releasing gotdms

A Go library is distributed by a version tag. The module proxy (`proxy.golang.org`) fetches the tag from GitHub. This repository does not publish binaries for the library.

Nothing here has been tagged or published. `v0.1.0` is the proposed first tag. [CHANGELOG.md](../CHANGELOG.md) describes the reader and writer, analog waveforms and range reads, the dataset package, and CI. The Python package is [github.com/trey-copeland/gotdms-python](https://github.com/trey-copeland/gotdms-python). It was extracted from this repository on 2026-10-19 and is not on PyPI. This checklist does not publish it.

`.github/workflows/build-and-test-go-module.yml` is already on `main`. This checklist requires that workflow to be green for the commit you tag. This document does not add a second copy of that workflow. `.github/workflows/test-and-publish-go-release.yml` is separate: it runs only on a `v*` tag, re-checks this module, and opens a GitHub Release. It does not build binaries, it does not publish to PyPI, and it does not talk to the module proxy. It does nothing until it is on `main` and someone pushes a tag.

## Before the tag

1. Confirm `main` is the commit you mean. `git log -1` and the proposed `0.1.0` section in the changelog must still describe that commit.
2. Confirm the latest run of `.github/workflows/build-and-test-go-module.yml` on that commit is green.
3. From a clean checkout of that commit:

   ```bash
   gofmt -l .
   go vet ./...
   go test ./...
   go test -race .
   go test -race ./dataset/
   go test -run Example -count=1
   ```

   `gofmt -l` must print nothing. `go test ./...` covers this module, including `dataset/`. While `bridge/` lived here it was a separate Go module, so that command never entered it. The directory is gone; the Python package's tests run in [gotdms-python](https://github.com/trey-copeland/gotdms-python).

4. Confirm the core `go.mod` is still only:

   ```
   module github.com/trey-copeland/gotdms

   go 1.22
   ```

   There must be no `require` block. `go 1.22` is the minimum the module declares. CI also builds with Go 1.27.
5. Update `CHANGELOG.md`: move the proposed `0.1.0` section out of "not tagged" wording once you are about to tag. Commit that on `main`. The tag points at the commit that contains the notes.

## Tag and GitHub release

The tag is a semver tag with a leading `v`. For the first release that is `v0.1.0`.

```bash
git checkout main
git pull
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

Pushing the tag is what arms `.github/workflows/test-and-publish-go-release.yml`, and only after that file is on the tagged commit. The workflow runs `gofmt`, `go vet`, `go test ./...`, `go test -race .`, and `go test -race ./dataset/`, with the Go version in `go.mod`, then:

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

`go get` must select `v0.1.0`. A short program that imports `github.com/trey-copeland/gotdms` and calls `tdms.Create` on a `bytes.Buffer` must compile. The dataset package is `github.com/trey-copeland/gotdms/dataset` in the same module. The Python package is not part of this module.

pkg.go.dev (`https://pkg.go.dev/github.com/trey-copeland/gotdms@v0.1.0`) should show the package comment from `doc.go` and `Example_roundTrip`. The site lags the proxy. The README at the repository root is the page it renders above the package index.

## License

`LICENSE` is the MIT text. The core module vendors nothing and has no third-party Go dependencies, so there is no third-party notices file for that module. Test vectors in `golden_test.go` quote hex from NI's public TDMS format article. That is format documentation, not copied source. Comments mention npTDMS only to describe on-disk behavior. No npTDMS source is included.

The Python package's third-party notices live in [gotdms-python](https://github.com/trey-copeland/gotdms-python). They are not dependencies of this module.

## What not to do

- Do not tag while `go test ./...` is failing.
- Do not publish the Python package from this checklist. It lives in [gotdms-python](https://github.com/trey-copeland/gotdms-python) and is not on PyPI.
- Do not upload the module anywhere except by pushing the tag. The proxy pulls from the tag.
