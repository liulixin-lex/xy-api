package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	projecti18n "github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupVideoProxyDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/video-proxy.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Task{}, &model.RoutingCredentialRef{}))

	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	previousSecret := common.CryptoSecret
	const secret = "video-proxy-persisted-credential-secret"
	common.CryptoSecret = secret
	t.Setenv("CRYPTO_SECRET", secret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
	return db
}

func invokeVideoProxy(t *testing.T, userID int, taskID string, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", userID)
	ctx.Params = gin.Params{{Key: "task_id", Value: taskID}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/"+taskID+"/content", nil)
	ctx.Request.Header = headers.Clone()
	VideoProxy(ctx)
	return recorder
}

func TestResolveVideoProxyCredentialUsesPersistedIdentity(t *testing.T) {
	db := setupVideoProxyDatabase(t)
	channel := &model.Channel{
		Id:                101,
		RoutingGeneration: "video-generation-101",
		Key:               "key-b\nkey-a",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusEnabled,
				1: common.ChannelStatusEnabled,
			},
		},
	}
	fingerprint, err := model.RoutingCredentialFingerprint(channel.Id, channel.RoutingGeneration, "key-a")
	require.NoError(t, err)
	credential := model.RoutingCredentialRef{
		ID:                 1_001,
		ChannelID:          channel.Id,
		ChannelGeneration:  channel.RoutingGeneration,
		Fingerprint:        fingerprint,
		FingerprintVersion: model.RoutingCredentialFingerprintVersion,
		Active:             true,
		CurrentOccurrences: 1,
	}
	require.NoError(t, db.Create(&credential).Error)

	privateData := model.TaskPrivateData{RoutingCredentialID: credential.ID}
	key, err := resolveVideoProxyCredential(context.Background(), channel, privateData)
	require.NoError(t, err)
	assert.Equal(t, "key-a", key)

	require.NoError(t, db.Model(&credential).Update("active", false).Error)
	_, err = resolveVideoProxyCredential(context.Background(), channel, privateData)
	assert.ErrorIs(t, err, errVideoProxyCredentialUnavailable)

	require.NoError(t, db.Model(&credential).Updates(map[string]any{
		"active":             true,
		"channel_generation": "retired-generation",
	}).Error)
	_, err = resolveVideoProxyCredential(context.Background(), channel, privateData)
	assert.ErrorIs(t, err, errVideoProxyCredentialUnavailable)

	require.NoError(t, db.Model(&credential).Update("channel_generation", channel.RoutingGeneration).Error)
	duplicate := *channel
	duplicate.Key = "key-a\nkey-a"
	_, err = resolveVideoProxyCredential(context.Background(), &duplicate, privateData)
	assert.ErrorIs(t, err, errVideoProxyCredentialUnavailable)
}

func TestResolveVideoProxyCredentialLegacyBoundary(t *testing.T) {
	single := &model.Channel{Id: 201, Key: "current-single-key"}
	key, err := resolveVideoProxyCredential(context.Background(), single, model.TaskPrivateData{})
	require.NoError(t, err)
	assert.Equal(t, "current-single-key", key)

	multi := &model.Channel{
		Id:          202,
		Key:         "key-a\nkey-b",
		ChannelInfo: model.ChannelInfo{IsMultiKey: true},
	}
	_, err = resolveVideoProxyCredential(context.Background(), multi, model.TaskPrivateData{})
	assert.ErrorIs(t, err, errVideoProxyCredentialIdentityMissing)

	key, err = resolveVideoProxyCredential(context.Background(), multi, model.TaskPrivateData{
		BillingProtocolVersion: model.TaskBillingLegacyProtocolVersion,
		Key:                    " historical-key ",
	})
	require.NoError(t, err)
	assert.Equal(t, "historical-key", key)

	_, err = resolveVideoProxyCredential(context.Background(), multi, model.TaskPrivateData{
		BillingProtocolVersion: model.TaskBillingProtocolVersion,
		Key:                    "must-not-be-used",
	})
	assert.ErrorIs(t, err, errVideoProxyCredentialIdentityMissing)
}

func TestVideoProxyUsesStableCredentialAndPreservesRange(t *testing.T) {
	db := setupVideoProxyDatabase(t)

	type capturedRequest struct {
		path          string
		authorization string
		rangeHeader   string
	}
	captured := make(chan capturedRequest, 1)
	previousFactory := videoProxyHTTPClientFactory
	videoProxyHTTPClientFactory = func(proxyURL string) (*http.Client, error) {
		assert.Empty(t, proxyURL)
		return &http.Client{Transport: videoProxyRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			captured <- capturedRequest{
				path:          request.URL.Path,
				authorization: request.Header.Get("Authorization"),
				rangeHeader:   request.Header.Get("Range"),
			}
			return &http.Response{
				StatusCode: http.StatusPartialContent,
				Header: http.Header{
					"Accept-Ranges":  []string{"bytes"},
					"Content-Range":  []string{"bytes 2-4/6"},
					"Content-Type":   []string{"video/mp4"},
					"Content-Length": []string{"3"},
				},
				ContentLength: 3,
				Body:          io.NopCloser(strings.NewReader("cde")),
				Request:       request,
			}, nil
		})}, nil
	}
	t.Cleanup(func() { videoProxyHTTPClientFactory = previousFactory })

	baseURL := "https://video.example"
	channel := model.Channel{
		Id:      301,
		Type:    constant.ChannelTypeOpenAI,
		Key:     "key-b\nkey-a",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusEnabled,
				1: common.ChannelStatusEnabled,
			},
		},
	}
	require.NoError(t, db.Create(&channel).Error)
	fingerprint, err := model.RoutingCredentialFingerprint(channel.Id, channel.RoutingGeneration, "key-a")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.RoutingCredentialRef{
		ID: 3_001, ChannelID: channel.Id, ChannelGeneration: channel.RoutingGeneration,
		Fingerprint: fingerprint, FingerprintVersion: model.RoutingCredentialFingerprintVersion,
		Active: true, CurrentOccurrences: 1,
	}).Error)
	task := model.Task{
		TaskID:    "task_public_range",
		UserId:    42,
		ChannelId: channel.Id,
		Status:    model.TaskStatusSuccess,
		PrivateData: model.TaskPrivateData{
			Key:                 "historical-wrong-key",
			RoutingCredentialID: 3_001,
			UpstreamTaskID:      "upstream-video-id",
		},
	}
	require.NoError(t, db.Create(&task).Error)

	recorder := invokeVideoProxy(t, task.UserId, task.TaskID, http.Header{"Range": []string{"bytes=2-4"}})
	request := <-captured
	assert.Equal(t, "/v1/videos/upstream-video-id/content", request.path)
	assert.Equal(t, "Bearer key-a", request.authorization)
	assert.Equal(t, "bytes=2-4", request.rangeHeader)
	assert.Equal(t, http.StatusPartialContent, recorder.Code)
	assert.Equal(t, "video/mp4", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "bytes 2-4/6", recorder.Header().Get("Content-Range"))
	assert.Equal(t, "cde", recorder.Body.String())
}

func TestVideoProxyRejectsScopedTokenBeforeUpstreamIO(t *testing.T) {
	require.NoError(t, projecti18n.Init())
	db := setupVideoProxyDatabase(t)
	channel := model.Channel{
		Id: 305, Type: constant.ChannelTypeOpenAI, Name: "scoped media", Key: "stable-key",
		Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.Task{
		TaskID: "task_scoped_media", UserId: 46, ChannelId: channel.Id,
		Status:      model.TaskStatusSuccess,
		Properties:  model.Properties{OriginModelName: "video-model"},
		PrivateData: model.TaskPrivateData{UpstreamTaskID: "upstream-media"},
	}).Error)

	var factoryCalled atomic.Bool
	previousFactory := videoProxyHTTPClientFactory
	videoProxyHTTPClientFactory = func(string) (*http.Client, error) {
		factoryCalled.Store(true)
		return nil, nil
	}
	t.Cleanup(func() { videoProxyHTTPClientFactory = previousFactory })

	tests := []struct {
		name            string
		modelLimits     map[string]bool
		specificChannel string
	}{
		{name: "model forbidden", modelLimits: map[string]bool{"other-model": true}, specificChannel: "305"},
		{name: "channel forbidden", modelLimits: map[string]bool{"video-model": true}, specificChannel: "306"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set("id", 46)
			ctx.Params = gin.Params{{Key: "task_id", Value: "task_scoped_media"}}
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/task_scoped_media/content", nil)
			common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, true)
			common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, test.modelLimits)
			common.SetContextKey(ctx, constant.ContextKeyTokenSpecificChannelId, test.specificChannel)

			VideoProxy(ctx)

			assert.Equal(t, http.StatusForbidden, recorder.Code)
			assert.False(t, factoryCalled.Load())
		})
	}
}

func TestVideoProxyUsesPrivateProviderResultURLInsteadOfPublicProxyURL(t *testing.T) {
	db := setupVideoProxyDatabase(t)
	providerURL := "https://media.example/video.mp4?X-Amz-Signature=provider-secret"
	captured := make(chan *http.Request, 1)
	previousFactory := videoProxyHTTPClientFactory
	videoProxyHTTPClientFactory = func(string) (*http.Client, error) {
		return &http.Client{Transport: videoProxyRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			captured <- request.Clone(request.Context())
			return &http.Response{
				StatusCode:    http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"video/mp4"}},
				ContentLength: 5,
				Body:          io.NopCloser(strings.NewReader("video")),
				Request:       request,
			}, nil
		})}, nil
	}
	t.Cleanup(func() { videoProxyHTTPClientFactory = previousFactory })

	channel := model.Channel{
		Id: 302, Type: constant.ChannelTypeVidu, Name: "private result", Key: "key-a\nkey-b",
		Status:      common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true},
	}
	require.NoError(t, db.Create(&channel).Error)
	task := model.Task{
		TaskID: "task_private_result", UserId: 43, ChannelId: channel.Id,
		Status: model.TaskStatusSuccess,
		PrivateData: model.TaskPrivateData{
			UpstreamResultURL: providerURL,
			ResultURL:         "/v1/videos/task_private_result/content",
		},
	}
	require.NoError(t, db.Create(&task).Error)

	recorder := invokeVideoProxy(t, task.UserId, task.TaskID, nil)
	request := <-captured
	assert.Equal(t, providerURL, request.URL.String())
	assert.Empty(t, request.Header.Get("Authorization"))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "video", recorder.Body.String())
}

func TestVideoProxyResolvesProtectedNewAPIRelativeMedia(t *testing.T) {
	db := setupVideoProxyDatabase(t)
	baseURL := "https://gateway.example/api"
	channel := model.Channel{
		Id: 303, Type: constant.ChannelTypeKling, Name: "cascaded new-api", Key: "key-b\nkey-a",
		BaseURL: &baseURL, Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true},
	}
	require.NoError(t, db.Create(&channel).Error)
	fingerprint, err := model.RoutingCredentialFingerprint(channel.Id, channel.RoutingGeneration, "key-a")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.RoutingCredentialRef{
		ID: 3_003, ChannelID: channel.Id, ChannelGeneration: channel.RoutingGeneration,
		Fingerprint: fingerprint, FingerprintVersion: model.RoutingCredentialFingerprintVersion,
		Active: true, CurrentOccurrences: 1,
	}).Error)
	task := model.Task{
		TaskID: "task_relative_media", UserId: 44, ChannelId: channel.Id, Status: model.TaskStatusSuccess,
		PrivateData: model.TaskPrivateData{
			RoutingCredentialID: 3_003,
			UpstreamResultURL:   "/v1/videos/upstream%2Fpublic/content",
		},
	}
	require.NoError(t, db.Create(&task).Error)

	type capturedRequest struct {
		url           string
		authorization string
		rangeHeader   string
	}
	captured := make(chan capturedRequest, 1)
	previousFactory := videoProxyHTTPClientFactory
	videoProxyHTTPClientFactory = func(string) (*http.Client, error) {
		return &http.Client{Transport: videoProxyRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			captured <- capturedRequest{
				url: request.URL.String(), authorization: request.Header.Get("Authorization"),
				rangeHeader: request.Header.Get("Range"),
			}
			return &http.Response{
				StatusCode: http.StatusPartialContent,
				Header: http.Header{
					"Content-Type": []string{"video/mp4"}, "Content-Range": []string{"bytes 0-4/5"},
				},
				ContentLength: 5, Body: io.NopCloser(strings.NewReader("video")), Request: request,
			}, nil
		})}, nil
	}
	t.Cleanup(func() { videoProxyHTTPClientFactory = previousFactory })

	recorder := invokeVideoProxy(t, task.UserId, task.TaskID, http.Header{"Range": []string{"bytes=0-4"}})
	request := <-captured
	assert.Equal(t, "https://gateway.example/v1/videos/upstream%2Fpublic/content", request.url)
	assert.Equal(t, "Bearer key-a", request.authorization)
	assert.Equal(t, "bytes=0-4", request.rangeHeader)
	assert.Equal(t, http.StatusPartialContent, recorder.Code)
}

func TestVideoProxyStripsProtectedCredentialOnCrossOriginRedirect(t *testing.T) {
	db := setupVideoProxyDatabase(t)
	baseURL := "https://gateway.example"
	channel := model.Channel{
		Id: 304, Type: constant.ChannelTypeKling, Name: "redirecting new-api", Key: "stable-key",
		BaseURL: &baseURL, Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(&channel).Error)
	fingerprint, err := model.RoutingCredentialFingerprint(channel.Id, channel.RoutingGeneration, "stable-key")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.RoutingCredentialRef{
		ID: 3_004, ChannelID: channel.Id, ChannelGeneration: channel.RoutingGeneration,
		Fingerprint: fingerprint, FingerprintVersion: model.RoutingCredentialFingerprintVersion,
		Active: true, CurrentOccurrences: 1,
	}).Error)
	task := model.Task{
		TaskID: "task_redirect_media", UserId: 45, ChannelId: channel.Id, Status: model.TaskStatusSuccess,
		PrivateData: model.TaskPrivateData{
			RoutingCredentialID: 3_004,
			UpstreamResultURL:   "/v1/videos/upstream/content",
		},
	}
	require.NoError(t, db.Create(&task).Error)

	var authorizations []string
	previousFactory := videoProxyHTTPClientFactory
	videoProxyHTTPClientFactory = func(string) (*http.Client, error) {
		return &http.Client{Transport: videoProxyRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			authorizations = append(authorizations, request.Header.Get("Authorization"))
			if request.URL.Host == "gateway.example" {
				return &http.Response{
					StatusCode: http.StatusFound,
					Header:     http.Header{"Location": []string{"https://cdn.example/video.mp4"}},
					Body:       io.NopCloser(strings.NewReader("")),
					Request:    request,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"video/mp4"}},
				ContentLength: 5, Body: io.NopCloser(strings.NewReader("video")), Request: request,
			}, nil
		})}, nil
	}
	t.Cleanup(func() { videoProxyHTTPClientFactory = previousFactory })

	recorder := invokeVideoProxy(t, task.UserId, task.TaskID, nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, []string{"Bearer stable-key", ""}, authorizations)
}

func TestVideoProxyCredentialFailuresPrecedeUpstreamIO(t *testing.T) {
	db := setupVideoProxyDatabase(t)
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	baseURL := upstream.URL
	channel := model.Channel{
		Id:      401,
		Type:    constant.ChannelTypeOpenAI,
		Key:     "secret-key-a\nsecret-key-b",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
	}
	require.NoError(t, db.Create(&channel).Error)
	fingerprint, err := model.RoutingCredentialFingerprint(channel.Id, channel.RoutingGeneration, "secret-key-a")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.RoutingCredentialRef{
		ID: 4_001, ChannelID: channel.Id, ChannelGeneration: channel.RoutingGeneration,
		Fingerprint: fingerprint, FingerprintVersion: model.RoutingCredentialFingerprintVersion,
		Active: false, CurrentOccurrences: 1,
	}).Error)

	tasks := []model.Task{
		{
			TaskID: "task_legacy_multi_key", UserId: 51, ChannelId: channel.Id,
			Status: model.TaskStatusSuccess,
		},
		{
			TaskID: "task_retired_credential", UserId: 51, ChannelId: channel.Id,
			Status: model.TaskStatusSuccess,
			PrivateData: model.TaskPrivateData{
				RoutingCredentialID: 4_001,
			},
		},
	}
	require.NoError(t, db.Create(&tasks).Error)

	legacyRecorder := invokeVideoProxy(t, 51, tasks[0].TaskID, nil)
	assert.Equal(t, http.StatusConflict, legacyRecorder.Code)
	assert.Contains(t, legacyRecorder.Body.String(), "task_credential_identity_missing")

	retiredRecorder := invokeVideoProxy(t, 51, tasks[1].TaskID, nil)
	assert.Equal(t, http.StatusServiceUnavailable, retiredRecorder.Code)
	assert.Contains(t, retiredRecorder.Body.String(), "task_credential_unavailable")
	assert.NotContains(t, retiredRecorder.Body.String(), "secret-key")
	assert.Zero(t, upstreamRequests.Load())
}

func TestGeminiVideoURLRemovesStaleQueryAPIKey(t *testing.T) {
	secured, err := ensureAPIKey("https://media.example/video.mp4?key=stale&alt=media", "stable key&value")
	require.NoError(t, err)
	parsed, err := url.Parse(secured)
	require.NoError(t, err)
	assert.Empty(t, parsed.Query().Get("key"))
	assert.Equal(t, "media", parsed.Query().Get("alt"))
	assert.NotContains(t, secured, "key=stale")
	assert.NotContains(t, secured, "stable")
}

func TestGeminiAndVertexVideoResolversPreferPrivateProviderURL(t *testing.T) {
	providerURL := "https://media.example/video.mp4?X-Goog-Signature=provider-secret&key=stale"
	task := &model.Task{
		TaskID: "task_private_resolver",
		PrivateData: model.TaskPrivateData{
			UpstreamResultURL: providerURL,
		},
		Data: []byte(`{"response":{"video":"https://wrong.example/video.mp4"}}`),
	}

	geminiURL, err := getGeminiVideoURL(context.Background(), &model.Channel{}, task, "stable-key")
	require.NoError(t, err)
	parsedGeminiURL, err := url.Parse(geminiURL)
	require.NoError(t, err)
	assert.Equal(t, "provider-secret", parsedGeminiURL.Query().Get("X-Goog-Signature"))
	assert.Empty(t, parsedGeminiURL.Query().Get("key"))

	vertexURL, err := getVertexVideoURL(context.Background(), &model.Channel{}, task, "stable-token")
	require.NoError(t, err)
	assert.Equal(t, providerURL, vertexURL)
}

func TestVideoProxyMetadataErrorDoesNotExposeBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader("upstream-secret-response")),
	}
	_, err := readVideoProxyMetadataResponse(resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 401")
	assert.NotContains(t, err.Error(), "upstream-secret-response")
}

func TestVideoProxyMetadataRejectsDeclaredOversizeBeforeRead(t *testing.T) {
	body := &trackedVideoProxyBody{}
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: maxVideoProxyMetadataResponseBytes + 1,
		Body:          body,
	}
	_, err := readVideoProxyMetadataResponse(resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds limit")
	assert.False(t, body.read.Load())
	assert.True(t, body.closed.Load())
}

type trackedVideoProxyBody struct {
	read   atomic.Bool
	closed atomic.Bool
}

func (body *trackedVideoProxyBody) Read([]byte) (int, error) {
	body.read.Store(true)
	return 0, io.EOF
}

func (body *trackedVideoProxyBody) Close() error {
	body.closed.Store(true)
	return nil
}

func TestWriteVideoDataURLRejectsActiveContent(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	err := writeVideoDataURL(ctx, "data:text/html;base64,PHNjcmlwdD4=")
	require.Error(t, err)
	assert.Zero(t, recorder.Body.Len())
}

func TestValidateVideoProxyResponseMediaType(t *testing.T) {
	for _, contentType := range []string{"video/mp4", "video/webm; codecs=vp9", "application/octet-stream", "multipart/byteranges; boundary=x"} {
		require.NoError(t, validateVideoProxyResponseMediaType(contentType))
	}
	for _, contentType := range []string{"", "text/html", "application/json", "image/svg+xml"} {
		require.Error(t, validateVideoProxyResponseMediaType(contentType))
	}
}

type videoProxyRoundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip videoProxyRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
