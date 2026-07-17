package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingManualCostProfilePreservesExplicitBusinessQuantities(t *testing.T) {
	zero := 0.0
	zeroTokens := int64(0)
	profile, sources := (channelRoutingManualCostProfile{
		InputTokens: &zeroTokens, CacheWriteTokens: &zeroTokens,
		ImageInputTokens: &zeroTokens, ImageUnits: &zero, AudioSeconds: &zero,
		VideoSeconds: &zero, TaskUnits: &zero, MaxAttempts: 1,
	}).routingCostProfile()

	assert.True(t, profile.InputTokensKnown)
	assert.True(t, profile.CacheWriteTokensKnown)
	assert.False(t, profile.CacheWriteOneHourTokensKnown)
	assert.True(t, profile.ImageInputTokensKnown)
	assert.False(t, profile.ImageOutputTokensKnown)
	assert.True(t, profile.ImageUnitsKnown)
	assert.False(t, profile.AudioInputTokensKnown)
	assert.False(t, profile.AudioOutputTokensKnown)
	assert.True(t, profile.AudioDurationKnown)
	assert.True(t, profile.VideoDurationKnown)
	assert.True(t, profile.TaskUnitsKnown)
	assert.Zero(t, profile.AudioSeconds)
	assert.Zero(t, profile.VideoSeconds)
	assert.Zero(t, profile.TaskUnits)
	assert.False(t, profile.UncataloguedSurchargePossible)
	assert.Equal(t, "manual", sources["cache_write_tokens"])
	assert.NotContains(t, sources, "cache_write_1h_tokens")
	assert.Equal(t, "manual", sources["image_input_tokens"])
	assert.NotContains(t, sources, "image_output_tokens")
	assert.Equal(t, "manual", sources["image_units"])
	assert.Equal(t, "manual", sources["audio_seconds"])
	assert.Equal(t, "manual", sources["video_seconds"])
	assert.Equal(t, "manual", sources["task_units"])

	videoSeconds := 8.0
	profile, sources = (channelRoutingManualCostProfile{
		VideoSeconds: &videoSeconds,
	}).routingCostProfile()
	require.Equal(t, 8.0, profile.VideoSeconds)
	assert.True(t, profile.VideoDurationKnown)
	assert.True(t, profile.UncataloguedSurchargePossible)
	assert.Equal(t, "manual", sources["video_seconds"])

	profile, sources = (channelRoutingManualCostProfile{
		CacheWriteOneHourTokens: &zeroTokens,
		AudioOutputTokens:       &zeroTokens,
	}).routingCostProfile()
	assert.False(t, profile.CacheWriteTokensKnown)
	assert.True(t, profile.CacheWriteOneHourTokensKnown)
	assert.False(t, profile.AudioInputTokensKnown)
	assert.True(t, profile.AudioOutputTokensKnown)
	assert.NotContains(t, sources, "cache_write_tokens")
	assert.Equal(t, "manual", sources["cache_write_1h_tokens"])
	assert.NotContains(t, sources, "audio_input_tokens")
	assert.Equal(t, "manual", sources["audio_output_tokens"])
}
