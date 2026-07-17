package channelrouting

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingChannelConfigurationEventConvergesRemoteNodeAndRejectsOldEpoch(t *testing.T) {
	db := openRoutingChannelConfigurationServiceTestDB(t)
	require.NoError(t, db.Create(&model.Channel{
		Id: 701, Name: "remote-configuration", Models: "gpt-test", Group: "default",
	}).Error)
	initial, err := model.GetRoutingChannelConfigurationContext(context.Background(), 701)
	require.NoError(t, err)
	first, err := model.UpdateRoutingChannelConfigurationContext(
		context.Background(), initial, 1, model.RoutingChannelTrafficClassClaudeCodeOnly, "", false, 10,
	)
	require.NoError(t, err)

	ResetRoutingEventsForTest()
	ResetRoutingEventTransportForTest()
	routinghotcache.ResetForTest()
	routinghotcache.ReplaceChannelTrafficConfigurations(nil, 1)
	t.Cleanup(func() {
		ResetRoutingEventsForTest()
		ResetRoutingEventTransportForTest()
		routinghotcache.ResetForTest()
	})
	client := &routingEventRedisMemory{}
	remoteState := newRoutingEventTransportState()
	remoteHub := newRoutingEventHub(8)
	require.NoError(t, initializeRoutingEventTransportContext(context.Background(), remoteState, client))

	broadcast := func(sequence uint64, outbox model.RoutingChannelConfigurationOutbox) {
		t.Helper()
		require.NoError(t, broadcastRoutingEventContext(
			context.Background(), newRoutingEventTransportState(), client, strings.Repeat("a", 32), RoutingEvent{
				ID: sequence, Type: RoutingEventTypeChannelConfigurationChanged,
				Revision: uint64(outbox.ConfigEpoch), CreatedTimeMs: time.Now().UnixMilli(),
				PayloadJSON: []byte(outbox.PayloadJSON),
			},
		))
	}
	consume := func() {
		t.Helper()
		processed, consumeErr := consumeRoutingEventsOnceContext(
			context.Background(), remoteHub, remoteState, client, strings.Repeat("b", 32),
		)
		require.NoError(t, consumeErr)
		assert.Equal(t, 1, processed)
	}

	broadcast(1, first.Outbox)
	consume()
	policy, initialized := routinghotcache.GetChannelTrafficPolicy(701)
	require.True(t, initialized)
	assert.True(t, policy.ClaudeCodeOnly)
	assert.Equal(t, uint64(1), routingChannelConfigurationEventEpoch.Load())

	second, err := model.UpdateRoutingChannelConfigurationContext(
		context.Background(), first.Configuration, 1, model.RoutingChannelTrafficClassAll, "", false, 10,
	)
	require.NoError(t, err)
	broadcast(2, second.Outbox)
	consume()
	policy, initialized = routinghotcache.GetChannelTrafficPolicy(701)
	require.True(t, initialized)
	assert.False(t, policy.ClaudeCodeOnly)
	assert.Equal(t, uint64(2), routingChannelConfigurationEventEpoch.Load())

	broadcast(3, first.Outbox)
	consume()
	policy, initialized = routinghotcache.GetChannelTrafficPolicy(701)
	require.True(t, initialized)
	assert.False(t, policy.ClaudeCodeOnly)
	assert.Equal(t, uint64(2), routingChannelConfigurationEventEpoch.Load())
	assert.Equal(t, 2, remoteHub.stats().Buffered, "the stale event must not be fanned out to SSE clients")
}

func TestRoutingChannelConfigurationEventsConvergeDifferentChannelsWhenGlobalEpochArrivesOutOfOrder(t *testing.T) {
	db := openRoutingChannelConfigurationServiceTestDB(t)
	require.NoError(t, db.Create(&model.Channel{
		Id: 711, Name: "out-of-order-a", Models: "gpt-test", Group: "default",
	}).Error)
	require.NoError(t, db.Create(&model.Channel{
		Id: 712, Name: "out-of-order-b", Models: "gpt-test", Group: "default",
	}).Error)
	firstInitial, err := model.GetRoutingChannelConfigurationContext(context.Background(), 711)
	require.NoError(t, err)
	first, err := model.UpdateRoutingChannelConfigurationContext(
		context.Background(), firstInitial, 1, model.RoutingChannelTrafficClassClaudeCodeOnly, "", false, 10,
	)
	require.NoError(t, err)
	secondInitial, err := model.GetRoutingChannelConfigurationContext(context.Background(), 712)
	require.NoError(t, err)
	second, err := model.UpdateRoutingChannelConfigurationContext(
		context.Background(), secondInitial, 1, model.RoutingChannelTrafficClassClaudeCodeOnly, "", false, 10,
	)
	require.NoError(t, err)

	ResetRoutingEventsForTest()
	ResetRoutingEventTransportForTest()
	routinghotcache.ResetForTest()
	routinghotcache.ReplaceChannelTrafficConfigurations(nil, 1)
	t.Cleanup(func() {
		ResetRoutingEventsForTest()
		ResetRoutingEventTransportForTest()
		routinghotcache.ResetForTest()
	})
	client := &routingEventRedisMemory{}
	remoteState := newRoutingEventTransportState()
	remoteHub := newRoutingEventHub(8)
	require.NoError(t, initializeRoutingEventTransportContext(context.Background(), remoteState, client))

	broadcast := func(sequence uint64, outbox model.RoutingChannelConfigurationOutbox) {
		t.Helper()
		require.NoError(t, broadcastRoutingEventContext(
			context.Background(), newRoutingEventTransportState(), client, strings.Repeat("a", 32), RoutingEvent{
				ID: sequence, Type: RoutingEventTypeChannelConfigurationChanged,
				Revision: uint64(outbox.ConfigEpoch), CreatedTimeMs: time.Now().UnixMilli(),
				PayloadJSON: []byte(outbox.PayloadJSON),
			},
		))
	}
	consume := func() {
		t.Helper()
		processed, consumeErr := consumeRoutingEventsOnceContext(
			context.Background(), remoteHub, remoteState, client, strings.Repeat("b", 32),
		)
		require.NoError(t, consumeErr)
		assert.Equal(t, 1, processed)
	}

	broadcast(2, second.Outbox)
	consume()
	assert.Equal(t, uint64(second.Outbox.ConfigEpoch), routingChannelConfigurationEventEpoch.Load())
	secondPolicy, initialized := routinghotcache.GetChannelTrafficPolicy(712)
	require.True(t, initialized)
	assert.True(t, secondPolicy.ClaudeCodeOnly)
	firstPolicy, initialized := routinghotcache.GetChannelTrafficPolicy(711)
	require.True(t, initialized)
	assert.False(t, firstPolicy.ClaudeCodeOnly)

	broadcast(1, first.Outbox)
	consume()
	firstPolicy, initialized = routinghotcache.GetChannelTrafficPolicy(711)
	require.True(t, initialized)
	assert.True(t, firstPolicy.ClaudeCodeOnly)
	secondPolicy, initialized = routinghotcache.GetChannelTrafficPolicy(712)
	require.True(t, initialized)
	assert.True(t, secondPolicy.ClaudeCodeOnly)
	assert.Equal(t, uint64(second.Outbox.ConfigEpoch), routingChannelConfigurationEventEpoch.Load())
	assert.Equal(t, 1, remoteHub.stats().Buffered, "the older global epoch must converge its channel without stale SSE fanout")
}

func TestRoutingChannelConfigurationEventTreatsAuthoritativelyDeletedChannelAsConsumed(t *testing.T) {
	db := openRoutingChannelConfigurationServiceTestDB(t)
	channel := &model.Channel{Id: 713, Name: "deleted-configuration", Models: "gpt-test", Group: "default"}
	require.NoError(t, db.Create(channel).Error)
	initial, err := model.GetRoutingChannelConfigurationContext(context.Background(), channel.Id)
	require.NoError(t, err)
	mutation, err := model.UpdateRoutingChannelConfigurationContext(
		context.Background(), initial, 1, model.RoutingChannelTrafficClassClaudeCodeOnly, "", false, 10,
	)
	require.NoError(t, err)

	ResetRoutingEventsForTest()
	ResetRoutingEventTransportForTest()
	routinghotcache.ResetForTest()
	routinghotcache.ReplaceChannelTrafficConfigurations([]model.RoutingChannelConfiguration{mutation.Configuration}, 1)
	t.Cleanup(func() {
		ResetRoutingEventsForTest()
		ResetRoutingEventTransportForTest()
		routinghotcache.ResetForTest()
	})
	client := &routingEventRedisMemory{}
	remoteState := newRoutingEventTransportState()
	remoteHub := newRoutingEventHub(8)
	require.NoError(t, initializeRoutingEventTransportContext(context.Background(), remoteState, client))
	require.NoError(t, broadcastRoutingEventContext(
		context.Background(), newRoutingEventTransportState(), client, strings.Repeat("a", 32), RoutingEvent{
			ID: 1, Type: RoutingEventTypeChannelConfigurationChanged,
			Revision: uint64(mutation.Outbox.ConfigEpoch), CreatedTimeMs: time.Now().UnixMilli(),
			PayloadJSON: []byte(mutation.Outbox.PayloadJSON),
		},
	))
	require.NoError(t, channel.Delete())
	normalEvent, err := remoteHub.publish(
		RoutingEventTypeProbeCompleted, 0, []byte(`{"channel_id":999}`), time.Now(),
	)
	require.NoError(t, err)
	require.NoError(t, broadcastRoutingEventContext(
		context.Background(), newRoutingEventTransportState(), client, strings.Repeat("a", 32), normalEvent,
	))

	processed, err := consumeRoutingEventsOnceContext(
		context.Background(), remoteHub, remoteState, client, strings.Repeat("b", 32),
	)
	require.NoError(t, err)
	assert.Equal(t, 2, processed)
	assert.Equal(t, "2-0", remoteState.snapshot().Cursor)
	policy, initialized := routinghotcache.GetChannelTrafficPolicy(channel.Id)
	require.True(t, initialized)
	assert.False(t, policy.ClaudeCodeOnly)

	processed, err = consumeRoutingEventsOnceContext(
		context.Background(), remoteHub, remoteState, client, strings.Repeat("b", 32),
	)
	require.NoError(t, err)
	assert.Zero(t, processed)
}

func TestRoutingChannelConfigurationOutboxRefreshesCostAndCannotRollBackWhenNewerEpochPublishesFirst(t *testing.T) {
	db := openRoutingChannelConfigurationServiceTestDB(t)
	withSnapshotSecret(t)
	restoreSystemRoutingRatioSettings(t)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"routing-config-refresh":0.25}`))
	require.NoError(t, db.Create(&model.Channel{
		Id: 702, Name: "outbox-order", Models: "routing-config-refresh", Group: "default",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	initialView, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	initialObservation, _, ok := ResolveObserveModelSnapshot("default", 702, "routing-config-refresh")
	require.True(t, ok)
	profile := model.RoutingCostRequestProfile{
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheTokensKnown: true,
		ImageInputTokensKnown: true, ImageOutputTokensKnown: true, ImageUnitsKnown: true,
		AudioInputTokensKnown: true, AudioOutputTokensKnown: true,
		RequestInputKnown: true, RequestPricingFeaturesKnown: true,
	}
	initialEstimate, exists, err := EstimateModelSnapshotRoutingCost(initialObservation, profile, time.Now().Unix())
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, initialEstimate.ExpectedKnown)
	assert.Equal(t, int64(1), initialObservation.ChannelConfigurationRevision)
	assert.Equal(t, 1.0, initialObservation.CostUpstreamMultiplier)
	assert.InDelta(t, 0.25, initialEstimate.ExpectedCost, 1e-12)

	initial, err := model.GetRoutingChannelConfigurationContext(context.Background(), 702)
	require.NoError(t, err)
	first, err := model.UpdateRoutingChannelConfigurationContext(
		context.Background(), initial, 0.5, model.RoutingChannelTrafficClassClaudeCodeOnly, "", false, 10,
	)
	require.NoError(t, err)
	second, err := model.UpdateRoutingChannelConfigurationContext(
		context.Background(), first.Configuration, 2, model.RoutingChannelTrafficClassAll, "", false, 10,
	)
	require.NoError(t, err)

	ResetRoutingEventsForTest()
	ResetRoutingEventTransportForTest()
	routinghotcache.ResetForTest()
	routinghotcache.ReplaceChannelTrafficConfigurations(nil, 1)
	client := &routingEventRedisMemory{}
	loadRoutingEventRedis = func() routingEventRedis { return client }
	t.Cleanup(func() {
		ResetRoutingEventsForTest()
		ResetRoutingEventTransportForTest()
		routinghotcache.ResetForTest()
	})

	published, err := PublishRoutingChannelConfigurationOutboxByIDContext(context.Background(), second.Outbox.ID)
	require.NoError(t, err)
	require.True(t, published)
	policy, initialized := routinghotcache.GetChannelTrafficPolicy(702)
	require.True(t, initialized)
	assert.False(t, policy.ClaudeCodeOnly)
	assert.Equal(t, uint64(2), routingChannelConfigurationEventEpoch.Load())
	refreshedObservation, _, ok := ResolveObserveModelSnapshot("default", 702, "routing-config-refresh")
	require.True(t, ok)
	assert.LessOrEqual(t, refreshedObservation.CostEffectiveTime, time.Now().Unix())
	require.NotNil(t, refreshedObservation.CostPricing)
	require.NotNil(t, refreshedObservation.CostPricing.PerRequestCost, "%+v", *refreshedObservation.CostPricing)
	refreshedEstimate, exists, err := EstimateModelSnapshotRoutingCost(refreshedObservation, profile, time.Now().Unix())
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, refreshedEstimate.ExpectedKnown, "%+v / %+v", refreshedObservation, refreshedEstimate)
	assert.Equal(t, second.Configuration.Revision, refreshedObservation.ChannelConfigurationRevision)
	assert.Equal(t, 2.0, refreshedObservation.CostUpstreamMultiplier)
	assert.InDelta(t, 0.5, refreshedEstimate.ExpectedCost, 1e-12)
	assert.NotEqual(t, initialObservation.CostPricingIdentity, refreshedObservation.CostPricingIdentity)
	assert.Contains(t, refreshedObservation.CostPricingIdentity, ":channel-config:3")
	refreshedView, ok := CurrentSnapshot()
	require.True(t, ok)
	assert.Equal(t, uint64(second.Outbox.ConfigEpoch), refreshedView.ConfigurationEpoch)
	assert.Greater(t, refreshedView.RuntimeGeneration, initialView.RuntimeGeneration)

	published, err = PublishRoutingChannelConfigurationOutboxByIDContext(context.Background(), first.Outbox.ID)
	require.NoError(t, err)
	require.True(t, published)
	policy, initialized = routinghotcache.GetChannelTrafficPolicy(702)
	require.True(t, initialized)
	assert.False(t, policy.ClaudeCodeOnly)
	assert.Equal(t, uint64(2), routingChannelConfigurationEventEpoch.Load())
	stableObservation, _, ok := ResolveObserveModelSnapshot("default", 702, "routing-config-refresh")
	require.True(t, ok)
	stableEstimate, exists, err := EstimateModelSnapshotRoutingCost(stableObservation, profile, time.Now().Unix())
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, refreshedObservation.ChannelConfigurationRevision, stableObservation.ChannelConfigurationRevision)
	assert.Equal(t, refreshedObservation.CostUpstreamMultiplier, stableObservation.CostUpstreamMultiplier)
	assert.Equal(t, refreshedObservation.CostPricingHash, stableObservation.CostPricingHash)
	assert.Equal(t, refreshedObservation.CostPricingIdentity, stableObservation.CostPricingIdentity)
	assert.InDelta(t, refreshedEstimate.ExpectedCost, stableEstimate.ExpectedCost, 1e-12)
	stableView, ok := CurrentSnapshot()
	require.True(t, ok)
	assert.Equal(t, refreshedView.ConfigurationEpoch, stableView.ConfigurationEpoch)
	assert.Equal(t, refreshedView.ConfigurationHash, stableView.ConfigurationHash)
	assert.Greater(t, stableView.RuntimeGeneration, refreshedView.RuntimeGeneration)

	var stored []model.RoutingChannelConfigurationOutbox
	require.NoError(t, db.Order("config_epoch asc").Find(&stored).Error)
	require.Len(t, stored, 2)
	assert.Positive(t, stored[0].PublishedTime)
	assert.Positive(t, stored[1].PublishedTime)
	events := RecentRoutingEvents(10, RoutingEventTypeChannelConfigurationChanged)
	require.Len(t, events, 1)
	assert.Equal(t, uint64(2), events[0].Revision)
	client.mu.Lock()
	assert.Len(t, client.messages, 2, "a stale outbox for another channel must still reach peer nodes")
	client.mu.Unlock()
}

func openRoutingChannelConfigurationServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	ResetSnapshotForTest()
	model.ResetRoutingTopologyChangesForTest()
	t.Cleanup(func() {
		ResetSnapshotForTest()
		model.ResetRoutingTopologyChangesForTest()
	})
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.Ability{}, &model.RoutingChannelConfigurationOutbox{}, &model.RoutingControlAudit{},
	))
	return db
}
