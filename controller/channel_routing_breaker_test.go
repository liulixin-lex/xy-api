package controller

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestResetChannelRoutingBreakerCreatesIdempotentOperationAndReplaysTerminalResult(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.Exec(
		"CREATE UNIQUE INDEX idx_routing_operation_request_key_unique ON routing_operations (request_key_hash)",
	).Error)
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 7, ActivationID: 9, PolicyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Pools: []channelrouting.PoolSnapshot{{
			ID: 11, GroupName: "default", Members: []channelrouting.PoolMemberSnapshot{{
				ID: 22, PoolID: 11, ChannelID: 33,
				Models: []channelrouting.ModelSnapshot{{ModelName: "gpt-reset"}},
			}},
		}},
	})
	seedChannelRoutingBreakerPolicy(t, db, 11, 22, 33, "default", 7, 9)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })

	body := `{"scope":"member","pool_id":11,"member_id":22,"model_name":"gpt-reset","reason":"operator recovery"}`
	first := performChannelRoutingBreakerResetRequest(t, body, "breaker-reset-key")
	assert.Equal(t, http.StatusAccepted, first.Code)
	assert.Contains(t, first.Body.String(), `"status":"pending"`)

	second := performChannelRoutingBreakerResetRequest(t, body, "breaker-reset-key")
	assert.Equal(t, http.StatusAccepted, second.Code)
	var operationCount int64
	var commandCount int64
	require.NoError(t, db.Model(&model.RoutingOperation{}).Count(&operationCount).Error)
	require.NoError(t, db.Model(&model.RoutingBreakerResetCommand{}).Count(&commandCount).Error)
	assert.Equal(t, int64(1), operationCount)
	assert.Equal(t, int64(1), commandCount)

	conflict := performChannelRoutingBreakerResetRequest(
		t, `{"scope":"member","pool_id":11,"member_id":22,"model_name":"gpt-reset","reason":"different reason"}`,
		"breaker-reset-key",
	)
	assert.Equal(t, http.StatusConflict, conflict.Code)
	assert.Contains(t, conflict.Body.String(), `"code":"idempotency_key_conflict"`)

	require.NoError(t, channelrouting.RunBreakerResetControlCycleContext(context.Background()))
	var operation model.RoutingOperation
	require.NoError(t, db.First(&operation).Error)
	assert.Equal(t, model.RoutingOperationStatusSucceeded, operation.Status)

	channelrouting.ResetSnapshotForTest()
	replay := performChannelRoutingBreakerResetRequest(t, body, "breaker-reset-key")
	assert.Equal(t, http.StatusOK, replay.Code)
	assert.Contains(t, replay.Body.String(), `"status":"succeeded"`)
	assert.Contains(t, replay.Body.String(), `"generation":1`)
	assert.Contains(t, replay.Body.String(), `"model_name":"gpt-reset"`)
	assert.Contains(t, replay.Body.String(), `"group_name":"default"`)
}

func seedChannelRoutingBreakerPolicy(
	t *testing.T,
	db *gorm.DB,
	poolID int,
	memberID int,
	channelID int,
	groupName string,
	revision int64,
	activationID int64,
) {
	t.Helper()
	require.NoError(t, db.Create(&model.RoutingPolicyHead{
		ID: 1, CurrentRevision: revision, CurrentActivationID: activationID,
		CurrentHash:  "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		CurrentStage: model.RoutingDeploymentStageActive, CreatedTime: 1, UpdatedTime: 1,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingPolicyPoolRevision{
		Revision: revision, PoolID: poolID,
		GroupKey:  "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		GroupName: groupName, DisplayName: groupName, DeploymentStage: model.RoutingDeploymentStageActive,
		PolicyProfile: "balanced", PolicyJSON: `{}`,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingPolicyMemberRevision{
		Revision: revision, PoolID: poolID, MemberID: memberID, ChannelID: channelID,
		Enabled: true, Weight: 1, CredentialIDsJSON: `[]`, OverridesJSON: `{}`,
	}).Error)
}

func TestResetChannelRoutingBreakerRejectsMissingIdempotencyKey(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 1, ActivationID: 1,
		Pools: []channelrouting.PoolSnapshot{{
			ID: 1, GroupName: "default", Members: []channelrouting.PoolMemberSnapshot{{
				ID: 2, PoolID: 1, ChannelID: 3, Models: []channelrouting.ModelSnapshot{{ModelName: "gpt-reset"}},
			}},
		}},
	})
	recorder := performChannelRoutingBreakerResetRequest(
		t, `{"scope":"member","pool_id":1,"member_id":2,"model_name":"gpt-reset"}`, "",
	)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"invalid_idempotency_key"`)
}

func TestResetChannelRoutingEndpointBreakerRejectsUnknownAuthorityAndRegionWithoutPersistence(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	t.Setenv("ROUTING_REGION", "test-region")
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 7, ActivationID: 9,
		Channels: []channelrouting.ChannelSnapshot{{ID: 33, Endpoint: "https://api.reset.test/v1"}},
	})
	require.NoError(t, db.Create(&model.RoutingPolicyHead{
		ID: 1, CurrentRevision: 7, CurrentActivationID: 9,
		CurrentHash: strings.Repeat("f", 64), CurrentStage: model.RoutingDeploymentStageActive,
		CreatedTime: 1, UpdatedTime: 1,
	}).Error)

	tests := []struct {
		name string
		body string
	}{
		{
			name: "unknown authority",
			body: `{"scope":"endpoint","endpoint_authority":"https://unknown.reset.test","region":"test-region"}`,
		},
		{
			name: "unknown region",
			body: `{"scope":"endpoint","endpoint_authority":"https://api.reset.test","region":"other-region"}`,
		},
	}
	for index, test := range tests {
		recorder := performChannelRoutingBreakerResetRequest(t, test.body, "endpoint-reset-key-"+string(rune('a'+index)))
		assert.Equal(t, http.StatusNotFound, recorder.Code, test.name)
		assert.Contains(t, recorder.Body.String(), `"code":"breaker_target_not_found"`, test.name)
	}
	require.NoError(t, channelrouting.RunBreakerResetControlCycleContext(context.Background()))

	for _, value := range []any{
		&model.RoutingOperation{}, &model.RoutingBreakerResetCommand{}, &model.RoutingBreakerResetFence{},
		&model.RoutingBreakerResetTombstone{}, &model.RoutingBreakerResetOutbox{},
	} {
		var count int64
		require.NoError(t, db.Model(value).Count(&count).Error)
		assert.Zero(t, count)
	}
}

func performChannelRoutingBreakerResetRequest(t *testing.T, body string, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/channel-routing/breakers/reset", bytes.NewBufferString(body))
	if idempotencyKey != "" {
		c.Request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	c.Set(string(constant.ContextKeyUserId), 7)
	ResetChannelRoutingBreaker(c)
	return recorder
}
