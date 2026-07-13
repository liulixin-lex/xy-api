package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateTextOtherInfoAuditsStableRoutingIDsWithoutCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "serving-secret")
	common.SetContextKey(ctx, constant.ContextKeyRoutingSnapshotRevision, uint64(23))
	common.SetContextKey(ctx, constant.ContextKeyRoutingPoolID, 3)
	common.SetContextKey(ctx, constant.ContextKeyRoutingMemberID, 5)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCredentialID, 7)
	now := time.Now()
	info := &relaycommon.RelayInfo{
		StartTime: now, FirstResponseTime: now, ChannelMeta: &relaycommon.ChannelMeta{},
	}

	other := GenerateTextOtherInfo(ctx, info, 1, 1, 1, 0, 0, 0, 1)

	adminInfo, ok := other["admin_info"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, uint64(23), adminInfo["routing_snapshot_revision"])
	assert.Equal(t, 3, adminInfo["routing_pool_id"])
	assert.Equal(t, 5, adminInfo["routing_member_id"])
	assert.Equal(t, 7, adminInfo["routing_credential_id"])
	encoded, err := common.Marshal(other)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "serving-secret")

	common.SetContextKey(ctx, constant.ContextKeyRoutingCredentialID, 0)
	keylessOther := GenerateTextOtherInfo(ctx, info, 1, 1, 1, 0, 0, 0, 1)
	keylessAdminInfo, ok := keylessOther["admin_info"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 3, keylessAdminInfo["routing_pool_id"])
	assert.Equal(t, 5, keylessAdminInfo["routing_member_id"])
	_, hasCredentialID := keylessAdminInfo["routing_credential_id"]
	assert.False(t, hasCredentialID)
}
