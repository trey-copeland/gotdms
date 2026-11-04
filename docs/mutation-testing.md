# Mutation testing

[Gremlins](https://github.com/go-gremlins/gremlins) v0.6.0 (Apache-2.0) mutation-tests the core package, which is the module root, and the `dataset` package. It is a dev and CI tool. It is not a `go.mod` dependency. The module stays `go 1.22` with no `require` block.

This is a periodic quality check. [`.github/workflows/run-mutation-testing.yml`](../.github/workflows/run-mutation-testing.yml) runs at 06:00 UTC and when someone starts it by hand (`workflow_dispatch`). It has two parallel jobs, `gremlins (core package)` and `gremlins (dataset package)`, each with its own report artifact (`gremlins-report` and `gremlins-report-dataset`). It does not run on pull requests. [Build and test Go module](../.github/workflows/build-and-test-go-module.yml) is the pull-request gate: its jobs are the checks the `main` ruleset requires (see [CONTRIBUTING.md](../CONTRIBUTING.md)).

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

`dataset/`, `cmd/`, and `examples/` are other packages. The run covers the root package, and Gremlins also lists mutants in `testdata/nptdms/roundtrip.go`, the differential harness's `package main`: the command does not exclude `testdata/`, and `go test` never runs that file, so every one of its mutants is `NOT COVERED`. `--timeout-coefficient 4` sets each mutant's test timeout to four times the coverage run, so a mutant that hangs is stopped and reported as `TIMED OUT`. `--output-statuses lct` prints lived, not-covered, and timed-out mutants. The JSON file has every mutant. `gremlins-report.json` is gitignored.

`--threshold-efficacy 0` and `--threshold-mcover 0` are the defaults. Gremlins then does not exit 10 or 11 for a low score. A crash, or a coverage run that cannot execute the tests, still exits non-zero.

On an 8 vCPU cloud VM (linux/amd64, Go 1.27.3) on 2026-11-03, with the defragmentation change (#31) and the other open review fixes (#32 to #35) applied to `main` (`e6cd0e9`), that command finished in 26 minutes 24 seconds (one run; Gremlins v0.6.0):

| | |
| --- | --- |
| Killed | 2617 |
| Lived | 0 |
| Not covered | 187 |
| Timed out | 0 |
| Not viable | 0 |
| Test efficacy | 100.00% |
| Mutant coverage | 93.33% |

Efficacy is killed / (killed + lived). Nothing lived. The run has 131 more killed mutants than the `v0.2.0` run (`290bb79`, 2026-10-23: 2486 killed, 0 lived, 191 not covered, 8 timed out, 29 minutes 11 seconds), from `defrag.go` and `stream.go` and the `segindex.go` and `writer.go` fixes. It finished sooner than the `v0.2.0` run on a VM of the same size; wall time varies between VMs, so compare the counts, not the times. None of the eight mutants that timed out at `v0.2.0` timed out here. The full root package was fast enough to run as a whole; it was not split per file.

### The dataset package

The second job mutates only `dataset/`. From the module root:

```bash
gremlins unleash \
  --exclude-files '^[^/]+\.go$' \
  --exclude-files '^cmd/' \
  --exclude-files '^examples/' \
  --exclude-files '^testdata/' \
  --workers 4 \
  --test-cpu 1 \
  --timeout-coefficient 4 \
  --threshold-efficacy 0 \
  --threshold-mcover 0 \
  --output gremlins-report-dataset.json \
  --output-statuses lct
```

`'^[^/]+\.go$'` excludes every file in the module root, which is the core package. On the same 8 vCPU VM on 2026-11-03, with the descending-merge fix (PR #34) applied to `e6cd0e9`, the run took 31 seconds (one run, so treat it as a size, not a benchmark):

| | |
| --- | --- |
| Killed | 550 |
| Lived | 0 |
| Not covered | 13 |
| Timed out | 0 |
| Not viable | 0 |
| Test efficacy | 100.00% |
| Mutant coverage | 97.69% |

The 13 not-covered mutants are error paths and edge guards in `dataset.go` and `read.go` that the tests do not reach: a timestamp read error, span overflow, a timestamp that does not fit in `time.Time`, a clamp on a short slice, a cursor error, a step rounding branch, and a zero-length resample span. None is in the descending-merge code.

## How to read the report

Gremlins only executes a mutant whose line is already covered.

- `KILLED` means a test failed on the mutated code. The suite noticed.
- `LIVED` is a survivor. The tests ran and passed. That is the line to triage.
- `NOT COVERED` means no test executes that expression, so Gremlins does not run it. It lowers mutant coverage. It is not a survivor, and it does not lower efficacy.
- `TIMED OUT` means the mutant exceeded the per-mutant timeout. On this package that is about four times the coverage run (about 20 seconds in the run above, whose coverage run took 5.1 seconds). It usually means the mutant hung, or the machine was slower than the coverage measurement. It is not a `LIVED` result. The run above had none; the `v0.2.0` run had eight, in `tail.go` (four), `scale.go` (three), and `reader.go` (one), which were not triaged.
- `NOT VIABLE` means the mutant did not compile. Gremlins leaves those out of the efficacy math.

A survivor is only a `LIVED` row. In `gremlins-report.json` that is `"status": "LIVED"`. The log line looks like:

```text
       LIVED CONDITIONALS_NEGATION at reader.go:12:3
```

Triage each one:

1. A real gap: add the smallest test that fails on that change, next to the tests that already cover that behavior.
2. An equivalent mutant: no test can kill it, because the mutated program does the same thing. Leave the code, and write down the one-line reason.
3. Dead or wrong code: change the library only when the change is clearly a bug, and add a regression test.

Stop when what remains is equivalent, or the next test would only pin a change nothing can observe. Do not chase a mutant-coverage number. Of the 187 not-covered mutants in the run above, 47 are in `testdata/nptdms/roundtrip.go` (see above). The other 140 are spread over `value.go` (23), `waveform.go` (22), `reader.go` (19), `timestamp.go` (14), `fastwrite.go` (14), `multiread.go` (10), and a few other files: mostly per-type scalar decode and encode loops that the bulk path bypasses on a little-endian host (including the big-endian stores in `fastwrite.go`), range checks in property conversion, and defensive branches the suite does not enter.

The nightly job uploads `gremlins-report.json` as the `gremlins-report` artifact, including when the step fails. The job limit is 90 minutes (`timeout-minutes: 90`). The measured run above took 26 minutes with four workers on an 8 vCPU VM; a GitHub-hosted runner may be slower, so the limit leaves room. If a later run starts printing `TIMED OUT` for mutants that are not hangs, raise `--timeout-coefficient` rather than treating those rows as gaps. If the root package grows past the job limit, narrow `--exclude-files` or split the run by file and say which files ran.
