package controller

import (
	"bytes"
	"compress/gzip"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadRoutingCostJSONValidatesContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		wantErr     bool
	}{
		{name: "JSON", contentType: "application/json"},
		{name: "JSON with charset", contentType: "application/json; charset=utf-8"},
		{name: "problem JSON", contentType: "application/problem+json"},
		{name: "empty structured suffix", contentType: "application/+json", wantErr: true},
		{name: "wildcard structured suffix", contentType: "application/*+json", wantErr: true},
		{name: "missing", wantErr: true},
		{name: "malformed", contentType: "application/json; charset", wantErr: true},
		{name: "text JSON", contentType: "text/json", wantErr: true},
		{name: "HTML", contentType: "text/html", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := routingJSONResponse(tt.contentType, "", []byte(`{"ok":true}`))
			body, err := readRoutingCostJSON(response, routingJSONLimits{WireBytes: 64, DecodedBytes: 64})
			if tt.wantErr {
				require.Error(t, err)
				assert.Empty(t, body)
				return
			}
			require.NoError(t, err)
			assert.JSONEq(t, `{"ok":true}`, string(body))
		})
	}
}

func TestReadRoutingCostJSONRejectsDeclaredWireLengthBeforeReading(t *testing.T) {
	reader := &countingReadCloser{Reader: strings.NewReader("123456789")}
	response := &http.Response{
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          reader,
		ContentLength: 9,
	}

	body, err := readRoutingCostJSON(response, routingJSONLimits{WireBytes: 8, DecodedBytes: 16})

	require.Error(t, err)
	assert.Empty(t, body)
	assert.Zero(t, reader.readBytes)
}

func TestReadRoutingCostJSONEnforcesUnknownWireLengthAtLimitPlusOne(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "exact limit", body: "12345678"},
		{name: "limit plus one", body: "123456789", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := routingJSONResponse("application/json", "", []byte(tt.body))
			response.ContentLength = -1

			body, err := readRoutingCostJSON(response, routingJSONLimits{WireBytes: 8, DecodedBytes: 16})
			if tt.wantErr {
				require.Error(t, err)
				assert.Empty(t, body)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.body, string(body))
		})
	}
}

func TestReadRoutingCostJSONEnforcesGzipWireAndDecodedLimits(t *testing.T) {
	compressed := gzipBytes(t, []byte(`{"ok":true}`))
	response := routingJSONResponse("application/json", "gzip", compressed)
	body, err := readRoutingCostJSON(response, routingJSONLimits{
		WireBytes:    int64(len(compressed)),
		DecodedBytes: 16,
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(body))

	response = routingJSONResponse("application/json", "gzip", compressed)
	body, err = readRoutingCostJSON(response, routingJSONLimits{
		WireBytes:    int64(len(compressed) - 1),
		DecodedBytes: 16,
	})
	require.Error(t, err)
	assert.Empty(t, body)

	bomb := gzipBytes(t, bytes.Repeat([]byte("x"), 32))
	response = routingJSONResponse("application/json", "gzip", bomb)
	body, err = readRoutingCostJSON(response, routingJSONLimits{
		WireBytes:    int64(len(bomb)),
		DecodedBytes: 16,
	})
	require.Error(t, err)
	assert.Empty(t, body)
}

func TestReadRoutingCostJSONRejectsUnsupportedOrMultipleContentEncoding(t *testing.T) {
	for _, encoding := range []string{"br", "deflate", "gzip, br"} {
		t.Run(encoding, func(t *testing.T) {
			response := routingJSONResponse("application/json", encoding, []byte(`{"ok":true}`))
			body, err := readRoutingCostJSON(response, routingJSONLimits{WireBytes: 64, DecodedBytes: 64})
			require.Error(t, err)
			assert.Empty(t, body)
		})
	}

	response := routingJSONResponse("application/json", "gzip", []byte(`{"ok":true}`))
	response.Header["Content-Encoding"] = []string{"gzip", "br"}
	body, err := readRoutingCostJSON(response, routingJSONLimits{WireBytes: 64, DecodedBytes: 64})
	require.Error(t, err)
	assert.Empty(t, body)
}

func TestReadRoutingCostJSONRejectsMultipleContentTypesAndPredecodedResponses(t *testing.T) {
	response := routingJSONResponse("application/json", "", []byte(`{"ok":true}`))
	response.Header["Content-Type"] = []string{"application/json", "application/problem+json"}
	body, err := readRoutingCostJSON(response, routingJSONLimits{WireBytes: 64, DecodedBytes: 64})
	require.Error(t, err)
	assert.Empty(t, body)

	response = routingJSONResponse("application/json", "", []byte(`{"ok":true}`))
	response.Uncompressed = true
	body, err = readRoutingCostJSON(response, routingJSONLimits{WireBytes: 64, DecodedBytes: 64})
	require.Error(t, err)
	assert.Empty(t, body)
}

func TestReadRoutingCostJSONRejectsInvalidOrCorruptGzip(t *testing.T) {
	valid := gzipBytes(t, []byte(`{"ok":true}`))
	tests := [][]byte{
		[]byte("not gzip"),
		valid[:len(valid)-1],
		append(append([]byte(nil), valid[:len(valid)-1]...), valid[len(valid)-1]^0xff),
	}
	for _, compressed := range tests {
		response := routingJSONResponse("application/json", "gzip", compressed)
		body, err := readRoutingCostJSON(response, routingJSONLimits{WireBytes: 128, DecodedBytes: 64})
		require.Error(t, err)
		assert.Empty(t, body)
	}
}

func TestReadRoutingCostJSONRejectsExcessiveStructuredValues(t *testing.T) {
	response := routingJSONResponse("application/json", "", []byte(`[{}, {}, {}, {}]`))
	body, err := readRoutingCostJSON(response, routingJSONLimits{WireBytes: 64, DecodedBytes: 64, MaxValues: 4})

	require.Error(t, err)
	assert.Empty(t, body)
}

func TestReadRoutingCostJSONRejectsUnsafeLimits(t *testing.T) {
	for _, limits := range []routingJSONLimits{
		{WireBytes: 0, DecodedBytes: 1},
		{WireBytes: 1, DecodedBytes: 0},
		{WireBytes: math.MaxInt64, DecodedBytes: 1},
		{WireBytes: 1, DecodedBytes: math.MaxInt64},
	} {
		response := routingJSONResponse("application/json", "", []byte("1"))
		body, err := readRoutingCostJSON(response, limits)
		require.Error(t, err)
		assert.Empty(t, body)
	}
}

type countingReadCloser struct {
	io.Reader
	readBytes int
}

func (reader *countingReadCloser) Read(buffer []byte) (int, error) {
	count, err := reader.Reader.Read(buffer)
	reader.readBytes += count
	return count, err
}

func (reader *countingReadCloser) Close() error { return nil }

func routingJSONResponse(contentType string, contentEncoding string, body []byte) *http.Response {
	header := make(http.Header)
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	if contentEncoding != "" {
		header.Set("Content-Encoding", contentEncoding)
	}
	return &http.Response{
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func gzipBytes(t *testing.T, body []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return compressed.Bytes()
}
