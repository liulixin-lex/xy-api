package routingdistribution

import (
	"errors"
	"fmt"
	"math"

	"github.com/DataDog/sketches-go/ddsketch"
	"github.com/DataDog/sketches-go/ddsketch/mapping"
	"github.com/DataDog/sketches-go/ddsketch/pb/sketchpb"
	"github.com/DataDog/sketches-go/ddsketch/store"
	"google.golang.org/protobuf/proto"
)

const (
	SketchCodecVersion      = 1
	CodecVersion            = SketchCodecVersion
	RelativeAccuracy        = 0.02
	MaxBins                 = 384
	MaxDurationMilliseconds = int64(60 * 60 * 1000)
	MaxEncodedBytes         = 4 << 10

	maxExactCount = int64(1<<53 - 1)
)

var (
	ErrUnsupportedCodec = errors.New("unsupported routing distribution codec")
	ErrPayloadTooLarge  = errors.New("routing distribution payload exceeds limit")
	ErrInvalidPayload   = errors.New("invalid routing distribution payload")
	ErrInvalidMapping   = errors.New("invalid routing distribution mapping")
	ErrInvalidBins      = errors.New("invalid routing distribution bins")
	ErrInvalidCount     = errors.New("invalid routing distribution count")
	ErrNegativeValues   = errors.New("routing distribution contains negative values")
	ErrInvalidQuantile  = errors.New("invalid routing distribution quantile")
	ErrInvalidSketch    = errors.New("invalid routing distribution sketch")
)

var canonicalIndexMapping = func() mapping.IndexMapping {
	value, err := mapping.NewLogarithmicMapping(RelativeAccuracy)
	if err != nil {
		panic("invalid routing distribution constants: " + err.Error())
	}
	return value
}()

type AddResult struct {
	RecordedMilliseconds int64
	Clamped              bool
}

type QuantileResult struct {
	ValueMilliseconds float64
	Known             bool
}

// DurationSketch is a bounded, mergeable distribution of millisecond values.
// It is not safe for concurrent use; callers should synchronize per bucket.
type DurationSketch struct {
	sketch *ddsketch.DDSketch
}

func New() *DurationSketch {
	return NewDurationSketch()
}

func NewDurationSketch() *DurationSketch {
	return &DurationSketch{sketch: newDDSketch()}
}

func (s *DurationSketch) Add(milliseconds int64) (AddResult, error) {
	return s.AddMillis(milliseconds)
}

func (s *DurationSketch) AddMillis(milliseconds int64) (AddResult, error) {
	if !validSketchReference(s) {
		return AddResult{}, ErrInvalidSketch
	}
	recorded := milliseconds
	if recorded < 0 {
		recorded = 0
	}
	if recorded > MaxDurationMilliseconds {
		recorded = MaxDurationMilliseconds
	}
	if s.Count() >= maxExactCount {
		return AddResult{}, ErrInvalidCount
	}
	if err := s.sketch.Add(float64(recorded)); err != nil {
		return AddResult{}, fmt.Errorf("%w: %v", ErrInvalidSketch, err)
	}
	return AddResult{
		RecordedMilliseconds: recorded,
		Clamped:              recorded != milliseconds,
	}, nil
}

func (s *DurationSketch) Merge(other *DurationSketch) error {
	if !validSketchReference(s) || !validSketchReference(other) {
		return ErrInvalidSketch
	}
	leftCount, err := checkedCount(s.sketch)
	if err != nil {
		return err
	}
	rightCount, err := checkedCount(other.sketch)
	if err != nil {
		return err
	}
	if leftCount > maxExactCount-rightCount {
		return ErrInvalidCount
	}

	merged := s.sketch.Copy()
	if err := merged.MergeWith(other.sketch); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidMapping, err)
	}
	if err := validateRuntimeSketch(merged); err != nil {
		return err
	}
	s.sketch = merged
	return nil
}

func (s *DurationSketch) Marshal() ([]byte, error) {
	return s.MarshalBinary()
}

func (s *DurationSketch) MarshalBinary() ([]byte, error) {
	if !validSketchReference(s) {
		return nil, ErrInvalidSketch
	}
	if err := validateRuntimeSketch(s.sketch); err != nil {
		return nil, err
	}
	message := s.sketch.ToProto()
	if err := validateProto(message); err != nil {
		return nil, err
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if len(data) > MaxEncodedBytes {
		return nil, ErrPayloadTooLarge
	}
	return data, nil
}

func Decode(data []byte, version int) (*DurationSketch, error) {
	return DecodeDurationSketch(data, version)
}

func DecodeDurationSketch(data []byte, version int) (*DurationSketch, error) {
	if version != SketchCodecVersion {
		return nil, ErrUnsupportedCodec
	}
	if len(data) == 0 {
		return nil, ErrInvalidPayload
	}
	if len(data) > MaxEncodedBytes {
		return nil, ErrPayloadTooLarge
	}

	var message sketchpb.DDSketch
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(data, &message); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if err := validateProto(&message); err != nil {
		return nil, err
	}

	decoded, err := ddsketch.FromProtoWithStoreProvider(&message, boundedStoreProvider)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if err := validateRuntimeSketch(decoded); err != nil {
		return nil, err
	}
	return &DurationSketch{sketch: decoded}, nil
}

func (s *DurationSketch) Quantile(quantile float64) (QuantileResult, error) {
	if !validSketchReference(s) {
		return QuantileResult{}, ErrInvalidSketch
	}
	if math.IsNaN(quantile) || math.IsInf(quantile, 0) || quantile < 0 || quantile > 1 {
		return QuantileResult{}, ErrInvalidQuantile
	}
	if s.Count() == 0 {
		return QuantileResult{}, nil
	}
	value, err := s.sketch.GetValueAtQuantile(quantile)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return QuantileResult{}, ErrInvalidSketch
	}
	if value > float64(MaxDurationMilliseconds) {
		value = float64(MaxDurationMilliseconds)
	}
	return QuantileResult{ValueMilliseconds: value, Known: true}, nil
}

func (s *DurationSketch) Count() int64 {
	if !validSketchReference(s) {
		return 0
	}
	count, err := checkedCount(s.sketch)
	if err != nil {
		return 0
	}
	return count
}

func (s *DurationSketch) Clone() *DurationSketch {
	if !validSketchReference(s) {
		return nil
	}
	return &DurationSketch{sketch: s.sketch.Copy()}
}

func newDDSketch() *ddsketch.DDSketch {
	return ddsketch.NewDDSketchFromStoreProvider(canonicalIndexMapping, boundedStoreProvider)
}

func boundedStoreProvider() store.Store {
	return store.NewCollapsingLowestDenseStore(MaxBins)
}

func validSketchReference(sketch *DurationSketch) bool {
	return sketch != nil && sketch.sketch != nil
}

func validateProto(message *sketchpb.DDSketch) error {
	if message == nil || hasUnknownFields(message) {
		return ErrInvalidPayload
	}
	if message.Mapping == nil {
		return ErrInvalidMapping
	}
	if hasUnknownFields(message.Mapping) {
		return ErrInvalidPayload
	}
	expected := canonicalIndexMapping.ToProto()
	if math.Float64bits(message.Mapping.Gamma) != math.Float64bits(expected.Gamma) ||
		math.Float64bits(message.Mapping.IndexOffset) != math.Float64bits(expected.IndexOffset) ||
		message.Mapping.Interpolation != expected.Interpolation {
		return ErrInvalidMapping
	}
	if hasUnknownFields(message.PositiveValues) || hasUnknownFields(message.NegativeValues) {
		return ErrInvalidPayload
	}
	if storeHasRepresentation(message.NegativeValues) {
		return ErrNegativeValues
	}
	if err := validatePositiveStore(message.PositiveValues); err != nil {
		return err
	}
	if _, err := checkedProtoCount(message); err != nil {
		return err
	}
	return nil
}

func validatePositiveStore(value *sketchpb.Store) error {
	if value == nil {
		return nil
	}
	if len(value.BinCounts) != 0 {
		return ErrInvalidBins
	}
	bins := value.ContiguousBinCounts
	if len(bins) > MaxBins {
		return ErrInvalidBins
	}
	if len(bins) == 0 {
		if value.ContiguousBinIndexOffset != 0 {
			return ErrInvalidBins
		}
		return nil
	}
	minimumIndex := int64(canonicalIndexMapping.Index(1))
	maximumIndex := int64(canonicalIndexMapping.Index(float64(MaxDurationMilliseconds)))
	start := int64(value.ContiguousBinIndexOffset)
	end := start + int64(len(bins)) - 1
	if start < minimumIndex || end > maximumIndex {
		return ErrInvalidBins
	}
	for _, count := range bins {
		if !validCount(count) {
			return ErrInvalidCount
		}
	}
	return nil
}

func checkedProtoCount(message *sketchpb.DDSketch) (int64, error) {
	if !validCount(message.ZeroCount) {
		return 0, ErrInvalidCount
	}
	total := int64(message.ZeroCount)
	if message.PositiveValues == nil {
		return total, nil
	}
	for _, count := range message.PositiveValues.ContiguousBinCounts {
		value := int64(count)
		if total > maxExactCount-value {
			return 0, ErrInvalidCount
		}
		total += value
	}
	return total, nil
}

func validateRuntimeSketch(value *ddsketch.DDSketch) error {
	if value == nil || value.IndexMapping == nil || !canonicalIndexMapping.Equals(value.IndexMapping) {
		return ErrInvalidMapping
	}
	if !value.GetNegativeValueStore().IsEmpty() {
		return ErrNegativeValues
	}
	minimumIndex := canonicalIndexMapping.Index(1)
	maximumIndex := canonicalIndexMapping.Index(float64(MaxDurationMilliseconds))
	invalidBins := false
	value.GetPositiveValueStore().ForEach(func(index int, count float64) bool {
		if index < minimumIndex || index > maximumIndex || !validCount(count) {
			invalidBins = true
			return true
		}
		return false
	})
	if invalidBins {
		return ErrInvalidBins
	}
	_, err := checkedCount(value)
	return err
}

func checkedCount(value *ddsketch.DDSketch) (int64, error) {
	if value == nil {
		return 0, ErrInvalidSketch
	}
	count := value.GetCount()
	if !validCount(count) {
		return 0, ErrInvalidCount
	}
	return int64(count), nil
}

func validCount(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= float64(maxExactCount) && math.Trunc(value) == value
}

func storeHasRepresentation(value *sketchpb.Store) bool {
	return value != nil && (len(value.BinCounts) != 0 || len(value.ContiguousBinCounts) != 0 || value.ContiguousBinIndexOffset != 0)
}

func hasUnknownFields(message proto.Message) bool {
	return message != nil && len(message.ProtoReflect().GetUnknown()) != 0
}
