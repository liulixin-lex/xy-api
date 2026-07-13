package channelrouting

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"

	"github.com/QuantumNous/new-api/model"
)

const (
	canaryBucketCount   = 10_000
	canaryBucketDomain  = "new-api/channel-routing/canary-bucket/v1\x00"
	canaryRolloutDomain = "new-api/channel-routing/canary-rollout/v1\x00"
)

var ErrCanaryGateInvalid = errors.New("invalid channel routing canary gate")

type RolloutKey string

type CanaryGate struct {
	PoolID             int        `json:"pool_id"`
	ActivationID       int64      `json:"activation_id"`
	PolicyRevision     uint64     `json:"policy_revision"`
	TrafficBasisPoints int        `json:"traffic_basis_points"`
	Bucket             int        `json:"bucket"`
	InCanary           bool       `json:"in_canary"`
	RolloutKey         RolloutKey `json:"rollout_key"`
}

func EvaluateCanaryGate(
	poolID int,
	activationID int64,
	policyRevision uint64,
	requestID string,
	trafficBasisPoints int,
) (CanaryGate, error) {
	if poolID <= 0 || activationID <= 0 || policyRevision == 0 || requestID == "" ||
		trafficBasisPoints < model.RoutingPolicyCanaryMinBasisPoints ||
		trafficBasisPoints > model.RoutingPolicyCanaryMaxBasisPoints {
		return CanaryGate{}, ErrCanaryGateInvalid
	}

	var encoded [8]byte
	bucketHash := sha256.New()
	_, _ = bucketHash.Write([]byte(canaryBucketDomain))
	binary.BigEndian.PutUint64(encoded[:], uint64(poolID))
	_, _ = bucketHash.Write(encoded[:])
	_, _ = bucketHash.Write([]byte(requestID))
	bucketSum := bucketHash.Sum(nil)
	bucket := int(binary.BigEndian.Uint64(bucketSum[:8]) % canaryBucketCount)

	rolloutKey, err := CanaryRolloutKey(poolID, activationID, policyRevision, trafficBasisPoints)
	if err != nil {
		return CanaryGate{}, err
	}

	return CanaryGate{
		PoolID:             poolID,
		ActivationID:       activationID,
		PolicyRevision:     policyRevision,
		TrafficBasisPoints: trafficBasisPoints,
		Bucket:             bucket,
		InCanary:           bucket < trafficBasisPoints,
		RolloutKey:         rolloutKey,
	}, nil
}

func CanaryRolloutKey(
	poolID int,
	activationID int64,
	policyRevision uint64,
	trafficBasisPoints int,
) (RolloutKey, error) {
	if poolID <= 0 || activationID <= 0 || policyRevision == 0 ||
		trafficBasisPoints < model.RoutingPolicyCanaryMinBasisPoints ||
		trafficBasisPoints > model.RoutingPolicyCanaryMaxBasisPoints {
		return "", ErrCanaryGateInvalid
	}

	var encoded [8]byte
	rolloutHash := sha256.New()
	_, _ = rolloutHash.Write([]byte(canaryRolloutDomain))
	binary.BigEndian.PutUint64(encoded[:], uint64(poolID))
	_, _ = rolloutHash.Write(encoded[:])
	binary.BigEndian.PutUint64(encoded[:], uint64(activationID))
	_, _ = rolloutHash.Write(encoded[:])
	binary.BigEndian.PutUint64(encoded[:], policyRevision)
	_, _ = rolloutHash.Write(encoded[:])
	binary.BigEndian.PutUint64(encoded[:], uint64(trafficBasisPoints))
	_, _ = rolloutHash.Write(encoded[:])
	rolloutSum := rolloutHash.Sum(nil)
	return RolloutKey(hex.EncodeToString(rolloutSum)), nil
}
