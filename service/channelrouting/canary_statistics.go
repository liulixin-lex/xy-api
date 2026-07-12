package channelrouting

import (
	"math"
	"math/bits"
)

const (
	canaryBasisPointScale = 10_000
	canaryWilson95Z       = 1.959963984540054
)

func canaryBasisPoints(part int64, total int64) int {
	if part <= 0 || total <= 0 {
		return 0
	}
	if part >= total {
		return canaryBasisPointScale
	}
	high, low := bits.Mul64(uint64(part), canaryBasisPointScale)
	quotient, _ := bits.Div64(high, low, uint64(total))
	return int(quotient)
}

func canaryRatioBasisPoints(numerator float64, denominator float64) (int64, bool) {
	if math.IsNaN(numerator) || math.IsInf(numerator, 0) || numerator < 0 ||
		math.IsNaN(denominator) || math.IsInf(denominator, 0) || denominator <= 0 {
		return 0, false
	}
	ratio := numerator / denominator
	if math.IsInf(ratio, 1) {
		return math.MaxInt64, true
	}
	scaled := math.Round(ratio * canaryBasisPointScale)
	if math.IsInf(scaled, 1) || scaled >= float64(math.MaxInt64) {
		return math.MaxInt64, true
	}
	return int64(scaled), true
}

func canaryWilsonInterval(successes int64, total int64) (float64, float64, bool) {
	if total <= 0 || successes < 0 || successes > total {
		return 0, 0, false
	}
	n := float64(total)
	proportion := float64(successes) / n
	zSquared := canaryWilson95Z * canaryWilson95Z
	denominator := 1 + zSquared/n
	center := (proportion + zSquared/(2*n)) / denominator
	margin := canaryWilson95Z * math.Sqrt((proportion*(1-proportion)+zSquared/(4*n))/n) / denominator
	lower := math.Max(0, center-margin)
	upper := math.Min(1, center+margin)
	if math.IsNaN(lower) || math.IsInf(lower, 0) || math.IsNaN(upper) || math.IsInf(upper, 0) || lower > upper {
		return 0, 0, false
	}
	return lower, upper, true
}
