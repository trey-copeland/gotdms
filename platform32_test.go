package tdms

// Regression tests for 32-bit platforms (GOARCH=386, GOARCH=arm). Untrusted
// uint32 and int64 fields must be range-checked before they become an int, so
// a 32-bit build returns an error instead of wrapping or panicking. CI runs
// the whole suite with GOARCH=386; on 64-bit platforms these tests check that
// the same inputs still behave as before.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/bits"
	"testing"
)

// zeroTail is an io.ReaderAt of head followed by zeros up to size. It lets a
// test describe a multi-GiB file without allocating it.
type zeroTail struct {
	head []byte
	size int64
}

func (z zeroTail) Size() int64 { return z.size }

func (z zeroTail) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if off >= z.size {
		return 0, io.EOF
	}
	n := len(p)
	if left := z.size - off; int64(n) > left {
		n = int(left)
	}
	done := 0
	if off < int64(len(z.head)) {
		done = copy(p[:n], z.head[off:])
	}
	clear(p[done:n])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// A uint32 end offset with the high bit set is past any payload. On a 32-bit
// platform int(end) is negative, which used to pass the bound and panic.
func TestDecodeStringsHighBitOffset(t *testing.T) {
	block := binary.LittleEndian.AppendUint32(nil, 0x80000000)
	block = append(block, "abc"...)
	if _, err := decodeStrings(block, 1, binary.LittleEndian); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

// A DAQmx raw-buffer index with the high bit set is out of range. On a 32-bit
// platform int(buffer) is negative, which used to pass the bound and panic.
func TestDAQmxBufferIndexHighBit(t *testing.T) {
	obj := chDAQmx("/'g'/'c'", 2, 0, []uint32{2}, []dmScaler{{typ: 3, buf: 0x80000000}})
	file := buildDAQmx(daqTOC, []dmObj{obj}, i16Raw(7, 8))
	if _, err := NewReader(bytes.NewReader(file)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

// A channel with more values than int is refused at open on 32-bit
// platforms. Len, Values, and Slice index with int and would wrap.
func TestChannelLongerThanInt(t *testing.T) {
	const per = 1 << 30
	head := fixedIndexFile(t, TypeInt8, per, true, nil)
	r := zeroTail{head: head, size: int64(len(head)) + 3*per}
	f, err := NewReader(r)
	if bits.UintSize == 32 {
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("32-bit open: %v, want ErrUnsupported", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	if int64(c.Len()) != 3*per {
		t.Fatalf("len %d", c.Len())
	}
}

// A segment-index sidecar must not admit what a parse refuses.
func TestSegmentIndexChannelLongerThanInt(t *testing.T) {
	const per = 1 << 30
	sp := span{dataPos: 0, chunkSize: per, nChunks: 3, nPerChunk: per, stride: 1, typeSize: 1}
	err := spanListInFile([]span{sp}, nil, 3*per)
	if bits.UintSize == 32 {
		if err == nil {
			t.Fatal("32-bit sidecar admitted a channel longer than int")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
}

// A read whose byte size exceeds int is refused instead of wrapping n*size.
// The values fit in int on 32-bit platforms; their bytes do not.
func TestReadBytesLongerThanInt(t *testing.T) {
	if bits.UintSize != 32 {
		t.Skip("the read would allocate 2 GiB on a 64-bit platform")
	}
	const per = 1 << 28
	head := fixedIndexFile(t, TypeFloat64, per, true, nil)
	r := zeroTail{head: head, size: int64(len(head)) + 8*per}
	f, err := NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	if c.Len() != per {
		t.Fatalf("len %d", c.Len())
	}
	if _, err := c.Values(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Values: %v, want ErrUnsupported", err)
	}
	if _, err := c.Slice(0, per); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Slice: %v, want ErrUnsupported", err)
	}
	if _, err := ReadValues([]*Channel{c}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ReadValues: %v, want ErrUnsupported", err)
	}
	if _, err := c.Float64s(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Float64s: %v, want ErrUnsupported", err)
	}
	if _, err := ScaledWaveform[float64](c); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ScaledWaveform: %v, want ErrUnsupported", err)
	}
	// A slice that fits is still served.
	v, err := c.Slice(per-2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.([]float64); len(got) != 2 || got[0] != 0 || got[1] != 0 {
		t.Fatalf("tail slice %v", got)
	}
}

// makeValues reports an allocation past math.MaxInt bytes instead of letting
// make panic. On 64-bit this is n > MaxInt/8; on 32-bit it binds at 2^28.
func TestMakeValuesBounded(t *testing.T) {
	if _, err := makeValues(TypeFloat64, math.MaxInt/4); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if _, err := makeValues(TypeTimestamp, -1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("negative: %v, want ErrUnsupported", err)
	}
	v, err := makeValues(TypeString, 3)
	if err != nil || len(v.([]string)) != 3 {
		t.Fatalf("small: %v %v", v, err)
	}
}

// Widening a channel to float64 or int64 is refused when the result would
// pass math.MaxInt bytes, even though the raw values fit.
func TestWideningLongerThanInt(t *testing.T) {
	if bits.UintSize != 32 {
		t.Skip("the conversion would allocate 2 GiB on a 64-bit platform")
	}
	const per = 1 << 28
	head := fixedIndexFile(t, TypeInt8, per, true, nil)
	f, err := NewReader(zeroTail{head: head, size: int64(len(head)) + per})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	if _, err := c.Float64s(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Float64s: %v, want ErrUnsupported", err)
	}
	if _, err := c.Int64s(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Int64s: %v, want ErrUnsupported", err)
	}
	// The raw int8 values are 256 MiB and still read.
	if v, err := c.Slice(per-3, 3); err != nil || len(v.([]int8)) != 3 {
		t.Fatalf("tail slice: %v %v", v, err)
	}
}
