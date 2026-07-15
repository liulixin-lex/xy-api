package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateRoutingBindingClearsCredentialsWhenProviderChanges(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelHealthState{},
	))
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-provider-switch-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := model.RoutingChannelBinding{
		ChannelID: 57, UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL: "https://sub2api.example.com", UpstreamGroup: "default", Enabled: true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{
		GatewayAPIKey: "old-gateway-key", Sub2APIEmail: "old@example.com",
		Sub2APIPassword: "old-password", Sub2APIToken: "old-token",
	}))
	require.NoError(t, db.Create(&binding).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "channelId", Value: "57"}}
	context.Request = httptest.NewRequest(http.MethodPut, "/api/smart-routing/bindings/57", bytes.NewBufferString(`{
		"upstream_type":"newapi",
		"base_url":"https://newapi.example.com",
		"upstream_group":"default",
		"enabled":false
	}`))
	setSmartRoutingBindingIfMatchForTest(t, context, binding.ChannelID)

	UpdateSmartRoutingBinding(context)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var stored model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", binding.ChannelID).First(&stored).Error)
	assert.Equal(t, model.RoutingUpstreamTypeNewAPI, stored.UpstreamType)
	assert.False(t, stored.Enabled)
	assert.Nil(t, stored.EncCredentials)
	assert.Zero(t, stored.KeyVersion)
	credentials, err := stored.GetCredentials()
	require.NoError(t, err)
	assert.True(t, credentials.Empty())
}
