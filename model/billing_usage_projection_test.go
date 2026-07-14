package model

import (
	"math"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestApplyBillingUsageProjectionUpdatesUserAndChannel(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Create(&User{
		Id: 9801, Username: "projection-user", AffCode: "projection-aff", Status: common.UserStatusEnabled,
		UsedQuota: 10, RequestCount: 2,
	}).Error)
	require.NoError(t, DB.Create(&Channel{
		Id: 9801, Name: "projection-channel", Key: "projection-key", Status: common.ChannelStatusEnabled, UsedQuota: 20,
	}).Error)

	var result billingUsageProjectionResult
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = applyBillingUsageProjectionTx(tx, billingUsageProjectionSpec{
			OperationKey: "projection:normal", UserID: 9801, ChannelID: 9801, QuotaDelta: 30, RequestDelta: 1,
		})
		return err
	}))
	assert.Equal(t, BillingUsageOutcomeApplied, result.UserOutcome)
	assert.Equal(t, BillingUsageOutcomeApplied, result.ChannelOutcome)
	var user User
	var channel Channel
	require.NoError(t, DB.First(&user, 9801).Error)
	require.NoError(t, DB.First(&channel, 9801).Error)
	assert.Equal(t, 40, user.UsedQuota)
	assert.Equal(t, 3, user.RequestCount)
	assert.Equal(t, int64(50), channel.UsedQuota)
}

func TestApplyBillingUsageProjectionSaturatesUserAndAuditsChannelOverflow(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Create(&User{
		Id: 9802, Username: "projection-saturated", AffCode: "projection-saturated-aff", Status: common.UserStatusEnabled,
		UsedQuota: math.MaxInt32 - 2, RequestCount: math.MaxInt32,
	}).Error)
	require.NoError(t, DB.Create(&Channel{
		Id: 9802, Name: "projection-overflow", Key: "projection-overflow-key", Status: common.ChannelStatusEnabled,
		UsedQuota: math.MaxInt64 - 1,
	}).Error)

	var result billingUsageProjectionResult
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = applyBillingUsageProjectionTx(tx, billingUsageProjectionSpec{
			OperationKey: "projection:saturated", UserID: 9802, ChannelID: 9802, QuotaDelta: 10, RequestDelta: 1,
		})
		return err
	}))
	assert.Equal(t, BillingUsageOutcomeSaturated, result.UserOutcome)
	assert.Equal(t, BillingUsageOutcomeSkippedOverflow, result.ChannelOutcome)
	var user User
	var channel Channel
	require.NoError(t, DB.First(&user, 9802).Error)
	require.NoError(t, DB.First(&channel, 9802).Error)
	assert.Equal(t, math.MaxInt32, user.UsedQuota)
	assert.Equal(t, math.MaxInt32, user.RequestCount)
	assert.Equal(t, int64(math.MaxInt64-1), channel.UsedQuota)
}

func TestApplyBillingUsageProjectionSkipsDeletedDimensions(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Create(&User{
		Id: 9803, Username: "projection-deleted", AffCode: "projection-deleted-aff", Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, DB.Delete(&User{}, 9803).Error)

	var result billingUsageProjectionResult
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = applyBillingUsageProjectionTx(tx, billingUsageProjectionSpec{
			OperationKey: "projection:deleted", UserID: 9803, ChannelID: 999999, QuotaDelta: 10, RequestDelta: 1,
		})
		return err
	}))
	assert.Equal(t, BillingUsageOutcomeSkippedDeleted, result.UserOutcome)
	assert.Equal(t, BillingUsageOutcomeSkippedMissing, result.ChannelOutcome)
}

func TestApplyBillingUsageProjectionRepairsCorruptUserCountersWithoutOverflow(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("the corrupt 64-bit counter fixture requires a 64-bit int")
	}
	truncateTables(t)
	require.NoError(t, DB.Create(&User{
		Id: 9804, Username: "projection-corrupt", AffCode: "projection-corrupt-aff", Status: common.UserStatusEnabled,
		UsedQuota: int(math.MaxInt64), RequestCount: -1,
	}).Error)

	var result billingUsageProjectionResult
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = applyBillingUsageProjectionTx(tx, billingUsageProjectionSpec{
			OperationKey: "projection:corrupt", UserID: 9804, QuotaDelta: 10, RequestDelta: 1,
		})
		return err
	}))
	assert.Equal(t, BillingUsageOutcomeSaturated, result.UserOutcome)
	assert.Equal(t, BillingUsageOutcomeNotRequired, result.ChannelOutcome)
	var user User
	require.NoError(t, DB.First(&user, 9804).Error)
	assert.Equal(t, math.MaxInt32, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount, "the current request must not be lost while repairing a negative counter")
}

func TestApplyBillingUsageProjectionRepairsNegativeChannelCounter(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Create(&User{
		Id: 9805, Username: "projection-channel-repair", AffCode: "projection-channel-repair-aff",
		Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, DB.Create(&Channel{
		Id: 9805, Name: "projection-channel-repair", Key: "projection-channel-repair-key",
		Status: common.ChannelStatusEnabled, UsedQuota: -100,
	}).Error)

	var result billingUsageProjectionResult
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = applyBillingUsageProjectionTx(tx, billingUsageProjectionSpec{
			OperationKey: "projection:channel-repair", UserID: 9805, ChannelID: 9805, QuotaDelta: 10,
		})
		return err
	}))
	assert.Equal(t, BillingUsageOutcomeApplied, result.UserOutcome)
	assert.Equal(t, BillingUsageOutcomeSaturated, result.ChannelOutcome)
	var channel Channel
	require.NoError(t, DB.First(&channel, 9805).Error)
	assert.Equal(t, int64(10), channel.UsedQuota)
}
