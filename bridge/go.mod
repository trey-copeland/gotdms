module github.com/trey-copeland/gotdms-python

go 1.22.0

toolchain go1.22.2

require github.com/trey-copeland/gotdms v0.0.0

require github.com/apache/arrow-go/v18 v18.0.0

require (
	github.com/goccy/go-json v0.10.3 // indirect
	github.com/google/flatbuffers v24.3.25+incompatible // indirect
	github.com/klauspost/compress v1.17.11 // indirect
	github.com/klauspost/cpuid/v2 v2.2.8 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
	golang.org/x/exp v0.0.0-20240909161429-701f63a606c0 // indirect
	golang.org/x/mod v0.21.0 // indirect
	golang.org/x/sync v0.8.0 // indirect
	golang.org/x/sys v0.26.0 // indirect
	golang.org/x/tools v0.26.0 // indirect
	golang.org/x/xerrors v0.0.0-20231012003039-104605ab7028 // indirect
)

// Module path is the future gotdms-python repository, not a subdirectory
// of the core module. In this checkout the parent directory is still the
// core module, so the replace below is what `go test` uses.
// After extraction, delete this replace and require a published core tag.
// main already contains the dataset API. No tag has been published yet.
// See EXTRACTION.md.
replace github.com/trey-copeland/gotdms => ../
