package model

import (
	"errors"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var paymentOptionSyncTestKeys = []string{
	PaymentConfigurationVersionOptionKey,
	"EpayCurrency",
	"StripeApiSecret",
}

func preparePaymentOptionSyncTest(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&Option{}))

	var previousRows []Option
	require.NoError(t, DB.Where(fmt.Sprintf("%s IN ?", optionKeyColumn()), paymentOptionSyncTestKeys).Find(&previousRows).Error)
	require.NoError(t, DB.Where(fmt.Sprintf("%s IN ?", optionKeyColumn()), paymentOptionSyncTestKeys).Delete(&Option{}).Error)

	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	previousValues := make(map[string]string, len(paymentOptionSyncTestKeys))
	previouslyExisted := make(map[string]bool, len(paymentOptionSyncTestKeys))
	for _, key := range paymentOptionSyncTestKeys {
		previousValues[key], previouslyExisted[key] = common.OptionMap[key]
	}
	common.OptionMap[PaymentConfigurationVersionOptionKey] = "1"
	common.OptionMap["EpayCurrency"] = "LOCAL"
	common.OptionMap["StripeApiSecret"] = "local-secret"
	common.OptionMapRWMutex.Unlock()

	previousEpayCurrency := operation_setting.EpayCurrency
	previousStripeAPISecret := setting.StripeApiSecret
	operation_setting.EpayCurrency = "LOCAL"
	setting.StripeApiSecret = "local-secret"

	t.Cleanup(func() {
		DB.Where(fmt.Sprintf("%s IN ?", optionKeyColumn()), paymentOptionSyncTestKeys).Delete(&Option{})
		if len(previousRows) > 0 {
			DB.Create(&previousRows)
		}
		common.OptionMapRWMutex.Lock()
		for _, key := range paymentOptionSyncTestKeys {
			if previouslyExisted[key] {
				common.OptionMap[key] = previousValues[key]
			} else {
				delete(common.OptionMap, key)
			}
		}
		common.OptionMapRWMutex.Unlock()
		operation_setting.EpayCurrency = previousEpayCurrency
		setting.StripeApiSecret = previousStripeAPISecret
	})
}

func TestSyncPaymentConfigurationIfStaleSkipsMatchingVersion(t *testing.T) {
	preparePaymentOptionSyncTest(t)
	require.NoError(t, DB.Create(&Option{Key: "EpayCurrency", Value: "CNY"}).Error)

	require.NoError(t, SyncPaymentConfigurationIfStale())
	assert.Equal(t, "LOCAL", operation_setting.EpayCurrency)
	common.OptionMapRWMutex.RLock()
	assert.Equal(t, "1", common.OptionMap[PaymentConfigurationVersionOptionKey])
	assert.Equal(t, "LOCAL", common.OptionMap["EpayCurrency"])
	common.OptionMapRWMutex.RUnlock()
}

func TestSyncPaymentConfigurationIfStaleRefreshesNewVersion(t *testing.T) {
	preparePaymentOptionSyncTest(t)
	require.NoError(t, DB.Create([]Option{
		{Key: PaymentConfigurationVersionOptionKey, Value: "2"},
		{Key: "EpayCurrency", Value: "CNY"},
	}).Error)

	require.NoError(t, SyncPaymentConfigurationIfStale())
	assert.Equal(t, "CNY", operation_setting.EpayCurrency)
	common.OptionMapRWMutex.RLock()
	assert.Equal(t, "2", common.OptionMap[PaymentConfigurationVersionOptionKey])
	assert.Equal(t, "CNY", common.OptionMap["EpayCurrency"])
	common.OptionMapRWMutex.RUnlock()
}

func TestSyncPaymentConfigurationIfStalePropagatesDecryptionError(t *testing.T) {
	preparePaymentOptionSyncTest(t)
	t.Setenv("PAYMENT_SECRET_KEY", "test-payment-master-key-at-least-32-bytes")
	require.NoError(t, DB.Create([]Option{
		{Key: PaymentConfigurationVersionOptionKey, Value: "2"},
		{Key: "EpayCurrency", Value: "CNY"},
		{Key: "StripeApiSecret", Value: "enc:v2:0000000000000000:invalid"},
	}).Error)

	err := SyncPaymentConfigurationIfStale()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decrypt payment option StripeApiSecret")
	assert.Equal(t, "LOCAL", operation_setting.EpayCurrency)
	assert.Equal(t, "local-secret", setting.StripeApiSecret)
	common.OptionMapRWMutex.RLock()
	assert.Equal(t, "1", common.OptionMap[PaymentConfigurationVersionOptionKey])
	assert.Equal(t, "LOCAL", common.OptionMap["EpayCurrency"])
	assert.Equal(t, "local-secret", common.OptionMap["StripeApiSecret"])
	common.OptionMapRWMutex.RUnlock()
}

func TestSyncPaymentConfigurationIfStalePropagatesReloadDatabaseError(t *testing.T) {
	preparePaymentOptionSyncTest(t)
	require.NoError(t, DB.Create([]Option{
		{Key: PaymentConfigurationVersionOptionKey, Value: "2"},
		{Key: "EpayCurrency", Value: "CNY"},
	}).Error)

	forcedErr := errors.New("forced option reload query failure")
	callbackName := "test:payment_option_sync_query_failure"
	triggered := false
	require.NoError(t, DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]*Option); !ok {
			return
		}
		triggered = true
		tx.AddError(forcedErr)
	}))
	t.Cleanup(func() {
		require.NoError(t, DB.Callback().Query().Remove(callbackName))
	})

	err := SyncPaymentConfigurationIfStale()
	assert.ErrorIs(t, err, forcedErr)
	assert.True(t, triggered)
	assert.Equal(t, "LOCAL", operation_setting.EpayCurrency)
	common.OptionMapRWMutex.RLock()
	assert.Equal(t, "1", common.OptionMap[PaymentConfigurationVersionOptionKey])
	assert.Equal(t, "LOCAL", common.OptionMap["EpayCurrency"])
	common.OptionMapRWMutex.RUnlock()
}
