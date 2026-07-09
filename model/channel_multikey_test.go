package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetNextEnabledKeyFilteredSkipsDisallowedIndexes(t *testing.T) {
	channel := &Channel{
		Id:  9101,
		Key: "key-0\nkey-1\nkey-2",
		ChannelInfo: ChannelInfo{
			IsMultiKey:         true,
			MultiKeyMode:       constant.MultiKeyModeRandom,
			MultiKeyStatusList: map[int]int{0: common.ChannelStatusEnabled, 1: common.ChannelStatusEnabled, 2: common.ChannelStatusEnabled},
		},
	}

	key, index, err := channel.GetNextEnabledKeyFiltered(func(index int) bool {
		return index == 1
	})

	require.Nil(t, err)
	assert.Equal(t, "key-1", key)
	assert.Equal(t, 1, index)
}

func TestGetNextEnabledKeyFilteredReturnsErrorWhenAllIndexesDisallowed(t *testing.T) {
	channel := &Channel{
		Id:  9102,
		Key: "key-0\nkey-1",
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeyMode: constant.MultiKeyModeRandom,
		},
	}

	key, index, err := channel.GetNextEnabledKeyFiltered(func(index int) bool {
		return false
	})

	require.NotNil(t, err)
	assert.Empty(t, key)
	assert.Zero(t, index)
}

func TestGetNextEnabledKeyKeepsExistingBehaviorWithoutFilter(t *testing.T) {
	channel := &Channel{
		Id:  9103,
		Key: "key-0\nkey-1",
		ChannelInfo: ChannelInfo{
			IsMultiKey:         true,
			MultiKeyMode:       constant.MultiKeyModeRandom,
			MultiKeyStatusList: map[int]int{0: common.ChannelStatusManuallyDisabled, 1: common.ChannelStatusEnabled},
		},
	}

	key, index, err := channel.GetNextEnabledKey()

	require.Nil(t, err)
	assert.Equal(t, "key-1", key)
	assert.Equal(t, 1, index)
}
