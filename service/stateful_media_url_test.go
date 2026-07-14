package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveStatefulMediaURLRestrictsRelativeNewAPIRoutes(t *testing.T) {
	resolved, err := ResolveStatefulMediaURL(
		"https://gateway.example/api",
		"/v1/videos/task%2Fpublic/content",
		StatefulMediaNewAPIVideo,
	)
	require.NoError(t, err)
	assert.Equal(t, "https://gateway.example/v1/videos/task%2Fpublic/content", resolved.URL)
	assert.True(t, resolved.TrustedSameOrigin)

	for _, rawURL := range []string{
		"v1/videos/task/content",
		"//evil.example/v1/videos/task/content",
		"/v1/videos/../content",
		"/v1/videos/task/content/extra",
		"/v1/videos/task/content?token=secret",
		"/v1/videos/task\\evil/content",
		"/v1/videos/task%2F..%2Fadmin/content",
	} {
		_, err := ResolveStatefulMediaURL("https://gateway.example", rawURL, StatefulMediaNewAPIVideo)
		require.Error(t, err, rawURL)
	}
}

func TestResolveStatefulMediaURLClassifiesOnlySameOriginExactRoutesAsTrusted(t *testing.T) {
	tests := []struct {
		name     string
		rawURL   string
		kind     StatefulMediaKind
		trusted  bool
		wantFail bool
	}{
		{
			name: "same origin image", rawURL: "https://gateway.example/mj/image/task-1",
			kind: StatefulMediaNewAPIMidjourneyImage, trusted: true,
		},
		{
			name: "same origin indexed video", rawURL: "https://gateway.example/mj/video/task-1/0",
			kind: StatefulMediaNewAPIMidjourneyVideo, trusted: true,
		},
		{
			name: "cross origin provider", rawURL: "https://cdn.example/video.mp4?sig=secret",
			kind: StatefulMediaNewAPIMidjourneyVideo,
		},
		{
			name: "same origin unrelated path", rawURL: "https://gateway.example/admin/export",
			kind: StatefulMediaNewAPIMidjourneyVideo,
		},
		{
			name: "negative video index", rawURL: "/mj/video/task-1/-1",
			kind: StatefulMediaNewAPIMidjourneyVideo, wantFail: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := ResolveStatefulMediaURL("https://gateway.example", test.rawURL, test.kind)
			if test.wantFail {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.trusted, resolved.TrustedSameOrigin)
		})
	}
}
