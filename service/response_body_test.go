package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadUpstreamResponseBodyEnforcesHardLimit(t *testing.T) {
	body, err := ReadUpstreamResponseBody(strings.NewReader("1234"), 4)
	require.NoError(t, err)
	assert.Equal(t, []byte("1234"), body)

	_, err = ReadUpstreamResponseBody(strings.NewReader("12345"), 4)
	require.ErrorIs(t, err, ErrUpstreamResponseBodyTooLarge)
}

func TestUnparseableUpstreamResponseErrorKeepsOnlyBoundedMetadata(t *testing.T) {
	cause := errors.New("invalid JSON")
	err := UnparseableUpstreamResponseError(cause, []byte("secret response"))

	require.ErrorIs(t, err, cause)
	require.NotContains(t, err.Error(), "secret response")
	require.Contains(t, err.Error(), "body_bytes=15")
	require.Contains(t, err.Error(), "body_sha256=fc25e63e5de7566f3a30baef34e477c82df66f2864a35aa693624a533886e928")
}

func TestSanitizeTaskDataRecursivelyRemovesSecretsSignedURLsAndBinary(t *testing.T) {
	raw := []byte(`{
		"model":"video-model",
			"total_tokens":42,
			"safe_url":"https://cdn.example.com/video.mp4",
			"signed_url":"https://cdn.example.com/video.mp4?X-Amz-Credential=cred&X-Amz-Signature=secret",
			"message":"Authorization: Bearer sk-secret at https://provider.example/video.mp4",
		"nested":[{"api_key":"sk-secret","bytesBase64Encoded":"AAAA","video":"` + strings.Repeat("A", 512) + `"}]
	}`)

	sanitized := SanitizeTaskData(raw)
	var payload map[string]any
	require.NoError(t, common.Unmarshal(sanitized, &payload))
	assert.Equal(t, "video-model", payload["model"])
	assert.Equal(t, float64(42), payload["total_tokens"])
	assert.Equal(t, "[redacted_external_url]", payload["safe_url"])
	assert.Equal(t, "[redacted_external_url]", payload["signed_url"])
	assert.NotContains(t, payload["message"], "sk-secret")
	assert.NotContains(t, payload["message"], "provider.example")

	nested, ok := payload["nested"].([]any)
	require.True(t, ok)
	require.Len(t, nested, 1)
	item, ok := nested[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "[redacted]", item["api_key"])
	assert.Equal(t, "[redacted_binary]", item["bytesBase64Encoded"])
	assert.Equal(t, "[redacted_binary]", item["video"])
	assert.NotContains(t, string(sanitized), "sk-secret")
	assert.NotContains(t, string(sanitized), "X-Amz-Signature")
}

func TestSanitizeTaskDataReplacesInvalidPayloadWithFingerprint(t *testing.T) {
	raw := []byte("secret non-json task response")
	sanitized := SanitizeTaskData(raw)

	var payload map[string]any
	require.NoError(t, common.Unmarshal(sanitized, &payload))
	assert.Equal(t, true, payload["redacted"])
	assert.Equal(t, float64(len(raw)), payload["body_bytes"])
	assert.Equal(t, "invalid_json", payload["body_type"])
	assert.NotEmpty(t, payload["body_sha256"])
	assert.NotContains(t, string(sanitized), "secret non-json")
}
