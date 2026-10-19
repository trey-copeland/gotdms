# npTDMS differential files

This directory is the CI oracle for files this module did not write itself.
`harness.py` writes TDMS files with [npTDMS](https://github.com/adamreeve/npTDMS)
and reads them with `roundtrip.go`, then the other way around.

There is no LabVIEW install in this environment, so npTDMS is the external
anchor. npTDMS is LGPL-2.1. It is installed with pip in the differential
CI job only. It is not imported by the Go module, not linked, and not
shipped. `go.mod` has no third-party requirements.

The NI five-segment worked example is hex in `golden_test.go`
(`TestGoldenNIFiveSegments`), copied from the NI article, not generated here.

Files are generated in CI rather than checked in. The layouts are the
common scalar types, a string channel, a timestamp, two segments, and two
channels. npTDMS 1.13 writes contiguous segments only, so the interleaved
file is written by gotdms and read by npTDMS. Extended-float bytes are not compared:
that layout is spec-derived and npTDMS does not write it.
