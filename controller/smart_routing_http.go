package controller

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strings"
)

type routingJSONLimits struct {
	WireBytes    int64
	DecodedBytes int64
}

var defaultRoutingJSONLimits = routingJSONLimits{
	WireBytes:    maxRatioConfigBytes,
	DecodedBytes: maxRatioConfigBytes,
}

func readRoutingCostJSON(response *http.Response, limits routingJSONLimits) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("routing cost response body is missing")
	}
	if limits.WireBytes <= 0 || limits.DecodedBytes <= 0 ||
		limits.WireBytes == math.MaxInt64 || limits.DecodedBytes == math.MaxInt64 {
		return nil, fmt.Errorf("invalid routing cost response limits")
	}
	if response.Uncompressed {
		return nil, fmt.Errorf("routing cost response was decompressed before validation")
	}

	contentTypes := response.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return nil, fmt.Errorf("routing cost response must contain one JSON Content-Type")
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil {
		return nil, fmt.Errorf("invalid routing cost response Content-Type: %w", err)
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType != "application/json" &&
		!(strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json")) {
		return nil, fmt.Errorf("routing cost response Content-Type must be JSON")
	}

	if response.ContentLength > limits.WireBytes {
		return nil, fmt.Errorf("routing cost response exceeds wire size limit")
	}
	wireBody, err := readRoutingCostBodyLimit(response.Body, limits.WireBytes)
	if err != nil {
		return nil, fmt.Errorf("read routing cost response: %w", err)
	}

	contentEncodings := response.Header.Values("Content-Encoding")
	if len(contentEncodings) > 1 {
		return nil, fmt.Errorf("routing cost response has multiple content encodings")
	}
	contentEncoding := ""
	if len(contentEncodings) == 1 {
		contentEncoding = strings.ToLower(strings.TrimSpace(contentEncodings[0]))
	}
	switch contentEncoding {
	case "", "identity":
		if int64(len(wireBody)) > limits.DecodedBytes {
			return nil, fmt.Errorf("routing cost response exceeds decoded size limit")
		}
		return wireBody, nil
	case "gzip":
		reader, gzipErr := gzip.NewReader(bytes.NewReader(wireBody))
		if gzipErr != nil {
			return nil, fmt.Errorf("invalid routing cost gzip response: %w", gzipErr)
		}
		decodedBody, readErr := readRoutingCostBodyLimit(reader, limits.DecodedBytes)
		closeErr := reader.Close()
		if readErr != nil {
			return nil, fmt.Errorf("decode routing cost gzip response: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close routing cost gzip response: %w", closeErr)
		}
		return decodedBody, nil
	default:
		return nil, fmt.Errorf("unsupported routing cost Content-Encoding")
	}
}

func readRoutingCostBodyLimit(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds size limit")
	}
	return body, nil
}
