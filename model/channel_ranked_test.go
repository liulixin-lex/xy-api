package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetRankedSatisfiedChannelsReturnsAllPriorityCandidates(t *testing.T) {
	previousMemoryCache := common.MemoryCacheEnabled
	previousGroupMap := group2model2channels
	previousChannels := channelsIDM
	common.MemoryCacheEnabled = true
	group2model2channels = map[string]map[string][]int{
		"default": {
			"gpt-test": {1, 2, 3},
		},
	}
	high := int64(10)
	low := int64(1)
	weight := uint(100)
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Name: "high-a", Priority: &high, Weight: &weight},
		2: {Id: 2, Name: "low", Priority: &low, Weight: &weight},
		3: {Id: 3, Name: "high-b", Priority: &high, Weight: &weight},
	}
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		group2model2channels = previousGroupMap
		channelsIDM = previousChannels
	})

	channels, err := GetRankedSatisfiedChannels("default", "gpt-test", "/v1/chat/completions")

	require.NoError(t, err)
	require.Len(t, channels, 3)
	assert.Equal(t, []int{1, 2, 3}, []int{channels[0].Id, channels[1].Id, channels[2].Id})
}

func TestGetRankedSatisfiedChannelsUsesNormalizedModelFallback(t *testing.T) {
	previousMemoryCache := common.MemoryCacheEnabled
	previousGroupMap := group2model2channels
	previousChannels := channelsIDM
	common.MemoryCacheEnabled = true
	group2model2channels = map[string]map[string][]int{
		"default": {
			"gpt-4-gizmo-*": {4},
		},
	}
	weight := uint(100)
	channelsIDM = map[int]*Channel{4: {Id: 4, Name: "normalized", Weight: &weight}}
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		group2model2channels = previousGroupMap
		channelsIDM = previousChannels
	})

	channels, err := GetRankedSatisfiedChannels("default", "gpt-4-gizmo-preview", "")

	require.NoError(t, err)
	require.Len(t, channels, 1)
	assert.Equal(t, 4, channels[0].Id)
}

func TestAutomaticChannelSelectionExcludesZeroWeight(t *testing.T) {
	previousMemoryCache := common.MemoryCacheEnabled
	previousGroupMap := group2model2channels
	previousChannels := channelsIDM
	common.MemoryCacheEnabled = true
	priority := int64(10)
	pausedWeight := uint(0)
	activeWeight := uint(100)
	group2model2channels = map[string]map[string][]int{
		"default": {"gpt-test": {1, 2}},
	}
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Name: "paused", Priority: &priority, Weight: &pausedWeight},
		2: {Id: 2, Name: "active", Priority: &priority, Weight: &activeWeight},
	}
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		group2model2channels = previousGroupMap
		channelsIDM = previousChannels
	})

	for range 20 {
		channel, err := GetRandomSatisfiedChannel("default", "gpt-test", 0, "")
		require.NoError(t, err)
		require.NotNil(t, channel)
		assert.Equal(t, 2, channel.Id)
	}

	ranked, err := GetRankedSatisfiedChannels("default", "gpt-test", "")
	require.NoError(t, err)
	require.Len(t, ranked, 1)
	assert.Equal(t, 2, ranked[0].Id)
}

func TestAutomaticChannelSelectionReturnsNoCandidateWhenAllWeightsAreZero(t *testing.T) {
	previousMemoryCache := common.MemoryCacheEnabled
	previousGroupMap := group2model2channels
	previousChannels := channelsIDM
	common.MemoryCacheEnabled = true
	priority := int64(10)
	pausedWeight := uint(0)
	group2model2channels = map[string]map[string][]int{
		"default": {"gpt-test": {1}},
	}
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Name: "paused", Priority: &priority, Weight: &pausedWeight},
	}
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		group2model2channels = previousGroupMap
		channelsIDM = previousChannels
	})

	channel, err := GetRandomSatisfiedChannel("default", "gpt-test", 0, "")
	require.NoError(t, err)
	assert.Nil(t, channel)

	ranked, err := GetRankedSatisfiedChannels("default", "gpt-test", "")
	require.NoError(t, err)
	assert.Empty(t, ranked)
}

func TestGetRandomSatisfiedChannelWithEligibilityFiltersBeforePriority(t *testing.T) {
	previousMemoryCache := common.MemoryCacheEnabled
	previousGroupMap := group2model2channels
	previousChannels := channelsIDM
	common.MemoryCacheEnabled = true
	high := int64(100)
	low := int64(10)
	weight := uint(100)
	group2model2channels = map[string]map[string][]int{
		"default": {"claude-test": {1, 2}},
	}
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Name: "restricted-high", Priority: &high, Weight: &weight},
		2: {Id: 2, Name: "ordinary-low", Priority: &low, Weight: &weight},
	}
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		group2model2channels = previousGroupMap
		channelsIDM = previousChannels
	})

	channel, err := GetRandomSatisfiedChannelWithEligibility(
		"default",
		"claude-test",
		0,
		"/v1/messages",
		func(channel *Channel) bool { return channel.Id != 1 },
	)

	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 2, channel.Id)
}
