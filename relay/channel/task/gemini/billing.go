package gemini

import (
	"strconv"
	"strings"

	taskcommon "github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// ParseVeoDurationSeconds extracts durationSeconds from metadata.
// Returns 8 (Veo default) when not specified or invalid.
func ParseVeoDurationSeconds(metadata map[string]any) (int, bool, error) {
	return taskcommon.NormalizeMetadataInt(metadata, "durationSeconds", 1, relaycommon.MaxTaskDurationSeconds)
}

// ParseVeoResolution extracts resolution from metadata.
// Returns "720p" when not specified.
func ParseVeoResolution(metadata map[string]any) string {
	if metadata == nil {
		return "720p"
	}
	v, ok := metadata["resolution"]
	if !ok {
		return "720p"
	}
	if s, ok := v.(string); ok && s != "" {
		return strings.ToLower(s)
	}
	return "720p"
}

// ResolveVeoDuration returns the effective duration in seconds.
// Priority: metadata["durationSeconds"] > stdDuration > stdSeconds > default (8).
// The result is capped because it is used as a billing multiplier and the
// metadata path bypasses standard request validation.
func ResolveVeoDuration(metadata map[string]any, stdDuration int, stdSeconds string) (int, error) {
	if duration, present, err := ParseVeoDurationSeconds(metadata); err != nil {
		return 0, err
	} else if present {
		return duration, nil
	}
	if stdDuration > 0 {
		if stdDuration > relaycommon.MaxTaskDurationSeconds {
			return 0, strconv.ErrRange
		}
		return stdDuration, nil
	}
	if strings.TrimSpace(stdSeconds) != "" {
		seconds, err := strconv.ParseInt(strings.TrimSpace(stdSeconds), 10, 0)
		if err != nil || seconds <= 0 || seconds > relaycommon.MaxTaskDurationSeconds {
			if err != nil {
				return 0, err
			}
			return 0, strconv.ErrRange
		}
		return int(seconds), nil
	}
	return 8, nil
}

// ResolveVeoResolution returns the effective resolution string (lowercase).
// Priority: metadata["resolution"] > SizeToVeoResolution(stdSize) > default ("720p").
func ResolveVeoResolution(metadata map[string]any, stdSize string) string {
	if metadata != nil {
		if _, exists := metadata["resolution"]; exists {
			if r := ParseVeoResolution(metadata); r != "" {
				return r
			}
		}
	}
	if stdSize != "" {
		return SizeToVeoResolution(stdSize)
	}
	return "720p"
}

// SizeToVeoResolution converts a "WxH" size string to a Veo resolution label.
func SizeToVeoResolution(size string) string {
	parts := strings.SplitN(strings.ToLower(size), "x", 2)
	if len(parts) != 2 {
		return "720p"
	}
	w, _ := strconv.Atoi(parts[0])
	h, _ := strconv.Atoi(parts[1])
	maxDim := w
	if h > maxDim {
		maxDim = h
	}
	if maxDim >= 3840 {
		return "4k"
	}
	if maxDim >= 1920 {
		return "1080p"
	}
	return "720p"
}

// SizeToVeoAspectRatio converts a "WxH" size string to a Veo aspect ratio.
func SizeToVeoAspectRatio(size string) string {
	parts := strings.SplitN(strings.ToLower(size), "x", 2)
	if len(parts) != 2 {
		return "16:9"
	}
	w, _ := strconv.Atoi(parts[0])
	h, _ := strconv.Atoi(parts[1])
	if w <= 0 || h <= 0 {
		return "16:9"
	}
	if h > w {
		return "9:16"
	}
	return "16:9"
}

// VeoResolutionRatio returns the pricing multiplier for the given resolution.
// Standard resolutions (720p, 1080p) return 1.0.
// 4K returns a model-specific multiplier based on Google's official pricing.
func VeoResolutionRatio(modelName, resolution string) float64 {
	if resolution != "4k" {
		return 1.0
	}
	// 4K multipliers derived from Vertex AI official pricing (video+audio base):
	//   veo-3.1-generate:      $0.60 / $0.40 = 1.5
	//   veo-3.1-fast-generate: $0.35 / $0.15 ≈ 2.333
	// Veo 3.0 models do not support 4K; return 1.0 as fallback.
	if strings.Contains(modelName, "3.1-fast-generate") {
		return 2.333333
	}
	if strings.Contains(modelName, "3.1-generate") || strings.Contains(modelName, "3.1") {
		return 1.5
	}
	return 1.0
}
