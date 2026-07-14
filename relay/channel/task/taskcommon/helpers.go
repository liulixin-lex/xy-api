package taskcommon

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
)

// UnmarshalMetadata converts a map[string]any metadata to a typed struct via JSON round-trip.
// This replaces the repeated pattern: json.Marshal(metadata) → json.Unmarshal(bytes, &target).
func UnmarshalMetadata(metadata map[string]any, target any) error {
	if metadata == nil {
		return nil
	}
	// Prevent metadata from overriding model fields to avoid billing bypass.
	delete(metadata, "model")
	metaBytes, err := common.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata failed: %w", err)
	}
	if err := common.Unmarshal(metaBytes, target); err != nil {
		return fmt.Errorf("unmarshal metadata failed: %w", err)
	}
	return nil
}

// NormalizeMetadataInt canonicalizes a user-controlled metadata multiplier so
// typed adaptor DTOs and billing see the same bounded integer.
func NormalizeMetadataInt(metadata map[string]any, key string, minValue, maxValue int) (int, bool, error) {
	if metadata == nil {
		return 0, false, nil
	}
	raw, present := metadata[key]
	if !present {
		return 0, false, nil
	}
	if minValue > maxValue {
		return 0, true, fmt.Errorf("invalid bounds for metadata field %s", key)
	}
	var value int64
	switch typed := raw.(type) {
	case int:
		value = int64(typed)
	case int8:
		value = int64(typed)
	case int16:
		value = int64(typed)
	case int32:
		value = int64(typed)
	case int64:
		value = typed
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, true, fmt.Errorf("metadata field %s is out of range", key)
		}
		value = int64(typed)
	case uint8:
		value = int64(typed)
	case uint16:
		value = int64(typed)
	case uint32:
		value = int64(typed)
	case uint64:
		if typed > math.MaxInt64 {
			return 0, true, fmt.Errorf("metadata field %s is out of range", key)
		}
		value = int64(typed)
	case float32:
		floatValue := float64(typed)
		if math.IsNaN(floatValue) || math.IsInf(floatValue, 0) || math.Trunc(floatValue) != floatValue ||
			floatValue < math.MinInt64 || floatValue > math.MaxInt64 {
			return 0, true, fmt.Errorf("metadata field %s must be a finite integer", key)
		}
		value = int64(floatValue)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed ||
			typed < math.MinInt64 || typed > math.MaxInt64 {
			return 0, true, fmt.Errorf("metadata field %s must be a finite integer", key)
		}
		value = int64(typed)
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		if err != nil {
			return 0, true, fmt.Errorf("metadata field %s must be an integer: %w", key, err)
		}
		value = parsed
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, true, fmt.Errorf("metadata field %s must be an integer: %w", key, err)
		}
		value = parsed
	default:
		return 0, true, fmt.Errorf("metadata field %s must be an integer", key)
	}
	if value < int64(minValue) || value > int64(maxValue) {
		return 0, true, fmt.Errorf("metadata field %s must be between %d and %d", key, minValue, maxValue)
	}
	canonical := int(value)
	metadata[key] = canonical
	return canonical, true, nil
}

// DefaultString returns val if non-empty, otherwise fallback.
func DefaultString(val, fallback string) string {
	if val == "" {
		return fallback
	}
	return val
}

// DefaultInt returns val if non-zero, otherwise fallback.
func DefaultInt(val, fallback int) int {
	if val == 0 {
		return fallback
	}
	return val
}

// EncodeLocalTaskID encodes an upstream operation name to a URL-safe base64 string.
// Used by Gemini/Vertex to store upstream names as task IDs.
func EncodeLocalTaskID(name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(name))
}

// DecodeLocalTaskID decodes a base64-encoded upstream operation name.
func DecodeLocalTaskID(id string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// BuildProxyURL constructs the video proxy URL using the public task ID.
// e.g., "https://your-server.com/v1/videos/task_xxxx/content"
func BuildProxyURL(taskID string) string {
	return fmt.Sprintf("%s/v1/videos/%s/content", strings.TrimRight(system_setting.ServerAddress, "/"), url.PathEscape(taskID))
}

// Status-to-progress mapping constants for polling updates.
const (
	ProgressSubmitted  = "10%"
	ProgressQueued     = "20%"
	ProgressInProgress = "30%"
	ProgressComplete   = "100%"
)

// ---------------------------------------------------------------------------
// BaseBilling — embeddable no-op implementations for TaskAdaptor billing methods.
// Adaptors that do not need custom billing can embed this struct directly.
// ---------------------------------------------------------------------------

type BaseBilling struct{}

// EstimateBilling returns nil (no extra ratios; use base model price).
func (BaseBilling) EstimateBilling(_ *gin.Context, _ *relaycommon.RelayInfo) map[string]float64 {
	return nil
}

// AdjustBillingOnSubmit returns nil (no submit-time adjustment).
func (BaseBilling) AdjustBillingOnSubmit(_ *relaycommon.RelayInfo, _ []byte) map[string]float64 {
	return nil
}

// AdjustBillingOnComplete returns 0 (keep pre-charged amount).
func (BaseBilling) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}
