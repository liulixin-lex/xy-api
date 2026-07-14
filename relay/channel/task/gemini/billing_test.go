package gemini

import (
	"math"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveVeoDurationNormalizesMetadataTypesAndPriority(t *testing.T) {
	for _, raw := range []any{8, float64(8), "8"} {
		metadata := map[string]any{"durationSeconds": raw}
		duration, err := ResolveVeoDuration(metadata, 10, "6")
		require.NoError(t, err)
		assert.Equal(t, 8, duration)
		assert.Equal(t, 8, metadata["durationSeconds"])
	}

	duration, err := ResolveVeoDuration(nil, 10, "6")
	require.NoError(t, err)
	assert.Equal(t, 10, duration)
	duration, err = ResolveVeoDuration(nil, 0, "6")
	require.NoError(t, err)
	assert.Equal(t, 6, duration)
	duration, err = ResolveVeoDuration(nil, 0, "")
	require.NoError(t, err)
	assert.Equal(t, 8, duration)
}

func TestResolveVeoDurationRejectsUnboundedOrNonIntegralMetadata(t *testing.T) {
	for _, raw := range []any{
		0, relaycommon.MaxTaskDurationSeconds + 1, 8.5, "many", math.NaN(), math.Inf(1),
	} {
		_, err := ResolveVeoDuration(map[string]any{"durationSeconds": raw}, 0, "")
		require.Error(t, err)
	}
}
