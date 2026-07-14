package service

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const (
	DefaultMaxUpstreamResponseBytes int64 = 64 << 20
	MaxPublicTaskDataBytes                = 4 << 20
	maxTaskDataSanitizeDepth              = 64
)

var ErrUpstreamResponseBodyTooLarge = errors.New("upstream response body is too large")

func ReadUpstreamResponseBody(body io.Reader, maxBytes int64) ([]byte, error) {
	if body == nil || maxBytes <= 0 {
		return nil, errors.New("upstream response body reader is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrUpstreamResponseBodyTooLarge
	}
	return data, nil
}

func UnparseableUpstreamResponseError(cause error, data []byte) error {
	digest := sha256.Sum256(data)
	if cause == nil {
		cause = errors.New("upstream response could not be parsed")
	}
	return fmt.Errorf(
		"unparseable upstream response: body_bytes=%d body_sha256=%x: %w",
		len(data),
		digest,
		cause,
	)
}

// SanitizeTaskData removes credential material, signed URLs, and embedded
// binary payloads before task data is persisted or returned by public APIs.
// Invalid or still-oversized payloads are replaced with a stable fingerprint so
// operators retain diagnostic evidence without retaining the original body.
func SanitizeTaskData(data []byte) json.RawMessage {
	if len(data) == 0 {
		return nil
	}

	var payload any
	if err := common.Unmarshal(data, &payload); err != nil {
		return taskDataFingerprint(data, "invalid_json")
	}

	sanitized := sanitizeTaskDataValue(payload, "", 0)
	encoded, err := common.Marshal(sanitized)
	if err != nil {
		return taskDataFingerprint(data, "marshal_failed")
	}
	if len(encoded) > MaxPublicTaskDataBytes {
		return taskDataFingerprint(data, "sanitized_too_large")
	}
	return json.RawMessage(encoded)
}

func sanitizeTaskDataValue(value any, field string, depth int) any {
	if depth > maxTaskDataSanitizeDepth {
		return "[redacted_depth_limit]"
	}

	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, child := range typed {
			if taskDataSensitiveField(key) {
				clean[key] = "[redacted]"
				continue
			}
			if taskDataBinaryField(key) {
				clean[key] = "[redacted_binary]"
				continue
			}
			clean[key] = sanitizeTaskDataValue(child, key, depth+1)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for index, child := range typed {
			clean[index] = sanitizeTaskDataValue(child, field, depth+1)
		}
		return clean
	case string:
		trimmed := strings.TrimSpace(typed)
		if strings.HasPrefix(strings.ToLower(trimmed), "data:") && strings.Contains(strings.ToLower(trimmed), ";base64,") {
			return "[redacted_binary]"
		}
		if taskDataLikelyBase64(trimmed) && taskDataBinaryValueField(field) {
			return "[redacted_binary]"
		}
		if taskDataExternalURL(trimmed) {
			return "[redacted_external_url]"
		}
		return common.SanitizeErrorMessage(typed)
	default:
		return value
	}
}

func taskDataSensitiveField(field string) bool {
	normalized := normalizeTaskDataField(field)
	switch normalized {
	case "key", "token", "authorization", "proxyauthorization", "cookie", "setcookie",
		"password", "passwd", "pwd", "credential", "credentials", "jwt":
		return true
	}
	return strings.Contains(normalized, "apikey") ||
		strings.Contains(normalized, "accesstoken") ||
		strings.Contains(normalized, "refreshtoken") ||
		strings.Contains(normalized, "oauthtoken") ||
		strings.Contains(normalized, "clientsecret") ||
		strings.Contains(normalized, "secretkey") ||
		strings.Contains(normalized, "accesskey") ||
		strings.Contains(normalized, "securitytoken") ||
		strings.Contains(normalized, "signature") ||
		strings.Contains(normalized, "signedheaders")
}

func taskDataBinaryField(field string) bool {
	normalized := normalizeTaskDataField(field)
	return strings.Contains(normalized, "base64") ||
		strings.Contains(normalized, "binarydata") ||
		strings.Contains(normalized, "binarypayload")
}

func taskDataBinaryValueField(field string) bool {
	normalized := normalizeTaskDataField(field)
	switch normalized {
	case "video", "audio", "image", "data", "payload", "content":
		return true
	default:
		return strings.Contains(normalized, "binary")
	}
}

func taskDataLikelyBase64(value string) bool {
	if len(value) < 512 {
		return false
	}
	for _, char := range value {
		switch {
		case char >= 'A' && char <= 'Z':
		case char >= 'a' && char <= 'z':
		case char >= '0' && char <= '9':
		case char == '+', char == '/', char == '=', char == '-', char == '_':
		default:
			return false
		}
	}
	return true
}

func taskDataExternalURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.Host == "" ||
		(!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) {
		return false
	}
	return true
}

func normalizeTaskDataField(field string) string {
	var builder strings.Builder
	builder.Grow(len(field))
	for _, char := range strings.ToLower(field) {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func taskDataFingerprint(data []byte, bodyType string) json.RawMessage {
	digest := sha256.Sum256(data)
	marker, err := common.Marshal(map[string]any{
		"body_bytes":  len(data),
		"body_sha256": fmt.Sprintf("%x", digest),
		"body_type":   bodyType,
		"redacted":    true,
	})
	if err != nil {
		return json.RawMessage(`{"redacted":true}`)
	}
	return json.RawMessage(marker)
}
