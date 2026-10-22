package dataset

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A hand-built DAQmx segment: the first segment sets kTocDAQmxRawData, the
// channel is int16, and waveform timing is a zero timestamp plus wf_increment
// 1. The bytes are not produced by the TDMS writer.
func TestDAQmxWaveformRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hsai.tdms")
	if err := os.WriteFile(path, daqmxWaveFile(), 0o644); err != nil {
		t.Fatal(err)
	}
	ds := openWave(t, dir)
	s, err := ds.Read(Selector{Name: "ai0", Group: "acq"})
	if err != nil {
		t.Fatal(err)
	}
	epoch1904 := time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	if s.Source != TimeWaveform || s.Dt != 1 || !sameFloats(s.Y, []float64{1, 2, -1}) {
		t.Fatalf("source=%s dt=%v Y=%v", s.Source, s.Dt, s.Y)
	}
	if len(s.Times) != 3 || !s.Times[0].Equal(epoch1904) || !s.Times[1].Equal(epoch1904.Add(time.Second)) || !s.Times[2].Equal(epoch1904.Add(2*time.Second)) {
		t.Fatalf("times %v", s.Times)
	}
}

func daqmxWaveFile() []byte {
	var meta []byte
	meta = appendU32(meta, 1)
	path := "/'acq'/'ai0'"
	meta = appendU32(meta, uint32(len(path)))
	meta = append(meta, path...)
	meta = appendU32(meta, 0x00001269)
	meta = appendU32(meta, 0xFFFFFFFF)
	meta = appendU32(meta, 1)
	meta = appendU64(meta, 3)
	meta = appendU32(meta, 1)
	for _, v := range []uint32{3, 0, 0, 0, 0} {
		meta = appendU32(meta, v)
	}
	meta = appendU32(meta, 1)
	meta = appendU32(meta, 2)
	meta = appendU32(meta, 2)
	meta = appendProp(meta, "wf_start_time", 0x44, make([]byte, 16))
	var inc [8]byte
	binary.LittleEndian.PutUint64(inc[:], 0x3FF0000000000000) // 1.0
	meta = appendProp(meta, "wf_increment", 10, inc[:])
	raw := []byte{0x01, 0x00, 0x02, 0x00, 0xFF, 0xFF}
	return finishLE(uint32(1<<1|1<<2|1<<3|1<<7), meta, raw)
}

func appendProp(meta []byte, name string, dt uint32, val []byte) []byte {
	meta = appendU32(meta, uint32(len(name)))
	meta = append(meta, name...)
	meta = appendU32(meta, dt)
	return append(meta, val...)
}

func appendU32(b []byte, v uint32) []byte {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	return append(b, buf[:]...)
}

func appendU64(b []byte, v uint64) []byte {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	return append(b, buf[:]...)
}

func finishLE(toc uint32, meta, raw []byte) []byte {
	file := make([]byte, 28, 28+len(meta)+len(raw))
	copy(file[0:4], "TDSm")
	binary.LittleEndian.PutUint32(file[4:8], toc)
	binary.LittleEndian.PutUint32(file[8:12], 4713)
	binary.LittleEndian.PutUint64(file[12:20], uint64(len(meta)+len(raw)))
	binary.LittleEndian.PutUint64(file[20:28], uint64(len(meta)))
	file = append(file, meta...)
	return append(file, raw...)
}
