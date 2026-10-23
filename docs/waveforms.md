# Waveforms

[Back to the docs index](README.md)

LabVIEW does not store an analog waveform as its own TDMS type. A waveform is a channel's Y samples plus `wf_start_time`, `wf_increment` (float64 seconds), `wf_samples`, and `unit_string`. `Waveform[T]` is that bundle. `T` is `float64`, `float32`, or an integer width (`int8` through `uint64`). Bool, string, timestamp, extended, and complex channels are not waveforms.

`Start` is a `Timestamp`, not a `time.Time`. That keeps the full 2^-64 second fraction across a round trip. Call `Start.Time` for UTC at nanosecond resolution. `Increment` is the stored `wf_increment`. `Dt` rounds it to the nearest `time.Duration` and is only a convenience.

A missing `wf_start_time` or `wf_increment` is not an error. The field is the zero value and `HasStart` or `HasIncrement` is false. A stored zero is a real value: `HasStart` true with a zero `Timestamp` is the TDMS epoch (1904-01-01 UTC), and `HasIncrement` true with increment 0 means every sample is at t0. `TimeAt(i)` computes t0 + i*dt in one step, not by adding dt over and over. `Times` returns one `time.Time` per sample.

`ReadWaveform[T]` and `Channel.Waveform` (float64 only) load every sample, same as `Values`. They do not apply a linear scale. `ApplyScale` and `ScaledWaveform` do, and they return `Waveform[float64]`. `WriteWaveform` writes Y unchanged — it does not invert a scale — and sets `wf_start_time`, `wf_increment`, `wf_samples` (`len(Y)`), and, when the unit is non-empty, `unit_string`, in that order. An empty unit does not clear a `unit_string` already set on the channel.

`ReadWaveform` is the wrong call for a multi-gigabyte channel. Take `WaveformHeader` (no samples) and walk the channel with `Cursor`. `WaveformHeader.TimeAt` takes the channel's absolute sample index, so a chunk that starts at sample 4096 is `header.TimeAt(4096 + i)`.

```go
start := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
err = ch.WriteWaveform(tdms.Waveform[float64]{
    Start:     tdms.NewTimestamp(start),
    Increment: 0.001,
    Y:         []float64{0.1, 0.2, 0.3},
    Unit:      "V",
})
// ...
wf, err := tdms.ReadWaveform[float64](c)
t1, _ := wf.TimeAt(1) // 2024-06-01T12:00:00.001Z
```

`ExampleReadWaveform` is the compile-checked copy.
