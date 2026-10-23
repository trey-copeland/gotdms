# npTDMS differential files

This directory is the CI oracle for files this module did not write itself.
`harness.py` writes TDMS files with [npTDMS](https://github.com/adamreeve/npTDMS)
and reads them with `roundtrip.go`, then the other way around. It runs only
in the `differential test against npTDMS` CI job; `go test ./...` does not
run it, and nothing outside that job needs npTDMS.

There is no LabVIEW install in this environment, so npTDMS is the external
anchor. npTDMS is LGPL-3.0-only. CI installs version 1.13.0 with pip in
the differential job only. It is not imported by the Go module, not
linked, and not shipped. `go.mod` has no third-party requirements.

The NI five-segment worked example is hex in `golden_test.go`
(`TestGoldenNIFiveSegments`), copied from the NI article, not generated here.

Files are generated in CI rather than checked in. The layouts are the
common scalar types, a string channel, a timestamp, two segments, and two
channels. npTDMS 1.13 writes contiguous segments only, so the interleaved
file is written by gotdms and read by npTDMS. Extended-float bytes are not compared:
that layout is spec-derived and npTDMS does not write it.

DAQmx files are also generated in that job, not checked in. In that CI
job only, `harness.py` imports npTDMS's own test helpers (among them
`daqmx_scaler_metadata`, `digital_scaler_metadata`,
`daqmx_channel_metadata`, `GeneratedFile`) at run time from the npTDMS
package the job installed (`nptdms.test.test_daqmx` and
`nptdms.test.util`). Their code is not copied into this repository. They
build the bytes; the harness then compares gotdms `Float64s` with npTDMS `raw_data`
(`read_data(scaled=False)`). One file carries a linear `NI_Scale`. Both
sides are checked against the raw samples. npTDMS does not apply that scale
through `channel[:]` for a DAQmx raw buffer, so the harness does not call
it. The helpers write index word `0x00001269` for a format-changing scaler
and `0x0000126A` for a digital line. The `0x00001369` file is that same
`0x00001269` file with the index word changed; gotdms is compared to the
raw values npTDMS read from the original. Four more files cover layouts
npTDMS reads: a DAQmx index typed with its scaler's type, standard indexes
under `kTocDAQmxRawData`, a buffer that widens in a later segment, and
segments whose raw-data offset points past the end of the object list. Five
files with `NI_Scale` chains compare `DAQmxScaledFloat64s` to npTDMS
`channel[:]`, including a chain whose input is a second DAQmx scaler. No
npTDMS decoder source is vendored or ported.
