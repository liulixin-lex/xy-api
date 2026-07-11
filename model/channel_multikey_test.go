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

func TestRemapMultiKeyStateMatchesUniqueRawKeys(t *testing.T) {
	info := ChannelInfo{
		MultiKeySize: 2,
		MultiKeyStatusList: map[int]int{
			0: common.ChannelStatusAutoDisabled,
			1: common.ChannelStatusManuallyDisabled,
		},
		MultiKeyDisabledReason: map[int]string{
			0: "automatic failure",
			1: "manual operation",
		},
		MultiKeyDisabledTime: map[int]int64{
			0: 101,
			1: 202,
		},
		MultiKeyPollingIndex: 2,
	}

	info.RemapMultiKeyState(
		[]string{"raw-a", "raw-b"},
		[]string{"raw-b", "raw-new", "raw-a"},
	)

	assert.Equal(t, 3, info.MultiKeySize)
	assert.Equal(t, map[int]int{
		0: common.ChannelStatusManuallyDisabled,
		2: common.ChannelStatusAutoDisabled,
	}, info.MultiKeyStatusList)
	assert.Equal(t, map[int]string{
		0: "manual operation",
		2: "automatic failure",
	}, info.MultiKeyDisabledReason)
	assert.Equal(t, map[int]int64{
		0: 202,
		2: 101,
	}, info.MultiKeyDisabledTime)
	assert.Zero(t, info.MultiKeyPollingIndex)
}

func TestRemapMultiKeyStateClearsAmbiguousDuplicateKeys(t *testing.T) {
	info := ChannelInfo{
		MultiKeyStatusList: map[int]int{
			0: common.ChannelStatusAutoDisabled,
			1: common.ChannelStatusManuallyDisabled,
			2: common.ChannelStatusManuallyDisabled,
		},
		MultiKeyDisabledReason: map[int]string{
			0: "duplicate automatic failure",
			1: "duplicate manual operation",
			2: "unique manual operation",
		},
		MultiKeyDisabledTime: map[int]int64{
			0: 101,
			1: 202,
			2: 303,
		},
		MultiKeyPollingIndex: 2,
	}

	info.RemapMultiKeyState(
		[]string{"dup", "dup", "unique"},
		[]string{"unique", "dup", "dup"},
	)

	assert.Equal(t, 3, info.MultiKeySize)
	assert.Equal(t, map[int]int{0: common.ChannelStatusManuallyDisabled}, info.MultiKeyStatusList)
	assert.Equal(t, map[int]string{0: "unique manual operation"}, info.MultiKeyDisabledReason)
	assert.Equal(t, map[int]int64{0: 303}, info.MultiKeyDisabledTime)
	assert.Zero(t, info.MultiKeyPollingIndex)
}

func TestEnablingMultiKeyClearsOperationalMetadata(t *testing.T) {
	channel := &Channel{
		Key:    "raw-a\nraw-b",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:             true,
			MultiKeyStatusList:     map[int]int{1: common.ChannelStatusManuallyDisabled},
			MultiKeyDisabledReason: map[int]string{1: "manual operation"},
			MultiKeyDisabledTime:   map[int]int64{1: 123},
		},
	}

	handlerMultiKeyUpdate(channel, "raw-b", common.ChannelStatusEnabled, "")

	assert.NotContains(t, channel.ChannelInfo.MultiKeyStatusList, 1)
	assert.NotContains(t, channel.ChannelInfo.MultiKeyDisabledReason, 1)
	assert.NotContains(t, channel.ChannelInfo.MultiKeyDisabledTime, 1)
}

func TestChannelUpdateCleansAllOutOfRangeMultiKeyState(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))

	channel := &Channel{
		Id:     9201,
		Name:   "multi-key cleanup",
		Key:    "raw-a\nraw-b\nraw-c",
		Status: common.ChannelStatusEnabled,
		Models: "gpt-test",
		Group:  "default",
		ChannelInfo: ChannelInfo{
			IsMultiKey: true,
		},
	}
	require.NoError(t, db.Create(channel).Error)

	channel.Key = "raw-a\nraw-b"
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{
		-1: common.ChannelStatusAutoDisabled,
		0:  common.ChannelStatusManuallyDisabled,
		2:  common.ChannelStatusAutoDisabled,
	}
	channel.ChannelInfo.MultiKeyDisabledReason = map[int]string{
		-1: "negative",
		0:  "kept",
		2:  "overflow",
	}
	channel.ChannelInfo.MultiKeyDisabledTime = map[int]int64{
		-1: 11,
		0:  22,
		2:  33,
	}
	channel.ChannelInfo.MultiKeyPollingIndex = -1

	require.NoError(t, channel.Update())

	assert.Equal(t, 2, channel.ChannelInfo.MultiKeySize)
	assert.Equal(t, map[int]int{0: common.ChannelStatusManuallyDisabled}, channel.ChannelInfo.MultiKeyStatusList)
	assert.Equal(t, map[int]string{0: "kept"}, channel.ChannelInfo.MultiKeyDisabledReason)
	assert.Equal(t, map[int]int64{0: 22}, channel.ChannelInfo.MultiKeyDisabledTime)
	assert.Zero(t, channel.ChannelInfo.MultiKeyPollingIndex)
}
