package controller

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRoutingSub2APIFencingTest(t *testing.T) {
	t.Helper()
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
	})

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-fencing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
}

func routingSub2APITestAuthKey(channelID int) routingSub2APIAuthKey {
	return routingSub2APIAuthKey{
		ChannelID:   channelID,
		Fingerprint: fmt.Sprintf("test-fingerprint-%d", channelID),
	}
}

func routingSub2APITestJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func TestRoutingSub2APIJWTAuthenticationChangesDoNotReuseCachedToken(t *testing.T) {
	tests := []struct {
		name      string
		baseURL   string
		email     string
		password  string
		newOrigin bool
	}{
		{
			name:      "base URL",
			baseURL:   "https://new.example.com",
			email:     "old@example.com",
			password:  "old-password",
			newOrigin: true,
		},
		{
			name:     "email",
			baseURL:  "https://old.example.com",
			email:    "new@example.com",
			password: "old-password",
		},
		{
			name:     "password",
			baseURL:  "https://old.example.com",
			email:    "old@example.com",
			password: "new-password",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupRoutingSub2APIFencingTest(t)

			type loginRequest struct {
				host     string
				email    string
				password string
			}
			loginRequests := make([]loginRequest, 0, 2)
			probeAuthorization := ""
			restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/api/v1/auth/login":
					var payload struct {
						Email    string `json:"email"`
						Password string `json:"password"`
					}
					if err := common.DecodeJson(request.Body, &payload); err != nil {
						return nil, err
					}
					loginRequests = append(loginRequests, loginRequest{
						host:     request.URL.Host,
						email:    payload.Email,
						password: payload.Password,
					})
					token := fmt.Sprintf("jwt-%d", len(loginRequests))
					return routingSub2APITestJSONResponse(fmt.Sprintf(`{"code":0,"data":{"token":%q,"expires_in":3600}}`, token)), nil
				case "/probe":
					probeAuthorization = request.Header.Get("Authorization")
					return routingSub2APITestJSONResponse(`{"code":0,"data":{}}`), nil
				default:
					return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
				}
			}))

			oldCredentials := model.RoutingCredentials{
				Sub2APIEmail:    "old@example.com",
				Sub2APIPassword: "old-password",
			}
			oldBinding := model.RoutingChannelBinding{
				ID:           1001,
				ChannelID:    701,
				UpstreamType: model.RoutingUpstreamTypeSub2API,
				BaseURL:      "https://old.example.com",
			}
			require.NoError(t, oldBinding.SetCredentials(oldCredentials))

			newCredentials := oldCredentials
			newCredentials.Sub2APIEmail = tt.email
			newCredentials.Sub2APIPassword = tt.password
			newBinding := oldBinding
			newBinding.BaseURL = tt.baseURL
			if newCredentials != oldCredentials {
				require.NoError(t, newBinding.SetCredentials(newCredentials))
			}

			oldToken, err := routingSub2APIJWT(context.Background(), oldBinding, oldCredentials)
			require.NoError(t, err)
			newToken, err := routingSub2APIJWT(context.Background(), newBinding, newCredentials)
			require.NoError(t, err)
			if tt.newOrigin {
				_, err = routingSub2APIRequest(context.Background(), newBinding, newCredentials, http.MethodGet, "/probe", newToken, nil)
				require.NoError(t, err)
			}

			assert.Equal(t, "jwt-1", oldToken)
			assert.Equal(t, "jwt-2", newToken)
			require.Len(t, loginRequests, 2)
			assert.Equal(t, tt.email, loginRequests[1].email)
			assert.Equal(t, tt.password, loginRequests[1].password)
			if tt.newOrigin {
				assert.Equal(t, "new.example.com", loginRequests[1].host)
				assert.Equal(t, "Bearer jwt-2", probeAuthorization)
				assert.NotEqual(t, "Bearer jwt-1", probeAuthorization)
			}
		})
	}
}

func TestRoutingSub2APIJWTRecreatedBindingDoesNotReuseDeletedBindingToken(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)

	loginCount := 0
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/auth/login" {
			return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
		}
		loginCount++
		token := fmt.Sprintf("jwt-%d", loginCount)
		return routingSub2APITestJSONResponse(fmt.Sprintf(`{"code":0,"data":{"token":%q,"expires_in":3600}}`, token)), nil
	}))

	credentialsCiphertext := "persisted-credential-ciphertext"
	credentials := model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "same-password",
	}
	oldBinding := model.RoutingChannelBinding{
		ID:             2001,
		ChannelID:      702,
		UpstreamType:   model.RoutingUpstreamTypeSub2API,
		BaseURL:        "https://routing.example.com",
		EncCredentials: &credentialsCiphertext,
		KeyVersion:     model.RoutingCredentialKeyVersion,
	}
	newBinding := oldBinding
	newBinding.ID = 2002

	oldToken, err := routingSub2APIJWT(context.Background(), oldBinding, credentials)
	require.NoError(t, err)
	newToken, err := routingSub2APIJWT(context.Background(), newBinding, credentials)
	require.NoError(t, err)

	assert.Equal(t, "jwt-1", oldToken)
	assert.Equal(t, "jwt-2", newToken)
	assert.Equal(t, 2, loginCount)
}

func TestRoutingSub2APIAuthFingerprintScopesCacheCoordinationKeys(t *testing.T) {
	oldCiphertext := "old-encrypted-credentials"
	newCiphertext := "new-encrypted-credentials"
	oldBinding := model.RoutingChannelBinding{
		ID:             3001,
		ChannelID:      703,
		UpstreamType:   model.RoutingUpstreamTypeSub2API,
		BaseURL:        "https://routing.example.com",
		EncCredentials: &oldCiphertext,
		KeyVersion:     model.RoutingCredentialKeyVersion,
	}
	newBinding := oldBinding
	newBinding.EncCredentials = &newCiphertext
	credentials := model.RoutingCredentials{
		Sub2APIEmail:    "operator@example.com",
		Sub2APIPassword: "plaintext-password",
	}

	oldKey := newRoutingSub2APIAuthKey(oldBinding, credentials)
	newKey := newRoutingSub2APIAuthKey(newBinding, credentials)

	assert.NotEqual(t, oldKey, newKey)
	assert.NotEqual(t, routingSub2APIRedisJWTKey(oldKey), routingSub2APIRedisJWTKey(newKey))
	assert.NotEqual(t, routingSub2APIRedisLockKey(oldKey), routingSub2APIRedisLockKey(newKey))
	assert.NotEqual(t, routingSub2APISingleflightKey(oldKey), routingSub2APISingleflightKey(newKey))
	for _, key := range []string{
		routingSub2APIRedisJWTKey(oldKey),
		routingSub2APIRedisLockKey(oldKey),
		routingSub2APISingleflightKey(oldKey),
	} {
		assert.NotContains(t, key, credentials.Sub2APIEmail)
		assert.NotContains(t, key, credentials.Sub2APIPassword)
		assert.NotContains(t, key, oldCiphertext)
	}
	changedStoredCredentials := credentials
	changedStoredCredentials.Sub2APIPassword = "different-plaintext-password"
	assert.Equal(t, oldKey, newRoutingSub2APIAuthKey(oldBinding, changedStoredCredentials))

	inlineBinding := oldBinding
	inlineBinding.ID = 0
	inlineBinding.EncCredentials = nil
	inlineBinding.KeyVersion = 0
	changedInlineEmail := credentials
	changedInlineEmail.Sub2APIEmail = "different@example.com"
	changedInlinePassword := credentials
	changedInlinePassword.Sub2APIPassword = "different-password"
	assert.NotEqual(t,
		newRoutingSub2APIAuthKey(inlineBinding, credentials),
		newRoutingSub2APIAuthKey(inlineBinding, changedInlineEmail),
	)
	assert.NotEqual(t,
		newRoutingSub2APIAuthKey(inlineBinding, credentials),
		newRoutingSub2APIAuthKey(inlineBinding, changedInlinePassword),
	)

	slashBinding := oldBinding
	slashBinding.BaseURL += "/"
	assert.Equal(t, oldKey, newRoutingSub2APIAuthKey(slashBinding, credentials))
}

func TestRoutingSub2APIInlineAuthFingerprintIgnoresRandomCredentialCiphertext(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)

	credentials := model.RoutingCredentials{
		Sub2APIEmail:    " operator@example.com ",
		Sub2APIPassword: "plaintext-password",
	}
	binding := model.RoutingChannelBinding{
		ChannelID:    703,
		UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL:      "https://routing.example.com/",
	}

	require.NoError(t, binding.SetCredentials(credentials))
	firstCiphertext := *binding.EncCredentials
	firstKey := newRoutingSub2APIAuthKey(binding, credentials)
	require.NoError(t, binding.SetCredentials(credentials))
	secondKey := newRoutingSub2APIAuthKey(binding, credentials)

	assert.NotEqual(t, firstCiphertext, *binding.EncCredentials)
	assert.Equal(t, firstKey, secondKey)
	assert.Equal(t, firstKey, newRoutingSub2APIAuthKey(binding, model.RoutingCredentials{
		Sub2APIEmail:    "operator@example.com",
		Sub2APIPassword: credentials.Sub2APIPassword,
	}))
	assert.NotEqual(t, firstKey, newRoutingSub2APIAuthKey(binding, model.RoutingCredentials{
		Sub2APIEmail:    "changed@example.com",
		Sub2APIPassword: credentials.Sub2APIPassword,
	}))
	assert.NotEqual(t, firstKey, newRoutingSub2APIAuthKey(binding, model.RoutingCredentials{
		Sub2APIEmail:    credentials.Sub2APIEmail,
		Sub2APIPassword: "changed-password",
	}))
	for _, key := range []string{
		firstKey.Fingerprint,
		routingSub2APIRedisJWTKey(firstKey),
		routingSub2APIRedisLockKey(firstKey),
		routingSub2APISingleflightKey(firstKey),
	} {
		assert.NotContains(t, key, "operator@example.com")
		assert.NotContains(t, key, credentials.Sub2APIPassword)
	}
}

func TestRoutingSub2APIJWTDoesNotCoalesceDistinctAuthIdentities(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)

	started := make(chan string, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseLogins := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseLogins)
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/auth/login" {
			return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
		}
		started <- request.URL.Host
		<-release
		token := "jwt-" + request.URL.Host
		return routingSub2APITestJSONResponse(fmt.Sprintf(`{"code":0,"data":{"token":%q,"expires_in":3600}}`, token)), nil
	}))

	credentials := model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "password",
	}
	bindings := []model.RoutingChannelBinding{
		{ID: 4001, ChannelID: 704, UpstreamType: model.RoutingUpstreamTypeSub2API, BaseURL: "https://first.example.com"},
		{ID: 4002, ChannelID: 704, UpstreamType: model.RoutingUpstreamTypeSub2API, BaseURL: "https://second.example.com"},
	}
	type loginResult struct {
		token string
		err   error
	}
	results := make(chan loginResult, len(bindings))
	for _, binding := range bindings {
		binding := binding
		go func() {
			token, err := routingSub2APIJWT(context.Background(), binding, credentials)
			results <- loginResult{token: token, err: err}
		}()
	}

	hosts := make(map[string]struct{}, 2)
	for range bindings {
		select {
		case host := <-started:
			hosts[host] = struct{}{}
		case <-time.After(500 * time.Millisecond):
			require.FailNow(t, "distinct auth identity joined another identity's singleflight login")
		}
	}
	releaseLogins()

	for range bindings {
		result := <-results
		require.NoError(t, result.err)
		assert.Contains(t, result.token, ".example.com")
	}
	assert.Len(t, hosts, 2)
}

func TestFetchRoutingSub2APICostSnapshotsReloginsOnceAfterManagedJWTRejection(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)
	setupModelListControllerTestDB(t)

	loginCount := 0
	profileAuthorizations := make([]string, 0, 3)
	groupAuthorizations := make([]string, 0, 3)
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		authorization := request.Header.Get("Authorization")
		switch request.URL.Path {
		case "/api/v1/auth/login":
			loginCount++
			token := fmt.Sprintf("jwt-%d", loginCount)
			return routingSub2APITestJSONResponse(fmt.Sprintf(`{"code":0,"data":{"token":%q,"expires_in":86400}}`, token)), nil
		case "/api/v1/auth/me":
			profileAuthorizations = append(profileAuthorizations, authorization)
			if authorization == "Bearer jwt-1" {
				response := routingSub2APITestJSONResponse(`{"code":401,"message":"expired token"}`)
				response.StatusCode = http.StatusUnauthorized
				response.Status = "401 Unauthorized"
				return response, nil
			}
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"id":1,"balance":1}}`), nil
		case "/api/v1/groups/available":
			groupAuthorizations = append(groupAuthorizations, authorization)
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"id":10,"name":"vip","platform":"openai","subscription_type":"standard","rate_multiplier":1}]}`), nil
		case "/api/v1/groups/rates":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"10":1}}`), nil
		case "/api/v1/channels/available":
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"name":"primary","platforms":[{"platform":"openai","groups":[{"id":10,"name":"vip","platform":"openai","subscription_type":"standard","rate_multiplier":1}],"supported_models":[{"name":"gpt-test","platform":"openai","pricing":{"billing_mode":"token","input_price":0.000001,"output_price":0.000002,"cache_write_price":0,"cache_read_price":0,"image_output_price":0,"intervals":[]}}]}]}]}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
		}
	}))

	binding := model.RoutingChannelBinding{
		ChannelID:     708,
		UpstreamType:  model.RoutingUpstreamTypeSub2API,
		BaseURL:       "https://routing.example.com",
		UpstreamGroup: "vip",
	}
	credentials := model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "password",
		GatewayAPIKey:   "supplemental-gateway-key",
	}

	firstSnapshots, err := fetchRoutingSub2APICostSnapshots(context.Background(), binding, credentials)
	require.NoError(t, err)
	require.Len(t, firstSnapshots, 1)
	secondSnapshots, err := fetchRoutingSub2APICostSnapshots(context.Background(), binding, credentials)
	require.NoError(t, err)
	require.Len(t, secondSnapshots, 1)

	assert.Equal(t, 2, loginCount)
	assert.Equal(t, []string{"Bearer jwt-1", "Bearer jwt-2", "Bearer jwt-2"}, profileAuthorizations)
	assert.Equal(t, []string{"Bearer jwt-2", "Bearer jwt-2"}, groupAuthorizations)
}

func TestFetchRoutingSub2APICostSnapshotsDoesNotRetryExplicitToken(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)

	loginCount := 0
	groupCount := 0
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/auth/login":
			loginCount++
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"token":"unexpected"}}`), nil
		case "/api/v1/auth/me":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"id":1}}`), nil
		case "/api/v1/groups/available":
			groupCount++
			response := routingSub2APITestJSONResponse(`{"code":403,"message":"forbidden"}`)
			response.StatusCode = http.StatusForbidden
			response.Status = "403 Forbidden"
			return response, nil
		default:
			return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
		}
	}))

	_, err := fetchRoutingSub2APICostSnapshots(context.Background(), model.RoutingChannelBinding{
		ChannelID:     709,
		UpstreamType:  model.RoutingUpstreamTypeSub2API,
		BaseURL:       "https://routing.example.com",
		UpstreamGroup: "vip",
	}, model.RoutingCredentials{Sub2APIToken: "configured-token"})

	require.Error(t, err)
	assert.False(t, routingUpstreamAuthError(err))
	assert.Zero(t, loginCount)
	assert.Equal(t, 1, groupCount)
}

func TestFetchRoutingSub2APICostSnapshotsDoesNotReloginManagedJWTOnCapabilityForbidden(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)

	loginCount := 0
	groupCount := 0
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/auth/login":
			loginCount++
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"token":"managed-jwt","expires_in":86400}}`), nil
		case "/api/v1/auth/me":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"id":1}}`), nil
		case "/api/v1/groups/available":
			groupCount++
			response := routingSub2APITestJSONResponse(`{"code":403,"message":"Backend mode is active. User self-service is disabled."}`)
			response.StatusCode = http.StatusForbidden
			response.Status = "403 Forbidden"
			return response, nil
		default:
			return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
		}
	}))

	_, err := fetchRoutingSub2APICostSnapshots(context.Background(), model.RoutingChannelBinding{
		ChannelID:     710,
		UpstreamType:  model.RoutingUpstreamTypeSub2API,
		BaseURL:       "https://routing.example.com",
		UpstreamGroup: "vip",
	}, model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "password",
	})

	require.Error(t, err)
	assert.False(t, routingUpstreamAuthError(err))
	assert.Contains(t, err.Error(), "Backend mode")
	assert.Equal(t, 1, loginCount)
	assert.Equal(t, 1, groupCount)
}

func TestRoutingSub2APIRequestSanitizesManagedBearerFromEnvelopeError(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)

	const managedJWT = "managed-jwt-secret"
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(*http.Request) (*http.Response, error) {
		return routingSub2APITestJSONResponse(`{"code":401,"message":"invalid token managed-jwt-secret"}`), nil
	}))
	credentials := model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "password",
	}

	_, err := routingSub2APIRequest(context.Background(), model.RoutingChannelBinding{
		ChannelID: 712,
		BaseURL:   "https://routing.example.com",
	}, credentials, http.MethodGet, "/api/v1/groups/available", managedJWT, nil)

	require.Error(t, err)
	assert.NotContains(t, err.Error(), managedJWT)
	message := err.Error()
	view := buildRoutingBindingView(model.RoutingChannelBinding{
		ChannelID:     712,
		LastSyncError: &message,
	}, credentials)
	require.NotNil(t, view.LastSyncError)
	assert.NotContains(t, *view.LastSyncError, managedJWT)
}

func TestRoutingSub2APIJWTInFlightLoginCannotPublishAfterBindingMutation(t *testing.T) {
	tests := []struct {
		name   string
		action func(*testing.T, int) *httptest.ResponseRecorder
	}{
		{
			name: "update",
			action: func(t *testing.T, channelID int) *httptest.ResponseRecorder {
				body := []byte(`{
					"upstream_type":"sub2api",
					"base_url":"https://new.example.com",
					"upstream_group":"vip",
					"enabled":true,
					"credentials":{"sub2api_password":"new-password"}
				}`)
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Params = gin.Params{{Key: "channelId", Value: strconv.Itoa(channelID)}}
				ctx.Request = httptest.NewRequest(http.MethodPut, "/api/smart-routing/bindings/704", bytes.NewReader(body))
				setSmartRoutingBindingIfMatchForTest(t, ctx, channelID)
				UpdateSmartRoutingBinding(ctx)
				return recorder
			},
		},
		{
			name: "delete",
			action: func(t *testing.T, channelID int) *httptest.ResponseRecorder {
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Params = gin.Params{{Key: "channelId", Value: strconv.Itoa(channelID)}}
				ctx.Request = httptest.NewRequest(http.MethodDelete, "/api/smart-routing/bindings/704", nil)
				setSmartRoutingBindingIfMatchForTest(t, ctx, channelID)
				DeleteSmartRoutingBinding(ctx)
				return recorder
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupRoutingSub2APIFencingTest(t)
			db := setupModelListControllerTestDB(t)
			require.NoError(t, db.AutoMigrate(
				&model.RoutingChannelBinding{},
				&model.RoutingCostSnapshot{},
				&model.RoutingBreakerState{},
				&model.RoutingChannelMetric{},
				&model.RoutingChannelHealthState{},
			))

			credentials := model.RoutingCredentials{
				Sub2APIEmail:    "admin@example.com",
				Sub2APIPassword: "old-password",
			}
			binding := model.RoutingChannelBinding{
				ChannelID:     705,
				UpstreamType:  model.RoutingUpstreamTypeSub2API,
				BaseURL:       "https://old.example.com",
				UpstreamGroup: "vip",
				Enabled:       true,
			}
			require.NoError(t, binding.SetCredentials(credentials))
			require.NoError(t, db.Create(&binding).Error)

			loginStarted := make(chan struct{})
			releaseLogin := make(chan struct{})
			var startedOnce sync.Once
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseLogin) }) }
			t.Cleanup(release)
			restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != "/api/v1/auth/login" {
					return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
				}
				startedOnce.Do(func() { close(loginStarted) })
				select {
				case <-releaseLogin:
					return routingSub2APITestJSONResponse(`{"code":0,"data":{"token":"retired-jwt","expires_in":3600}}`), nil
				case <-request.Context().Done():
					return nil, request.Context().Err()
				}
			}))

			type loginResult struct {
				token string
				err   error
			}
			resultChannel := make(chan loginResult, 1)
			go func() {
				token, err := routingSub2APIJWT(context.Background(), binding, credentials)
				resultChannel <- loginResult{token: token, err: err}
			}()

			select {
			case <-loginStarted:
			case <-time.After(time.Second):
				require.FailNow(t, "login did not start")
			}

			staleKey := routingSub2APIAuthKey{ChannelID: binding.ChannelID, Fingerprint: "stale-fingerprint"}
			setRoutingSub2APICachedJWT(context.Background(), staleKey, "stale-jwt", time.Hour)
			require.Equal(t, 1, RoutingSub2APIJWTCacheRuntimeStats().Entries)

			recorder := tt.action(t, binding.ChannelID)
			require.Equal(t, http.StatusOK, recorder.Code)
			assert.Zero(t, RoutingSub2APIJWTCacheRuntimeStats().Entries)

			release()
			var result loginResult
			select {
			case result = <-resultChannel:
			case <-time.After(time.Second):
				require.FailNow(t, "retired login did not finish")
			}
			require.NoError(t, result.err)
			assert.Equal(t, "retired-jwt", result.token)
			assert.Zero(t, RoutingSub2APIJWTCacheRuntimeStats().Entries)
		})
	}
}

func TestUpdateSmartRoutingBindingGroupOnlyChangeKeepsSub2APIJWTIdentityActive(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelMetric{},
		&model.RoutingChannelHealthState{},
	))

	credentials := model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "password",
	}
	binding := model.RoutingChannelBinding{
		ChannelID:     707,
		UpstreamType:  model.RoutingUpstreamTypeSub2API,
		BaseURL:       "https://routing.example.com",
		UpstreamGroup: "standard",
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(credentials))
	require.NoError(t, db.Create(&binding).Error)
	authKey := newRoutingSub2APIAuthKey(binding, credentials)
	setRoutingSub2APICachedJWT(context.Background(), authKey, "cached-jwt", time.Hour)

	body := []byte(`{
		"upstream_type":"sub2api",
		"base_url":"https://routing.example.com",
		"upstream_group":"vip",
		"enabled":true
	}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: strconv.Itoa(binding.ChannelID)}}
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/smart-routing/bindings/707", bytes.NewReader(body))
	setSmartRoutingBindingIfMatchForTest(t, ctx, binding.ChannelID)
	UpdateSmartRoutingBinding(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	token, ok := getRoutingSub2APICachedJWT(context.Background(), authKey)
	assert.True(t, ok)
	assert.Equal(t, "cached-jwt", token)
}

func TestUpdateSmartRoutingBindingReactivatesPreviouslyRetiredSub2APIAuthKey(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelMetric{},
		&model.RoutingChannelHealthState{},
	))
	state := &routingSub2APIFencingRedis{values: map[string]string{}, ttlMillis: map[string]int64{}}
	common.RedisEnabled = true
	common.RDB = newRoutingSub2APIFencingRedisClient(t, state)

	credentials := model.RoutingCredentials{Sub2APIEmail: "admin@example.com", Sub2APIPassword: "password"}
	binding := model.RoutingChannelBinding{
		ChannelID: 713, UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL: "https://first.example.com", UpstreamGroup: "vip", Enabled: true,
	}
	require.NoError(t, binding.SetCredentials(credentials))
	require.NoError(t, db.Create(&binding).Error)
	firstAuthKey := newRoutingSub2APIAuthKey(binding, credentials)

	update := func(baseURL string) {
		body := []byte(fmt.Sprintf(`{"upstream_type":"sub2api","base_url":%q,"upstream_group":"vip","enabled":true}`, baseURL))
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "channelId", Value: strconv.Itoa(binding.ChannelID)}}
		ctx.Request = httptest.NewRequest(http.MethodPut, "/api/smart-routing/bindings/713", bytes.NewReader(body))
		setSmartRoutingBindingIfMatchForTest(t, ctx, binding.ChannelID)
		UpdateSmartRoutingBinding(ctx)
		require.Equal(t, http.StatusOK, recorder.Code)
	}
	update("https://second.example.com")
	update("https://first.example.com")

	state.Lock()
	_, remainsRetired := state.values[routingSub2APIRedisRetiredKey(firstAuthKey)]
	state.Unlock()
	assert.False(t, remainsRetired)
	setRoutingSub2APICachedJWT(context.Background(), firstAuthKey, "reactivated-jwt", time.Hour)
	token, ok := getRoutingSub2APICachedJWT(context.Background(), firstAuthKey)
	assert.True(t, ok)
	assert.Equal(t, "reactivated-jwt", token)
}

func TestInvalidatedRoutingSub2APIAuthCannotReloginWithoutRedisUntilReactivated(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)

	loginCount := 0
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(*http.Request) (*http.Response, error) {
		loginCount++
		return routingSub2APITestJSONResponse(`{"code":0,"data":{"token":"jwt","expires_in":3600}}`), nil
	}))
	credentials := model.RoutingCredentials{Sub2APIEmail: "admin@example.com", Sub2APIPassword: "password"}
	binding := model.RoutingChannelBinding{
		ID: 6001, ChannelID: 714, UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL: "https://routing.example.com",
	}
	ciphertext := "persisted-ciphertext"
	binding.EncCredentials = &ciphertext
	binding.KeyVersion = model.RoutingCredentialKeyVersion
	invalidateRoutingSub2APIJWT(context.Background(), binding)

	token, err := routingSub2APIJWT(context.Background(), binding, credentials)
	require.Error(t, err)
	assert.Empty(t, token)
	assert.Zero(t, loginCount)

	activationFence, err := prepareRoutingSub2APIJWTActivation(context.Background(), binding)
	require.NoError(t, err)
	activateRoutingSub2APIJWT(context.Background(), activationFence)
	token, err = routingSub2APIJWT(context.Background(), binding, credentials)
	require.NoError(t, err)
	assert.Equal(t, "jwt", token)
	assert.Equal(t, 1, loginCount)
}

func TestEvictRoutingSub2APIJWTDoesNotDeleteTokenRefreshedAfterRejectedJWT(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)
	authKey := routingSub2APITestAuthKey(715)

	setRoutingSub2APICachedJWT(context.Background(), authKey, "rejected-jwt", time.Hour)
	evictRoutingSub2APIJWT(context.Background(), authKey, "rejected-jwt")
	setRoutingSub2APICachedJWT(context.Background(), authKey, "refreshed-jwt", time.Hour)
	evictRoutingSub2APIJWT(context.Background(), authKey, "rejected-jwt")

	token, ok := getRoutingSub2APICachedJWT(context.Background(), authKey)
	assert.True(t, ok)
	assert.Equal(t, "refreshed-jwt", token)
}

func TestEvictRoutingSub2APIJWTUsesRedisCompareAndDelete(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)
	state := &routingSub2APIFencingRedis{values: map[string]string{}, ttlMillis: map[string]int64{}}
	common.RedisEnabled = true
	common.RDB = newRoutingSub2APIFencingRedisClient(t, state)
	authKey := routingSub2APITestAuthKey(716)

	setRoutingSub2APICachedJWT(context.Background(), authKey, "rejected-jwt", time.Hour)
	evictRoutingSub2APIJWT(context.Background(), authKey, "rejected-jwt")
	setRoutingSub2APICachedJWT(context.Background(), authKey, "refreshed-jwt", time.Hour)
	evictRoutingSub2APIJWT(context.Background(), authKey, "rejected-jwt")

	token, ok := getRoutingSub2APICachedJWT(context.Background(), authKey)
	assert.True(t, ok)
	assert.Equal(t, "refreshed-jwt", token)
}

func TestActivateRoutingSub2APIJWTDoesNotDeleteRetirementMarkerCreatedAfterPreparation(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)
	state := &routingSub2APIFencingRedis{
		values:           map[string]string{},
		ttlMillis:        map[string]int64{},
		replaceBeforeCAS: map[string]string{},
	}
	common.RedisEnabled = true
	common.RDB = newRoutingSub2APIFencingRedisClient(t, state)
	ciphertext := "persisted-ciphertext"
	binding := model.RoutingChannelBinding{
		ID: 6002, ChannelID: 717, UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL: "https://routing.example.com", EncCredentials: &ciphertext,
		KeyVersion: model.RoutingCredentialKeyVersion,
	}
	retiredKey := routingSub2APIRedisRetiredKey(newRoutingSub2APIAuthKey(binding, model.RoutingCredentials{}))
	state.values[retiredKey] = "older-marker"
	activationFence, err := prepareRoutingSub2APIJWTActivation(context.Background(), binding)
	require.NoError(t, err)

	// Model a delete committed by another instance after this update commits but
	// before its prepared activation is applied.
	state.replaceBeforeCAS[retiredKey] = "newer-delete-marker"

	activateRoutingSub2APIJWT(context.Background(), activationFence)

	state.Lock()
	marker := state.values[retiredKey]
	state.Unlock()
	assert.Equal(t, "newer-delete-marker", marker)
}

func TestActivateRoutingSub2APIJWTPreservesDeleteRetirementCreatedAfterPreparation(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)
	state := &routingSub2APIFencingRedis{values: map[string]string{}, ttlMillis: map[string]int64{}}
	common.RedisEnabled = true
	common.RDB = newRoutingSub2APIFencingRedisClient(t, state)
	ciphertext := "persisted-ciphertext"
	binding := model.RoutingChannelBinding{
		ID: 6004, ChannelID: 719, UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL: "https://routing.example.com", EncCredentials: &ciphertext,
		KeyVersion: model.RoutingCredentialKeyVersion,
	}
	authKey := newRoutingSub2APIAuthKey(binding, model.RoutingCredentials{})
	activationFence, err := prepareRoutingSub2APIJWTActivation(context.Background(), binding)
	require.NoError(t, err)

	// The update associated with activation has committed. A delete on another
	// instance now retires the same identity before activation is applied.
	invalidateRoutingSub2APIJWT(context.Background(), binding)
	activateRoutingSub2APIJWT(context.Background(), activationFence)

	state.Lock()
	_, remainsRetired := state.values[routingSub2APIRedisRetiredKey(authKey)]
	state.Unlock()
	assert.True(t, remainsRetired)
	routingSub2APILoginCoordinator.RLock()
	_, remainsLocallyRetired := routingSub2APILoginCoordinator.retired[authKey]
	routingSub2APILoginCoordinator.RUnlock()
	assert.True(t, remainsLocallyRetired)
}

func TestActivateRoutingSub2APIJWTClearsRetirementObservedBeforePreparation(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)
	state := &routingSub2APIFencingRedis{values: map[string]string{}, ttlMillis: map[string]int64{}}
	common.RedisEnabled = true
	common.RDB = newRoutingSub2APIFencingRedisClient(t, state)
	ciphertext := "persisted-ciphertext"
	binding := model.RoutingChannelBinding{
		ID: 6003, ChannelID: 718, UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL: "https://routing.example.com", EncCredentials: &ciphertext,
		KeyVersion: model.RoutingCredentialKeyVersion,
	}
	authKey := newRoutingSub2APIAuthKey(binding, model.RoutingCredentials{})
	invalidateRoutingSub2APIJWT(context.Background(), binding)

	activationFence, err := prepareRoutingSub2APIJWTActivation(context.Background(), binding)
	require.NoError(t, err)
	activateRoutingSub2APIJWT(context.Background(), activationFence)

	state.Lock()
	_, remainsRetired := state.values[routingSub2APIRedisRetiredKey(authKey)]
	state.Unlock()
	assert.False(t, remainsRetired)
	routingSub2APILoginCoordinator.RLock()
	_, remainsLocallyRetired := routingSub2APILoginCoordinator.retired[authKey]
	routingSub2APILoginCoordinator.RUnlock()
	assert.False(t, remainsLocallyRetired)
}

func TestUpdateSmartRoutingBindingCredentialRewriteRetiresPreviousSub2APIAuthKey(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelMetric{},
		&model.RoutingChannelHealthState{},
	))

	credentials := model.RoutingCredentials{
		GatewayAPIKey:   "old-gateway-key",
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "password",
	}
	binding := model.RoutingChannelBinding{
		ChannelID:     710,
		UpstreamType:  model.RoutingUpstreamTypeSub2API,
		BaseURL:       "https://routing.example.com",
		UpstreamGroup: "vip",
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(credentials))
	require.NoError(t, db.Create(&binding).Error)
	oldAuthKey := newRoutingSub2APIAuthKey(binding, credentials)
	setRoutingSub2APICachedJWT(context.Background(), oldAuthKey, "cached-jwt", time.Hour)

	body := []byte(`{
		"upstream_type":"sub2api",
		"base_url":"https://routing.example.com",
		"upstream_group":"vip",
		"enabled":true,
		"credentials":{"gateway_api_key":"new-gateway-key"}
	}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: strconv.Itoa(binding.ChannelID)}}
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/smart-routing/bindings/710", bytes.NewReader(body))
	setSmartRoutingBindingIfMatchForTest(t, ctx, binding.ChannelID)
	UpdateSmartRoutingBinding(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	_, oldKeyStillCached := getRoutingSub2APICachedJWT(context.Background(), oldAuthKey)
	assert.False(t, oldKeyStillCached)
	var updated model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", binding.ChannelID).First(&updated).Error)
	assert.NotEqual(t, oldAuthKey, newRoutingSub2APIAuthKey(updated, model.RoutingCredentials{}))
}

type routingSub2APIFencingRedis struct {
	sync.Mutex
	values           map[string]string
	ttlMillis        map[string]int64
	commandLog       [][]string
	replaceBeforeCAS map[string]string
}

func newRoutingSub2APIFencingRedisClient(t *testing.T, state *routingSub2APIFencingRedis) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{
		Addr: "pipe",
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			clientConn, serverConn := net.Pipe()
			go state.serve(serverConn)
			return clientConn, nil
		},
		MaxRetries: -1,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func (state *routingSub2APIFencingRedis) serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		arguments, err := readRoutingSub2APIFencingRedisCommand(reader)
		if err != nil {
			return
		}
		state.Lock()
		state.commandLog = append(state.commandLog, arguments)
		command := strings.ToLower(arguments[0])
		switch command {
		case "set":
			state.values[arguments[1]] = arguments[2]
			_, _ = fmt.Fprint(conn, "+OK\r\n")
		case "get":
			value, ok := state.values[arguments[1]]
			if !ok {
				_, _ = fmt.Fprint(conn, "$-1\r\n")
				break
			}
			_, _ = fmt.Fprintf(conn, "$%d\r\n%s\r\n", len(value), value)
		case "del":
			deleted := 0
			for _, key := range arguments[1:] {
				if _, ok := state.values[key]; ok {
					deleted++
					delete(state.values, key)
				}
			}
			_, _ = fmt.Fprintf(conn, ":%d\r\n", deleted)
		case "mget":
			_, _ = fmt.Fprintf(conn, "*%d\r\n", len(arguments)-1)
			for _, key := range arguments[1:] {
				value, ok := state.values[key]
				if !ok {
					_, _ = fmt.Fprint(conn, "$-1\r\n")
					continue
				}
				_, _ = fmt.Fprintf(conn, "$%d\r\n%s\r\n", len(value), value)
			}
		case "eval":
			keyCount, parseErr := strconv.Atoi(arguments[2])
			if parseErr != nil || len(arguments) < 3+keyCount {
				_, _ = fmt.Fprint(conn, "-ERR invalid eval\r\n")
				state.Unlock()
				continue
			}
			keys := arguments[3 : 3+keyCount]
			args := arguments[3+keyCount:]
			switch keyCount {
			case 1:
				if replacement, ok := state.replaceBeforeCAS[keys[0]]; ok {
					state.values[keys[0]] = replacement
					delete(state.replaceBeforeCAS, keys[0])
				}
				if value, ok := state.values[keys[0]]; ok && len(args) > 0 && value == args[0] {
					delete(state.values, keys[0])
					_, _ = fmt.Fprint(conn, ":1\r\n")
					break
				}
				_, _ = fmt.Fprint(conn, ":0\r\n")
			case 2:
				if _, retired := state.values[keys[0]]; retired {
					_, _ = fmt.Fprint(conn, ":0\r\n")
					break
				}
				state.values[keys[1]] = args[0]
				state.ttlMillis[keys[1]], _ = strconv.ParseInt(args[1], 10, 64)
				_, _ = fmt.Fprint(conn, ":1\r\n")
			case 3:
				marker := "retired"
				if len(args) > 1 {
					marker = args[1]
				}
				state.values[keys[0]] = marker
				state.ttlMillis[keys[0]], _ = strconv.ParseInt(args[0], 10, 64)
				deleted := 0
				for _, key := range keys[1:] {
					if _, ok := state.values[key]; ok {
						deleted++
						delete(state.values, key)
					}
				}
				_, _ = fmt.Fprintf(conn, ":%d\r\n", deleted)
			default:
				_, _ = fmt.Fprint(conn, "-ERR unsupported eval\r\n")
			}
		default:
			_, _ = fmt.Fprint(conn, "-ERR unsupported command\r\n")
		}
		state.Unlock()
	}
}

func readRoutingSub2APIFencingRedisCommand(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	argumentCount, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
	if err != nil {
		return nil, err
	}
	arguments := make([]string, 0, argumentCount)
	for range argumentCount {
		lengthLine, readErr := reader.ReadString('\n')
		if readErr != nil {
			return nil, readErr
		}
		length, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lengthLine, "$")))
		if parseErr != nil || length < 0 {
			return nil, fmt.Errorf("invalid RESP argument length")
		}
		argument := make([]byte, length+2)
		if _, readErr = io.ReadFull(reader, argument); readErr != nil {
			return nil, readErr
		}
		arguments = append(arguments, string(argument[:length]))
	}
	return arguments, nil
}

func TestRetiredRoutingSub2APIIdentityCannotRefillRedisOrLocalJWTCache(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)
	state := &routingSub2APIFencingRedis{
		values:    map[string]string{},
		ttlMillis: map[string]int64{},
	}
	common.RedisEnabled = true
	common.RDB = newRoutingSub2APIFencingRedisClient(t, state)

	ciphertext := "persisted-ciphertext"
	binding := model.RoutingChannelBinding{
		ID:             5001,
		ChannelID:      706,
		UpstreamType:   model.RoutingUpstreamTypeSub2API,
		BaseURL:        "https://routing.example.com",
		EncCredentials: &ciphertext,
		KeyVersion:     model.RoutingCredentialKeyVersion,
	}
	authKey := newRoutingSub2APIAuthKey(binding, model.RoutingCredentials{})
	jwtKey := routingSub2APIRedisJWTKey(authKey)
	retiredKey := fmt.Sprintf("routing:sub2api:retired:%d:%s", authKey.ChannelID, authKey.Fingerprint)

	setRoutingSub2APICachedJWT(context.Background(), authKey, "old-jwt", time.Hour)
	invalidateRoutingSub2APIJWT(context.Background(), binding)
	setRoutingSub2APICachedJWT(context.Background(), authKey, "stale-refill-jwt", time.Hour)

	state.Lock()
	_, hasJWT := state.values[jwtKey]
	_, hasTombstone := state.values[retiredKey]
	tombstoneTTL := state.ttlMillis[retiredKey]
	commands := append([][]string(nil), state.commandLog...)
	state.Unlock()
	routingSub2APIJWTCache.Lock()
	_, hasLocalJWT := routingSub2APIJWTCache.values[authKey]
	routingSub2APIJWTCache.Unlock()

	assert.False(t, hasJWT)
	assert.True(t, hasTombstone)
	assert.GreaterOrEqual(t, tombstoneTTL, (routingSub2APIMaxTokenTTL + routingSub2APILockTTL).Milliseconds())
	assert.False(t, hasLocalJWT)
	for _, command := range commands {
		assert.NotContains(t, command, routingSub2APIRedisLockKey(authKey))
		assert.NotContains(t, strings.Join(command, " "), "stale-refill-jwt")
	}
}

func TestRetiredRoutingSub2APIIdentityRejectsLocalCacheWrittenAfterRedisSet(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)
	state := &routingSub2APIFencingRedis{
		values:    map[string]string{},
		ttlMillis: map[string]int64{},
	}
	common.RedisEnabled = true
	common.RDB = newRoutingSub2APIFencingRedisClient(t, state)

	authKey := routingSub2APITestAuthKey(711)
	retiredKey := fmt.Sprintf("routing:sub2api:retired:%d:%s", authKey.ChannelID, authKey.Fingerprint)
	state.values[retiredKey] = "retired"
	encrypted, err := common.EncryptAESGCMString("stale-local-jwt")
	require.NoError(t, err)
	routingSub2APIJWTCache.Lock()
	routingSub2APIJWTCache.values[authKey] = routingSub2APIJWTCacheEntry{
		Ciphertext: encrypted,
		ExpiresAt:  common.GetTimestamp() + 3600,
	}
	routingSub2APIJWTCache.Unlock()

	token, ok := getRoutingSub2APICachedJWT(context.Background(), authKey)

	assert.False(t, ok)
	assert.Empty(t, token)
	routingSub2APIJWTCache.Lock()
	_, remainsLocal := routingSub2APIJWTCache.values[authKey]
	routingSub2APIJWTCache.Unlock()
	assert.False(t, remainsLocal)
}
