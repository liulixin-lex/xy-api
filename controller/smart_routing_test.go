package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildRoutingBindingViewMasksCredentials(t *testing.T) {
	token := "sk-1234567890abcdef"
	binding := model.RoutingChannelBinding{
		ID:             10,
		ChannelID:      20,
		UpstreamType:   model.RoutingUpstreamTypeNewAPI,
		BaseURL:        "https://upstream.example.com",
		UpstreamGroup:  "vip",
		EncCredentials: &token,
		Enabled:        true,
	}

	view := buildRoutingBindingView(binding, model.RoutingCredentials{
		NewAPIAccessToken: token,
		GatewayAPIKey:     "gw-secret-987654",
		Sub2APIPassword:   "password-secret",
	})

	require.Equal(t, binding.ChannelID, view.ChannelID)
	assert.Equal(t, "****cdef", view.CredentialMasks.NewAPIAccessToken)
	assert.Equal(t, "****7654", view.CredentialMasks.GatewayAPIKey)
	assert.Equal(t, "********", view.CredentialMasks.Sub2APIPassword)
	assert.NotContains(t, view.CredentialMasks.NewAPIAccessToken, "1234567890")
}

func TestBuildRoutingCredentialsKeepsEmptyEditFieldsUnset(t *testing.T) {
	request := routingBindingRequest{
		UpstreamType: model.RoutingUpstreamTypeSub2API,
		Credentials: routingCredentialRequest{
			Sub2APIEmail:    "admin@example.com",
			Sub2APIPassword: "",
			Sub2APIToken:    "jwt-token",
		},
	}

	credentials := buildRoutingCredentials(request)

	assert.Equal(t, "admin@example.com", credentials.Sub2APIEmail)
	assert.Empty(t, credentials.Sub2APIPassword)
	assert.Equal(t, "jwt-token", credentials.Sub2APIToken)
}

func TestRoutingBindingFromRequestClearsUpstreamSpecificFields(t *testing.T) {
	userID := 42
	newAPIBinding := routingBindingFromRequest(routingBindingRequest{
		ChannelID:        11,
		UpstreamType:     model.RoutingUpstreamTypeNewAPI,
		BaseURL:          "https://newapi.example.com",
		UpstreamGroup:    "vip",
		ServesClaudeCode: true,
		NewAPIUserID:     &userID,
	})
	assert.False(t, newAPIBinding.ServesClaudeCode)
	require.NotNil(t, newAPIBinding.NewAPIUserID)

	sub2apiBinding := routingBindingFromRequest(routingBindingRequest{
		ChannelID:        12,
		UpstreamType:     model.RoutingUpstreamTypeSub2API,
		BaseURL:          "https://sub2api.example.com",
		UpstreamGroup:    "vip",
		ServesClaudeCode: true,
		NewAPIUserID:     &userID,
	})
	assert.True(t, sub2apiBinding.ServesClaudeCode)
	assert.Nil(t, sub2apiBinding.NewAPIUserID)
}

func TestLoadSmartRoutingBindingGroupsAcceptsInlineCreateBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer upstream-token", r.Header.Get("Authorization"))
		assert.Equal(t, "42", r.Header.Get("New-Api-User"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			_, _ = fmt.Fprint(w, `{"success":true,"data":{"quota":1000000,"used_quota":0}}`)
		case "/api/pricing":
			_, _ = fmt.Fprint(w, `{"success":true,"data":[{"model_name":"gpt-test","enable_groups":["vip"]}],"group_ratio":{"vip":1}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	body, err := common.Marshal(routingBindingRequest{
		ChannelID:     991,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		NewAPIUserID:  common.GetPointer(42),
		Credentials: routingCredentialRequest{
			NewAPIAccessToken: "upstream-token",
		},
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: "new"}}
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/smart-routing/bindings/new/groups", bytes.NewReader(body))

	LoadSmartRoutingBindingGroups(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			ChannelID int      `json:"channel_id"`
			Groups    []string `json:"groups"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, 991, response.Data.ChannelID)
	assert.Contains(t, response.Data.Groups, "vip")
}

func TestResetSmartRoutingBreakerClearsStoredAndHotcacheState(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingBreakerState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	state := model.RoutingBreakerState{
		ChannelID:           41,
		APIKeyIndex:         model.RoutingMetricSingleKeyIndex,
		ModelName:           "gpt-test",
		Group:               "default",
		State:               model.RoutingBreakerStateOpen,
		Reason:              "5xx",
		ConsecutiveFailures: 5,
		EjectionCount:       1,
	}
	require.NoError(t, db.Create(&state).Error)
	routinghotcache.SetBreakerForTest(routinghotcache.Key{
		ChannelID:   state.ChannelID,
		APIKeyIndex: state.APIKeyIndex,
		Model:       state.ModelName,
		Group:       state.Group,
	}, routinghotcache.BreakerSnapshot{State: model.RoutingBreakerStateOpen, Reason: "5xx"})

	router := gin.New()
	router.POST("/breakers/:id/reset", ResetSmartRoutingBreaker)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/breakers/1/reset", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var count int64
	require.NoError(t, db.Model(&model.RoutingBreakerState{}).Where("id = ?", state.ID).Count(&count).Error)
	assert.Zero(t, count)
	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{
		ChannelID:   state.ChannelID,
		APIKeyIndex: state.APIKeyIndex,
		Model:       state.ModelName,
		Group:       state.Group,
	})
	require.True(t, ok)
	assert.Equal(t, model.RoutingBreakerStateHealthy, cached.State)
	assert.Empty(t, cached.Reason)
}
