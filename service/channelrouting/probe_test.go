package channelrouting

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActiveProbeSchedulerRequiresExplicitBalancedEnablement(t *testing.T) {
	now := time.Unix(10_000, 0)
	var targetCalls atomic.Int64
	var executed atomic.Int64
	scheduler := newActiveProbeScheduler(activeProbeDeps{
		now: func() time.Time { return now },
		targets: func(smart_routing_setting.SmartRoutingSetting, time.Time) []ActiveProbeTarget {
			targetCalls.Add(1)
			return []ActiveProbeTarget{activeProbeTargetForTest("a", "one.example")}
		},
		executor: func() ActiveProbeExecutor {
			return func(context.Context, ActiveProbeTarget) ActiveProbeExecution {
				executed.Add(1)
				return ActiveProbeExecution{}
			}
		},
		acquire:   successfulProbeLeaseForTest,
		persist:   func(context.Context, model.RoutingControlLease, model.RoutingProbeResult) error { return nil },
		complete:  func(context.Context, model.RoutingControlLease, int64) error { return nil },
		release:   func(context.Context, model.RoutingControlLease, int64) error { return nil },
		waitUntil: func(context.Context, time.Time) bool { return true },
	})

	settings := activeProbeSettingForTest()
	settings.Mode = smart_routing_setting.ModeObserve
	require.NoError(t, scheduler.RunCycle(context.Background(), settings))
	settings.Mode = smart_routing_setting.ModeBalanced
	settings.ActiveProbeEnabled = false
	require.NoError(t, scheduler.RunCycle(context.Background(), settings))
	settings.ActiveProbeEnabled = true
	settings.Enabled = false
	require.NoError(t, scheduler.RunCycle(context.Background(), settings))
	assert.Zero(t, targetCalls.Load())

	settings.Enabled = true
	require.NoError(t, scheduler.RunCycle(context.Background(), settings))
	assert.Equal(t, int64(1), targetCalls.Load())
	assert.Equal(t, int64(1), executed.Load())
}

func TestActiveProbeSchedulerFencesResultAndEffectsAfterTargetChanges(t *testing.T) {
	now := time.Unix(15_000, 0)
	target := activeProbeTargetForTest("a", "one.example")
	var validations atomic.Int64
	var persisted model.RoutingProbeResult
	scheduler := newActiveProbeScheduler(activeProbeDeps{
		now: func() time.Time { return now },
		targets: func(smart_routing_setting.SmartRoutingSetting, time.Time) []ActiveProbeTarget {
			return []ActiveProbeTarget{target}
		},
		executor: func() ActiveProbeExecutor {
			return func(context.Context, ActiveProbeTarget) ActiveProbeExecution {
				return ActiveProbeExecution{StatusCode: 401, Err: errors.New("retired credential rejected")}
			}
		},
		validate: func(ActiveProbeTarget) error {
			if validations.Add(1) >= 1 {
				return ErrActiveProbeTargetStale
			}
			return nil
		},
		acquire: successfulProbeLeaseForTest,
		persist: func(_ context.Context, _ model.RoutingControlLease, result model.RoutingProbeResult) error {
			persisted = result
			return nil
		},
		complete:  func(context.Context, model.RoutingControlLease, int64) error { return nil },
		release:   func(context.Context, model.RoutingControlLease, int64) error { return nil },
		waitUntil: func(context.Context, time.Time) bool { return true },
	})

	require.NoError(t, scheduler.RunCycle(context.Background(), activeProbeSettingForTest()))
	assert.Equal(t, model.RoutingProbeOutcomeLocalError, persisted.Outcome)
	assert.Equal(t, "active_probe_target_stale", persisted.ClassificationRule)
	assert.Equal(t, int64(1), validations.Load(), "stale results must stop before the effect phase")

	validations.Store(0)
	persisted = model.RoutingProbeResult{}
	scheduler = newActiveProbeScheduler(activeProbeDeps{
		now: func() time.Time { return now },
		targets: func(smart_routing_setting.SmartRoutingSetting, time.Time) []ActiveProbeTarget {
			return []ActiveProbeTarget{target}
		},
		executor: func() ActiveProbeExecutor {
			return func(context.Context, ActiveProbeTarget) ActiveProbeExecution {
				return ActiveProbeExecution{Err: errors.New("provider failed")}
			}
		},
		validate: func(ActiveProbeTarget) error {
			if validations.Add(1) == 2 {
				return ErrActiveProbeTargetStale
			}
			return nil
		},
		acquire: successfulProbeLeaseForTest,
		persist: func(_ context.Context, _ model.RoutingControlLease, result model.RoutingProbeResult) error {
			persisted = result
			return nil
		},
		complete:  func(context.Context, model.RoutingControlLease, int64) error { return nil },
		release:   func(context.Context, model.RoutingControlLease, int64) error { return nil },
		waitUntil: func(context.Context, time.Time) bool { return true },
	})
	require.NoError(t, scheduler.RunCycle(context.Background(), activeProbeSettingForTest()))
	assert.Equal(t, model.RoutingProbeOutcomeFailure, persisted.Outcome)
	assert.Equal(t, int64(2), validations.Load(), "the target must be fenced again after persistence")
}

func TestActiveProbeSchedulerSeparatesBudgetsAndBoundsConcurrencyPerHost(t *testing.T) {
	now := time.Unix(20_000, 0)
	targets := []ActiveProbeTarget{
		activeProbeTargetForTest("a", "shared.example"),
		activeProbeTargetForTest("b", "shared.example"),
		activeProbeTargetForTest("c", "other.example"),
		activeProbeTargetForTest("d", "other.example"),
	}
	var persisted atomic.Int64
	var completed atomic.Int64
	var released atomic.Int64
	currentByHost := map[string]int{}
	maxByHost := map[string]int{}
	currentTotal := 0
	maxTotal := 0
	var concurrencyMu sync.Mutex

	scheduler := newActiveProbeScheduler(activeProbeDeps{
		now:     func() time.Time { return now },
		targets: func(smart_routing_setting.SmartRoutingSetting, time.Time) []ActiveProbeTarget { return targets },
		executor: func() ActiveProbeExecutor {
			return func(_ context.Context, target ActiveProbeTarget) ActiveProbeExecution {
				concurrencyMu.Lock()
				currentByHost[target.EndpointHost]++
				currentTotal++
				maxByHost[target.EndpointHost] = max(maxByHost[target.EndpointHost], currentByHost[target.EndpointHost])
				maxTotal = max(maxTotal, currentTotal)
				concurrencyMu.Unlock()
				time.Sleep(20 * time.Millisecond)
				concurrencyMu.Lock()
				currentByHost[target.EndpointHost]--
				currentTotal--
				concurrencyMu.Unlock()
				return ActiveProbeExecution{PromptTokens: 10, CompletionTokens: 5, CostNanoUSD: 1_000}
			}
		},
		acquire: successfulProbeLeaseForTest,
		persist: func(_ context.Context, _ model.RoutingControlLease, result model.RoutingProbeResult) error {
			assert.Equal(t, model.RoutingProbeOutcomeSuccess, result.Outcome)
			persisted.Add(1)
			return nil
		},
		complete: func(context.Context, model.RoutingControlLease, int64) error {
			completed.Add(1)
			return nil
		},
		release: func(context.Context, model.RoutingControlLease, int64) error {
			released.Add(1)
			return nil
		},
		waitUntil: func(context.Context, time.Time) bool { return true },
	})
	settings := activeProbeSettingForTest()
	settings.ActiveProbeConcurrency = 4
	settings.ActiveProbePerHost = 1
	settings.ActiveProbeTokenBudget = 64

	require.NoError(t, scheduler.RunCycle(context.Background(), settings))
	stats := scheduler.Stats()
	assert.Equal(t, int64(2), stats.Executed)
	assert.Equal(t, int64(2), stats.Succeeded)
	assert.Equal(t, int64(2), stats.SkippedBudget)
	assert.Equal(t, int64(64), stats.ReservedTokens)
	assert.Equal(t, int64(2), persisted.Load())
	assert.Equal(t, int64(2), completed.Load())
	assert.Equal(t, int64(2), released.Load())
	assert.LessOrEqual(t, maxTotal, 2)
	assert.LessOrEqual(t, maxByHost["shared.example"], 1)
	assert.LessOrEqual(t, maxByHost["other.example"], 1)
}

func TestActiveProbeSchedulerAcquiresLeaseAfterPerHostAdmission(t *testing.T) {
	now := time.Unix(25_000, 0)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var leaseAcquisitions atomic.Int64
	var executions atomic.Int64
	scheduler := newActiveProbeScheduler(activeProbeDeps{
		now: func() time.Time { return now },
		targets: func(smart_routing_setting.SmartRoutingSetting, time.Time) []ActiveProbeTarget {
			return []ActiveProbeTarget{
				activeProbeTargetForTest("a", "shared.example"),
				activeProbeTargetForTest("b", "shared.example"),
			}
		},
		executor: func() ActiveProbeExecutor {
			return func(_ context.Context, _ ActiveProbeTarget) ActiveProbeExecution {
				if executions.Add(1) == 1 {
					close(firstStarted)
					<-releaseFirst
				}
				return ActiveProbeExecution{}
			}
		},
		acquire: func(ctx context.Context, target ActiveProbeTarget, nowMs int64, ttlMs int64, minimumIntervalMs int64) (model.RoutingControlLease, bool, error) {
			leaseAcquisitions.Add(1)
			return successfulProbeLeaseForTest(ctx, target, nowMs, ttlMs, minimumIntervalMs)
		},
		persist:   func(context.Context, model.RoutingControlLease, model.RoutingProbeResult) error { return nil },
		complete:  func(context.Context, model.RoutingControlLease, int64) error { return nil },
		release:   func(context.Context, model.RoutingControlLease, int64) error { return nil },
		waitUntil: func(context.Context, time.Time) bool { return true },
	})
	settings := activeProbeSettingForTest()
	settings.ActiveProbeConcurrency = 2
	settings.ActiveProbePerHost = 1

	done := make(chan error, 1)
	go func() { done <- scheduler.RunCycle(context.Background(), settings) }()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first probe did not start")
	}
	assert.Equal(t, int64(1), leaseAcquisitions.Load())
	close(releaseFirst)
	require.NoError(t, <-done)
	assert.Equal(t, int64(2), leaseAcquisitions.Load())
}

func TestActiveProbeSchedulerStopsQueuedWorkWhenDisabled(t *testing.T) {
	now := time.Unix(27_000, 0)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var enabled atomic.Bool
	enabled.Store(true)
	var executed atomic.Int64
	scheduler := newActiveProbeScheduler(activeProbeDeps{
		now:     func() time.Time { return now },
		enabled: enabled.Load,
		targets: func(smart_routing_setting.SmartRoutingSetting, time.Time) []ActiveProbeTarget {
			return []ActiveProbeTarget{
				activeProbeTargetForTest("a", "shared.example"),
				activeProbeTargetForTest("b", "shared.example"),
			}
		},
		executor: func() ActiveProbeExecutor {
			return func(_ context.Context, _ ActiveProbeTarget) ActiveProbeExecution {
				if executed.Add(1) == 1 {
					close(firstStarted)
					<-releaseFirst
				}
				return ActiveProbeExecution{}
			}
		},
		acquire:   successfulProbeLeaseForTest,
		persist:   func(context.Context, model.RoutingControlLease, model.RoutingProbeResult) error { return nil },
		complete:  func(context.Context, model.RoutingControlLease, int64) error { return nil },
		release:   func(context.Context, model.RoutingControlLease, int64) error { return nil },
		waitUntil: func(context.Context, time.Time) bool { return true },
	})
	settings := activeProbeSettingForTest()
	settings.ActiveProbeConcurrency = 2
	settings.ActiveProbePerHost = 1

	done := make(chan error, 1)
	go func() { done <- scheduler.RunCycle(context.Background(), settings) }()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first probe did not start")
	}
	enabled.Store(false)
	close(releaseFirst)
	require.NoError(t, <-done)
	assert.Equal(t, int64(1), executed.Load())
}

func TestActiveProbeSchedulerReportsLeaseContentionAndDatabaseFailure(t *testing.T) {
	now := time.Unix(30_000, 0)
	leaseErr := errors.New("database unavailable")
	targets := []ActiveProbeTarget{
		activeProbeTargetForTest("a", "one.example"),
		activeProbeTargetForTest("b", "two.example"),
	}
	scheduler := newActiveProbeScheduler(activeProbeDeps{
		now:     func() time.Time { return now },
		targets: func(smart_routing_setting.SmartRoutingSetting, time.Time) []ActiveProbeTarget { return targets },
		executor: func() ActiveProbeExecutor {
			return func(context.Context, ActiveProbeTarget) ActiveProbeExecution {
				t.Fatal("executor must not run without a lease")
				return ActiveProbeExecution{}
			}
		},
		acquire: func(_ context.Context, target ActiveProbeTarget, _ int64, _ int64, _ int64) (model.RoutingControlLease, bool, error) {
			if strings.HasPrefix(target.TargetKey, "a") {
				return model.RoutingControlLease{}, false, nil
			}
			return model.RoutingControlLease{}, false, leaseErr
		},
		persist:   func(context.Context, model.RoutingControlLease, model.RoutingProbeResult) error { return nil },
		complete:  func(context.Context, model.RoutingControlLease, int64) error { return nil },
		release:   func(context.Context, model.RoutingControlLease, int64) error { return nil },
		waitUntil: func(context.Context, time.Time) bool { return true },
	})

	err := scheduler.RunCycle(context.Background(), activeProbeSettingForTest())
	assert.ErrorIs(t, err, leaseErr)
	stats := scheduler.Stats()
	assert.Equal(t, int64(1), stats.LeaseContended)
	assert.Equal(t, int64(1), stats.LeaseErrors)
	assert.Zero(t, stats.Executed)
}

func TestActiveProbeSchedulerCancellationStopsAndReleasesLease(t *testing.T) {
	now := time.Unix(40_000, 0)
	started := make(chan struct{})
	released := make(chan struct{}, 1)
	scheduler := newActiveProbeScheduler(activeProbeDeps{
		now: func() time.Time { return now },
		targets: func(smart_routing_setting.SmartRoutingSetting, time.Time) []ActiveProbeTarget {
			return []ActiveProbeTarget{activeProbeTargetForTest("a", "one.example")}
		},
		executor: func() ActiveProbeExecutor {
			return func(ctx context.Context, _ ActiveProbeTarget) ActiveProbeExecution {
				close(started)
				<-ctx.Done()
				return ActiveProbeExecution{Err: ctx.Err()}
			}
		},
		acquire: successfulProbeLeaseForTest,
		persist: func(context.Context, model.RoutingControlLease, model.RoutingProbeResult) error {
			t.Fatal("canceled probe must not persist a stale result")
			return nil
		},
		complete: func(context.Context, model.RoutingControlLease, int64) error {
			t.Fatal("canceled probe must release rather than complete the lease")
			return nil
		},
		release: func(context.Context, model.RoutingControlLease, int64) error {
			released <- struct{}{}
			return nil
		},
		waitUntil: func(context.Context, time.Time) bool { return true },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.RunCycle(ctx, activeProbeSettingForTest()) }()
	<-started
	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("probe lease was not released")
	}
	assert.Equal(t, int64(1), scheduler.Stats().Canceled)
	assert.Zero(t, scheduler.Stats().Inflight)
}

func TestCurrentActiveProbeTargetsAreBoundedDeterministicAndActiveOnly(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	models := make([]ModelSnapshot, 0, 40)
	for index := 0; index < 40; index++ {
		models = append(models, ModelSnapshot{ModelName: "model-" + string(rune('a'+index))})
	}
	SetSnapshotForTest(SnapshotView{
		Revision: 9,
		Channels: []ChannelSnapshot{
			{ID: 101, Status: common.ChannelStatusEnabled, Endpoint: "https://API.Example.test/v1"},
			{ID: 102, Status: common.ChannelStatusManuallyDisabled, Endpoint: "https://disabled.example/v1"},
		},
		Pools: []PoolSnapshot{
			{ID: 1, GroupName: "active", DeploymentStage: model.RoutingDeploymentStageActive, Members: []PoolMemberSnapshot{
				{ID: 11, PoolID: 1, ChannelID: 101, CredentialIDs: []int{77}, Models: models},
				{ID: 12, PoolID: 1, ChannelID: 102, Models: []ModelSnapshot{{ModelName: "disabled"}}},
			}},
			{ID: 2, GroupName: "shadow", DeploymentStage: model.RoutingDeploymentStageShadow, Members: []PoolMemberSnapshot{
				{ID: 21, PoolID: 2, ChannelID: 101, Models: []ModelSnapshot{{ModelName: "shadow-only"}}},
			}},
		},
	})
	setting := activeProbeSettingForTest()
	setting.ActiveProbeMaxTargets = 5
	now := time.Unix(50_000, 0)
	first := currentActiveProbeTargets(setting, now)
	second := currentActiveProbeTargets(setting, now)
	require.Len(t, first, 5)
	assert.Equal(t, first, second)
	for _, target := range first {
		assert.Equal(t, 1, target.PoolID)
		assert.Equal(t, 101, target.ChannelID)
		assert.Equal(t, 77, target.CredentialID)
		assert.Equal(t, "api.example.test", target.EndpointHost)
		assert.Equal(t, 64, len(target.TargetKey))
	}
}

func TestCurrentActiveProbeTargetsPrioritizeRecoveryStates(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	models := make([]ModelSnapshot, 0, 41)
	for index := 0; index < 40; index++ {
		models = append(models, ModelSnapshot{ModelName: "healthy-" + string(rune('a'+index))})
	}
	models = append(models, ModelSnapshot{ModelName: "open-target", BreakerState: model.RoutingBreakerStateOpen})
	SetSnapshotForTest(SnapshotView{
		Revision: 11,
		Channels: []ChannelSnapshot{{ID: 301, Status: common.ChannelStatusEnabled, Endpoint: "https://priority.example/v1"}},
		Pools: []PoolSnapshot{{
			ID: 3, GroupName: "active", DeploymentStage: model.RoutingDeploymentStageActive,
			Members: []PoolMemberSnapshot{{ID: 31, PoolID: 3, ChannelID: 301, CredentialIDs: []int{91}, Models: models}},
		}},
	})
	setting := activeProbeSettingForTest()
	setting.ActiveProbeMaxTargets = 5

	targets := currentActiveProbeTargets(setting, time.Unix(52_000, 0))
	require.Len(t, targets, 5)
	selectedOpen := false
	for _, target := range targets {
		selectedOpen = selectedOpen || target.ModelName == "open-target"
	}
	assert.True(t, selectedOpen)
}

func TestCurrentActiveProbeTargetsCarrySelectedEndpointCooldown(t *testing.T) {
	ResetSnapshotForTest()
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routinghotcache.ResetForTest()
	})
	now := time.Unix(53_000, 0)
	endpoint := "https://endpoint-cooldown.example/v1"
	authority := EndpointAuthority(endpoint, 302)
	region := RoutingRegion()
	endpointKey := routingbreaker.NewEndpointKey(authority, region)
	routinghotcache.SetBreakerForTest(endpointKey.HotcacheKey(), routinghotcache.BreakerSnapshot{
		State: model.RoutingBreakerStateOpen, CooldownUntilUnix: now.Add(90 * time.Second).Unix(), UpdatedUnix: now.Unix(),
	})
	SetSnapshotForTest(SnapshotView{
		Revision: 12,
		Channels: []ChannelSnapshot{{
			ID: 302, Status: common.ChannelStatusEnabled, Endpoint: endpoint,
		}},
		Pools: []PoolSnapshot{{
			ID: 3, GroupName: "active", DeploymentStage: model.RoutingDeploymentStageActive,
			Members: []PoolMemberSnapshot{{
				ID: 32, PoolID: 3, ChannelID: 302, CredentialIDs: []int{92},
				Models: []ModelSnapshot{{ModelName: "gpt-test", BreakerState: model.RoutingBreakerStateHealthy}},
			}},
		}},
	})

	targets := currentActiveProbeTargets(activeProbeSettingForTest(), now)
	require.Len(t, targets, 1)
	assert.Equal(t, BreakerScopeEndpoint, targets[0].BreakerScope)
	assert.Equal(t, model.RoutingBreakerStateOpen, targets[0].BreakerState)
	assert.Equal(t, now.Add(90*time.Second).Unix(), targets[0].BreakerCooldownUntil)
}

func TestCurrentActiveProbeTargetsDoNotMisattributeMultiKeyCredential(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	SetSnapshotForTest(SnapshotView{
		Revision: 10,
		Channels: []ChannelSnapshot{{
			ID: 201, Status: common.ChannelStatusEnabled, Endpoint: "https://multi.example/v1", MultiKey: true,
		}},
		Pools: []PoolSnapshot{{
			ID: 2, GroupName: "active", DeploymentStage: model.RoutingDeploymentStageActive,
			Members: []PoolMemberSnapshot{{
				ID: 21, PoolID: 2, ChannelID: 201, MultiKey: true, CredentialIDs: []int{81, 82},
				Models: []ModelSnapshot{{ModelName: "gpt-test"}},
			}},
		}},
	})

	targets := currentActiveProbeTargets(activeProbeSettingForTest(), time.Unix(55_000, 0))
	require.Len(t, targets, 1)
	assert.True(t, targets[0].MultiKey)
	assert.Zero(t, targets[0].CredentialID)
}

func TestActiveProbeEstimatedCostUsesRequestSizedPricing(t *testing.T) {
	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = previousQuotaPerUnit })

	tokenCost := activeProbeEstimatedCostNanoUSD(ModelSnapshot{
		CostKnown: true, CostGroupRatio: 1, CostBaseRatio: 2, CostCompletionRatio: 3, CostBillingMode: "token",
	})
	assert.Equal(t, int64(256_000), tokenCost)

	perRequestCost := activeProbeEstimatedCostNanoUSD(ModelSnapshot{
		CostKnown: true, CostGroupRatio: 1.5, CostModelPrice: 0.02, CostBillingMode: "per_request",
	})
	assert.Equal(t, int64(30_000_000), perRequestCost)
	assert.Equal(t, activeProbeUnknownCostNanoUSD, activeProbeEstimatedCostNanoUSD(ModelSnapshot{}))
	assert.Greater(t, activeProbeUnknownCostNanoUSD, activeProbeCostBudgetNanoUSD(1_000))
}

func TestActiveProbeResultClassifiesTimeoutLocalAndCanceledOutcomes(t *testing.T) {
	target := activeProbeTargetForTest("a", "one.example")
	lease, _, _ := successfulProbeLeaseForTest(context.Background(), target, 1_000, 10_000, 1_000)
	started := time.Unix(60_000, 0)

	timedOut := activeProbeResult(target, lease, ActiveProbeExecution{
		StatusCode: 999,
		Err:        context.DeadlineExceeded,
	}, started, started.Add(time.Second), context.DeadlineExceeded, nil)
	assert.Equal(t, model.RoutingProbeOutcomeTimeout, timedOut.Outcome)
	assert.Equal(t, 599, timedOut.StatusCode)
	assert.Contains(t, timedOut.ErrorMessage, "deadline")

	local := activeProbeResult(target, lease, ActiveProbeExecution{
		Err:        errors.New("local setup failed"),
		LocalError: true,
	}, started, started.Add(time.Second), nil, nil)
	assert.Equal(t, model.RoutingProbeOutcomeLocalError, local.Outcome)
	localTimeout := activeProbeResult(target, lease, ActiveProbeExecution{
		Err:        context.DeadlineExceeded,
		LocalError: true,
	}, started, started.Add(time.Second), context.DeadlineExceeded, nil)
	assert.Equal(t, model.RoutingProbeOutcomeLocalError, localTimeout.Outcome)

	canceled := activeProbeResult(target, lease, ActiveProbeExecution{}, started, started, context.Canceled, context.Canceled)
	assert.Equal(t, model.RoutingProbeOutcomeCanceled, canceled.Outcome)
	assert.Equal(t, activeProbeResultID(target.TargetKey, lease.FencingToken), canceled.ProbeID)
}

func TestActiveProbeBreakerUsesPersistedOutcomeBoundary(t *testing.T) {
	now := time.Unix(65_000, 0)
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 5, FailureRateThreshold: 1, FailureRateMinSamples: 100, WindowSize: 100,
		BaseCooldown: time.Minute, MaxCooldown: time.Minute, EntryTTL: time.Hour, MaxEntries: 16,
		DegradedConsecutiveFailures: 1, DegradedFailureRateThreshold: 1, DegradedMinSamples: 100,
		Now: func() time.Time { return now },
	})
	t.Cleanup(func() { routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig()) })
	target := activeProbeTargetForTest("a", "one.example")
	target.ChannelID = 101
	target.GroupName = "default"
	target.ModelName = "gpt-test"
	target.BreakerState = model.RoutingBreakerStateDegraded

	require.NoError(t, applyActiveProbeBreakerOutcome(
		context.Background(), activeProbeSettingForTest(), target, ActiveProbeExecution{}, model.RoutingProbeOutcomeTimeout, now,
	))
	assert.Empty(t, routingbreaker.DirtySnapshots())
	snapshots := routingbreaker.DirtyEndpointSnapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, 1, snapshots[0].ConsecutiveFailures)
	assert.Equal(t, routingbreaker.StateDegraded, snapshots[0].State)
}

func activeProbeSettingForTest() smart_routing_setting.SmartRoutingSetting {
	return smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		ActiveProbeEnabled:       true,
		ActiveProbeHealthySec:    60,
		ActiveProbeDegradedSec:   30,
		ActiveProbeOpenSec:       10,
		ActiveProbeTimeoutMs:     1_000,
		ActiveProbeMaxTargets:    16,
		ActiveProbeConcurrency:   4,
		ActiveProbePerHost:       1,
		ActiveProbeTokenBudget:   1_024,
		ActiveProbeCostBudgetUSD: 1,
	}
}

func activeProbeTargetForTest(prefix string, host string) ActiveProbeTarget {
	return ActiveProbeTarget{
		TargetKey:            prefix + strings.Repeat("0", 63),
		SnapshotRevision:     1,
		PoolID:               1,
		MemberID:             int(prefix[0]),
		ChannelID:            int(prefix[0]),
		GroupName:            "default",
		ModelName:            "gpt-test",
		EndpointHost:         host,
		EndpointAuthority:    "https://" + host + ":443",
		Region:               "default",
		BreakerScope:         "member",
		BreakerState:         model.RoutingBreakerStateHealthy,
		Interval:             time.Minute,
		EstimatedTokens:      32,
		EstimatedCostNanoUSD: 1,
	}
}

func successfulProbeLeaseForTest(
	_ context.Context,
	target ActiveProbeTarget,
	nowMs int64,
	ttlMs int64,
	_ int64,
) (model.RoutingControlLease, bool, error) {
	return model.RoutingControlLease{
		LeaseName:    activeProbeLeaseName(target.TargetKey),
		HolderID:     "node-test",
		LeaseToken:   strings.Repeat("1", 32),
		LeaseUntilMs: nowMs + ttlMs,
		FencingToken: int64(target.TargetKey[0]),
	}, true, nil
}
