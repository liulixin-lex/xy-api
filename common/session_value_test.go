package common

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSessionValueInt64AcceptsExactSerializedIntegers(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value any
		want  int64
	}{
		{name: "int", value: int(42), want: 42},
		{name: "int64", value: int64(42), want: 42},
		{name: "uint64", value: uint64(42), want: 42},
		{name: "json number", value: json.Number("42"), want: 42},
		{name: "decimal string", value: "42", want: 42},
		{name: "json float", value: float64(42), want: 42},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := SessionValueInt64(testCase.value)
			assert.True(t, ok)
			assert.Equal(t, testCase.want, got)
		})
	}
}

func TestSessionValueInt64RejectsUnsafeSerializedValues(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value any
	}{
		{name: "fractional", value: 42.5},
		{name: "nan", value: math.NaN()},
		{name: "infinity", value: math.Inf(1)},
		{name: "unsafe float integer", value: float64(maxExactSessionFloat) + 1},
		{name: "overflowing unsigned", value: uint64(math.MaxUint64)},
		{name: "decimal JSON number", value: json.Number("42.0")},
		{name: "nonnumeric string", value: "forty-two"},
		{name: "unsupported type", value: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, ok := SessionValueInt64(testCase.value)
			assert.False(t, ok)
		})
	}
}

func TestSessionValueIntChecksBoundsBeforeConversion(t *testing.T) {
	maximum, ok := SessionValueInt(json.Number(strconv.FormatInt(int64(math.MaxInt), 10)))
	assert.True(t, ok)
	assert.Equal(t, math.MaxInt, maximum)

	minimum, ok := SessionValueInt(json.Number(strconv.FormatInt(int64(math.MinInt), 10)))
	assert.True(t, ok)
	assert.Equal(t, math.MinInt, minimum)

	if strconv.IntSize == 32 {
		_, ok = SessionValueInt(int64(math.MaxInt32) + 1)
		assert.False(t, ok)
		_, ok = SessionValueInt(int64(math.MinInt32) - 1)
		assert.False(t, ok)
	}
}
