package channelrouting

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCanaryBasisPointsIsExactAndBounded(t *testing.T) {
	tests := []struct {
		name        string
		numerator   int64
		denominator int64
		want        int
	}{
		{name: "zero samples", numerator: 0, denominator: 0, want: 0},
		{name: "positive over zero", numerator: 1, denominator: 0, want: 0},
		{name: "negative numerator", numerator: -1, denominator: 10, want: 0},
		{name: "negative denominator", numerator: 1, denominator: -10, want: 0},
		{name: "all failures", numerator: 0, denominator: 10, want: 0},
		{name: "all successes", numerator: 10, denominator: 10, want: 10_000},
		{name: "invalid excess clamps", numerator: 11, denominator: 10, want: 10_000},
		{name: "one third truncates deterministically", numerator: 1, denominator: 3, want: 3_333},
		{name: "threshold below", numerator: 8_999, denominator: 10_000, want: 8_999},
		{name: "threshold exact", numerator: 9, denominator: 10, want: 9_000},
		{name: "maximum counts", numerator: math.MaxInt64, denominator: math.MaxInt64, want: 10_000},
		{name: "maximum counts below full", numerator: math.MaxInt64 - 1, denominator: math.MaxInt64, want: 9_999},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, canaryBasisPoints(test.numerator, test.denominator))
		})
	}
}

func TestCanaryRatioBasisPointsDefinesInvalidAndSaturationSemantics(t *testing.T) {
	tests := []struct {
		name        string
		numerator   float64
		denominator float64
		want        int64
		wantKnown   bool
	}{
		{name: "zero over zero is unknown", numerator: 0, denominator: 0},
		{name: "zero over positive", numerator: 0, denominator: 1, wantKnown: true},
		{name: "positive over zero is unknown", numerator: 1, denominator: 0},
		{name: "negative numerator", numerator: -1, denominator: 1},
		{name: "negative denominator", numerator: 1, denominator: -1},
		{name: "nan numerator", numerator: math.NaN(), denominator: 1},
		{name: "nan denominator", numerator: 1, denominator: math.NaN()},
		{name: "positive infinity is unknown", numerator: math.Inf(1), denominator: 1},
		{name: "infinity over infinity is unknown", numerator: math.Inf(1), denominator: math.Inf(1)},
		{name: "finite over infinity is unknown", numerator: 1, denominator: math.Inf(1)},
		{name: "negative infinity", numerator: math.Inf(-1), denominator: 1},
		{name: "one to one", numerator: 1, denominator: 1, want: 10_000, wantKnown: true},
		{name: "threshold below", numerator: 1.1999, denominator: 1, want: 11_999, wantKnown: true},
		{name: "threshold exact", numerator: 1.2, denominator: 1, want: 12_000, wantKnown: true},
		{name: "threshold above", numerator: 1.2001, denominator: 1, want: 12_001, wantKnown: true},
		{name: "finite overflowing ratio saturates", numerator: math.MaxFloat64, denominator: math.SmallestNonzeroFloat64, want: math.MaxInt64, wantKnown: true},
		{name: "scaled int64 boundary saturates", numerator: float64(math.MaxInt64) / 10_000, denominator: 1, want: math.MaxInt64, wantKnown: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, known := canaryRatioBasisPoints(test.numerator, test.denominator)
			assert.Equal(t, test.wantKnown, known)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestCanaryWilson95IntervalCoversBoundariesAndExtremeCounts(t *testing.T) {
	tests := []struct {
		name      string
		successes int64
		samples   int64
		wantLower float64
		wantUpper float64
		wantKnown bool
	}{
		{name: "zero samples is unknown", successes: 0, samples: 0},
		{name: "negative input is unknown", successes: -1, samples: 10},
		{name: "successes above samples is unknown", successes: 11, samples: 10},
		{name: "one failure", successes: 0, samples: 1, wantLower: 0, wantUpper: 0.7934506856227626, wantKnown: true},
		{name: "one success", successes: 1, samples: 1, wantLower: 0.20654931437723745, wantUpper: 1, wantKnown: true},
		{name: "balanced sample", successes: 50, samples: 100, wantLower: 0.4038315303659956, wantUpper: 0.5961684696340044, wantKnown: true},
		{name: "ninety percent", successes: 90, samples: 100, wantLower: 0.8256343384950865, wantUpper: 0.9447708629393249, wantKnown: true},
		{name: "threshold below", successes: 8_999, samples: 10_000, wantLower: 0.893863059890281, wantUpper: 0.9056298182128519, wantKnown: true},
		{name: "threshold exact", successes: 9_000, samples: 10_000, wantLower: 0.8939656314740893, wantUpper: 0.9057271698293695, wantKnown: true},
		{name: "extreme all failures", successes: 0, samples: math.MaxInt64, wantKnown: true},
		{name: "extreme all successes", successes: math.MaxInt64, samples: math.MaxInt64, wantLower: 1, wantUpper: 1, wantKnown: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lower, upper, known := canaryWilsonInterval(test.successes, test.samples)
			assert.Equal(t, test.wantKnown, known)
			assert.InDelta(t, test.wantLower, lower, 1e-15)
			assert.InDelta(t, test.wantUpper, upper, 1e-15)
		})
	}
}
