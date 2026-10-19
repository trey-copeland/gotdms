# Mutation testing

[Gremlins](https://github.com/go-gremlins/gremlins) v0.6.0 (Apache-2.0) mutation-tests the core package, which is the module root. It is a dev and CI tool. It is not a `go.mod` dependency. The module stays `go 1.22` with no `require` block.

This is a periodic quality check. [`.github/workflows/run-mutation-testing.yml`](../.github/workflows/run-mutation-testing.yml) runs at 06:00 UTC and when someone starts it by hand (`workflow_dispatch`). It does not run on pull requests. [Build and test Go module](../.github/workflows/build-and-test-go-module.yml) is the pull-request gate.

## Install

The install paths are the ones in the [Gremlins v0.6 install docs](https://gremlins.dev/0.6/install/). The license text is the Apache License 2.0 at the root of `github.com/go-gremlins/gremlins`.

With Go 1.25 or newer (Gremlins v0.6.0's `go.mod` says `go 1.25`):

```bash
go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0
```

That command does not edit this module's `go.mod`. Pin `v0.6.0`. Do not install `@latest`: Gremlins is still 0.x, and flags can change between minor releases.

On Linux the same release is the tarball `gremlins_0.6.0_linux_amd64.tar.gz` from the [v0.6.0 release](https://github.com/go-gremlins/gremlins/releases/tag/v0.6.0). The nightly workflow uses that tarball and checks its sha256, then runs the tests with the Go version in `go.mod`.

## Run it locally

From the module root:

```bash
gremlins unleash \
  --exclude-files '^dataset/' \
  --exclude-files '^cmd/' \
  --exclude-files '^examples/' \
  --workers 4 \
  --test-cpu 1 \
  --timeout-coefficient 4 \
  --threshold-efficacy 0 \
  --threshold-mcover 0 \
  --output gremlins-report.json \
  --output-statuses lct
```

`dataset/`, `cmd/`, and `examples/` are other packages. The root package is the whole run. `--timeout-coefficient 4` sets each mutant's test timeout to four times the coverage run, so a mutant that hangs is stopped and reported as `TIMED OUT`. `--output-statuses lct` prints lived, not-covered, and timed-out mutants. The JSON file has every mutant. `gremlins-report.json` is gitignored.

`--threshold-efficacy 0` and `--threshold-mcover 0` are the defaults. Gremlins then does not exit 10 or 11 for a low score. A crash, or a coverage run that cannot execute the tests, still exits non-zero.

On a 4-vCPU cloud VM (linux/amd64, Go 1.22.2) on 2026-10-19 that command finished in 17 minutes 5 seconds:

| | |
| --- | --- |
| Killed | 1766 |
| Lived | 0 |
| Not covered | 153 |
| Timed out | 0 |
| Not viable | 0 |
| Test efficacy | 100.00% |
| Mutant coverage | 92.03% |

Efficacy is killed / (killed + lived). Nothing lived, so no tests were added for this run. The full root package was fast enough to run as a whole; it was not split per file.

## How to read the report

Gremlins only executes a mutant whose line is already covered.

- `KILLED` means a test failed on the mutated code. The suite noticed.
- `LIVED` is a survivor. The tests ran and passed. That is the line to triage.
- `NOT COVERED` means no test executes that expression, so Gremlins does not run it. It lowers mutant coverage. It is not a survivor, and it does not lower efficacy.
- `TIMED OUT` means the mutant exceeded the per-mutant timeout. On this package that is about four times the coverage run (just under 10 seconds when coverage takes about 2 seconds). It usually means the mutant hung, or the machine was slower than the coverage measurement. It is not a `LIVED` result. The 2026-10-19 run had none.
- `NOT VIABLE` means the mutant did not compile. Gremlins leaves those out of the efficacy math.

A survivor is only a `LIVED` row. In `gremlins-report.json` that is `"status": "LIVED"`. The log line looks like:

```text
       LIVED CONDITIONALS_NEGATION at reader.go:12:3
```

Triage each one:

1. A real gap: add the smallest test that fails on that change, next to the tests that already cover that behavior.
2. An equivalent mutant: no test can kill it, because the mutated program does the same thing. Leave the code, and write down the one-line reason.
3. Dead or wrong code: change the library only when the change is clearly a bug, and add a regression test.

Stop when what remains is equivalent, or the next test would only pin a change nothing can observe. Do not chase a mutant-coverage number. The not-covered set on the 2026-10-19 run is mostly the big-endian scalar stores in `fastwrite.go` (skipped when the host is little-endian), arithmetic inside error strings, and defensive branches the suite does not enter.

The nightly job uploads `gremlins-report.json` as the `gremlins-report` artifact, including when the step fails. The job limit is 45 minutes. The measured run was 17 minutes with four workers, so a night on a 4-vCPU runner is practical. If a later run starts printing `TIMED OUT` for mutants that are not hangs, raise `--timeout-coefficient` rather than treating those rows as gaps. If the root package grows past the job limit, narrow `--exclude-files` or split the run by file and say which files ran.
