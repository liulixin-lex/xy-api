package channelrouting

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestActiveProbeOutcomeAppliesUnifiedRoutingEffects(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/probe-outcome.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Channel{}, &model.RoutingCredentialRef{}, &model.RoutingChannelHealthState{},
	))
	previousDB := model.DB
	previousSecret := common.CryptoSecret
	model.DB = db
	common.CryptoSecret = "stable-active-probe-outcome-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() {
		model.DB = previousDB
		common.CryptoSecret = previousSecret
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})

	now := time.Unix(70_000, 0)
	setting := activeProbeSettingForTest()
	setting.SnapshotStaleSec = 300
	setting.BackoffBaseMs429 = 1_000
	setting.MaxCooldownSec = 60
	resetEffects := func() {
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.Config{
			Consecutive5xxThreshold: 5, FailureRateThreshold: 1, FailureRateMinSamples: 100, WindowSize: 100,
			BaseCooldown: time.Minute, MaxCooldown: time.Minute, EntryTTL: time.Hour, MaxEntries: 64,
			DegradedConsecutiveFailures: 1, DegradedFailureRateThreshold: 1, DegradedMinSamples: 100,
			Now: func() time.Time { return now },
		})
	}
	targetForChannel := func(channelID int) ActiveProbeTarget {
		target := activeProbeTargetForTest("a", "one.example")
		target.ChannelID = channelID
		target.CredentialID = channelID + 100_000
		target.GroupName = "default"
		target.ModelName = "gpt-test"
		return target
	}
	seedCredential := func(target ActiveProbeTarget) {
		key := "probe-key-" + strconv.Itoa(target.ChannelID)
		fingerprint, err := model.RoutingCredentialFingerprint(target.ChannelID, key)
		require.NoError(t, err)
		require.NoError(t, db.Create(&model.Channel{
			Id: target.ChannelID, Key: key, Status: common.ChannelStatusEnabled,
		}).Error)
		require.NoError(t, db.Create(&model.RoutingCredentialRef{
			ID: target.CredentialID, ChannelID: target.ChannelID, Fingerprint: fingerprint,
			FingerprintVersion: model.RoutingCredentialFingerprintVersion, Active: true,
		}).Error)
	}

	for _, statusCode := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run("credential_"+strconv.Itoa(statusCode), func(t *testing.T) {
			resetEffects()
			target := targetForChannel(1_000 + statusCode)
			seedCredential(target)
			execution := ActiveProbeExecution{
				StatusCode: statusCode,
				Err:        errors.New("credential rejected"),
				Classification: routingerror.Classification{
					Responsibility: routingerror.ResponsibilityCredential,
					HealthEffect:   routingerror.HealthOpen,
					CapacityEffect: routingerror.CapacityNone,
				},
			}

			require.NoError(t, applyActiveProbeBreakerOutcome(
				context.Background(), setting, target, execution, model.RoutingProbeOutcomeFailure, now,
			))

			marker, found := routinghotcache.GetAuthFailure(target.ChannelID)
			require.True(t, found)
			assert.True(t, marker.Marked)
			assert.Equal(t, now.Unix(), marker.UpdatedUnix)
			var persisted model.RoutingChannelHealthState
			require.NoError(t, db.Where("channel_id = ?", target.ChannelID).First(&persisted).Error)
			assert.True(t, persisted.AuthFailure)
			assert.Equal(t, "active_probe_http_"+strconv.Itoa(statusCode), persisted.AuthFailureReason)
			assert.Equal(t, now.Add(300*time.Second).Unix(), persisted.AuthFailureUntil)
			assert.Empty(t, routingbreaker.DirtySnapshots())
		})
	}

	for _, statusCode := range []int{http.StatusPaymentRequired, http.StatusTooManyRequests, 529} {
		t.Run("capacity_"+strconv.Itoa(statusCode), func(t *testing.T) {
			resetEffects()
			target := targetForChannel(2_000 + statusCode)
			execution := ActiveProbeExecution{
				StatusCode: statusCode,
				Err:        errors.New("capacity unavailable"),
				Classification: routingerror.Classification{
					Responsibility: routingerror.ResponsibilityCapacity,
					HealthEffect:   routingerror.HealthIgnore,
					CapacityEffect: routingerror.CapacityCooldown,
				},
			}

			require.NoError(t, applyActiveProbeBreakerOutcome(
				context.Background(), setting, target, execution, model.RoutingProbeOutcomeFailure, now,
			))

			cooldown, found := routinghotcache.GetCapacityCooldown(routingbreaker.Key{
				ChannelID: target.ChannelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
				Model: target.ModelName, Group: target.GroupName,
			}.HotcacheKey())
			require.True(t, found)
			assert.Equal(t, statusCode, cooldown.SourceStatusCode)
			assert.Equal(t, now.Add(time.Second).UnixMilli(), cooldown.CooldownUntilUnixMilli)
			assert.Empty(t, routingbreaker.DirtySnapshots())
		})
	}

	t.Run("provider_503_with_retry_after", func(t *testing.T) {
		resetEffects()
		target := targetForChannel(3_503)
		execution := ActiveProbeExecution{
			StatusCode:   http.StatusServiceUnavailable,
			RetryAfterMs: 2_500,
			Err:          errors.New("provider unavailable"),
			Classification: routingerror.Classification{
				Responsibility: routingerror.ResponsibilityProvider,
				HealthEffect:   routingerror.HealthDegrade,
				CapacityEffect: routingerror.CapacityCooldown,
			},
		}

		require.NoError(t, applyActiveProbeBreakerOutcome(
			context.Background(), setting, target, execution, model.RoutingProbeOutcomeFailure, now,
		))

		key := routingbreaker.Key{
			ChannelID: target.ChannelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
			Model: target.ModelName, Group: target.GroupName,
		}
		cooldown, found := routinghotcache.GetCapacityCooldown(key.HotcacheKey())
		require.True(t, found)
		assert.Equal(t, int64(2_500), cooldown.RetryAfterMs)
		assert.Equal(t, now.Add(2_500*time.Millisecond).UnixMilli(), cooldown.CooldownUntilUnixMilli)
		snapshots := routingbreaker.DirtySnapshots()
		require.Len(t, snapshots, 1)
		assert.Equal(t, "5xx", snapshots[0].Reason)
	})

	t.Run("provider_5xx_without_retry_after", func(t *testing.T) {
		resetEffects()
		target := targetForChannel(3_500)
		execution := ActiveProbeExecution{
			StatusCode: http.StatusInternalServerError,
			Err:        errors.New("provider failed"),
			Classification: routingerror.Classification{
				Responsibility: routingerror.ResponsibilityProvider,
				HealthEffect:   routingerror.HealthDegrade,
				CapacityEffect: routingerror.CapacityNone,
			},
		}

		require.NoError(t, applyActiveProbeBreakerOutcome(
			context.Background(), setting, target, execution, model.RoutingProbeOutcomeFailure, now,
		))

		_, found := routinghotcache.GetCapacityCooldown(routingbreaker.Key{
			ChannelID: target.ChannelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
			Model: target.ModelName, Group: target.GroupName,
		}.HotcacheKey())
		assert.False(t, found)
		snapshots := routingbreaker.DirtySnapshots()
		require.Len(t, snapshots, 1)
		assert.Equal(t, "5xx", snapshots[0].Reason)
	})

	t.Run("timeout_is_network_reliability", func(t *testing.T) {
		resetEffects()
		target := targetForChannel(4_000)
		execution := ActiveProbeExecution{
			Err: context.DeadlineExceeded,
			Classification: routingerror.Classification{
				Responsibility: routingerror.ResponsibilityNetwork,
				Scope:          routingerror.ScopeEndpoint,
				HealthEffect:   routingerror.HealthDegrade,
				CapacityEffect: routingerror.CapacityNone,
			},
		}

		require.NoError(t, applyActiveProbeBreakerOutcome(
			context.Background(), setting, target, execution, model.RoutingProbeOutcomeTimeout, now,
		))
		assert.Empty(t, routingbreaker.DirtySnapshots())
		snapshots := routingbreaker.DirtyEndpointSnapshots()
		require.Len(t, snapshots, 1)
		assert.Equal(t, "network", snapshots[0].Reason)
	})

	t.Run("local_timeout_never_changes_health", func(t *testing.T) {
		resetEffects()
		target := targetForChannel(4_001)
		execution := ActiveProbeExecution{
			StatusCode: http.StatusUnauthorized,
			Err:        context.DeadlineExceeded,
			LocalError: true,
			Classification: routingerror.Classification{
				Responsibility: routingerror.ResponsibilityGateway,
				Scope:          routingerror.ScopeRequest,
				HealthEffect:   routingerror.HealthIgnore,
				CapacityEffect: routingerror.CapacityNone,
			},
		}

		record := activeProbeResult(
			target,
			model.RoutingControlLease{HolderID: "node", LeaseToken: strings.Repeat("1", 32), FencingToken: 1},
			execution, now, now.Add(time.Second), context.DeadlineExceeded, nil,
		)
		assert.Equal(t, model.RoutingProbeOutcomeLocalError, record.Outcome)
		require.NoError(t, applyActiveProbeBreakerOutcome(
			context.Background(), setting, target, execution, record.Outcome, now,
		))
		_, authFound := routinghotcache.GetAuthFailure(target.ChannelID)
		assert.False(t, authFound)
		assert.Empty(t, routingbreaker.DirtyEndpointSnapshots())
	})

	t.Run("success_clears_serving_markers_and_recovers_breaker", func(t *testing.T) {
		resetEffects()
		target := targetForChannel(5_000)
		seedCredential(target)
		target.AuthFailure = true
		target.BreakerState = model.RoutingBreakerStateDegraded
		key := routingbreaker.Key{
			ChannelID: target.ChannelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
			Model: target.ModelName, Group: target.GroupName,
		}
		require.NoError(t, model.UpsertRoutingChannelAuthFailure(target.ChannelID, true, "old_failure", now.Add(time.Minute).Unix()))
		routinghotcache.SetAuthFailure(target.ChannelID, routinghotcache.HealthMarker{Marked: true, UpdatedUnix: now.Unix() - 1})
		_, recorded := routinghotcache.RecordCapacityCooldown(
			key.HotcacheKey(), http.StatusTooManyRequests, time.Minute, time.Second, time.Minute, now,
		)
		require.True(t, recorded)
		routingbreaker.RecordReliabilityFailure(key, routingbreaker.FailureProvider5xx)

		require.NoError(t, applyActiveProbeBreakerOutcome(
			context.Background(), setting, target, ActiveProbeExecution{StatusCode: http.StatusOK}, model.RoutingProbeOutcomeSuccess, now,
		))

		_, authFound := routinghotcache.GetAuthFailure(target.ChannelID)
		assert.False(t, authFound)
		_, capacityFound := routinghotcache.GetCapacityCooldown(key.HotcacheKey())
		assert.False(t, capacityFound)
		var persisted model.RoutingChannelHealthState
		require.NoError(t, db.Where("channel_id = ?", target.ChannelID).First(&persisted).Error)
		assert.False(t, persisted.AuthFailure)
		assert.Empty(t, persisted.AuthFailureReason)
		assert.Zero(t, persisted.AuthFailureUntil)
		snapshots := routingbreaker.DirtySnapshots()
		require.Len(t, snapshots, 1)
		assert.Equal(t, routingbreaker.StateHealthy, snapshots[0].State)
	})

	t.Run("multi_key_never_writes_channel_aggregate_state", func(t *testing.T) {
		resetEffects()
		target := targetForChannel(6_000)
		target.MultiKey = true
		execution := ActiveProbeExecution{
			StatusCode:   http.StatusServiceUnavailable,
			RetryAfterMs: 2_500,
			Err:          errors.New("provider unavailable"),
			Classification: routingerror.Classification{
				Responsibility: routingerror.ResponsibilityProvider,
				HealthEffect:   routingerror.HealthDegrade,
				CapacityEffect: routingerror.CapacityCooldown,
			},
		}

		require.NoError(t, applyActiveProbeBreakerOutcome(
			context.Background(), setting, target, execution, model.RoutingProbeOutcomeFailure, now,
		))
		assert.Empty(t, routingbreaker.DirtySnapshots())
		assert.Equal(t, 0, routinghotcache.RuntimeStats().CapacityCooldowns)
		var count int64
		require.NoError(t, db.Model(&model.RoutingChannelHealthState{}).Where("channel_id = ?", target.ChannelID).Count(&count).Error)
		assert.Zero(t, count)
	})
}
