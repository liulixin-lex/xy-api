package channelrouting

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
)

const (
	RoutingCanaryNodePresenceCheckpointKind = "canary_presence"
	canaryNodePresenceSchemaVersion         = 1
	canaryNodePresenceTTL                   = 2 * time.Hour
	canaryNodePresencePollInterval          = 30 * time.Second
)

var (
	ErrCanaryNodePresenceInvalid = errors.New("invalid channel routing canary node presence")
	canaryNodePresenceSequence   atomic.Int64
)

type canaryNodePresenceIdentity struct {
	PolicyRevision     int64
	PolicyHash         string
	ActivationID       int64
	TrafficBasisPoints int
}

type canaryNodePresencePayload struct {
	SchemaVersion      int    `json:"schema_version"`
	PolicyHash         string `json:"policy_hash"`
	ActivationID       int64  `json:"activation_id"`
	ActivationStage    string `json:"activation_stage"`
	TrafficBasisPoints int    `json:"traffic_basis_points"`
}

func persistRoutingCanaryNodePresenceContext(
	ctx context.Context,
	_ smart_routing_setting.SmartRoutingSetting,
) error {
	return persistRoutingCanaryNodePresenceAtContext(ctx, time.Now())
}

func persistRoutingCanaryNodePresenceAtContext(ctx context.Context, now time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot := currentSnapshot.Load()
	if snapshot == nil || snapshot.view.ActivationStage != model.RoutingDeploymentStageCanary {
		return nil
	}
	identity, err := canaryNodePresenceIdentityFromView(snapshot.view)
	if err != nil {
		return err
	}
	sequence, err := nextCanaryNodePresenceSequence()
	if err != nil {
		return err
	}
	checkpoint, err := newCanaryNodePresenceCheckpoint(NodeEpochID(), identity, sequence, now)
	if err != nil {
		return err
	}
	_, err = model.UpsertRoutingRuntimeCheckpointContext(ctx, checkpoint)
	return err
}

func canaryNodePresenceIdentityFromView(view SnapshotView) (canaryNodePresenceIdentity, error) {
	if view.Revision == 0 || view.Revision > math.MaxInt64 || !validShadowHash(view.PolicyHash) ||
		view.ActivationID <= 0 || view.ActivationStage != model.RoutingDeploymentStageCanary ||
		view.TrafficBasisPoints < model.RoutingPolicyCanaryMinBasisPoints ||
		view.TrafficBasisPoints > model.RoutingPolicyCanaryMaxBasisPoints {
		return canaryNodePresenceIdentity{}, ErrCanaryNodePresenceInvalid
	}
	return canaryNodePresenceIdentity{
		PolicyRevision:     int64(view.Revision),
		PolicyHash:         view.PolicyHash,
		ActivationID:       view.ActivationID,
		TrafficBasisPoints: view.TrafficBasisPoints,
	}, nil
}

func canaryNodePresenceIdentityFromTarget(target canaryEvaluationTarget) (canaryNodePresenceIdentity, error) {
	identity := canaryNodePresenceIdentity{
		PolicyRevision:     target.PolicyRevision,
		PolicyHash:         target.PolicyHash,
		ActivationID:       target.ActivationID,
		TrafficBasisPoints: target.TrafficBasisPoints,
	}
	if !validCanaryNodePresenceIdentity(identity) {
		return canaryNodePresenceIdentity{}, ErrCanaryNodePresenceInvalid
	}
	return identity, nil
}

func newCanaryNodePresenceCheckpoint(
	nodeID string,
	identity canaryNodePresenceIdentity,
	sequence int64,
	now time.Time,
) (model.RoutingRuntimeCheckpoint, error) {
	if nodeID == "" || !validCanaryNodePresenceIdentity(identity) || sequence <= 0 || now.IsZero() {
		return model.RoutingRuntimeCheckpoint{}, ErrCanaryNodePresenceInvalid
	}
	nowUnix := now.Unix()
	ttlSeconds := int64(canaryNodePresenceTTL / time.Second)
	if nowUnix <= 0 || ttlSeconds <= 0 || nowUnix > math.MaxInt64-ttlSeconds {
		return model.RoutingRuntimeCheckpoint{}, ErrCanaryNodePresenceInvalid
	}
	scope := canaryNodePresenceScope(identity)
	checkpoint, err := model.NewRoutingRuntimeCheckpoint(
		nodeID,
		RoutingCanaryNodePresenceCheckpointKind,
		scope,
		identity.PolicyRevision,
		sequence,
		canaryNodePresencePayload{
			SchemaVersion:      canaryNodePresenceSchemaVersion,
			PolicyHash:         identity.PolicyHash,
			ActivationID:       identity.ActivationID,
			ActivationStage:    model.RoutingDeploymentStageCanary,
			TrafficBasisPoints: identity.TrafficBasisPoints,
		},
		nowUnix,
		nowUnix+ttlSeconds,
	)
	if err != nil {
		return model.RoutingRuntimeCheckpoint{}, err
	}
	checkpoint.CreatedTime = nowUnix
	checkpoint.UpdatedTime = nowUnix
	return checkpoint, nil
}

func loadCanaryNodePresenceCheckpointsContext(
	ctx context.Context,
	target canaryEvaluationTarget,
	now time.Time,
) ([]model.RoutingRuntimeCheckpoint, []model.RoutingRuntimeCheckpoint, error) {
	identity, err := canaryNodePresenceIdentityFromTarget(target)
	if err != nil || now.IsZero() {
		return nil, nil, ErrCanaryNodePresenceInvalid
	}
	checkpoints, err := listCanaryCheckpointsContext(
		ctx,
		RoutingCanaryNodePresenceCheckpointKind,
		canaryNodePresenceScope(identity),
		now.Unix(),
	)
	if err != nil {
		return nil, nil, err
	}
	matching := make([]model.RoutingRuntimeCheckpoint, 0, len(checkpoints))
	invalid := make([]model.RoutingRuntimeCheckpoint, 0)
	for index := range checkpoints {
		checkpoint := checkpoints[index]
		if checkpoint.PolicyRevision != identity.PolicyRevision || checkpoint.ObservedTime <= 0 ||
			checkpoint.ObservedTime > now.Unix() || checkpoint.CreatedTime <= 0 ||
			checkpoint.CreatedTime > checkpoint.ObservedTime {
			invalid = append(invalid, checkpoint)
			continue
		}
		var payload canaryNodePresencePayload
		if checkpoint.DecodePayload(&payload) != nil || payload.SchemaVersion != canaryNodePresenceSchemaVersion ||
			payload.PolicyHash != identity.PolicyHash || payload.ActivationID != identity.ActivationID ||
			payload.ActivationStage != model.RoutingDeploymentStageCanary ||
			payload.TrafficBasisPoints != identity.TrafficBasisPoints {
			invalid = append(invalid, checkpoint)
			continue
		}
		matching = append(matching, checkpoint)
	}
	return matching, invalid, nil
}

func canaryNodePresenceScope(identity canaryNodePresenceIdentity) string {
	return fmt.Sprintf(
		"v1/revision/%d/activation/%d/traffic/%d",
		identity.PolicyRevision,
		identity.ActivationID,
		identity.TrafficBasisPoints,
	)
}

func validCanaryNodePresenceIdentity(identity canaryNodePresenceIdentity) bool {
	return identity.PolicyRevision > 0 && validShadowHash(identity.PolicyHash) && identity.ActivationID > 0 &&
		identity.TrafficBasisPoints >= model.RoutingPolicyCanaryMinBasisPoints &&
		identity.TrafficBasisPoints <= model.RoutingPolicyCanaryMaxBasisPoints
}

func nextCanaryNodePresenceSequence() (int64, error) {
	for {
		current := canaryNodePresenceSequence.Load()
		if current == math.MaxInt64 {
			return 0, ErrCanaryNodePresenceInvalid
		}
		if canaryNodePresenceSequence.CompareAndSwap(current, current+1) {
			return current + 1, nil
		}
	}
}
