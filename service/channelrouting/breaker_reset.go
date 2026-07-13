package channelrouting

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
)

const (
	breakerResetOperationLease       = 30 * time.Second
	breakerResetOperationMaxAttempts = 8
	breakerResetOutboxLease          = 30 * time.Second
	breakerResetSyncPageSize         = 256
)

var (
	ErrBreakerResetSnapshotUnavailable = errors.New("channel routing snapshot is unavailable")
	ErrBreakerResetTargetNotFound      = errors.New("channel routing breaker reset target was not found")

	breakerResetSync = struct {
		sync.Mutex
		initialized  bool
		outboxCursor int64
	}{}
)

type BreakerResetResolvedTarget struct {
	Target               model.RoutingBreakerResetTarget
	ExpectedRevision     int64
	ExpectedActivationID int64
}

func ResolveMemberBreakerResetTarget(
	poolID int,
	memberID int,
	modelName string,
) (BreakerResetResolvedTarget, error) {
	modelName = strings.TrimSpace(modelName)
	if poolID <= 0 || memberID <= 0 || modelName == "" || !utf8.ValidString(modelName) ||
		utf8.RuneCountInString(modelName) > 128 {
		return BreakerResetResolvedTarget{}, model.ErrRoutingBreakerResetInvalid
	}
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return BreakerResetResolvedTarget{}, ErrBreakerResetSnapshotUnavailable
	}
	poolIndex, exists := snapshot.poolIndexByID[poolID]
	if !exists || poolIndex < 0 || poolIndex >= len(snapshot.view.Pools) {
		return BreakerResetResolvedTarget{}, ErrBreakerResetTargetNotFound
	}
	pool := snapshot.view.Pools[poolIndex]
	var member *PoolMemberSnapshot
	for index := range pool.Members {
		if pool.Members[index].ID == memberID {
			member = &pool.Members[index]
			break
		}
	}
	if member == nil || member.PoolID != poolID || member.ChannelID <= 0 {
		return BreakerResetResolvedTarget{}, ErrBreakerResetTargetNotFound
	}
	if _, exists := snapshot.modelByMemberModel[memberModelKey{memberID: memberID, model: modelName}]; !exists {
		return BreakerResetResolvedTarget{}, ErrBreakerResetTargetNotFound
	}
	if snapshot.view.Revision > math.MaxInt64 {
		return BreakerResetResolvedTarget{}, model.ErrRoutingBreakerResetInvalid
	}
	return BreakerResetResolvedTarget{
		Target: model.RoutingBreakerResetTarget{
			Scope: model.RoutingBreakerResetScopeMember, PoolID: poolID, MemberID: memberID,
			ChannelID: member.ChannelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
			ModelName: modelName, GroupName: pool.GroupName,
		},
		ExpectedRevision: int64(snapshot.view.Revision), ExpectedActivationID: snapshot.view.ActivationID,
	}, nil
}

func ResolveLegacyMemberBreakerResetTarget(
	channelID int,
	modelName string,
	groupName string,
) (BreakerResetResolvedTarget, error) {
	modelName = strings.TrimSpace(modelName)
	groupName = strings.TrimSpace(groupName)
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return BreakerResetResolvedTarget{}, ErrBreakerResetSnapshotUnavailable
	}
	poolID, exists := snapshot.poolByGroup[groupName]
	if !exists {
		return BreakerResetResolvedTarget{}, ErrBreakerResetTargetNotFound
	}
	memberID, exists := snapshot.memberByPoolChannel[poolChannelKey{PoolID: poolID, ChannelID: channelID}]
	if !exists {
		return BreakerResetResolvedTarget{}, ErrBreakerResetTargetNotFound
	}
	if _, exists := snapshot.modelByMemberModel[memberModelKey{memberID: memberID, model: modelName}]; !exists {
		return BreakerResetResolvedTarget{}, ErrBreakerResetTargetNotFound
	}
	return ResolveMemberBreakerResetTarget(poolID, memberID, modelName)
}

func ResolveEndpointBreakerResetTarget(
	endpoint string,
	region string,
) (BreakerResetResolvedTarget, error) {
	endpoint = strings.TrimSpace(endpoint)
	region = strings.ToLower(strings.TrimSpace(region))
	if endpoint == "" || region == "" || normalizeRoutingRegion(region) != region {
		return BreakerResetResolvedTarget{}, model.ErrRoutingBreakerResetInvalid
	}
	host, authority := endpointScopeIdentity(endpoint, 0)
	if host == "unknown" || authority == "channel://unknown" || host == "" || authority == "" {
		return BreakerResetResolvedTarget{}, model.ErrRoutingBreakerResetInvalid
	}
	snapshot := currentSnapshot.Load()
	if snapshot == nil || snapshot.view.Revision > math.MaxInt64 {
		return BreakerResetResolvedTarget{}, ErrBreakerResetSnapshotUnavailable
	}
	if region != RoutingRegion() {
		return BreakerResetResolvedTarget{}, ErrBreakerResetTargetNotFound
	}
	authorityKnown := false
	for index := range snapshot.view.Channels {
		channel := snapshot.view.Channels[index]
		if EndpointAuthority(channel.Endpoint, channel.ID) == authority {
			authorityKnown = true
			break
		}
	}
	if !authorityKnown {
		return BreakerResetResolvedTarget{}, ErrBreakerResetTargetNotFound
	}
	return BreakerResetResolvedTarget{
		Target: model.RoutingBreakerResetTarget{
			Scope: model.RoutingBreakerResetScopeEndpoint, EndpointHost: host,
			EndpointAuthority: authority, Region: region,
		},
		ExpectedRevision: int64(snapshot.view.Revision), ExpectedActivationID: snapshot.view.ActivationID,
	}, nil
}

func RunBreakerResetControlCycleContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !model.RoutingBreakerResetSchemaReady() {
		return nil
	}
	nowMs, err := model.RoutingEndpointDatabaseNowMsContext(ctx)
	if err != nil {
		return err
	}
	claimed, err := model.ClaimRoutingOperationContext(
		ctx, model.RoutingOperationTypeBreakerReset, nowMs, int64(breakerResetOperationLease/time.Millisecond),
	)
	if err != nil {
		return err
	}
	if claimed != nil {
		renewedAtMs, timeErr := model.RoutingEndpointDatabaseNowMsContext(ctx)
		if timeErr != nil {
			return timeErr
		}
		if err := model.RenewRoutingOperationClaimContext(
			ctx, claimed.ID, claimed.ClaimToken, renewedAtMs, int64(breakerResetOperationLease/time.Millisecond),
		); err != nil {
			return err
		}
		execution, executeErr := model.ExecuteRoutingBreakerResetOperationContext(ctx, *claimed)
		if executeErr != nil {
			if errors.Is(executeErr, model.ErrRoutingOperationClaimLost) {
				return nil
			}
			failedAtMs, clockErr := model.RoutingEndpointDatabaseNowMsContext(ctx)
			if clockErr != nil {
				return errors.Join(executeErr, clockErr)
			}
			if claimed.Attempts >= breakerResetOperationMaxAttempts {
				return errors.Join(executeErr, model.FailRoutingOperationContext(
					ctx, claimed.ID, claimed.ClaimToken, failedAtMs, executeErr,
				))
			}
			backoff := time.Second << min(claimed.Attempts, 6)
			return errors.Join(executeErr, model.RetryRoutingOperationContext(
				ctx, claimed.ID, claimed.ClaimToken, failedAtMs, failedAtMs+int64(backoff/time.Millisecond), executeErr,
			))
		}
		if execution.Operation.Status == model.RoutingOperationStatusSucceeded {
			applyRoutingBreakerReset(execution.Event)
			if _, err := publishLocalRoutingEvent(RoutingEventTypeBreakerReset, 0, execution.Event); err != nil {
				return err
			}
		}
	}
	if _, err := PublishRoutingBreakerResetOutboxOnceContext(ctx); err != nil {
		return err
	}
	return SyncRoutingBreakerResetStateContext(ctx)
}

func PublishRoutingBreakerResetOutboxOnceContext(ctx context.Context) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client := loadRoutingEventRedis()
	if client == nil {
		return false, nil
	}
	nowMs, err := model.RoutingEndpointDatabaseNowMsContext(ctx)
	if err != nil {
		return false, err
	}
	outbox, err := model.ClaimRoutingBreakerResetOutboxContext(
		ctx, nowMs, int64(breakerResetOutboxLease/time.Millisecond),
	)
	if err != nil || outbox == nil {
		return false, err
	}
	event, err := outbox.DecodePayload()
	if err == nil {
		payload := []byte(outbox.PayloadJSON)
		err = broadcastRoutingEventContext(ctx, defaultRoutingEventTransport, client, NodeEpochID(), RoutingEvent{
			ID: uint64(outbox.ID), Type: RoutingEventTypeBreakerReset,
			CreatedTimeMs: event.ResetAtMs, PayloadJSON: payload,
		})
	}
	finishedAtMs, clockErr := model.RoutingEndpointDatabaseNowMsContext(ctx)
	if clockErr != nil {
		return false, errors.Join(err, clockErr)
	}
	if err != nil {
		backoff := time.Second << min(outbox.Attempts, 6)
		releaseErr := model.ReleaseRoutingBreakerResetOutboxClaimContext(
			ctx, outbox.ID, outbox.ClaimToken, finishedAtMs,
			finishedAtMs+int64(backoff/time.Millisecond), err,
		)
		return false, errors.Join(err, releaseErr)
	}
	if err := model.MarkRoutingBreakerResetOutboxPublishedContext(
		ctx, outbox.ID, outbox.ClaimToken, finishedAtMs,
	); err != nil {
		return false, err
	}
	return true, nil
}

func SyncRoutingBreakerResetStateContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !model.RoutingBreakerResetSchemaReady() {
		return nil
	}
	breakerResetSync.Lock()
	defer breakerResetSync.Unlock()
	return syncRoutingBreakerResetStateLockedContext(ctx)
}

func syncRoutingBreakerResetStateLockedContext(ctx context.Context) error {
	if !breakerResetSync.initialized {
		highWater, err := model.MaxRoutingBreakerResetOutboxIDContext(ctx)
		if err != nil {
			return err
		}
		afterID := int64(0)
		for {
			rows, err := model.ListRoutingBreakerResetTombstonesPageContext(ctx, afterID, breakerResetSyncPageSize)
			if err != nil {
				return err
			}
			for index := range rows {
				tombstone := rows[index]
				applyRoutingBreakerReset(model.RoutingBreakerResetEvent{
					SchemaVersion: 1, OperationID: tombstone.LastOperationID,
					Generation: tombstone.Generation, ResetAtMs: tombstone.ResetAtMs, Target: tombstone.Target(),
				})
				afterID = tombstone.ID
			}
			if len(rows) < breakerResetSyncPageSize {
				break
			}
		}
		breakerResetSync.outboxCursor = highWater
		breakerResetSync.initialized = true
	}
	for {
		rows, err := model.ListRoutingBreakerResetOutboxAfterContext(
			ctx, breakerResetSync.outboxCursor, breakerResetSyncPageSize,
		)
		if err != nil {
			return err
		}
		for index := range rows {
			event, err := rows[index].DecodePayload()
			if err != nil {
				return err
			}
			applyRoutingBreakerReset(event)
			breakerResetSync.outboxCursor = rows[index].ID
		}
		if len(rows) < breakerResetSyncPageSize {
			return nil
		}
	}
}

func ApplyRoutingBreakerResetEventPayload(payload []byte) error {
	_, err := applyRoutingBreakerResetEventPayload(payload)
	return err
}

func applyRoutingBreakerResetEventPayload(payload []byte) (bool, error) {
	var event model.RoutingBreakerResetEvent
	if common.Unmarshal(payload, &event) != nil {
		return false, model.ErrRoutingBreakerResetInvalid
	}
	validated, err := model.ValidateRoutingBreakerResetEvent(event)
	if err != nil {
		return false, err
	}
	return applyRoutingBreakerReset(validated), nil
}

func applyDurableRoutingBreakerResetEventPayloadContext(ctx context.Context, payload []byte) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var event model.RoutingBreakerResetEvent
	if common.Unmarshal(payload, &event) != nil {
		return false, model.ErrRoutingBreakerResetInvalid
	}
	validated, err := model.ValidateRoutingBreakerResetEvent(event)
	if err != nil {
		return false, model.ErrRoutingBreakerResetInvalid
	}
	breakerResetSync.Lock()
	defer breakerResetSync.Unlock()
	if breakerResetSync.initialized && validated.OutboxID <= breakerResetSync.outboxCursor {
		return false, nil
	}
	if !model.RoutingBreakerResetSchemaReady() {
		return false, model.ErrRoutingBreakerResetInvalid
	}
	outbox, err := model.GetRoutingBreakerResetOutboxContext(ctx, validated.OutboxID)
	if err != nil {
		return false, err
	}
	durableEvent, err := outbox.DecodePayload()
	if err != nil || durableEvent != validated || outbox.PayloadJSON != string(payload) {
		return false, model.ErrRoutingBreakerResetInvalid
	}
	previousCursor := breakerResetSync.outboxCursor
	if err := syncRoutingBreakerResetStateLockedContext(ctx); err != nil {
		return false, err
	}
	if validated.OutboxID > breakerResetSync.outboxCursor {
		return false, model.ErrRoutingBreakerResetInvalid
	}
	return validated.OutboxID > previousCursor, nil
}

func applyRoutingBreakerReset(event model.RoutingBreakerResetEvent) bool {
	target := event.Target
	if event.Generation <= 0 {
		return false
	}
	if target.Scope == model.RoutingBreakerResetScopeMember {
		_, applied := routingbreaker.ApplyDefaultResetGeneration(routingbreaker.Key{
			ChannelID: target.ChannelID, APIKeyIndex: target.APIKeyIndex,
			Model: target.ModelName, Group: target.GroupName,
		}, event.Generation)
		return applied
	}
	if target.Scope != model.RoutingBreakerResetScopeEndpoint {
		return false
	}
	endpointSharedMaintenance.Lock()
	defer endpointSharedMaintenance.Unlock()
	key := routingbreaker.NewEndpointKey(target.EndpointAuthority, target.Region)
	_, applied := routingbreaker.ApplyDefaultResetGenerationWithCallback(key, event.Generation, func() {
		routingmetrics.ClearEndpoint(target.EndpointAuthority, target.Region)
		routinghotcache.ClearSharedEndpointBreaker(key.HotcacheKey())
	})
	return applied
}

func resetRoutingBreakerResetRuntimeForTest() {
	breakerResetSync.Lock()
	breakerResetSync.initialized = false
	breakerResetSync.outboxCursor = 0
	breakerResetSync.Unlock()
}
