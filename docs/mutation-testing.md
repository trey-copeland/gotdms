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
  --exclude-files '^testdata/' \
  --workers 4 \
  --timeout-coefficient 4 \
  --threshold-efficacy 0 \
  --threshold-mcover 0 \
  --output gremlins-report.json \
  --output-statuses lct
```

`dataset/`, `cmd/`, and `examples/` are other packages. `testdata/` is the npTDMS harness (`package main`); `go test` never runs it, so every mutant there is `NOT COVERED`. The core job excludes it, the same way the dataset job does. `--timeout-coefficient 4` sets each mutant's test timeout to four times the coverage run, so a mutant that hangs is stopped and reported as `TIMED OUT`. `--output-statuses lct` prints lived, not-covered, and timed-out mutants. The JSON file has every mutant. `gremlins-report.json` is gitignored.

`--threshold-efficacy 0` and `--threshold-mcover 0` are the defaults. Gremlins then does not exit 10 or 11 for a low score. A crash, or a coverage run that cannot execute the tests, still exits non-zero.

Do not pass `--test-cpu`. Gremlins v0.6.0 hands it to the test binary as the single argument `-cpu N`, which the binary rejects (`flag provided but not defined: -cpu 1`). Every mutant then exits 1 and is reported `KILLED`, whatever it changed. Every figure this page published before the 32-bit build fix was measured that way, so their 100% efficacy and zero lived mutants did not reflect the tests. Those figures are withdrawn.

The checks that only bind on a 32-bit build compare against the package variable `maxInt` (`intlimit.go`), and `intlimit_test.go` lowers it so an amd64 run tests each one at its boundary. Their mutants are killed.

The first nightly without `--test-cpu` is run [41979231407](https://github.com/trey-copeland/gotdms/actions/runs/41979231407) on `d648108` (v0.3.0). The core job finished in 1h47m:

| | |
| --- | --- |
| Killed | 2024 |
| Lived | 620 |
| Not covered | 198 |
| Timed out | 17 |
| Test efficacy | 76.55% |
| Mutator coverage | 93.03% |

47 of the 198 not-covered rows are in `testdata/nptdms/roundtrip.go`. The core job now excludes `testdata/`, so the next run will not list them. The dataset job in that nightly, and the one on `edeecbc` the next night, logged a hosted-runner shutdown signal and did not finish. Neither produced a report artifact. Details are under [Dataset runner shutdown](#dataset-runner-shutdown).

### The dataset package

The second job mutates only `dataset/`. From the module root:

```bash
gremlins unleash \
  --exclude-files '^[^/]+\.go$' \
  --exclude-files '^cmd/' \
  --exclude-files '^examples/' \
  --exclude-files '^testdata/' \
  --workers 4 \
  --timeout-coefficient 4 \
  --threshold-efficacy 0 \
  --threshold-mcover 0 \
  --output gremlins-report-dataset.json \
  --output-statuses lct
```

`'^[^/]+\.go$'` excludes every file in the module root, which is the core package. Its earlier figures (550 killed, 0 lived) were measured with `--test-cpu 1` and are withdrawn for the reason above.

A local run of this command on 2026-11-05, before the worker-option tests, finished in 4m56s on 4 vCPUs (linux/amd64, Go 1.22): 395 killed, 145 lived, 13 not covered, 10 timed out, efficacy 73.15%, mutant coverage 97.65%.

The same command on `edeecbc` (those tests included) finished in 4m19s on the same kind of machine:

| | |
| --- | --- |
| Killed | 403 |
| Lived | 137 |
| Not covered | 13 |
| Timed out | 10 |
| Test efficacy | 74.63% |
| Mutator coverage | 97.65% |

The eight mutants that moved from lived to killed are the ones `dataset/workers_test.go` was written for: both edits of `WithWorkers` and `WithReadWorkers` (`n > 0`), and the negations of the `GOMAXPROCS` cap and of the two clamps in `OpenFiles`. The four boundary edits that assign the boundary value to itself (`n > 8`, `n < 1`, and both `workers < 1` clamps) still lived. Nothing else changed. The 2026-11-06 dataset log, which got past those lines before the runner stopped, shows the same split: those eight are absent, and the four equivalent boundaries are `LIVED`.

The workflow's dataset step also passes `k` to `--output-statuses`, and prints `nproc` and `MemAvailable` before Gremlins starts. The command above does not. `k` only adds killed mutants to the log.

### Dataset runner shutdown

The first two dataset jobs that ran without `--test-cpu` did not finish. Neither has a `gremlins-report-dataset` artifact.

| | [41979231407](https://github.com/trey-copeland/gotdms/actions/runs/41979231407) | [42173407387](https://github.com/trey-copeland/gotdms/actions/runs/42173407387) |
| --- | --- | --- |
| Commit | `d648108` | `edeecbc` |
| Dataset job | [127085201122](https://github.com/trey-copeland/gotdms/actions/runs/41979231407/job/127085201122) | [127746242634](https://github.com/trey-copeland/gotdms/actions/runs/42173407387/job/127746242634) |
| Unleash step | 06:16:00Z to 06:18:22Z (2m22s) | 06:16:53Z to 06:21:51Z (4m58s) |
| Job timeout | 30 minutes, not reached | 30 minutes, not reached |
| Runner | GitHub Actions 1000000828, westus, image `ubuntu-24.04` `20261025.353` | GitHub Actions 1000000848, westus3, image `ubuntu-24.04` `20261101.360` |
| Core job on the other runner | success, about 107 minutes | success, 69m13s (2062 killed, 586 lived, 147 not covered, 17 timed out) |
| Last dataset progress | still in `dataset.go` | into `read.go`, mutants still printing |
| Upload step | skipped | skipped |

Both logs then say Gremlins is shutting down gracefully, the hosted runner has received a shutdown signal, and Go 1.25.4 panics in `os/signal` (`send on closed channel`, `signal.go:246`). That panic is part of teardown. It is not a mutant result. Gremlins writes `gremlins-report-dataset.json` only from `report.Do`, after a clean finish. The upload step, which has `if: always()`, was skipped both times, so there is no artifact.

The logs of those scheduled jobs show that the hosted runner received a shutdown signal. They do not by themselves establish why. The configured 30-minute timeout had not been reached, and mutants were still being printed. No out-of-memory line appears in those logs.

A hosted run with a memory sampler on the same Gremlins command ([42623494302](https://github.com/trey-copeland/gotdms/actions/runs/42623494302)) did finish, and the sampler shows why the scheduled jobs are fragile. From 13:14:20Z to 13:20:26Z one `dataset.test` held about 14–15 GiB RSS (peak 15138320 KiB at 13:14:33Z, cgroup `memory.current` 15994724352, `MemAvailable` 321036 KiB). `memory.max` was `max` and `oom_kill` stayed 0, so that run survived with a few hundred megabytes left. The process is the per-mutant timeout of `run`'s worker loop in `dataset.go`: `for w := 0; w < workers; w++` mutated to `w--` starts a goroutine every iteration and does not stop. Gremlins reported that mutant, and the `w < workers` negation, as `TIMED OUT` (13:15:01Z and 13:15:02Z) while that `dataset.test` was already at 14 GiB. The scheduled jobs stop in that same early part of `dataset.go`, about two minutes into the step. `run` now starts goroutines with `for range workers` and sends indexes with `for range n`, which that mutator cannot turn into an open-ended spawn.

## How to read the report

Gremlins only executes a mutant whose line is already covered. The per-mutant timeout is a multiple of the coverage run, so run `go clean -testcache` first locally: a cached coverage run takes milliseconds, and every mutant then times out.

- `KILLED` means a test failed on the mutated code. The suite noticed.
- `LIVED` is a survivor. The tests ran and passed. That is the line to triage.
- `NOT COVERED` means no test executes that expression, so Gremlins does not run it. It lowers mutant coverage. It is not a survivor, and it does not lower efficacy.
- `TIMED OUT` means the mutant exceeded the per-mutant timeout. On this package that is about four times the coverage run. It usually means the mutant hung, or the machine was slower than the coverage measurement. It is not a `LIVED` result.
- `NOT VIABLE` means the mutant did not compile. Gremlins leaves those out of the efficacy math.

A survivor is only a `LIVED` row. In `gremlins-report.json` that is `"status": "LIVED"`. The log line looks like:

```text
       LIVED CONDITIONALS_NEGATION at reader.go:12:3
```

Triage each one:

1. A real gap: add the smallest test that fails on that change, next to the tests that already cover that behavior.
2. An equivalent mutant: no test can kill it, because the mutated program does the same thing. Leave the code, and write down the one-line reason.
3. Dead or wrong code: change the library only when the change is clearly a bug, and add a regression test.

Stop when what remains is equivalent, or the next test would only pin a change nothing can observe. Do not chase a mutant-coverage number.

## Triage of run 41979231407

No production bug turned up. The survivors are missing assertions, equivalent edits, or branches a test cannot reach. `CONDITIONALS_BOUNDARY` is 322 of the 620 core rows. Many of those in `reader.go`, `fastread.go`, and `segindex.go` are off-by-ones, but not because the suite forgot an easy edge. Checked one by one:

- `CursorAt`'s `i < 0`, `n < 0`, and `i > total` were real gaps. `i == 0`, `n == 0`, and `i == total` with an empty tail never went through `CursorAt`. `edges_test.go` covers them, and the one-past range that distinguishes `total-i` from `total+i`.
- `cur.pos+n > lim` is not a gap. When the two sides are equal the next statement sets `n` to `lim-pos`, which is already `n`.
- `addSigned` (`timestamp.go`) has four boundary survivors. Equality means the addend was 0, and the other half of the overflow test is then false. No input tells them apart.
- `nsToFraction`'s `r >= 500_000_000` never sees a remainder of exactly 500_000_000 for any nanosecond in `0..999_999_999`. The boundary edit is equivalent. `q++` is not: `Time()` still matches a fraction two units away, and the documented value is the nearest `2^-64` second. The test pins one nanosecond.
- `seqReader`'s cache window (`off >= base` and the end `<=`) does change bytes when the underlying `ReaderAt` would return something else on a second read. That is tested. `n >= len(p)` does not matter to callers: `readFull` ignores the error once `n` fills the slice, and `io.ReaderAt` allows either `nil` or `EOF` in that case.
- `fitsSpan` and `wordAligned` failing closed take the byte copy. The samples match. A test would only be asserting which path ran.
- Chunk min/max updating on `x < min` does not change the stored value when `x == min`, except signed zero: `-0 <= +0` is true, so the boundary edit would store `-0`. Same caveat as `applyInPlace`. Not pinned.

`INVERT_NEGATIVES` and `ARITHMETIC_BASE` both replace the `-` token with `+`. All 31 core `INVERT_NEGATIVES` rows are that same edit at the same column as an arithmetic row. They are one behavior counted twice.

| Category | Rows | What happened |
| --- | --- | --- |
| Killed by tests in this change | 30 | `edges_test.go`, the multiplexed-string error text, and `dataset` is separate below |
| Duplicate `-` to `+` left in place | 26 | The same edit as an `ARITHMETIC_BASE` row this change did not kill. 5 of the 31 duplicates are in the 30 above |
| Equivalent | 9 | `addSigned` (4), the impossible half-nanosecond remainder (1), min/max except signed zero (2), the cursor batch shrink (1), `seqReader`'s full-slice error (1) |
| Left | 555 | Integer extremes, I/O failures, or a second check / slow path that still returns the same result |

79 of the 555 sit on a line that compares against an integer extreme (`math.MaxInt`, `MaxUint32`, `maxInt`, and the same family). `intlimit_test.go` already kills the checks that go through the `maxInt` variable. The survivors use the constant, so the equal value is not reachable from an amd64 test without another harness. A few of those lines also have a `<= 0` guard beside the extreme; that guard fails closed into the same error or the slow path.

24 of the 555 negate `err == nil` or `err != nil` on rename, stat, write, or a read that a later check still fails. Those two groups overlap (a line can mention both an extreme and an error), so they are not a partition of the 555.

Files the new tests touch: `reader.go` (`CursorAt`, `Close`, empty `Slice`, a string `Slice` one past the end, `DAQmxSlice` via `readTyped`), `fastread.go` (the sequential buffer), `stream.go` (one past the values still declared), `writer.go` (int32 and uint property bounds, and the multiplexed-string error), `value.go` (encoding `int32` / `uint32` at the type's own limits), `errors.go` (the `Error` text), `timestamp.go`.

Deliberately left, with the reason:

- `readRangeFrom`'s `total-start` subtraction on a numeric `Slice`. `readTyped` repeats that check, so a bad numeric range still fails. String channels, and channels with no data type, return from `readRangeFrom` before `readTyped`. `Slice(1, 2)` on a 2-value string channel now requires `ErrArgument`, which is the case `total+start` would accept.
- Reader `normalizeLayout` on one string channel. Returning interleaved or not builds the same column offsets. Those flags are still unasserted.
- Writer `checkInterleaved` in `writer.go`. Several string channels also fail later in `rawByteSize` with `ErrLayout`. The test requires the text "cannot be multiplexed", which is `checkInterleaved`'s error, not `normalizeLayout`.
- `applyInPlace` skipping `y = y*1 + 0`. The values match, including every test, except that a negative zero becomes `+0` if the loop runs. Signed zero is not part of the waveform contract.
- Deinterleave word loads, `contiguousBytes`, and `directRead`'s size cutoff. Each one selects a faster path. The byte path and the buffered path return the same values.
- Segment-index `count`'s element size (`4+4+4+4+4+1+1` and the division). A smaller minimum still fails when the following field reads run out of bytes, and `ns > 4096` caps the allocation.

The dataset run's 145 lived rows are the same kinds of edit. Eight are killed by `dataset/workers_test.go`: `WithWorkers` / `WithReadWorkers` of `0` and of a positive count, the `GOMAXPROCS` cap, and the clamp inside `OpenFiles`. Four siblings are equivalent: `n > 8` versus `n >= 8` assigns 8 either way, `n < 1` versus `n <= 1` assigns 1 either way, and both `workers < 1` clamps do the same at 1.

Still open on `dataset`, not in this change:

- `clipFloat` / `clipTime` (`read.go`) when the window ends exactly on `len(src)`, and the alias return for a full unshared slice. Windows in the suite do not separate those from the copy path.
- Catalog sort (`dataset.go` around the occurrence order). Inside `File != File` the `<` / `<=` edit cannot see equal keys. Negating `<` reverses the order, and no test asserts catalog order separate from time order.
- Resample interpolation (`read.go`, the `Y[i-1] + frac*(Y[i]-Y[i-1])` terms) and `spanNumeric`'s `head[1] - head[0]`. The times the suite reads back still match.
- `run`'s worker clamp. Forcing one goroutine still calls every index. A test would be counting goroutines.

The nightly job uploads `gremlins-report.json` as the `gremlins-report` artifact, including when the step fails. The core job limit is 180 minutes. The run above took 1h47m on a GitHub-hosted runner. If a later run starts printing `TIMED OUT` for mutants that are not hangs, raise `--timeout-coefficient` rather than treating those rows as gaps. If the root package grows past the job limit, narrow `--exclude-files` or split the run by file and say which files ran.
