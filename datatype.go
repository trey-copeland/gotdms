package tdms

import "fmt"

// DataType is a TDMS tdsDataType code.
//
// Values match the enum in the NI TDMS internal-structure article. Decimal
// and hex are both shown because the article mixes the two: string is 32
// (0x20), not 33, and boolean is 33 (0x21), not 34.
type DataType uint32

const (
	TypeVoid                  DataType = 0
	TypeInt8                  DataType = 1
	TypeInt16                 DataType = 2
	TypeInt32                 DataType = 3
	TypeInt64                 DataType = 4
	TypeUint8                 DataType = 5
	TypeUint16                DataType = 6
	TypeUint32                DataType = 7
	TypeUint64                DataType = 8
	TypeFloat32               DataType = 9
	TypeFloat64               DataType = 10
	TypeExtendedFloat         DataType = 11
	TypeFloat32WithUnit       DataType = 0x19
	TypeFloat64WithUnit       DataType = 0x1A
	TypeExtendedFloatWithUnit DataType = 0x1B
	TypeString                DataType = 0x20
	TypeBool                  DataType = 0x21
	TypeTimestamp             DataType = 0x44
	// TypeFixedPoint is the NI code 0x4F. The public article does not define
	// its byte layout; encountering it returns ErrUnsupported.
	TypeFixedPoint DataType = 0x4F
	// TypeComplex64 is two little-endian float32 values, real then imaginary.
	TypeComplex64 DataType = 0x08000c
	// TypeComplex128 is two little-endian float64 values, real then imaginary.
	TypeComplex128 DataType = 0x10000d
	// TypeDAQmxRaw is the data-type word 0xFFFFFFFF inside a DAQmx raw-data
	// index. It marks the index, not the samples. The channel's DataType after
	// a successful read is the scaler's native type. A DAQmx index may instead
	// carry its one scaler's type, which npTDMS also reads. A property value of
	// this type, or a standard raw-data index whose type word is 0xFFFFFFFF,
	// still returns ErrDAQmx.
	TypeDAQmxRaw DataType = 0xFFFFFFFF
)

// String returns the NI enum name, or tdsType(N) for an unknown code.
func (d DataType) String() string {
	if s, ok := typeNames[d]; ok {
		return s
	}
	return fmt.Sprintf("tdsType(%d)", uint32(d))
}

// Size returns the fixed on-disk size in bytes. ok is false for strings and
// for types without a defined layout (void, fixed-point, DAQmx).
func (d DataType) Size() (int, bool) {
	switch d.storage() {
	case TypeInt8, TypeUint8, TypeBool:
		return 1, true
	case TypeInt16, TypeUint16:
		return 2, true
	case TypeInt32, TypeUint32, TypeFloat32:
		return 4, true
	case TypeInt64, TypeUint64, TypeFloat64, TypeComplex64:
		return 8, true
	case TypeExtendedFloat:
		return extendedSize, true
	case TypeTimestamp, TypeComplex128:
		return 16, true
	default:
		return 0, false
	}
}

// storage maps unit-qualified floats onto the plain layout. The NI article
// states that a float-with-unit is stored as the float itself; the unit text
// lives in the unit_string property, not in the raw value.
func (d DataType) storage() DataType {
	switch d {
	case TypeFloat32WithUnit:
		return TypeFloat32
	case TypeFloat64WithUnit:
		return TypeFloat64
	case TypeExtendedFloatWithUnit:
		return TypeExtendedFloat
	default:
		return d
	}
}

func (d DataType) supported() bool {
	switch d {
	case TypeVoid, TypeFixedPoint, TypeDAQmxRaw:
		return false
	default:
		_, known := typeNames[d]
		return known
	}
}

func (d DataType) variable() bool { return d == TypeString }

var typeNames = map[DataType]string{
	TypeVoid:                  "tdsTypeVoid",
	TypeInt8:                  "tdsTypeI8",
	TypeInt16:                 "tdsTypeI16",
	TypeInt32:                 "tdsTypeI32",
	TypeInt64:                 "tdsTypeI64",
	TypeUint8:                 "tdsTypeU8",
	TypeUint16:                "tdsTypeU16",
	TypeUint32:                "tdsTypeU32",
	TypeUint64:                "tdsTypeU64",
	TypeFloat32:               "tdsTypeSingleFloat",
	TypeFloat64:               "tdsTypeDoubleFloat",
	TypeExtendedFloat:         "tdsTypeExtendedFloat",
	TypeFloat32WithUnit:       "tdsTypeSingleFloatWithUnit",
	TypeFloat64WithUnit:       "tdsTypeDoubleFloatWithUnit",
	TypeExtendedFloatWithUnit: "tdsTypeExtendedFloatWithUnit",
	TypeString:                "tdsTypeString",
	TypeBool:                  "tdsTypeBoolean",
	TypeTimestamp:             "tdsTypeTimeStamp",
	TypeFixedPoint:            "tdsTypeFixedPoint",
	TypeComplex64:             "tdsTypeComplexSingleFloat",
	TypeComplex128:            "tdsTypeComplexDoubleFloat",
	TypeDAQmxRaw:              "tdsTypeDAQmxRawData",
}
