package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAsyncBillingV2FleetReadyFailsClosedAndRestartsStabilityWindow(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.SystemInstance{}))
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
		resetAsyncBillingFleetGateForTest(false)
	})
	t.Setenv("ASYNC_BILLING_V2_ENABLED", "true")
	t.Setenv("ASYNC_BILLING_V2_ROLLOUT_EPOCH", "fleet-test-epoch")
	t.Setenv("ASYNC_BILLING_V2_FLEET_STABLE_SECONDS", "0")
	previousBatchUpdateEnabled := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = false
	t.Cleanup(func() {
		common.BatchUpdateEnabled = previousBatchUpdateEnabled
	})

	insertInstance := func(name string, protocol int, epoch string) {
		t.Helper()
		payload, err := common.Marshal(SystemInstanceInfo{
			SchemaVersion: 1,
			Capabilities: SystemInstanceCapabilities{
				AsyncBillingProtocol: protocol, AsyncBillingRolloutEpoch: epoch,
				AsyncBillingIncarnationID: name + "-incarnation",
			},
		})
		require.NoError(t, err)
		now := time.Now().Unix()
		require.NoError(t, model.DB.Create(&model.SystemInstance{
			NodeName: name, Info: string(payload), StartedAt: now, LastSeenAt: now,
		}).Error)
	}

	resetAsyncBillingFleetGateForTest(true)
	assert.False(t, AsyncBillingV2FleetReady(context.Background()), "an empty fleet must fail closed")
	assertFleetCandidateCleared(t)

	insertInstance("old-node", 0, "")
	expireAsyncBillingFleetCacheForTest()
	assert.False(t, AsyncBillingV2FleetReady(context.Background()), "legacy payloads must fail closed")
	assertFleetCandidateCleared(t)

	insertInstance("v2-node", asyncBillingProtocolVersion, "fleet-test-epoch")
	expireAsyncBillingFleetCacheForTest()
	assert.False(t, AsyncBillingV2FleetReady(context.Background()), "mixed fleets must fail closed")
	assertFleetCandidateCleared(t)

	require.NoError(t, model.DB.Delete(&model.SystemInstance{}, "node_name = ?", "old-node").Error)
	expireAsyncBillingFleetCacheForTest()
	assert.False(t, AsyncBillingV2FleetReady(context.Background()), "the all-v2 candidate needs a fresh observation")
	expireAsyncBillingFleetCacheForTest()
	assert.True(t, AsyncBillingV2FleetReady(context.Background()), "a stable all-v2 fleet may enable")

	common.BatchUpdateEnabled = true
	assert.False(t, AsyncBillingV2FleetReady(context.Background()), "batch updates must disable the durable send gate immediately")
	assertFleetCandidateCleared(t)
	common.BatchUpdateEnabled = false
	assert.False(t, AsyncBillingV2FleetReady(context.Background()), "leaving batch mode must restart the stability window")
	expireAsyncBillingFleetCacheForTest()
	assert.True(t, AsyncBillingV2FleetReady(context.Background()))

	t.Setenv("ASYNC_BILLING_V2_ENABLED", "false")
	assert.False(t, AsyncBillingV2FleetReady(context.Background()))
	assertFleetCandidateCleared(t)
	t.Setenv("ASYNC_BILLING_V2_ENABLED", "true")
	assert.False(t, AsyncBillingV2FleetReady(context.Background()), "re-enable must start a new stability window")
	expireAsyncBillingFleetCacheForTest()
	assert.True(t, AsyncBillingV2FleetReady(context.Background()))

	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	insertInstance("wrong-epoch", asyncBillingProtocolVersion, "old-epoch")
	expireAsyncBillingFleetCacheForTest()
	assert.False(t, AsyncBillingV2FleetReady(context.Background()), "epoch mismatch must fail closed")
	assertFleetCandidateCleared(t)

	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	now := time.Now().Unix()
	validPayload, err := common.Marshal(SystemInstanceInfo{
		SchemaVersion: 1,
		Capabilities: SystemInstanceCapabilities{
			AsyncBillingProtocol: asyncBillingProtocolVersion, AsyncBillingRolloutEpoch: "fleet-test-epoch",
			AsyncBillingIncarnationID: "bulk-incarnation",
		},
	})
	require.NoError(t, err)
	instances := make([]model.SystemInstance, 0, asyncBillingFleetActiveLimit+1)
	for index := 0; index <= asyncBillingFleetActiveLimit; index++ {
		instances = append(instances, model.SystemInstance{
			NodeName: fmt.Sprintf("node-%03d-%s", index, strings.Repeat("x", 8)),
			Info:     string(validPayload), StartedAt: now, LastSeenAt: now,
		})
	}
	require.NoError(t, model.DB.CreateInBatches(&instances, 100).Error)
	expireAsyncBillingFleetCacheForTest()
	assert.False(t, AsyncBillingV2FleetReady(context.Background()), "oversized fleets must fail closed")
	assertFleetCandidateCleared(t)
}

func TestReportCurrentSystemInstancePublishesRoutingSchemaCapability(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.SystemInstance{}))
	const nodeName = "routing-schema-capability-test"
	require.NoError(t, model.DB.Delete(&model.SystemInstance{}, "node_name = ?", nodeName).Error)

	previousNodeName := common.NodeName
	previousNodeNameSource := common.NodeNameSource
	previousNodeNameManuallyConfigured := common.NodeNameManuallyConfigured
	previousVersion := common.Version
	previousStartTime := common.StartTime
	common.NodeName = nodeName
	common.NodeNameSource = common.NodeNameSourceManual
	common.NodeNameManuallyConfigured = true
	common.Version = "v0.1.14-test"
	common.StartTime = time.Now().Unix() - 1
	t.Cleanup(func() {
		common.NodeName = previousNodeName
		common.NodeNameSource = previousNodeNameSource
		common.NodeNameManuallyConfigured = previousNodeNameManuallyConfigured
		common.Version = previousVersion
		common.StartTime = previousStartTime
		require.NoError(t, model.DB.Delete(&model.SystemInstance{}, "node_name = ?", nodeName).Error)
	})

	require.NoError(t, ReportCurrentSystemInstance())
	var instance model.SystemInstance
	require.NoError(t, model.DB.First(&instance, "node_name = ?", nodeName).Error)
	var info SystemInstanceInfo
	require.NoError(t, common.UnmarshalJsonStr(instance.Info, &info))
	assert.Equal(t, "v0.1.14-test", info.Runtime.Version)
	assert.Equal(t, model.RoutingSchemaCurrentVersion, info.Capabilities.ChannelRoutingSchema)
}

func resetAsyncBillingFleetGateForTest(schemaReady bool) {
	asyncBillingFleetGateCache.Lock()
	defer asyncBillingFleetGateCache.Unlock()
	resetAsyncBillingFleetCandidateLocked()
	asyncBillingFleetGateCache.expiresAt = time.Time{}
	asyncBillingFleetGateCache.schemaReady = schemaReady
	if schemaReady {
		asyncBillingFleetGateCache.schemaExpiresAt = time.Now().Add(time.Hour)
	} else {
		asyncBillingFleetGateCache.schemaExpiresAt = time.Time{}
	}
}

func expireAsyncBillingFleetCacheForTest() {
	asyncBillingFleetGateCache.Lock()
	defer asyncBillingFleetGateCache.Unlock()
	asyncBillingFleetGateCache.expiresAt = time.Time{}
}

func assertFleetCandidateCleared(t *testing.T) {
	t.Helper()
	asyncBillingFleetGateCache.Lock()
	defer asyncBillingFleetGateCache.Unlock()
	assert.False(t, asyncBillingFleetGateCache.ready)
	assert.Empty(t, asyncBillingFleetGateCache.candidateHash)
	assert.True(t, asyncBillingFleetGateCache.candidateSince.IsZero())
}
