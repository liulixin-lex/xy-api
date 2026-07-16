package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	projecti18n "github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDistributeDeferredLeavesSelectionUntilValidatedHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/v1/chat/completions", DistributeDeferred(), func(c *gin.Context) {
		_, selected := common.GetContextKey(c, constant.ContextKeyChannelId)
		assert.False(t, selected)
		assert.Equal(t, "gpt-test", c.GetString("original_model"))
		assert.False(t, common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime).IsZero())
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusNoContent, response.Code)
}

func TestSelectedChannelMetadataDoesNotRequireOrAdvanceCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	channel := &model.Channel{
		Id: 88, Type: constant.ChannelTypeSora, Name: "metadata-only", Key: "disabled-a\ndisabled-b",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusAutoDisabled,
				1: common.ChannelStatusManuallyDisabled,
			},
		},
	}
	routinghotcache.ReplaceChannelTrafficConfigurations([]model.RoutingChannelConfiguration{
		{ChannelID: channel.Id, TrafficClass: model.RoutingChannelTrafficClassAll},
	}, time.Now().Unix())

	require.Nil(t, SetupContextForSelectedChannelMetadata(ctx, channel, "sora-test"))
	assert.Equal(t, channel.Id, common.GetContextKeyInt(ctx, constant.ContextKeyChannelId))
	assert.Empty(t, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	assert.False(t, common.GetContextKeyBool(ctx, constant.ContextKeyChannelIsMultiKey))

	credentialErr := CommitSelectedChannelCredential(ctx, channel)
	require.NotNil(t, credentialErr)
	assert.Equal(t, types.ErrorCodeChannelNoAvailableKey, credentialErr.GetErrorCode())
}

func TestAuthorizeTokenRoutingTargetEnforcesFinalModelAndChannel(t *testing.T) {
	require.NoError(t, projecti18n.Init())
	tests := []struct {
		name                   string
		model                  string
		modelLimits            map[string]bool
		specificChannel        string
		finalChannelID         int
		enforceSpecificChannel bool
		expectedStatus         int
	}{
		{
			name: "allowed model", model: "model-a",
			modelLimits: map[string]bool{"model-a": true},
		},
		{
			name: "forbidden model", model: "model-b",
			modelLimits: map[string]bool{"model-a": true}, expectedStatus: http.StatusForbidden,
		},
		{
			name: "false model entry stays forbidden", model: "model-a",
			modelLimits: map[string]bool{"model-a": false}, expectedStatus: http.StatusForbidden,
		},
		{
			name: "specific channel matches", model: "model-a",
			modelLimits: map[string]bool{"model-a": true}, specificChannel: "17",
			finalChannelID: 17, enforceSpecificChannel: true,
		},
		{
			name: "specific channel mismatch", model: "model-a",
			modelLimits: map[string]bool{"model-a": true}, specificChannel: "17",
			finalChannelID: 18, enforceSpecificChannel: true, expectedStatus: http.StatusForbidden,
		},
		{
			name: "read only fetch ignores specific channel", model: "model-a",
			modelLimits: map[string]bool{"model-a": true}, specificChannel: "17",
			finalChannelID: 18, enforceSpecificChannel: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/task/remix", nil)
			common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, true)
			common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, test.modelLimits)
			if test.specificChannel != "" {
				common.SetContextKey(ctx, constant.ContextKeyTokenSpecificChannelId, test.specificChannel)
			}

			authorizationErr := AuthorizeTokenRoutingTarget(
				ctx, test.model, test.finalChannelID, test.enforceSpecificChannel,
			)
			if test.expectedStatus == 0 {
				require.Nil(t, authorizationErr)
				return
			}
			require.NotNil(t, authorizationErr)
			assert.Equal(t, test.expectedStatus, authorizationErr.StatusCode)
			assert.Equal(t, types.ErrorCodeAccessDenied, authorizationErr.GetErrorCode())
		})
	}
}
