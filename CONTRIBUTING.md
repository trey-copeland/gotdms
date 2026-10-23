# Contributing

Collaborators push a branch to this repository (no fork needed) and open a pull request against `main`. `main` is protected by a ruleset:

- Changes reach `main` through a pull request.
- A pull request needs one approving review, and the most recent push must be approved. A new push dismisses earlier approvals.
- Force-pushes to `main` and deleting `main` are blocked.
- Repository admins can bypass the ruleset.

These checks must pass before a pull request can merge:

- `test (ubuntu, go 1.22)`
- `test (ubuntu, go 1.27)`
- `test (windows, go 1.22)`
- `differential test against npTDMS`

All four are jobs of [`.github/workflows/build-and-test-go-module.yml`](.github/workflows/build-and-test-go-module.yml). The module declares `go 1.22`; CI also builds with Go 1.27. To run the test jobs' checks locally from the repository root:

```bash
gofmt -l .
go vet ./...
go test ./...
go test -race .
go test -race ./dataset/
```

`gofmt -l` must print nothing. The differential job installs npTDMS in CI as a test-only oracle; it is not a dependency of this module. See [docs/testing.md](docs/testing.md#nptdms-differential-tests-ci-only).

Only repository admins can create, update, or delete `v*` tags. The tag checklist is [docs/releasing.md](docs/releasing.md).
