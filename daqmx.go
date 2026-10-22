package tdms

import (
	"fmt"
	"math"
	"strings"
)

// DAQmx scaler type codes are not tdsDataType values. The NI article names the
// field and does not list the codes. The widths below are the ones a scaler of
// that code occupies in the raw buffer: 0 uint8, 1 int8, 2 uint16, 3 int16,
// 4 uint32, 5 int32, 6 uint64, 7 int64, 8 float32, 9 float64, and 0xFFFFFFFF
// a TDMS timestamp. A differential read against npTDMS raw values checks the
// integer and float codes. 0xFFFFFFFF is the same width as tdsTypeTimeStamp.
func daqmxType(code uint32) (DataType, bool) {
	switch code {
	case 0:
		return TypeUint8, true
	case 1:
		return TypeInt8, true
	case 2:
		return TypeUint16, true
	case 3:
		return TypeInt16, true
	case 4:
		return TypeUint32, true
	case 5:
		return TypeInt32, true
	case 6:
		return TypeUint64, true
	case 7:
		return TypeInt64, true
	case 8:
		return TypeFloat32, true
	case 9:
		return TypeFloat64, true
	case 0xFFFFFFFF:
		return TypeTimestamp, true
	default:
		return 0, false
	}
}

// DAQmxScaler is one entry in a channel's DAQmx scaler vector.
//
// A format-changing scaler (index words 0x00001269 and 0x00001369) stores a
// native value of DataType at ByteOffset within the stride of RawBuffer.
// A digital-line scaler (index word 0x0000126A) stores one bit. BitOffset is
// that bit's index from the start of the stride, bit 0 being the least
// significant bit of the first byte. Values are uint8 0 or 1. ByteOffset is
// BitOffset/8 for a digital scaler and the value's first byte otherwise.
//
// FormatBitmap is the sample-format bitmap from the index. This package only
// decodes bitmap 0. A non-zero bitmap fails the open with ErrDAQmx, so a
// scaler on an opened channel has FormatBitmap 0.
type DAQmxScaler struct {
	DataType     DataType
	RawBuffer    uint32
	ByteOffset   uint32
	BitOffset    uint32
	FormatBitmap uint32
	ScaleID      uint32
	Digital      bool
}

// DAQmxRaw is the raw-data index of one DAQmx channel.
//
// Widths is the raw-data width vector: Widths[i] is the stride, in bytes, of
// raw buffer i. One chunk holds, for each buffer, (samples in that buffer)
// strides, and the buffers are concatenated in index order. Channels that
// share a buffer are interleaved at their byte offsets inside each stride.
//
// Primary is the scaler index that Values, Slice, Cursor, ReadValues, and
// Float64s decode. It is the only scaler, or the first scaler whose ScaleID
// is 0, or otherwise the scaler with the lowest ScaleID. DAQmxSlice reads
// any scaler by ScaleID.
//
// When segments differ, this is the most recent segment's index. Widths,
// buffer indexes, and byte offsets may change between segments; the scaler
// count, scale ids, and types may not.
type DAQmxRaw struct {
	Header  uint32
	Widths  []uint32
	Scalers []DAQmxScaler
	Primary int
}

// DAQmxScale is one NI_Scale[i] description stored in properties.
//
// InputSource is the scale index this scale reads, or 0 when the property is
// absent. InputRaw is true when the input is not another typed scale: the
// input property is missing, it is 0xFFFFFFFF, or it names an index with no
// NI_Scale type. DAQmxScaledFloat64s reads that last case as the DAQmx
// scaler whose ScaleID is InputSource; ApplyDAQmxScale reads it as raw. The
// NI article's linear example uses input source 0 with no NI_Scale[0].
//
// Linear uses Slope and Intercept (y = Slope*x + Intercept). Polynomial uses
// Coefficients, lowest degree first, so Coefficients[0] is the constant term.
// Other Type strings are returned as metadata and are not applied.
type DAQmxScale struct {
	Index        int
	Type         string
	Slope        float64
	Intercept    float64
	Coefficients []float64
	InputSource  uint32
	InputRaw     bool

	linearOK bool
	polyOK   bool
	hasInput bool
}

// DAQmxScaling is the NI_Scaling_* / NI_Scale[*]_* metadata for a channel.
//
// Status is NI_Scaling_Status ("unscaled", "scaled", or empty). Source is
// "channel", "group", or "file", the object whose properties supplied the
// scales. Channel properties win, then the group, then the file.
//
// HasCount is true when NI_Number_Of_Scales is present. Count is that value,
// including 0. When the property is absent, HasCount is false and Count is 0.
// A count of 0 applies no scale. When the property is absent, the output is
// the highest typed index.
//
// Scales holds only the indexes that have an NI_Scale[i]_Scale_Type. An index
// below Count with no type is the DAQmx scaler whose ScaleID is that index:
// its raw samples are that scale's output.
type DAQmxScaling struct {
	Status   string
	Source   string
	Count    int
	HasCount bool
	Scales   []DAQmxScale

	// badCount is set when NI_Number_Of_Scales does not fit in an int.
	badCount bool
}

// DAQmx reports the raw-data index when this channel's samples are DAQmx raw
// data. The second result is false for a standard channel.
func (c *Channel) DAQmx() (DAQmxRaw, bool) {
	if c == nil || c.daqmx == nil || c.daqmx.spec == nil {
		return DAQmxRaw{}, false
	}
	spec := c.daqmx.spec
	out := DAQmxRaw{
		Header:  spec.header,
		Widths:  append([]uint32(nil), spec.widths...),
		Scalers: make([]DAQmxScaler, len(spec.scalers)),
		Primary: spec.primary,
	}
	for i, sc := range spec.scalers {
		pub := DAQmxScaler{
			DataType:     sc.dt,
			RawBuffer:    sc.buffer,
			FormatBitmap: sc.bitmap,
			ScaleID:      sc.scaleID,
			Digital:      sc.digital,
		}
		if sc.digital {
			pub.BitOffset = sc.offset
			pub.ByteOffset = sc.offset / 8
			pub.DataType = TypeUint8
		} else {
			pub.ByteOffset = sc.offset
		}
		out.Scalers[i] = pub
	}
	return out, true
}

// DAQmxSlice reads raw values of the scaler whose ScaleID is scaleID.
// i is the first sample and n is the count. The concrete type matches
// Values of a channel of that scaler's native type. A digital scaler is
// []uint8 of 0 and 1. ScaleID 0xFFFFFFFF is a real id, not a wildcard.
// Two scalers with the same ScaleID return ErrDAQmx.
func (c *Channel) DAQmxSlice(scaleID uint32, i, n int) (any, error) {
	path := ""
	if c != nil {
		path = c.path
	}
	if c == nil || c.file == nil || c.file.r == nil {
		return nil, wrap("read", path, ErrClosed)
	}
	if c.daqmx == nil || c.daqmx.spec == nil {
		return nil, wrap("read", path, fmt.Errorf("%w: channel has no DAQmx raw data", ErrArgument))
	}
	idx, err := scalerIndex(c.daqmx.spec.scalers, scaleID)
	if err != nil {
		return nil, wrap("read", path, err)
	}
	return readTyped(c.file.r, path, c.daqmx.spec.scalers[idx].dt, true, c.daqmx.spans[idx], c.daqmx.runs[idx], c.daqmx.totals[idx], i, n, nil)
}

// DAQmxScaling reads NI_Scaling_Status and NI_Scale[*]_* properties.
// It does not read samples and does not apply a formula.
func (c *Channel) DAQmxScaling() DAQmxScaling {
	if c == nil {
		return DAQmxScaling{}
	}
	props, source := c.scalingProps()
	if source == "" {
		return DAQmxScaling{}
	}
	var count int
	var hasCount, badCount bool
	if n, ok := propUint(props, "NI_Number_Of_Scales"); ok {
		if n > uint64(math.MaxInt) {
			badCount = true
		} else {
			count = int(n)
			hasCount = true
		}
	}
	return DAQmxScaling{
		Status:   scalingStatus(props),
		Source:   source,
		Count:    count,
		HasCount: hasCount,
		Scales:   parseDAQmxScales(props),
		badCount: badCount,
	}
}

// DAQmxScaledFloat64s returns the channel's output scale as float64, which
// is what npTDMS returns for a scaled DAQmx channel.
//
// The output is the last scale: index Count-1 when NI_Number_Of_Scales is
// present and positive. A count of 0 applies nothing. When that property is
// absent, the output is the highest NI_Scale index with a type. A Linear or
// Polynomial scale is applied to its input source, which is computed the same
// way. An input of 0xFFFFFFFF, or a missing input property, is the primary
// scaler's raw samples. An index with no scale type is the DAQmx scaler with
// that ScaleID. Status "scaled" means the samples are already in engineering
// units, so the raw float64 values are returned. No scales is the same: the
// raw values. A chain that needs another scale type, or a DAQmx scaler the
// channel does not have, returns ErrUnsupported and does not change what
// Values returns.
func (c *Channel) DAQmxScaledFloat64s() ([]float64, error) {
	sc := c.DAQmxScaling()
	if sc.badCount {
		return nil, wrap("read", c.path, fmt.Errorf("%w: NI_Number_Of_Scales does not fit", ErrUnsupported))
	}
	if strings.EqualFold(sc.Status, "scaled") || (sc.HasCount && sc.Count == 0) {
		return c.Float64s()
	}
	final := -1
	if sc.HasCount {
		final = sc.Count - 1
	} else {
		for _, s := range sc.Scales {
			if s.Index > final {
				final = s.Index
			}
		}
	}
	if final < 0 {
		return c.Float64s()
	}
	out, err := evalDAQmxScales(sc.Scales, final, c.daqmxScaleInput)
	if err != nil {
		return nil, wrap("read", c.path, err)
	}
	return out, nil
}

// daqmxScaleInput is the input a scale chain reads at its leaves. named is
// false for raw data (0xFFFFFFFF or no input property); otherwise id is a
// scale index with no NI_Scale type, which is a DAQmx scaler's ScaleID.
func (c *Channel) daqmxScaleInput(id uint32, named bool) ([]float64, error) {
	if !named || id == 0xFFFFFFFF || c.daqmx == nil || c.daqmx.spec == nil {
		return c.Float64s()
	}
	scalers := c.daqmx.spec.scalers
	for i := range scalers {
		if scalers[i].scaleID != id {
			continue
		}
		idx, err := scalerIndex(scalers, id)
		if err != nil {
			return nil, err
		}
		if idx == c.daqmx.spec.primary {
			return c.Float64s()
		}
		total := c.daqmx.totals[idx]
		if total > int64(math.MaxInt) {
			return nil, fmt.Errorf("%w: scaler %d has %d values", ErrUnsupported, id, total)
		}
		v, err := c.DAQmxSlice(id, 0, int(total))
		if err != nil {
			return nil, err
		}
		return toFloat64s(v)
	}
	return nil, fmt.Errorf("%w: DAQmx scale input %d names no scaler on this channel", ErrUnsupported, id)
}

// ApplyDAQmxScale applies a linear and polynomial chain to raw.
//
// The output is the scale with the highest Index. Each scale is applied to
// its input: raw when InputRaw is set, otherwise the scale whose Index is
// InputSource, computed the same way. An InputSource that names no scale in
// scales is raw. A cycle, a duplicate Index, or a scale type other than
// Linear or Polynomial returns an error. The input slice is not modified.
func ApplyDAQmxScale(scales []DAQmxScale, raw []float64) ([]float64, error) {
	if len(scales) == 0 {
		return append([]float64{}, raw...), nil
	}
	final := scales[0].Index
	for _, s := range scales[1:] {
		if s.Index > final {
			final = s.Index
		}
	}
	return evalDAQmxScales(scales, final, func(uint32, bool) ([]float64, error) {
		return raw, nil
	})
}

// evalDAQmxScales computes scale index final. input supplies the leaves and
// its result is not modified.
func evalDAQmxScales(scales []DAQmxScale, final int, input func(id uint32, named bool) ([]float64, error)) ([]float64, error) {
	by := make(map[int]*DAQmxScale, len(scales))
	for i := range scales {
		if _, ok := by[scales[i].Index]; ok {
			return nil, fmt.Errorf("%w: duplicate NI_Scale[%d]", ErrCorrupt, scales[i].Index)
		}
		by[scales[i].Index] = &scales[i]
	}
	seen := make(map[int]bool, len(scales))
	var eval func(idx int) ([]float64, error)
	eval = func(idx int) ([]float64, error) {
		s, ok := by[idx]
		if !ok {
			return input(uint32(idx), true)
		}
		if seen[idx] {
			return nil, fmt.Errorf("%w: DAQmx scale %d is part of a cycle", ErrCorrupt, idx)
		}
		seen[idx] = true
		var in []float64
		var err error
		switch {
		case s.InputRaw && (!s.hasInput || s.InputSource == 0xFFFFFFFF):
			in, err = input(0xFFFFFFFF, false)
		case s.InputRaw:
			in, err = input(s.InputSource, true)
		case s.InputSource == 0xFFFFFFFF || uint64(s.InputSource) > uint64(math.MaxInt32):
			in, err = input(0xFFFFFFFF, false)
		default:
			in, err = eval(int(s.InputSource))
		}
		if err != nil {
			return nil, err
		}
		out := append([]float64{}, in...)
		if err := applyDAQmxScale(s, out); err != nil {
			return nil, err
		}
		return out, nil
	}
	return eval(final)
}

func applyDAQmxScale(s *DAQmxScale, buf []float64) error {
	switch strings.ToLower(s.Type) {
	case "linear":
		if !s.linearOK {
			return fmt.Errorf("%w: NI_Scale[%d] linear scale is missing slope or intercept", ErrCorrupt, s.Index)
		}
		for i, x := range buf {
			buf[i] = x*s.Slope + s.Intercept
		}
		return nil
	case "polynomial":
		if !s.polyOK {
			return fmt.Errorf("%w: NI_Scale[%d] polynomial coefficients are incomplete", ErrCorrupt, s.Index)
		}
		if len(s.Coefficients) == 0 {
			for i := range buf {
				buf[i] = 0
			}
			return nil
		}
		for i, x := range buf {
			buf[i] = polyEval(s.Coefficients, x)
		}
		return nil
	default:
		return fmt.Errorf("%w: NI_Scale[%d] type %q is not applied", ErrUnsupported, s.Index, s.Type)
	}
}

func polyEval(coeff []float64, x float64) float64 {
	y := 0.0
	p := 1.0
	for _, a := range coeff {
		y += a * p
		p *= x
	}
	return y
}

func (c *Channel) scalingProps() ([]Property, string) {
	if scalePropsPresent(c.props) {
		return c.props, "channel"
	}
	if c.group != nil && scalePropsPresent(c.group.props) {
		return c.group.props, "group"
	}
	if c.file != nil && scalePropsPresent(c.file.props) {
		return c.file.props, "file"
	}
	return nil, ""
}

func scalePropsPresent(ps []Property) bool {
	for _, p := range ps {
		if p.Name == "NI_Scaling_Status" || p.Name == "NI_Number_Of_Scales" || strings.HasPrefix(p.Name, "NI_Scale[") {
			return true
		}
	}
	return false
}

func scalingStatus(ps []Property) string {
	s, _ := propString(ps, "NI_Scaling_Status")
	return s
}

func parseDAQmxScales(ps []Property) []DAQmxScale {
	var scales []DAQmxScale
	for i := 0; i < 64; i++ {
		typ, ok := propString(ps, fmt.Sprintf("NI_Scale[%d]_Scale_Type", i))
		if !ok {
			continue
		}
		s := DAQmxScale{Index: i, Type: typ}
		switch strings.ToLower(typ) {
		case "linear":
			slope, okS := propFloat(ps, fmt.Sprintf("NI_Scale[%d]_Linear_Slope", i))
			icept, okI := propFloat(ps, fmt.Sprintf("NI_Scale[%d]_Linear_Y_Intercept", i))
			s.Slope, s.Intercept = slope, icept
			s.linearOK = okS && okI
			if src, ok := propUint(ps, fmt.Sprintf("NI_Scale[%d]_Linear_Input_Source", i)); ok && src <= 0xFFFFFFFF {
				s.InputSource = uint32(src)
				s.hasInput = true
			}
		case "polynomial":
			s.Coefficients, s.polyOK = polyCoefficients(ps, i)
			if src, ok := propUint(ps, fmt.Sprintf("NI_Scale[%d]_Polynomial_Input_Source", i)); ok && src <= 0xFFFFFFFF {
				s.InputSource = uint32(src)
				s.hasInput = true
			}
		default:
			if src, ok := propUint(ps, fmt.Sprintf("NI_Scale[%d]_Linear_Input_Source", i)); ok && src <= 0xFFFFFFFF {
				s.InputSource = uint32(src)
				s.hasInput = true
			}
		}
		scales = append(scales, s)
	}
	present := map[int]struct{}{}
	for _, s := range scales {
		present[s.Index] = struct{}{}
	}
	for i := range scales {
		src := scales[i].InputSource
		_, named := present[int(src)]
		scales[i].InputRaw = !scales[i].hasInput || src == 0xFFFFFFFF || !named
	}
	return scales
}

func polyCoefficients(ps []Property, index int) ([]float64, bool) {
	key := func(j int) string {
		return fmt.Sprintf("NI_Scale[%d]_Polynomial_Coefficients[%d]", index, j)
	}
	if n, ok := propUint(ps, fmt.Sprintf("NI_Scale[%d]_Polynomial_Coefficients_Size", index)); ok {
		if n > 32 {
			return nil, false
		}
		coeff := make([]float64, n)
		for j := 0; j < int(n); j++ {
			v, ok := propFloat(ps, key(j))
			if !ok {
				return nil, false
			}
			coeff[j] = v
		}
		return coeff, true
	}
	var coeff []float64
	for j := 0; j < 32; j++ {
		v, ok := propFloat(ps, key(j))
		if !ok {
			break
		}
		coeff = append(coeff, v)
	}
	return coeff, len(coeff) > 0
}

// daqmxSpec is the raw-data index, without the spans that address samples.
type daqmxSpec struct {
	header  uint32
	widths  []uint32
	scalers []daqmxScaler
	primary int
	n       int64
}

type daqmxScaler struct {
	rawType uint32
	dt      DataType
	buffer  uint32
	offset  uint32
	bitmap  uint32
	scaleID uint32
	digital bool
}

type daqmxState struct {
	spec   *daqmxSpec
	spans  [][]span
	runs   [][]spanRun
	totals []int64
}

func newDAQmxState(spec *daqmxSpec) *daqmxState {
	return &daqmxState{
		spec:   spec,
		spans:  make([][]span, len(spec.scalers)),
		runs:   make([][]spanRun, len(spec.scalers)),
		totals: make([]int64, len(spec.scalers)),
	}
}

func (d *decoder) daqmxIndex(header uint32) (*daqmxSpec, error) {
	digital := header == RawIndexDAQmxDigitalObserved
	code, err := d.u32()
	if err != nil {
		return nil, err
	}
	// The data type is normally 0xFFFFFFFF. npTDMS also reads an index whose
	// data type is the scaler's own type, with exactly one scaler; that is
	// checked after the scalers are read.
	typed := DataType(code) != TypeDAQmxRaw
	if typed {
		if sz, ok := DataType(code).Size(); !ok || sz <= 0 {
			return nil, fmt.Errorf("%w: DAQmx index data type %#x", ErrDAQmx, code)
		}
	}
	dim, err := d.u32()
	if err != nil {
		return nil, err
	}
	if dim != 1 {
		return nil, fmt.Errorf("%w: array dimension %d, TDMS 2.0 requires 1", ErrUnsupported, dim)
	}
	count, err := d.u64()
	if err != nil {
		return nil, err
	}
	n, err := u64ToI64(count)
	if err != nil {
		return nil, err
	}
	ns, err := d.u32()
	if err != nil {
		return nil, err
	}
	minScaler := 20
	if digital {
		minScaler = 17
	}
	if ns == 0 {
		return nil, fmt.Errorf("%w: DAQmx scaler vector is empty", ErrCorrupt)
	}
	if uint64(ns) > uint64(d.remain())/uint64(minScaler) {
		return nil, fmt.Errorf("%w: DAQmx scaler vector length %d", ErrCorrupt, ns)
	}
	scalers := make([]daqmxScaler, ns)
	for i := range scalers {
		sc, err := d.oneScaler(digital)
		if err != nil {
			return nil, err
		}
		scalers[i] = sc
	}
	if typed {
		if ns != 1 {
			return nil, fmt.Errorf("%w: DAQmx index data type %s with %d scalers", ErrDAQmx, DataType(code), ns)
		}
		if native, _ := daqmxType(scalers[0].rawType); native != DataType(code) {
			return nil, fmt.Errorf("%w: DAQmx index data type %s, scaler type %s", ErrDAQmx, DataType(code), native)
		}
	}
	nw, err := d.u32()
	if err != nil {
		return nil, err
	}
	if nw == 0 {
		return nil, fmt.Errorf("%w: DAQmx raw data width vector is empty", ErrCorrupt)
	}
	if uint64(nw) > uint64(d.remain())/4 {
		return nil, fmt.Errorf("%w: DAQmx raw data width vector length %d", ErrCorrupt, nw)
	}
	widths := make([]uint32, nw)
	for i := range widths {
		w, err := d.u32()
		if err != nil {
			return nil, err
		}
		if w == 0 {
			return nil, fmt.Errorf("%w: DAQmx raw buffer width is 0", ErrCorrupt)
		}
		widths[i] = w
	}
	for i, sc := range scalers {
		if int(sc.buffer) >= len(widths) {
			return nil, fmt.Errorf("%w: DAQmx raw buffer index %d", ErrCorrupt, sc.buffer)
		}
		w := widths[sc.buffer]
		if sc.digital {
			if sc.offset/8 >= w {
				return nil, fmt.Errorf("%w: digital bit offset %d outside buffer width %d", ErrCorrupt, sc.offset, w)
			}
			continue
		}
		sz, ok := sc.dt.Size()
		if !ok || sz <= 0 {
			return nil, fmt.Errorf("%w: %s has no raw width", ErrDAQmx, sc.dt)
		}
		if uint64(sc.offset)+uint64(sz) > uint64(w) {
			return nil, fmt.Errorf("%w: scaler %d extends past raw buffer width %d", ErrCorrupt, i, w)
		}
	}
	return &daqmxSpec{
		header:  header,
		widths:  widths,
		scalers: scalers,
		primary: primaryScaler(scalers),
		n:       n,
	}, nil
}

func (d *decoder) oneScaler(digital bool) (daqmxScaler, error) {
	rawType, err := d.u32()
	if err != nil {
		return daqmxScaler{}, err
	}
	buffer, err := d.u32()
	if err != nil {
		return daqmxScaler{}, err
	}
	offset, err := d.u32()
	if err != nil {
		return daqmxScaler{}, err
	}
	var bitmap uint32
	if digital {
		b, err := d.u8()
		if err != nil {
			return daqmxScaler{}, err
		}
		bitmap = uint32(b)
	} else {
		bitmap, err = d.u32()
		if err != nil {
			return daqmxScaler{}, err
		}
	}
	if bitmap != 0 {
		return daqmxScaler{}, fmt.Errorf("%w: sample format bitmap %#x is not 0", ErrDAQmx, bitmap)
	}
	scaleID, err := d.u32()
	if err != nil {
		return daqmxScaler{}, err
	}
	dt, ok := daqmxType(rawType)
	if !ok {
		return daqmxScaler{}, fmt.Errorf("%w: DAQmx data type %#x", ErrDAQmx, rawType)
	}
	if digital {
		switch dt {
		case TypeUint8, TypeInt8, TypeUint16, TypeInt16, TypeUint32, TypeInt32, TypeUint64, TypeInt64:
		default:
			return daqmxScaler{}, fmt.Errorf("%w: digital line scaler type %s", ErrDAQmx, dt)
		}
		dt = TypeUint8
	}
	return daqmxScaler{
		rawType: rawType,
		dt:      dt,
		buffer:  buffer,
		offset:  offset,
		bitmap:  bitmap,
		scaleID: scaleID,
		digital: digital,
	}, nil
}

func primaryScaler(scalers []daqmxScaler) int {
	if len(scalers) == 1 {
		return 0
	}
	zero := -1
	best := 0
	for i, sc := range scalers {
		if sc.scaleID == 0 && zero < 0 {
			zero = i
		}
		if sc.scaleID < scalers[best].scaleID {
			best = i
		}
	}
	if zero >= 0 {
		return zero
	}
	return best
}

func scalerIndex(scalers []daqmxScaler, id uint32) (int, error) {
	found := -1
	for i, sc := range scalers {
		if sc.scaleID != id {
			continue
		}
		if found >= 0 {
			return 0, fmt.Errorf("%w: more than one scaler with scale id %d", ErrDAQmx, id)
		}
		found = i
	}
	if found < 0 {
		return 0, fmt.Errorf("%w: no scaler with scale id %d", ErrArgument, id)
	}
	return found, nil
}

// sameDAQmxScalers reports whether b continues a: the same scalers, in the
// same order, with the same scale ids and native types. Raw buffer widths,
// buffer indexes, and byte offsets may change between segments, for example
// when a later acquisition adds a channel to the buffer. Each span records
// its own stride and column, so earlier segments keep their layout.
func sameDAQmxScalers(a, b *daqmxSpec) bool {
	if a == nil || b == nil || a.primary != b.primary || len(a.scalers) != len(b.scalers) {
		return false
	}
	for i := range a.scalers {
		x, y := a.scalers[i], b.scalers[i]
		// rawType is the DAQmx type code. A digital scaler's decoded dt is
		// always uint8, so the code is what distinguishes uint8 from uint32.
		if x.dt != y.dt || x.rawType != y.rawType || x.scaleID != y.scaleID || x.digital != y.digital {
			return false
		}
	}
	return true
}

func (f *File) addDAQmxSpans(slots []rawSlot, dataPos, rawLen int64, big, live bool) (int64, error) {
	widths := slots[0].daqmx.widths
	for _, s := range slots[1:] {
		if !sameWidths(widths, s.daqmx.widths) {
			return 0, fmt.Errorf("%w: DAQmx raw data widths disagree", ErrCorrupt)
		}
	}
	nBuf := len(widths)
	samples := make([]int64, nBuf)
	seen := make([]bool, nBuf)
	for _, s := range slots {
		if s.ch.daqmx == nil {
			return 0, ErrCorrupt
		}
		for _, sc := range s.daqmx.scalers {
			b := int(sc.buffer)
			if b < 0 || b >= nBuf {
				return 0, fmt.Errorf("%w: DAQmx raw buffer index %d", ErrCorrupt, sc.buffer)
			}
			if !seen[b] {
				seen[b] = true
				samples[b] = s.n
				continue
			}
			if samples[b] != s.n {
				return 0, fmt.Errorf("%w: DAQmx buffer %d chunk size %d and %d", ErrCorrupt, b, samples[b], s.n)
			}
		}
	}
	for i := range widths {
		if !seen[i] {
			return 0, fmt.Errorf("%w: DAQmx raw buffer %d has no scaler", ErrDAQmx, i)
		}
	}
	starts := make([]int64, nBuf)
	var chunk int64
	for i, w := range widths {
		starts[i] = chunk
		nb, err := mul64(samples[i], int64(w))
		if err != nil {
			return 0, err
		}
		chunk, err = add64(chunk, nb)
		if err != nil {
			return 0, err
		}
	}
	if chunk == 0 {
		if rawLen != 0 {
			return 0, fmt.Errorf("%w: empty DAQmx chunk with %d raw bytes", ErrCorrupt, rawLen)
		}
		return 0, nil
	}
	if rawLen%chunk != 0 && !live {
		return 0, fmt.Errorf("%w: raw length %d is not a multiple of chunk size %d", ErrCorrupt, rawLen, chunk)
	}
	nChunks := rawLen / chunk
	rem := rawLen % chunk
	partialN := make([]int64, nBuf)
	partialPos := make([]int64, nBuf)
	var unused int64
	if rem > 0 {
		base, err := add64(dataPos, mustMul(nChunks, chunk))
		if err != nil {
			return 0, err
		}
		left := rem
		at := base
		for i, w := range widths {
			need, err := mul64(samples[i], int64(w))
			if err != nil {
				return 0, err
			}
			if left < int64(w) {
				unused = left
				break
			}
			if left >= need {
				partialN[i] = samples[i]
				partialPos[i] = at
				left -= need
				at += need
				continue
			}
			can := left / int64(w)
			partialN[i] = can
			partialPos[i] = at
			unused = left - can*int64(w)
			break
		}
	}
	for _, s := range slots {
		spec := s.daqmx
		st := s.ch.daqmx
		for k, sc := range spec.scalers {
			sp, add, err := scalerSpan(sc, widths, samples, starts, partialN, partialPos, dataPos, chunk, nChunks, big, live)
			if err != nil {
				return 0, err
			}
			st.spans[k], st.runs[k] = appendSpan(st.spans[k], st.runs[k], sp)
			st.totals[k], err = add64(st.totals[k], add)
			if err != nil {
				return 0, err
			}
			if k != spec.primary {
				continue
			}
			// The primary scaler is the channel. One slice and one run
			// table, so a later merge cannot diverge the two views.
			s.ch.spans = st.spans[k]
			s.ch.runs = st.runs[k]
			s.ch.total = st.totals[k]
		}
	}
	if live {
		f.unusedTail = unused
		f.partialTail = rem > 0
	}
	return unused, nil
}

func scalerSpan(sc daqmxScaler, widths []uint32, samples, starts, partialN, partialPos []int64, dataPos, chunk, nChunks int64, big, live bool) (span, int64, error) {
	b := int(sc.buffer)
	w := int64(widths[b])
	col := starts[b]
	var bit uint8
	sz := 1
	partialCol := int64(0)
	if sc.digital {
		col += int64(sc.offset / 8)
		bit = uint8(sc.offset % 8)
		partialCol = int64(sc.offset / 8)
	} else {
		n, ok := sc.dt.Size()
		if !ok || n <= 0 {
			return span{}, 0, fmt.Errorf("%w: %s", ErrDAQmx, sc.dt)
		}
		sz = n
		col += int64(sc.offset)
		partialCol = int64(sc.offset)
	}
	sp := span{
		dataPos:   dataPos,
		chunkSize: chunk,
		nChunks:   nChunks,
		nPerChunk: samples[b],
		col:       col,
		stride:    w,
		typeSize:  sz,
		big:       big,
		digital:   sc.digital,
		bit:       bit,
		solo:      starts[b] != 0,
		live:      live,
	}
	if partialN[b] > 0 {
		sp.partialN = partialN[b]
		sp.partialPos = partialPos[b]
		sp.partialCol = partialCol
		sp.partialStride = w
	}
	full, err := mul64(nChunks, samples[b])
	if err != nil {
		return span{}, 0, err
	}
	add, err := add64(full, sp.partialN)
	if err != nil {
		return span{}, 0, err
	}
	return sp, add, nil
}

func sameWidths(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// indexHasType is the sidecar has-type byte for a standard channel.
// indexHasDAQmx is that byte when a DAQmx block follows the channel total.
const (
	indexHasType  = 1
	indexHasDAQmx = 2
)

func (w *ixw) daqmxBlock(st *daqmxState) {
	spec := st.spec
	w.u32(spec.header)
	w.u32(uint32(len(spec.widths)))
	for _, width := range spec.widths {
		w.u32(width)
	}
	w.u32(uint32(len(spec.scalers)))
	for i, sc := range spec.scalers {
		w.u32(uint32(sc.dt))
		w.u32(sc.buffer)
		w.u32(sc.offset)
		w.u32(sc.bitmap)
		w.u32(sc.scaleID)
		flags := byte(0)
		if sc.digital {
			flags |= 1
		}
		if i == spec.primary {
			flags |= 2
		}
		w.u8(flags)
		var bit byte
		if sc.digital {
			bit = byte(sc.offset % 8)
		}
		w.u8(bit)
		if i == spec.primary {
			continue
		}
		w.u32(uint32(len(st.spans[i])))
		for _, sp := range st.spans[i] {
			w.span(sp, st.runs[i])
		}
		w.i64(st.totals[i])
	}
}

func (r *ixr) daqmxBlock(primarySpans []span, primaryRuns []spanRun, primaryTotal int64, primaryDT DataType) (*daqmxState, error) {
	header, err := r.u32()
	if err != nil {
		return nil, err
	}
	switch header {
	case RawIndexDAQmxFormatChanging, RawIndexDAQmxDigitalLine, RawIndexDAQmxDigitalObserved:
	default:
		return nil, errIndex
	}
	nw, err := r.count(4)
	if err != nil {
		return nil, err
	}
	if nw == 0 || nw > 4096 {
		return nil, errIndex
	}
	widths := make([]uint32, nw)
	for i := range widths {
		widths[i], err = r.u32()
		if err != nil {
			return nil, err
		}
		if widths[i] == 0 {
			return nil, errIndex
		}
	}
	ns, err := r.count(4 + 4 + 4 + 4 + 4 + 1 + 1)
	if err != nil {
		return nil, err
	}
	if ns == 0 || ns > 4096 {
		return nil, errIndex
	}
	spec := &daqmxSpec{header: header, widths: widths, scalers: make([]daqmxScaler, ns), primary: -1}
	st := &daqmxState{spec: spec, spans: make([][]span, ns), runs: make([][]spanRun, ns), totals: make([]int64, ns)}
	digitalHeader := header == RawIndexDAQmxDigitalObserved
	for i := 0; i < ns; i++ {
		dt, err := r.u32()
		if err != nil {
			return nil, err
		}
		buffer, err := r.u32()
		if err != nil {
			return nil, err
		}
		offset, err := r.u32()
		if err != nil {
			return nil, err
		}
		bitmap, err := r.u32()
		if err != nil {
			return nil, err
		}
		scaleID, err := r.u32()
		if err != nil {
			return nil, err
		}
		flags, err := r.u8()
		if err != nil {
			return nil, err
		}
		bit, err := r.u8()
		if err != nil {
			return nil, err
		}
		if flags&^3 != 0 || int(buffer) >= len(widths) {
			return nil, errIndex
		}
		digital := flags&1 != 0
		primary := flags&2 != 0
		if digital != digitalHeader {
			return nil, errIndex
		}
		if digital && (bit > 7 || offset/8 >= widths[buffer] || bit != byte(offset%8)) {
			return nil, errIndex
		}
		sc := daqmxScaler{
			dt:      DataType(dt),
			buffer:  buffer,
			offset:  offset,
			bitmap:  bitmap,
			scaleID: scaleID,
			digital: digital,
		}
		if digital {
			sc.dt = TypeUint8
		}
		// A sidecar is a cache: a scaler type that has no fixed width, or a
		// span that disagrees with its scaler, is a miss rather than a read.
		sz, ok := sc.dt.Size()
		if !ok || sz <= 0 || (!digital && uint64(offset)+uint64(sz) > uint64(widths[buffer])) {
			return nil, errIndex
		}
		spec.scalers[i] = sc
		if primary {
			if spec.primary >= 0 || sc.dt != primaryDT || !spansMatch(primarySpans, sz, digital) {
				return nil, errIndex
			}
			spec.primary = i
			st.spans[i] = primarySpans
			st.runs[i] = primaryRuns
			st.totals[i] = primaryTotal
			continue
		}
		nspan, err := r.count(r.spanSize)
		if err != nil {
			return nil, err
		}
		spans := make([]span, nspan)
		var runs []spanRun
		for k := 0; k < nspan; k++ {
			spans[k], err = r.span(&runs)
			if err != nil {
				return nil, err
			}
		}
		total, err := r.i64()
		if err != nil {
			return nil, err
		}
		if !spansMatch(spans, sz, digital) {
			return nil, errIndex
		}
		got, err := spanTotal(spans, runs)
		if err != nil || got != total {
			return nil, errIndex
		}
		st.spans[i] = spans
		st.runs[i] = runs
		st.totals[i] = total
	}
	if spec.primary < 0 || primaryScaler(spec.scalers) != spec.primary {
		return nil, errIndex
	}
	return st, nil
}

func spansMatch(spans []span, size int, digital bool) bool {
	for i := range spans {
		if spans[i].str || spans[i].typeSize != size || spans[i].digital != digital {
			return false
		}
	}
	return true
}

func (sp *span) maskDigital(dst []byte) error {
	if !sp.digital {
		return nil
	}
	if sp.typeSize != 1 || sp.bit > 7 {
		return ErrCorrupt
	}
	bit := sp.bit
	for i := range dst {
		dst[i] = (dst[i] >> bit) & 1
	}
	return nil
}
