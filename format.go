package tdms

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

// Lead-in and table-of-contents flags from the NI TDMS internal-structure
// article. The article's worked example labels bytes 0E 00 00 00 as mask
// "0x1110"; that label is binary 0b1110 (metadata | new object list | raw
// data) and the bytes themselves are authoritative: the mask is 0x0E.
//
// ToC is always little-endian. When TocBigEndian is set, every other numeric
// field in the segment, including the version and the two offsets inside the
// lead-in, is big-endian.
const (
	leadInSize = 28
	tagTDSm    = "TDSm"
	tagTDSh    = "TDSh"

	// Version10 is TDMS file format 1.0 (accepted on read).
	Version10 uint32 = 4712
	// Version20 is TDMS file format 2.0. Writers emit this version.
	Version20 uint32 = 4713

	// TocMetaData means the segment contains a metadata section.
	TocMetaData uint32 = 1 << 1
	// TocNewObjList means the segment replaces the previous segment's object order.
	TocNewObjList uint32 = 1 << 2
	// TocRawData means the segment contains a raw-data section.
	TocRawData uint32 = 1 << 3
	// TocInterleavedData means raw values are channel-multiplexed per sample.
	TocInterleavedData uint32 = 1 << 5
	// TocBigEndian means numeric fields are big-endian. The ToC mask itself stays little-endian.
	TocBigEndian uint32 = 1 << 6
	// TocDAQmxRawData means the raw-data section is DAQmx raw buffers rather than
	// one standard block per channel. The raw-data index words decide the
	// layout, as in npTDMS: DAQmx indexes are decoded with or without this
	// bit, and standard indexes under it are read as standard data.
	TocDAQmxRawData uint32 = 1 << 7

	// RawIndexNoData is stored in place of a raw-data index when the object has
	// no raw data in this segment (the file, every group, and property-only channels).
	RawIndexNoData uint32 = 0xFFFFFFFF
	// RawIndexMatchesPrevious means the index is identical to the previous segment.
	RawIndexMatchesPrevious uint32 = 0x00000000
	// RawIndexDAQmxFormatChanging is the on-disk uint32 for bytes 69 12 00 00.
	// The NI article: a raw-data index that begins with those bytes contains a
	// DAQmx format-changing scaler. Decimal 4713, the same number as Version20,
	// but it occupies the index-length field rather than the version field.
	RawIndexDAQmxFormatChanging uint32 = 0x00001269
	// RawIndexDAQmxDigitalLine is bytes 69 13 00 00. The NI article calls this a
	// digital-line scaler. npTDMS does not accept this word and no NI file in
	// this repository uses it. This package reads it as a format-changing
	// scaler (the same five uint32 fields as RawIndexDAQmxFormatChanging); that
	// is an assumption, and a record of another shape fails its bounds checks.
	// Digital-line scalers in NI-produced files use RawIndexDAQmxDigitalObserved.
	// The article does not mention 0x12C9 (decimal 4809); that word is not a
	// DAQmx header here.
	RawIndexDAQmxDigitalLine uint32 = 0x00001369
	// RawIndexDAQmxDigitalObserved is bytes 6A 12 00 00. A raw-data index that
	// begins with this word contains a digital-line scaler: three uint32 fields,
	// a uint8 sample-format bitmap, and a uint32 scale id. Each value is one
	// bit, returned as uint8 0 or 1.
	RawIndexDAQmxDigitalObserved uint32 = 0x0000126A

	// incompleteSegment is the next-segment offset of a final segment whose
	// writer did not patch the length (crash, power loss, or a seekable writer
	// that has not finished the segment). The raw data runs to EOF.
	incompleteSegment uint64 = 0xFFFFFFFFFFFFFFFF
)

type leadIn struct {
	toc     uint32
	version uint32
	next    uint64
	raw     uint64
}

func decodeLeadIn(b []byte) (leadIn, error) {
	if len(b) < leadInSize {
		return leadIn{}, ErrTruncated
	}
	tag := string(b[0:4])
	switch tag {
	case tagTDSm:
	case tagTDSh:
		return leadIn{}, ErrIndexFile
	default:
		return leadIn{}, ErrBadTag
	}
	// ToC is little-endian even in a big-endian segment, and it is the field
	// that tells us which endianness the rest of the lead-in uses.
	toc := binary.LittleEndian.Uint32(b[4:8])
	bo := orderOf(toc)
	return leadIn{
		toc:     toc,
		version: bo.Uint32(b[8:12]),
		next:    bo.Uint64(b[12:20]),
		raw:     bo.Uint64(b[20:28]),
	}, nil
}

func encodeLeadIn(toc uint32, next, raw uint64) []byte {
	b := make([]byte, leadInSize)
	copy(b[0:4], tagTDSm)
	binary.LittleEndian.PutUint32(b[4:8], toc)
	binary.LittleEndian.PutUint32(b[8:12], Version20)
	binary.LittleEndian.PutUint64(b[12:20], next)
	binary.LittleEndian.PutUint64(b[20:28], raw)
	return b
}

func orderOf(toc uint32) binary.ByteOrder {
	if toc&TocBigEndian != 0 {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

func readFull(r io.ReaderAt, p []byte, off int64) error {
	if off < 0 {
		return ErrCorrupt
	}
	for len(p) > 0 {
		n, err := r.ReadAt(p, off)
		off += int64(n)
		p = p[n:]
		if len(p) == 0 {
			return nil
		}
		if err != nil {
			if err == io.EOF {
				return ErrTruncated
			}
			return err
		}
		if n == 0 {
			return ErrTruncated
		}
	}
	return nil
}

func readerSize(r io.ReaderAt) (int64, error) {
	if v, ok := r.(interface{ Size() int64 }); ok {
		return v.Size(), nil
	}
	if f, ok := r.(*os.File); ok {
		fi, err := f.Stat()
		if err != nil {
			return 0, err
		}
		return fi.Size(), nil
	}
	if s, ok := r.(io.Seeker); ok {
		cur, err := s.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, err
		}
		end, err := s.Seek(0, io.SeekEnd)
		if err != nil {
			return 0, err
		}
		if _, err := s.Seek(cur, io.SeekStart); err != nil {
			return 0, err
		}
		return end, nil
	}
	return 0, fmt.Errorf("%w: %T has no Size or Seek; wrap it in a reader that does", ErrArgument, r)
}

func mul64(a, b int64) (int64, error) {
	if a < 0 || b < 0 {
		return 0, ErrCorrupt
	}
	if a != 0 && b > math.MaxInt64/a {
		return 0, ErrCorrupt
	}
	return a * b, nil
}

func add64(a, b int64) (int64, error) {
	if a < 0 || b < 0 {
		return 0, ErrCorrupt
	}
	if a > math.MaxInt64-b {
		return 0, ErrCorrupt
	}
	return a + b, nil
}

func u64ToI64(v uint64) (int64, error) {
	if v > math.MaxInt64 {
		return 0, ErrCorrupt
	}
	return int64(v), nil
}
