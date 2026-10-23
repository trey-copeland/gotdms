# Releasing gotdms

A Go library is distributed by a version tag. The module proxy (`proxy.golang.org`) fetches the tag from GitHub. This repository does not publish binaries for the library.

`v0.1.2` (2026-10-23) is the latest tag. Earlier tags: `v0.1.1` at `7d514a7` and `v0.1.0` at `622cb34`. Each version uses the same `vX.Y.Z` steps. [CHANGELOG.md](../CHANGELOG.md) holds the notes: an empty `Unreleased` heading, then one `vX.Y.Z` section per release. The Python package is [github.com/trey-copeland/gotdms-python](https://github.com/trey-copeland/gotdms-python). It was extracted from this repository on 2026-10-19 and is not on PyPI. This checklist does not publish it.

`.github/workflows/build-and-test-go-module.yml` is on `main`. This checklist requires that workflow to be green for the commit you tag, including the `differential test against npTDMS` job. That job installs npTDMS (LGPL-2.1) with pip as a CI-only oracle. It is not a Go dependency and is not shipped. This document does not add a second copy of that workflow. `.github/workflows/test-and-publish-go-release.yml` is also on `main`. It runs only when a `v*` tag is pushed, re-checks this module, and opens a GitHub Release. It does not build binaries, it does not publish to PyPI, and it does not talk to the module proxy.

## Before the tag

1. Confirm `main` is the commit you mean. `git log -1` and the `vX.Y.Z` section in the changelog must describe that commit. Notes for the version you are about to tag belong in that section. `Unreleased` stays empty.
2. Confirm the latest run of `.github/workflows/build-and-test-go-module.yml` on that commit is green, including the `differential test against npTDMS` job.
3. From a clean checkout of that commit:

   ```bash
   gofmt -l .
   go vet ./...
   go test ./...
   go test -race .
   go test -race ./dataset/
   go test -run Example -count=1
   ```

   `gofmt -l` must print nothing. `go test ./...` covers this module, including `dataset/`. The Python package's tests run in [gotdms-python](https://github.com/trey-copeland/gotdms-python).

4. Confirm the core `go.mod` is still only:

   ```
   module github.com/trey-copeland/gotdms

   go 1.22
   ```

   There must be no `require` block. `go 1.22` is the minimum the module declares. CI also builds with Go 1.27.
5. Commit the changelog on `main` before you tag. The tag points at the commit that contains the notes.

## Tag and GitHub release

The tag is a semver tag with a leading `v`. Substitute the version you are releasing for `vX.Y.Z`.

```bash
git checkout main
git pull
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

Pushing the tag runs `.github/workflows/test-and-publish-go-release.yml` on that commit. The workflow runs `gofmt`, `go vet`, `go test ./...`, `go test -race .`, and `go test -race ./dataset/`, with the Go version in `go.mod`, then:

```bash
gh release create "$GITHUB_REF_NAME" --title "$GITHUB_REF_NAME" --generate-notes
```

Generated notes are commit subjects. After the workflow finishes, edit the GitHub release so the body leads with the `vX.Y.Z` section of `CHANGELOG.md`. Do not attach library binaries. Do not treat that GitHub release as a PyPI upload.

If that workflow fails, create the release by hand with the same `gh release create` command. The tests above still have to pass first.

## Prove the proxy

Wait until `https://proxy.golang.org/github.com/trey-copeland/gotdms/@v/vX.Y.Z.info` returns the tag. The first request can take a minute. Then, in an empty directory, outside this checkout:

```bash
mkdir /tmp/gotdms-mod && cd /tmp/gotdms-mod
go mod init example.com/consume
go get github.com/trey-copeland/gotdms@vX.Y.Z
```

`go get` must select `vX.Y.Z`. A short program that imports `github.com/trey-copeland/gotdms` and calls `tdms.Create` on a `bytes.Buffer` must compile. The dataset package is `github.com/trey-copeland/gotdms/dataset` in the same module. The Python package is not part of this module.

`v0.1.2` is the latest tag on the repository. The same proxy URL with that version in place of `vX.Y.Z` is the check for it; earlier tags are checked the same way. Each new tag gets the same check.

pkg.go.dev (`https://pkg.go.dev/github.com/trey-copeland/gotdms@vX.Y.Z`) should show the package comment from `doc.go` and `Example_roundTrip`. The site lags the proxy. The README at the repository root is the page it renders above the package index.

## License

`LICENSE` is the MIT text. The core module vendors nothing and has no third-party Go dependencies, so there is no third-party notices file for that module. Test vectors in `golden_test.go` quote hex from NI's public TDMS format article. That is format documentation, not copied source. Comments mention npTDMS only to describe on-disk behavior. No npTDMS source is included.

The Python package's third-party notices live in [gotdms-python](https://github.com/trey-copeland/gotdms-python). They are not dependencies of this module.

## What not to do

- Do not tag while `go test ./...` is failing.
- Do not publish the Python package from this checklist. It lives in [gotdms-python](https://github.com/trey-copeland/gotdms-python) and is not on PyPI.
- Do not upload the module anywhere except by pushing the tag. The proxy pulls from the tag.
