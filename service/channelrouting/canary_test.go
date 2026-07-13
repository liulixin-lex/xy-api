package channelrouting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluateCanaryGateKeepsCohortStableAcrossRollouts(t *testing.T) {
	onePercent, err := EvaluateCanaryGate(17, 101, 7, "request-stable", 100)
	require.NoError(t, err)
	fivePercent, err := EvaluateCanaryGate(17, 102, 8, "request-stable", 500)
	require.NoError(t, err)

	assert.Equal(t, onePercent.Bucket, fivePercent.Bucket)
	assert.Equal(t, onePercent.Bucket < 100, onePercent.InCanary)
	assert.Equal(t, fivePercent.Bucket < 500, fivePercent.InCanary)
	if onePercent.InCanary {
		assert.True(t, fivePercent.InCanary, "the one percent cohort must remain inside the five percent cohort")
	}
	assert.NotEqual(t, onePercent.RolloutKey, fivePercent.RolloutKey)
	assert.Equal(t, int64(101), onePercent.ActivationID)
	assert.Equal(t, uint64(7), onePercent.PolicyRevision)
	assert.Equal(t, 100, onePercent.TrafficBasisPoints)
}

func TestEvaluateCanaryGateRolloutKeyDoesNotContainRequestIdentity(t *testing.T) {
	first, err := EvaluateCanaryGate(17, 101, 7, "request-a", 250)
	require.NoError(t, err)
	second, err := EvaluateCanaryGate(17, 101, 7, "request-b", 250)
	require.NoError(t, err)

	assert.Equal(t, first.RolloutKey, second.RolloutKey)
	assert.Len(t, string(first.RolloutKey), 64)
	assert.NotEqual(t, first.Bucket, second.Bucket)
}

func TestEvaluateCanaryGateUsesOnlyPoolAndRequestForBucket(t *testing.T) {
	tests := []struct {
		requestID string
	}{
		{requestID: "request-001"},
		{requestID: "request-017"},
		{requestID: "request-113"},
		{requestID: "request-997"},
	}
	for _, test := range tests {
		t.Run(test.requestID, func(t *testing.T) {
			first, err := EvaluateCanaryGate(29, 401, 11, test.requestID, 100)
			require.NoError(t, err)
			changedRollout, err := EvaluateCanaryGate(29, 999, 88, test.requestID, 500)
			require.NoError(t, err)
			otherPool, err := EvaluateCanaryGate(30, 401, 11, test.requestID, 100)
			require.NoError(t, err)

			assert.Equal(t, first.Bucket, changedRollout.Bucket)
			assert.NotEqual(t, first.Bucket, otherPool.Bucket)
			if first.InCanary {
				assert.True(t, changedRollout.InCanary)
			}
		})
	}
}

func TestEvaluateCanaryGateHasStableGoldenBucketsAndNestedCohorts(t *testing.T) {
	onePercent, err := EvaluateCanaryGate(29, 401, 11, "cohort-0005", 100)
	require.NoError(t, err)
	fivePercent, err := EvaluateCanaryGate(29, 401, 11, "cohort-0005", 500)
	require.NoError(t, err)
	fivePercentOnly, err := EvaluateCanaryGate(29, 401, 11, "cohort-0027", 500)
	require.NoError(t, err)

	assert.Equal(t, 6, onePercent.Bucket)
	assert.Equal(t, onePercent.Bucket, fivePercent.Bucket)
	assert.True(t, onePercent.InCanary)
	assert.True(t, fivePercent.InCanary)
	assert.Equal(t, 221, fivePercentOnly.Bucket)
	assert.True(t, fivePercentOnly.InCanary)
	assert.Equal(t, RolloutKey("152de9f8334fb4ae5eeecca6fbe520dce1368e1526a0997a99175c949ccf0b82"), onePercent.RolloutKey)
}

func TestEvaluateCanaryGateRejectsInvalidIdentityAndTraffic(t *testing.T) {
	tests := []struct {
		name         string
		poolID       int
		activationID int64
		revision     uint64
		requestID    string
		basis        int
	}{
		{name: "pool", activationID: 1, revision: 1, requestID: "request", basis: 100},
		{name: "activation", poolID: 1, revision: 1, requestID: "request", basis: 100},
		{name: "revision", poolID: 1, activationID: 1, requestID: "request", basis: 100},
		{name: "request", poolID: 1, activationID: 1, revision: 1, basis: 100},
		{name: "below one percent", poolID: 1, activationID: 1, revision: 1, requestID: "request", basis: 99},
		{name: "above five percent", poolID: 1, activationID: 1, revision: 1, requestID: "request", basis: 501},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := EvaluateCanaryGate(test.poolID, test.activationID, test.revision, test.requestID, test.basis)
			assert.ErrorIs(t, err, ErrCanaryGateInvalid)
		})
	}
}
