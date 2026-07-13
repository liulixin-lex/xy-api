package routingdistribution

import (
	"errors"
	"math"
	"testing"

	"github.com/DataDog/sketches-go/ddsketch/pb/sketchpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestDurationSketchAddClampsAndCounts(t *testing.T) {
	sketch := NewDurationSketch()

	low, err := sketch.AddMillis(-5)
	require.NoError(t, err)
	assert.Equal(t, AddResult{RecordedMilliseconds: 0, Clamped: true}, low)

	normal, err := sketch.AddMillis(42)
	require.NoError(t, err)
	assert.Equal(t, AddResult{RecordedMilliseconds: 42}, normal)

	high, err := sketch.AddMillis(MaxDurationMilliseconds + 1)
	require.NoError(t, err)
	assert.Equal(t, AddResult{RecordedMilliseconds: MaxDurationMilliseconds, Clamped: true}, high)
	assert.Equal(t, int64(3), sketch.Count())

	minimum, err := sketch.Quantile(0)
	require.NoError(t, err)
	assert.True(t, minimum.Known)
	assert.Zero(t, minimum.ValueMilliseconds)

	maximum, err := sketch.Quantile(1)
	require.NoError(t, err)
	assert.True(t, maximum.Known)
	assert.Equal(t, float64(MaxDurationMilliseconds), maximum.ValueMilliseconds)
}

func TestDurationSketchEmptyAndInvalidQuantiles(t *testing.T) {
	sketch := New()

	empty, err := sketch.Quantile(0.95)
	require.NoError(t, err)
	assert.Equal(t, QuantileResult{}, empty)

	for _, quantile := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1)} {
		_, err := sketch.Quantile(quantile)
		assert.ErrorIs(t, err, ErrInvalidQuantile)
	}
}

func TestDurationSketchMergeIsAccurateAndOrderIndependent(t *testing.T) {
	partitions := [][]int64{
		{0, 5, 10, 20, 100, 250, 1_000},
		{7, 12, 80, 400, 900, 4_000, 60_000},
		{2, 15, 120, 500, 2_000, 10_000, MaxDurationMilliseconds},
	}
	all := New()
	parts := make([]*DurationSketch, len(partitions))
	for index, values := range partitions {
		parts[index] = New()
		for _, value := range values {
			_, err := parts[index].Add(value)
			require.NoError(t, err)
			_, err = all.Add(value)
			require.NoError(t, err)
		}
	}

	left := parts[0].Clone()
	require.NoError(t, left.Merge(parts[1]))
	require.NoError(t, left.Merge(parts[2]))
	right := parts[2].Clone()
	require.NoError(t, right.Merge(parts[0]))
	require.NoError(t, right.Merge(parts[1]))

	assert.Equal(t, all.Count(), left.Count())
	assert.Equal(t, all.Count(), right.Count())
	assert.Equal(t, int64(len(partitions[0])), parts[0].Count(), "merge must not mutate the source")
	for _, quantile := range []float64{0.5, 0.95, 0.99} {
		expected, err := all.Quantile(quantile)
		require.NoError(t, err)
		leftValue, err := left.Quantile(quantile)
		require.NoError(t, err)
		rightValue, err := right.Quantile(quantile)
		require.NoError(t, err)
		assert.Equal(t, expected, leftValue)
		assert.Equal(t, expected, rightValue)
		expectedRaw := map[float64]float64{
			0.5:  120,
			0.95: 60_000,
			0.99: 60_000,
		}[quantile]
		assert.InDelta(t, expectedRaw, expected.ValueMilliseconds, expectedRaw*RelativeAccuracy+1)
	}
}

func TestDurationSketchMarshalUsesOfficialProtobufAndStaysBounded(t *testing.T) {
	sketch := NewDurationSketch()
	gamma := (1 + RelativeAccuracy) / (1 - RelativeAccuracy)
	for value := 1.0; value <= float64(MaxDurationMilliseconds); value *= gamma {
		_, err := sketch.AddMillis(int64(math.Round(value)))
		require.NoError(t, err)
	}
	_, err := sketch.AddMillis(MaxDurationMilliseconds)
	require.NoError(t, err)

	encoded, err := sketch.MarshalBinary()
	require.NoError(t, err)
	assert.LessOrEqual(t, len(encoded), MaxEncodedBytes)

	var message sketchpb.DDSketch
	require.NoError(t, proto.Unmarshal(encoded, &message))
	require.NotNil(t, message.Mapping)
	assert.InDelta(t, RelativeAccuracy, 1-2/(1+message.Mapping.Gamma), 1e-15)
	assert.Empty(t, message.NegativeValues.GetContiguousBinCounts())
	assert.LessOrEqual(t, len(message.PositiveValues.GetContiguousBinCounts()), MaxBins)

	decoded, err := DecodeDurationSketch(encoded, SketchCodecVersion)
	require.NoError(t, err)
	assert.Equal(t, sketch.Count(), decoded.Count())
}

func TestDurationSketchCloneIsIndependent(t *testing.T) {
	original := New()
	_, err := original.Add(100)
	require.NoError(t, err)

	clone := original.Clone()
	_, err = clone.Add(1_000)
	require.NoError(t, err)

	assert.Equal(t, int64(1), original.Count())
	assert.Equal(t, int64(2), clone.Count())
}

func TestDecodeRejectsInvalidPayloads(t *testing.T) {
	validSketch := New()
	_, err := validSketch.Add(100)
	require.NoError(t, err)
	valid, err := validSketch.Marshal()
	require.NoError(t, err)
	validMessage := decodeProtoForTest(t, valid)

	tests := []struct {
		name    string
		version int
		payload func() []byte
		wantErr error
	}{
		{name: "unsupported version", version: CodecVersion + 1, payload: func() []byte { return valid }, wantErr: ErrUnsupportedCodec},
		{name: "empty", version: CodecVersion, payload: func() []byte { return nil }, wantErr: ErrInvalidPayload},
		{name: "oversized", version: CodecVersion, payload: func() []byte { return make([]byte, MaxEncodedBytes+1) }, wantErr: ErrPayloadTooLarge},
		{name: "malformed protobuf", version: CodecVersion, payload: func() []byte { return []byte{0xff} }, wantErr: ErrInvalidPayload},
		{
			name:    "unknown top level field",
			version: CodecVersion,
			payload: func() []byte {
				payload := append([]byte(nil), valid...)
				payload = protowire.AppendTag(payload, 100, protowire.VarintType)
				return protowire.AppendVarint(payload, 1)
			},
			wantErr: ErrInvalidPayload,
		},
		{
			name:    "unknown nested field",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				unknown := protowire.AppendTag(nil, 100, protowire.VarintType)
				message.Mapping.ProtoReflect().SetUnknown(protowire.AppendVarint(unknown, 1))
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrInvalidPayload,
		},
		{
			name:    "wrong gamma",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				message.Mapping.Gamma *= 2
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrInvalidMapping,
		},
		{
			name:    "wrong offset",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				message.Mapping.IndexOffset = 1
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrInvalidMapping,
		},
		{
			name:    "wrong interpolation",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				message.Mapping.Interpolation = sketchpb.IndexMapping_LINEAR
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrInvalidMapping,
		},
		{
			name:    "too many bins",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				message.PositiveValues = &sketchpb.Store{ContiguousBinCounts: make([]float64, MaxBins+1)}
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrInvalidBins,
		},
		{
			name:    "sparse bins",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				message.PositiveValues.BinCounts = map[int32]float64{0: 1}
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrInvalidBins,
		},
		{
			name:    "negative store",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				message.NegativeValues = &sketchpb.Store{ContiguousBinCounts: []float64{1}}
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrNegativeValues,
		},
		{
			name:    "nan zero count",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				message.ZeroCount = math.NaN()
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrInvalidCount,
		},
		{
			name:    "count exceeds exact integer range",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				message.ZeroCount = float64(maxExactCount) + 1
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrInvalidCount,
		},
		{
			name:    "infinite bin count",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				message.PositiveValues.ContiguousBinCounts[0] = math.Inf(1)
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrInvalidCount,
		},
		{
			name:    "negative bin count",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				message.PositiveValues.ContiguousBinCounts[0] = -1
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrInvalidCount,
		},
		{
			name:    "fractional bin count",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				message.PositiveValues.ContiguousBinCounts[0] = 1.5
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrInvalidCount,
		},
		{
			name:    "bin below duration range",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				message.PositiveValues.ContiguousBinIndexOffset = -1
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrInvalidBins,
		},
		{
			name:    "bin above duration range",
			version: CodecVersion,
			payload: func() []byte {
				message := proto.Clone(validMessage).(*sketchpb.DDSketch)
				message.PositiveValues.ContiguousBinIndexOffset = 1_000_000
				return marshalProtoForTest(t, message)
			},
			wantErr: ErrInvalidBins,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var decodeErr error
			assert.NotPanics(t, func() {
				_, decodeErr = Decode(test.payload(), test.version)
			})
			assert.ErrorIs(t, decodeErr, test.wantErr)
		})
	}
}

func TestDurationSketchNilAndInvalidStateDoNotPanic(t *testing.T) {
	var nilSketch *DurationSketch
	assert.Zero(t, nilSketch.Count())
	assert.Nil(t, nilSketch.Clone())
	_, err := nilSketch.Add(1)
	assert.ErrorIs(t, err, ErrInvalidSketch)
	_, err = nilSketch.Marshal()
	assert.ErrorIs(t, err, ErrInvalidSketch)
	_, err = nilSketch.Quantile(0.5)
	assert.ErrorIs(t, err, ErrInvalidSketch)
	assert.ErrorIs(t, nilSketch.Merge(New()), ErrInvalidSketch)

	invalid := &DurationSketch{}
	_, err = invalid.Marshal()
	assert.ErrorIs(t, err, ErrInvalidSketch)
	assert.ErrorIs(t, New().Merge(nil), ErrInvalidSketch)
}

func TestDecodeErrorsAreClassifiable(t *testing.T) {
	_, err := Decode(nil, CodecVersion)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidPayload))
}

func decodeProtoForTest(t *testing.T, data []byte) *sketchpb.DDSketch {
	t.Helper()
	var message sketchpb.DDSketch
	require.NoError(t, proto.Unmarshal(data, &message))
	return &message
}

func marshalProtoForTest(t *testing.T, message *sketchpb.DDSketch) []byte {
	t.Helper()
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	require.NoError(t, err)
	return data
}
