package service

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

type StatefulMediaKind uint8

const (
	StatefulMediaNewAPIVideo StatefulMediaKind = iota + 1
	StatefulMediaNewAPIMidjourneyImage
	StatefulMediaNewAPIMidjourneyVideo
)

type StatefulMediaResolution struct {
	URL               string
	TrustedSameOrigin bool
}

// ResolveStatefulMediaURL resolves only known new-api relative media routes.
// Absolute provider URLs remain usable but never gain upstream credentials
// unless they are the same-origin equivalent of an exact known route.
func ResolveStatefulMediaURL(baseURL, rawURL string, kind StatefulMediaKind) (StatefulMediaResolution, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || len(rawURL) > statefulFetchMaxURLBytes || strings.ContainsAny(rawURL, "\\\r\n\x00") {
		return StatefulMediaResolution{}, errors.New("invalid stateful media URL")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil || parsed.Opaque != "" || parsed.User != nil || parsed.Fragment != "" {
		return StatefulMediaResolution{}, errors.New("invalid stateful media URL")
	}

	if parsed.IsAbs() {
		if parsed.Host == "" || parsed.Hostname() == "" {
			return StatefulMediaResolution{}, errors.New("invalid absolute stateful media URL")
		}
		trusted := false
		if parsed.RawQuery == "" && validStatefulMediaPath(parsed, kind) {
			if base, baseErr := parseStatefulMediaBaseURL(baseURL); baseErr == nil {
				trusted = sameStatefulMediaOrigin(base, parsed)
			}
		}
		return StatefulMediaResolution{URL: parsed.String(), TrustedSameOrigin: trusted}, nil
	}

	if parsed.Scheme != "" || parsed.Host != "" || parsed.RawQuery != "" ||
		!strings.HasPrefix(rawURL, "/") || strings.HasPrefix(rawURL, "//") ||
		!validStatefulMediaPath(parsed, kind) {
		return StatefulMediaResolution{}, errors.New("untrusted relative stateful media URL")
	}
	base, err := parseStatefulMediaBaseURL(baseURL)
	if err != nil {
		return StatefulMediaResolution{}, err
	}
	resolved := base.ResolveReference(parsed)
	if resolved == nil || !sameStatefulMediaOrigin(base, resolved) || !validStatefulMediaPath(resolved, kind) {
		return StatefulMediaResolution{}, errors.New("stateful media URL escaped its channel origin")
	}
	return StatefulMediaResolution{URL: resolved.String(), TrustedSameOrigin: true}, nil
}

func parseStatefulMediaBaseURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.Host == "" ||
		parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" ||
		(!strings.EqualFold(parsed.Scheme, "https") && !strings.EqualFold(parsed.Scheme, "http")) {
		return nil, errors.New("invalid stateful media base URL")
	}
	return parsed, nil
}

func sameStatefulMediaOrigin(first, second *url.URL) bool {
	if first == nil || second == nil || !strings.EqualFold(first.Scheme, second.Scheme) ||
		!strings.EqualFold(first.Hostname(), second.Hostname()) {
		return false
	}
	return statefulMediaPort(first) == statefulMediaPort(second)
}

func statefulMediaPort(parsed *url.URL) string {
	if port := parsed.Port(); port != "" {
		return port
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(parsed.Scheme, "http") {
		return "80"
	}
	return ""
}

func validStatefulMediaPath(parsed *url.URL, kind StatefulMediaKind) bool {
	if parsed == nil {
		return false
	}
	escapedPath := parsed.EscapedPath()
	if escapedPath == "" || !strings.HasPrefix(escapedPath, "/") || strings.Contains(escapedPath, "//") {
		return false
	}
	escapedSegments := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	segments := make([]string, len(escapedSegments))
	for index, escapedSegment := range escapedSegments {
		if escapedSegment == "" {
			return false
		}
		segment, err := url.PathUnescape(escapedSegment)
		if err != nil || segment == "" || segment == "." || segment == ".." ||
			strings.ContainsAny(segment, "\\\r\n\x00") {
			return false
		}
		for _, decodedPathPart := range strings.Split(segment, "/") {
			if decodedPathPart == "." || decodedPathPart == ".." {
				return false
			}
		}
		segments[index] = segment
	}

	switch kind {
	case StatefulMediaNewAPIVideo:
		return len(segments) == 4 && segments[0] == "v1" && segments[1] == "videos" && segments[3] == "content"
	case StatefulMediaNewAPIMidjourneyImage:
		return len(segments) == 3 && segments[0] == "mj" && segments[1] == "image"
	case StatefulMediaNewAPIMidjourneyVideo:
		if len(segments) != 3 && len(segments) != 4 {
			return false
		}
		if segments[0] != "mj" || segments[1] != "video" {
			return false
		}
		if len(segments) == 4 {
			_, err := strconv.ParseUint(segments[3], 10, 64)
			return err == nil
		}
		return true
	default:
		return false
	}
}
