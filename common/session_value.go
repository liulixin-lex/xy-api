package common

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

const maxExactSessionFloat = int64(1<<53 - 1)

// SessionValueInt64 decodes an integer written by the supported session
// serializers. It accepts only exact, bounded integers so a serializer change
// cannot turn a fractional, overflowing, NaN, or infinite value into an
// authorization decision.
func SessionValueInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed ||
			typed < -float64(maxExactSessionFloat) || typed > float64(maxExactSessionFloat) {
			return 0, false
		}
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

// SessionValueInt applies the current platform's int bounds after decoding.
func SessionValueInt(value any) (int, bool) {
	parsed, ok := SessionValueInt64(value)
	if !ok {
		return 0, false
	}
	result, err := strconv.Atoi(strconv.FormatInt(parsed, 10))
	if err != nil {
		return 0, false
	}
	return result, true
}
