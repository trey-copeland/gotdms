# Reading

[Back to the docs index](README.md)

```go
f, err := tdms.Open("run.tdms")
if err != nil {
	return err
}
defer f.Close()

g, ok := f.Group("acquisition")
if !ok {
	return fmt.Errorf("group missing")
}
ch, ok := g.Channel("temperature")
if !ok {
	return fmt.Errorf("channel missing")
}
values, err := ch.Float64s()
if err != nil {
	return err
}
fmt.Println("values:", values)

// Stream without holding the whole channel.
cur := ch.Cursor(4)
for cur.Next() {
	fmt.Println("batch:", cur.Values().([]float64))
}
return cur.Err()
```

This is the read half of `ExampleCreate` in [example_docs_test.go](../example_docs_test.go), so `go test` compiles and runs it.

`Open` and `NewReader` parse every segment's metadata and do not load raw samples. `WithSegmentIndex` can reuse a sidecar of that layout on a later open; see [Segment index](segment-index.md). `NewReader` accepts an `io.ReaderAt` that can report its length (`*os.File`, `*bytes.Reader`, `*io.SectionReader`, or any `io.Seeker`). `Values` and the typed helpers read through `ReadAt`, so concurrent reads are safe when the underlying reader is. `Cursor` scans sequentially through a 256 KiB buffer (`WithReadBuffer`).

`Values` returns a freshly allocated slice in the channel's native Go type (`[]int32`, `[]float64`, `[]string`, `[]Timestamp`, `[]bool`, …). `Float64s` converts numeric channels. `Times` converts timestamp channels to UTC `time.Time` at nanosecond resolution. `Timestamps` keeps the full 2^-64 fraction.

Waveform properties are ordinary properties plus helpers: `Unit` (`unit_string`, then `NI_UnitDescription`), `Increment` (`wf_increment`), `StartTime` (`wf_start_time`), `SampleCount` (`wf_samples`). `LinearScale` reads the first `NI_Scale[0]`…`NI_Scale[8]` linear scale. NI's own example uses `NI_Scale[1]_…`. Nothing is applied automatically; call `Apply`, `ApplyScale`, or `ScaledWaveform` when engineering units are wanted. See [Waveforms](waveforms.md).

Runnable copies of the snippets live in `examples/write`, `examples/read`, `Example_roundTrip`, and `ExampleReadWaveform` (checked by `go test`). `ExampleCreate` is the write-then-read example in the README, and `ExampleWithLenientTail` opens a torn file.

Channel order, untyped channels, and the full API table are in [API](api.md). File status, torn tails, and `Follow` are in [File status, torn tails, and Follow](file-status.md).

## Chunk statistics

`Channel.ChunkStats(samplesPerChunk)` walks a numeric channel once and returns count, min, max, mean, and RMS for each chunk. The last chunk is shorter when the length is not a multiple of the size. The walk uses `Cursor` and does not retain earlier samples. Mean is the arithmetic mean. RMS is the square root of the mean of the squared samples. Values are accumulated as float64, with the same integer and extended-float conversion as `Float64s`. A NaN sample makes min, max, mean, and RMS NaN.

String, boolean, timestamp, and complex channels return `ErrType`. The check is the channel's `DataType`. A boolean is not coerced to 0 and 1 here, and a channel is not numeric because of its name.
