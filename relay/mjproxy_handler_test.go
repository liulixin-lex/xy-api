package relay

import (
	"bytes"
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	projecti18n "github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestValidatedMidjourneyImageContentTypeRejectsActiveAndMismatchedContent(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43, 0x00}
	contentType, err := validatedMidjourneyImageContentType("image/jpeg; charset=binary", jpeg)
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", contentType)

	contentType, err = validatedMidjourneyImageContentType("", jpeg)
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", contentType)
	contentType, err = validatedMidjourneyImageContentType("application/octet-stream", jpeg)
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", contentType)

	_, err = validatedMidjourneyImageContentType("text/html", []byte("<script>alert(1)</script>"))
	require.Error(t, err)
	_, err = validatedMidjourneyImageContentType("image/jpeg", []byte("<html>not an image</html>"))
	require.Error(t, err)
	_, err = validatedMidjourneyImageContentType("image/png", jpeg)
	require.Error(t, err)
	_, err = validatedMidjourneyImageContentType("image/jpeg", []byte{0, 1, 2, 3, 4})
	require.Error(t, err)
	_, err = validatedMidjourneyImageContentType("image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	require.Error(t, err)
}

func TestRelayMidjourneyImageUsesStatefulClientAndPrivateCache(t *testing.T) {
	db := setupMidjourneyImageProxyDatabase(t)
	setting := `{"proxy":"http://proxy.internal:3128"}`
	require.NoError(t, db.Create(&model.Channel{
		Id: 901, Name: "midjourney-image", Key: "unused", Setting: &setting,
	}).Error)
	require.NoError(t, db.Create(&model.Midjourney{
		UserId: 71, MjId: "task_public_image", ChannelId: 901,
		ImageUrl: "https://image.example/result.jpg?token=signed-value",
	}).Error)

	jpeg := []byte{0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43, 0x00}
	var capturedProxy string
	var capturedRequest *http.Request
	previousFactory := midjourneyImageHTTPClientFactory
	midjourneyImageHTTPClientFactory = func(proxyURL string) (*http.Client, error) {
		capturedProxy = proxyURL
		return &http.Client{Transport: midjourneyImageRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			capturedRequest = request.Clone(request.Context())
			return &http.Response{
				StatusCode:    http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"image/jpeg"}},
				Body:          io.NopCloser(bytes.NewReader(jpeg)),
				ContentLength: int64(len(jpeg)),
				Request:       request,
			}, nil
		})}, nil
	}
	t.Cleanup(func() { midjourneyImageHTTPClientFactory = previousFactory })

	recorder := invokeMidjourneyImageProxy(t, "task_public_image")
	require.NotNil(t, capturedRequest)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, jpeg, recorder.Body.Bytes())
	assert.Equal(t, "image/jpeg", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "private, max-age=86400", recorder.Header().Get("Cache-Control"))
	assert.Equal(t, "http://proxy.internal:3128", capturedProxy)
	assert.Equal(t, "https://image.example/result.jpg?token=signed-value", capturedRequest.URL.String())
}

func TestRelayMidjourneyProtectedNewAPIMediaUsesStableCredential(t *testing.T) {
	db := setupMidjourneyImageProxyDatabase(t)
	baseURL := "https://gateway.example/api"
	channel := model.Channel{
		Id: 904, Name: "cascaded new-api", Key: "key-b\nkey-a", BaseURL: &baseURL,
		Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: true},
	}
	require.NoError(t, db.Create(&channel).Error)
	fingerprint, err := model.RoutingCredentialFingerprint(channel.Id, channel.RoutingGeneration, "key-a")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.RoutingCredentialRef{
		ID: 9_004, ChannelID: channel.Id, ChannelGeneration: channel.RoutingGeneration,
		Fingerprint: fingerprint, FingerprintVersion: model.RoutingCredentialFingerprintVersion,
		Active: true, CurrentOccurrences: 1,
	}).Error)
	videoURLs, err := common.Marshal([]dto.ImgUrls{{Url: "/mj/video/upstream-video/0"}})
	require.NoError(t, err)
	require.NoError(t, db.Create(&[]model.Midjourney{
		{
			UserId: 71, MjId: "task_protected_image", ChannelId: channel.Id,
			RoutingCredentialID: 9_004, ChannelGeneration: channel.RoutingGeneration,
			ImageUrl: "/mj/image/upstream-image",
		},
		{
			UserId: 71, MjId: "task_protected_video", ChannelId: channel.Id,
			RoutingCredentialID: 9_004, ChannelGeneration: channel.RoutingGeneration,
			VideoUrl: "/mj/video/upstream-video", VideoUrls: string(videoURLs),
		},
	}).Error)

	jpeg := []byte{0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43, 0x00}
	var imageAuthorizations []string
	var videoAuthorization string
	var videoRange string
	previousFactory := midjourneyImageHTTPClientFactory
	midjourneyImageHTTPClientFactory = func(string) (*http.Client, error) {
		return &http.Client{Transport: midjourneyImageRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			switch {
			case request.URL.Host == "gateway.example" && strings.HasPrefix(request.URL.Path, "/mj/image/"):
				imageAuthorizations = append(imageAuthorizations, request.Header.Get("Authorization"))
				return &http.Response{
					StatusCode: http.StatusFound,
					Header:     http.Header{"Location": []string{"https://cdn.example/result.jpg"}},
					Body:       io.NopCloser(strings.NewReader("")), Request: request,
				}, nil
			case request.URL.Host == "cdn.example":
				imageAuthorizations = append(imageAuthorizations, request.Header.Get("Authorization"))
				return &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/jpeg"}},
					ContentLength: int64(len(jpeg)), Body: io.NopCloser(bytes.NewReader(jpeg)), Request: request,
				}, nil
			default:
				videoAuthorization = request.Header.Get("Authorization")
				videoRange = request.Header.Get("Range")
				return &http.Response{
					StatusCode: http.StatusPartialContent,
					Header: http.Header{
						"Content-Type": []string{"video/mp4"}, "Content-Range": []string{"bytes 0-4/5"},
					},
					ContentLength: 5, Body: io.NopCloser(strings.NewReader("video")), Request: request,
				}, nil
			}
		})}, nil
	}
	t.Cleanup(func() { midjourneyImageHTTPClientFactory = previousFactory })

	imageRecorder := invokeMidjourneyImageProxy(t, "task_protected_image")
	assert.Equal(t, http.StatusOK, imageRecorder.Code)
	assert.Equal(t, []string{"Bearer key-a", ""}, imageAuthorizations)

	videoRecorder := invokeMidjourneyVideoProxy(
		t, 71, "task_protected_video", "0", http.Header{"Range": []string{"bytes=0-4"}},
	)
	assert.Equal(t, http.StatusPartialContent, videoRecorder.Code)
	assert.Equal(t, "Bearer key-a", videoAuthorization)
	assert.Equal(t, "bytes=0-4", videoRange)
}

func TestRelayMidjourneyMediaRejectsChannelGenerationMismatch(t *testing.T) {
	db := setupMidjourneyImageProxyDatabase(t)
	baseURL := "https://gateway.example"
	channel := model.Channel{
		Id: 905, Name: "replacement channel", Key: "current-key", BaseURL: &baseURL,
		Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.Midjourney{
		UserId: 71, MjId: "task_replaced_channel", ChannelId: channel.Id,
		ChannelGeneration: "retired-generation", ImageUrl: "/mj/image/upstream-image",
	}).Error)

	var factoryCalled atomic.Bool
	previousFactory := midjourneyImageHTTPClientFactory
	midjourneyImageHTTPClientFactory = func(string) (*http.Client, error) {
		factoryCalled.Store(true)
		return nil, nil
	}
	t.Cleanup(func() { midjourneyImageHTTPClientFactory = previousFactory })

	recorder := invokeMidjourneyImageProxy(t, "task_replaced_channel")
	assert.Equal(t, http.StatusConflict, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "midjourney_image_channel_generation_mismatch")
	assert.False(t, factoryCalled.Load())
}

func TestRelayMidjourneyMediaRejectsScopedTokenBeforeUpstreamIO(t *testing.T) {
	require.NoError(t, projecti18n.Init())
	db := setupMidjourneyImageProxyDatabase(t)
	require.NoError(t, db.Create(&model.Midjourney{
		UserId: 71, MjId: "task_scoped_midjourney", ChannelId: 906,
		Action: constant.MjActionImagine, ImageUrl: "https://image.example/result.jpg",
	}).Error)

	var factoryCalled atomic.Bool
	previousFactory := midjourneyImageHTTPClientFactory
	midjourneyImageHTTPClientFactory = func(string) (*http.Client, error) {
		factoryCalled.Store(true)
		return nil, nil
	}
	t.Cleanup(func() { midjourneyImageHTTPClientFactory = previousFactory })

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "task_scoped_midjourney"}}
	ctx.Set("id", 71)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/mj/image/task_scoped_midjourney", nil)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, map[string]bool{"mj_imagine": true})
	common.SetContextKey(ctx, constant.ContextKeyTokenSpecificChannelId, "907")

	RelayMidjourneyImage(ctx)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.False(t, factoryCalled.Load())
}

func TestRelayMidjourneyMediaRejectsUntrustedRelativePath(t *testing.T) {
	db := setupMidjourneyImageProxyDatabase(t)
	require.NoError(t, db.Create(&model.Midjourney{
		UserId: 71, MjId: "task_invalid_relative", ImageUrl: "/mj/image/../admin",
	}).Error)

	var factoryCalled atomic.Bool
	previousFactory := midjourneyImageHTTPClientFactory
	midjourneyImageHTTPClientFactory = func(string) (*http.Client, error) {
		factoryCalled.Store(true)
		return nil, nil
	}
	t.Cleanup(func() { midjourneyImageHTTPClientFactory = previousFactory })

	recorder := invokeMidjourneyImageProxy(t, "task_invalid_relative")
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "midjourney_image_url_not_allowed")
	assert.False(t, factoryCalled.Load())
}

func TestRelayMidjourneyImageDoesNotReturnUpstreamErrorBody(t *testing.T) {
	db := setupMidjourneyImageProxyDatabase(t)
	require.NoError(t, db.Create(&model.Midjourney{
		UserId: 71, MjId: "task_public_error", ImageUrl: "https://image.example/failure",
	}).Error)

	previousFactory := midjourneyImageHTTPClientFactory
	midjourneyImageHTTPClientFactory = func(string) (*http.Client, error) {
		return &http.Client{Transport: midjourneyImageRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Body:       io.NopCloser(strings.NewReader("upstream-secret-response")),
				Request:    request,
			}, nil
		})}, nil
	}
	t.Cleanup(func() { midjourneyImageHTTPClientFactory = previousFactory })

	recorder := invokeMidjourneyImageProxy(t, "task_public_error")
	assert.Equal(t, http.StatusBadGateway, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "midjourney_image_upstream_failed")
	assert.NotContains(t, recorder.Body.String(), "upstream-secret-response")
}

func TestRelayMidjourneyImageRejectsInsecureURLBeforeNetwork(t *testing.T) {
	db := setupMidjourneyImageProxyDatabase(t)
	require.NoError(t, db.Create(&model.Midjourney{
		UserId: 71, MjId: "task_public_insecure", ImageUrl: "http://image.example/result.jpg",
	}).Error)

	var factoryCalled atomic.Bool
	previousFactory := midjourneyImageHTTPClientFactory
	midjourneyImageHTTPClientFactory = func(string) (*http.Client, error) {
		factoryCalled.Store(true)
		return nil, nil
	}
	t.Cleanup(func() { midjourneyImageHTTPClientFactory = previousFactory })

	recorder := invokeMidjourneyImageProxy(t, "task_public_insecure")
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "midjourney_image_url_not_allowed")
	assert.False(t, factoryCalled.Load())
}

func TestRelayMidjourneyMediaRequiresTaskOwnership(t *testing.T) {
	db := setupMidjourneyImageProxyDatabase(t)
	require.NoError(t, db.Create(&model.Midjourney{
		UserId: 72, MjId: "task_owned_by_another_user",
		ImageUrl: "https://image.example/private.jpg",
		VideoUrl: "https://video.example/private.mp4",
	}).Error)

	var factoryCalled atomic.Bool
	previousFactory := midjourneyImageHTTPClientFactory
	midjourneyImageHTTPClientFactory = func(string) (*http.Client, error) {
		factoryCalled.Store(true)
		return nil, nil
	}
	t.Cleanup(func() { midjourneyImageHTTPClientFactory = previousFactory })

	imageRecorder := invokeMidjourneyImageProxyAsUser(t, 71, "task_owned_by_another_user")
	assert.Equal(t, http.StatusNotFound, imageRecorder.Code)
	videoRecorder := invokeMidjourneyVideoProxy(t, 71, "task_owned_by_another_user", "", nil)
	assert.Equal(t, http.StatusNotFound, videoRecorder.Code)
	assert.False(t, factoryCalled.Load())
}

func TestRelayMidjourneyVideoUsesLocalAuthenticatedProxyAndPreservesRange(t *testing.T) {
	db := setupMidjourneyImageProxyDatabase(t)
	videoURLs, err := common.Marshal([]dto.ImgUrls{{Url: "https://video.example/variant.mp4?sig=secret"}})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.Midjourney{
		UserId: 71, MjId: "task_public_video", ChannelId: 903,
		VideoUrl:  "https://video.example/main.mp4?sig=secret",
		VideoUrls: string(videoURLs),
	}).Error)

	videoBody := []byte("bounded-video")
	var capturedRequest *http.Request
	previousFactory := midjourneyImageHTTPClientFactory
	midjourneyImageHTTPClientFactory = func(string) (*http.Client, error) {
		return &http.Client{Transport: midjourneyImageRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			capturedRequest = request.Clone(request.Context())
			return &http.Response{
				StatusCode: http.StatusPartialContent,
				Header: http.Header{
					"Content-Type":  []string{"video/mp4"},
					"Content-Range": []string{"bytes 0-12/13"},
					"Accept-Ranges": []string{"bytes"},
				},
				Body:          io.NopCloser(bytes.NewReader(videoBody)),
				ContentLength: int64(len(videoBody)),
				Request:       request,
			}, nil
		})}, nil
	}
	t.Cleanup(func() { midjourneyImageHTTPClientFactory = previousFactory })

	recorder := invokeMidjourneyVideoProxy(t, 71, "task_public_video", "0", http.Header{"Range": []string{"bytes=0-12"}})
	require.NotNil(t, capturedRequest)
	assert.Equal(t, http.StatusPartialContent, recorder.Code)
	assert.Equal(t, videoBody, recorder.Body.Bytes())
	assert.Equal(t, "bytes=0-12", capturedRequest.Header.Get("Range"))
	assert.Equal(t, "https://video.example/variant.mp4?sig=secret", capturedRequest.URL.String())
	assert.Equal(t, "private, max-age=86400", recorder.Header().Get("Cache-Control"))
	assert.Equal(t, "Authorization, Cookie", recorder.Header().Get("Vary"))
}

func TestCoverMidjourneyTaskDtoNeverExposesProviderMediaURLs(t *testing.T) {
	previousServerAddress := system_setting.ServerAddress
	previousForwarding := setting.MjForwardUrlEnabled
	system_setting.ServerAddress = "https://gateway.example/"
	setting.MjForwardUrlEnabled = true
	t.Cleanup(func() {
		system_setting.ServerAddress = previousServerAddress
		setting.MjForwardUrlEnabled = previousForwarding
	})

	videoURLs, err := common.Marshal([]dto.ImgUrls{{Url: "https://provider.example/video.mp4?secret=1"}})
	require.NoError(t, err)
	publicTask := coverMidjourneyTaskDto(nil, &model.Midjourney{
		MjId: "task/media id", Status: "SUCCESS",
		ImageUrl:  "https://provider.example/image.png?secret=1",
		VideoUrl:  "https://provider.example/main.mp4?secret=1",
		VideoUrls: string(videoURLs),
	})

	assert.Equal(t, "https://gateway.example/mj/image/task%2Fmedia%20id", publicTask.ImageUrl)
	assert.Equal(t, "https://gateway.example/mj/video/task%2Fmedia%20id", publicTask.VideoUrl)
	require.Len(t, publicTask.VideoUrls, 1)
	assert.Equal(t, "https://gateway.example/mj/video/task%2Fmedia%20id/0", publicTask.VideoUrls[0].Url)
	encoded, err := common.Marshal(publicTask)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "provider.example")
	assert.NotContains(t, string(encoded), "secret=1")
}

func TestMidjourneyAcceptedReplaySnapshotFallsBackForOversizedOrInvalidProperties(t *testing.T) {
	for _, properties := range []any{strings.Repeat("x", int(maxTaskSubmissionResponseBytes)+1), math.NaN()} {
		body, replay, err := midjourneyAcceptedReplaySnapshot(dto.MidjourneyResponse{
			Code: 1, Description: "accepted", Properties: properties, Result: "task_public_mj",
		}, http.StatusOK, "task_public_mj")
		require.Error(t, err)
		assert.Less(t, len(body), int(maxTaskSubmissionResponseBytes))
		assert.Equal(t, http.StatusOK, replay.StatusCode)
		assert.Equal(t, body, replay.Body)
		assert.Contains(t, string(body), "task_public_mj")
		assert.NotContains(t, string(body), strings.Repeat("x", 128))
	}
}

func TestMidjourneyAcceptedReplaySnapshotFallsBackForWrongPublicIdentity(t *testing.T) {
	body, replay, err := midjourneyAcceptedReplaySnapshot(dto.MidjourneyResponse{
		Code: 1, Description: "accepted", Result: "task_other",
	}, http.StatusOK, "task_public_mj")
	require.Error(t, err)
	assert.Equal(t, http.StatusOK, replay.StatusCode)
	assert.Empty(t, replay.HeadersJSON)
	assert.Equal(t, body, replay.Body)
	assert.Contains(t, string(body), "task_public_mj")
	assert.NotContains(t, string(body), "task_other")
}

func TestFailMidjourneySubmissionTransfersRefundToDurableOperation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/midjourney-billing.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.Token{},
		&model.Log{},
		&model.Midjourney{},
		&model.MidjourneyBillingOperation{},
	))
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMainType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	previousRedisEnabled := common.RedisEnabled
	model.DB = db
	model.LOG_DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		common.RedisEnabled = previousRedisEnabled
	})

	const id = 902
	require.NoError(t, db.Create(&model.User{
		Id: id, Username: "midjourney-relay-user", AffCode: "midjourney-relay-aff",
		Quota: 900, Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, db.Create(&model.Token{
		Id: id, UserId: id, Key: "sk-midjourney-relay", Name: "midjourney-relay",
		Status: common.TokenStatusEnabled, RemainQuota: 900, UsedQuota: 100,
	}).Error)
	task := &model.Midjourney{
		UserId: id, Action: constant.MjActionImagine, MjId: "task_midjourney_relay",
		Status: "NOT_START", Progress: "0%", ChannelId: id, Quota: 100,
		Group: "default", BillingSource: service.BillingSourceWallet, TokenId: id,
		BillingProtocolVersion: model.TaskBillingLegacyProtocolVersion,
	}
	require.NoError(t, db.Create(task).Error)

	durable, err := failMidjourneySubmission(context.Background(), task, 4, "upstream failed")
	require.NoError(t, err)
	require.True(t, durable)
	var user model.User
	var token model.Token
	var persisted model.Midjourney
	require.NoError(t, db.First(&user, id).Error)
	require.NoError(t, db.First(&token, id).Error)
	require.NoError(t, db.First(&persisted, task.Id).Error)
	assert.Equal(t, 1000, user.Quota)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Zero(t, persisted.Quota)

	durable, err = failMidjourneySubmission(context.Background(), task, 4, "upstream failed")
	require.NoError(t, err)
	require.True(t, durable)
	require.NoError(t, db.First(&user, id).Error)
	assert.Equal(t, 1000, user.Quota)
	operation, err := model.GetMidjourneyBillingOperationByTaskID(context.Background(), task.Id)
	require.NoError(t, err)
	var logCount int64
	require.NoError(t, db.Model(&model.Log{}).Where("billing_operation_key = ?", operation.OperationKey).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)
}

func setupMidjourneyImageProxyDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/midjourney-image.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Midjourney{}, &model.RoutingCredentialRef{}))
	previousDB := model.DB
	previousMemoryCache := common.MemoryCacheEnabled
	previousSecret := common.CryptoSecret
	model.DB = db
	common.MemoryCacheEnabled = false
	common.CryptoSecret = "midjourney-media-test-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCache
		common.CryptoSecret = previousSecret
	})
	return db
}

func invokeMidjourneyImageProxy(t *testing.T, taskID string) *httptest.ResponseRecorder {
	return invokeMidjourneyImageProxyAsUser(t, 71, taskID)
}

func invokeMidjourneyImageProxyAsUser(t *testing.T, userID int, taskID string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: taskID}}
	context.Set("id", userID)
	context.Request = httptest.NewRequest(http.MethodGet, "/mj/image/"+taskID, nil)
	RelayMidjourneyImage(context)
	return recorder
}

func invokeMidjourneyVideoProxy(t *testing.T, userID int, taskID, index string, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: taskID}}
	path := "/mj/video/" + taskID
	if index != "" {
		context.Params = append(context.Params, gin.Param{Key: "index", Value: index})
		path += "/" + index
	}
	context.Set("id", userID)
	context.Request = httptest.NewRequest(http.MethodGet, path, nil)
	context.Request.Header = headers.Clone()
	RelayMidjourneyVideo(context)
	return recorder
}

type midjourneyImageRoundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip midjourneyImageRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
