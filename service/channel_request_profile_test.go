package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingRequestProfileBuildsImmutableAttemptProfile(t *testing.T) {
	enableRoutingRequestProfileV2ForTest(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	template := channelrouting.RequestProfileV2Input{
		RequestPath:              "/v1/responses",
		ModelName:                "gpt-test",
		RequestKind:              channelrouting.RequestKindResponses,
		SourceFormat:             channelrouting.RequestSourceFormatOpenAIResponses,
		InputModalities:          channelrouting.RequestModalityText,
		OutputModalities:         channelrouting.RequestModalityText,
		RequiredCapabilities:     channelrouting.RequestCapabilityTools,
		IsStream:                 true,
		InputTokens:              channelrouting.UnknownRequestQuantity(),
		OutputTokens:             channelrouting.UnknownRequestQuantity(),
		CachedTokens:             channelrouting.UnknownRequestQuantity(),
		ImageUnits:               channelrouting.NotApplicableRequestQuantity(),
		AudioMillis:              channelrouting.NotApplicableRequestQuantity(),
		VideoMillis:              channelrouting.NotApplicableRequestQuantity(),
		RetrySafety:              channelrouting.RequestRetrySafetySafe,
		RetryAllowed:             true,
		CrossChannelRetryAllowed: true,
		HedgeAllowed:             true,
		TenantTier:               channelrouting.RequestTenantTierStandard,
	}
	common.SetContextKey(ctx, constant.ContextKeyRoutingRequestProfile, template)
	common.SetContextKey(ctx, constant.ContextKeyRoutingPromptKnown, true)
	common.SetContextKey(ctx, constant.ContextKeyRoutingOutputKnown, true)

	profile, err := routingRequestProfile(ctx, "vip", 2, 120, 40)
	require.NoError(t, err)
	require.NotNil(t, profile)
	assert.Equal(t, "vip", profile.GroupName)
	assert.Equal(t, 2, profile.RetryIndex)
	assert.Equal(t, 120, profile.PromptTokenEstimate)
	assert.Equal(t, 40, profile.CompletionTokenEstimate)
	assert.Equal(t, channelrouting.KnownRequestQuantity(120), *profile.InputTokens)
	assert.Equal(t, channelrouting.KnownRequestQuantity(40), *profile.OutputTokens)
	assert.Equal(t, channelrouting.UnknownRequestQuantity(), template.InputTokens)
	assert.Empty(t, template.GroupName)
}

func TestRoutingRequestProfilePreservesNotApplicableQuantities(t *testing.T) {
	enableRoutingRequestProfileV2ForTest(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/embeddings", nil)
	common.SetContextKey(ctx, constant.ContextKeyRoutingRequestProfile, channelrouting.RequestProfileV2Input{
		RequestPath:              "/v1/embeddings",
		ModelName:                "embedding-test",
		RequestKind:              channelrouting.RequestKindEmbedding,
		SourceFormat:             channelrouting.RequestSourceFormatEmbedding,
		InputModalities:          channelrouting.RequestModalityText,
		OutputModalities:         channelrouting.RequestModalityText,
		InputTokens:              channelrouting.UnknownRequestQuantity(),
		OutputTokens:             channelrouting.NotApplicableRequestQuantity(),
		CachedTokens:             channelrouting.NotApplicableRequestQuantity(),
		ImageUnits:               channelrouting.NotApplicableRequestQuantity(),
		AudioMillis:              channelrouting.NotApplicableRequestQuantity(),
		VideoMillis:              channelrouting.NotApplicableRequestQuantity(),
		RetrySafety:              channelrouting.RequestRetrySafetySafe,
		RetryAllowed:             true,
		CrossChannelRetryAllowed: true,
		TenantTier:               channelrouting.RequestTenantTierStandard,
	})
	common.SetContextKey(ctx, constant.ContextKeyRoutingPromptKnown, true)

	profile, err := routingRequestProfile(ctx, "default", 0, 20, 999)
	require.NoError(t, err)
	require.NotNil(t, profile)
	assert.Equal(t, 20, profile.PromptTokenEstimate)
	assert.Zero(t, profile.CompletionTokenEstimate)
	assert.Equal(t, channelrouting.NotApplicableRequestQuantity(), *profile.OutputTokens)
}

func TestRoutingRequestProfileWithoutTemplateKeepsV1Compatibility(t *testing.T) {
	enableRoutingRequestProfileV2ForTest(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	profile, err := routingRequestProfile(ctx, "default", 0, 10, 5)
	require.NoError(t, err)
	assert.Nil(t, profile)
}

func TestRoutingRequestProfileV2DefaultsOff(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyRoutingRequestProfile, channelrouting.RequestProfileV2Input{})
	profile, err := routingRequestProfile(ctx, "default", 0, 10, 5)
	require.NoError(t, err)
	assert.Nil(t, profile)
}

func TestRoutingRequestProfileCarriesTrafficClassWhenV2ScoringIsDisabled(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	setting.RequestProfileV2Enabled = false
	smart_routing_setting.UpdateSetting(setting)

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	common.SetContextKey(ctx, constant.ContextKeyRoutingRequestProfile, channelrouting.RequestProfileV2Input{
		RequestPath:  "/v1/messages",
		ModelName:    "claude-test",
		IsStream:     true,
		TrafficClass: channelrouting.RequestTrafficClassClaudeCode,
	})

	profile, err := routingRequestProfile(ctx, "default", 1, 100, 20)
	require.NoError(t, err)
	require.NotNil(t, profile)
	assert.Equal(t, channelrouting.RequestProfileSchemaV1, profile.SchemaVersion)
	assert.Equal(t, channelrouting.RequestTrafficClassClaudeCode, profile.TrafficClass)
	assert.Equal(t, 100, profile.PromptTokenEstimate)
	assert.Equal(t, 20, profile.CompletionTokenEstimate)
}

func TestRoutingAttemptPolicyRemainsActiveWhenProfileScoringIsDisabled(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyRoutingRequestProfile, channelrouting.RequestProfileV2Input{
		RequestKind:              channelrouting.RequestKindImage,
		RetrySafety:              channelrouting.RequestRetrySafetyUnsafe,
		RetryAllowed:             false,
		CrossChannelRetryAllowed: false,
		HedgeAllowed:             false,
	})

	policy, ok := ChannelRoutingRequestAttemptPolicy(ctx)
	require.True(t, ok)
	assert.False(t, policy.RetryAllowed)
	assert.False(t, policy.CrossChannelRetryAllowed)
	assert.False(t, policy.HedgeAllowed)
}

func enableRoutingRequestProfileV2ForTest(t *testing.T) {
	t.Helper()
	smart_routing_setting.ResetForTest()
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	setting.RequestProfileV2Enabled = true
	smart_routing_setting.UpdateSetting(setting)
	t.Cleanup(smart_routing_setting.ResetForTest)
}
