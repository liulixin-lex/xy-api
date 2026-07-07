package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompletionRatioInfoUnlocksGptFiveDotFiveAndLater(t *testing.T) {
	originalRatios := completionRatioMap.ReadAll()
	completionRatioMap.Clear()
	completionRatioMap.AddAll(defaultCompletionRatio)
	t.Cleanup(func() {
		completionRatioMap.Clear()
		completionRatioMap.AddAll(originalRatios)
	})

	completionRatioMap.Set("gpt-5.5", 11)

	info := GetCompletionRatioInfo("gpt-5.5")
	require.False(t, info.Locked)
	assert.Equal(t, 11.0, info.Ratio)

	info = GetCompletionRatioInfo("gpt-5.6-preview")
	require.False(t, info.Locked)
	assert.Equal(t, 6.0, info.Ratio)
}

func TestCompletionRatioInfoKeepsBareGptFiveLocked(t *testing.T) {
	originalRatios := completionRatioMap.ReadAll()
	completionRatioMap.Clear()
	completionRatioMap.AddAll(defaultCompletionRatio)
	t.Cleanup(func() {
		completionRatioMap.Clear()
		completionRatioMap.AddAll(originalRatios)
	})

	completionRatioMap.Set("gpt-5", 1)

	info := GetCompletionRatioInfo("gpt-5")
	require.True(t, info.Locked)
	assert.Equal(t, 8.0, info.Ratio)
}
