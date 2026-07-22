package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUpdateOptionMapCanonicalizesStripeCheckoutAllowedHosts(t *testing.T) {
	common.OptionMapRWMutex.Lock()
	optionMapWasNil := common.OptionMap == nil
	if optionMapWasNil {
		common.OptionMap = make(map[string]string)
	}
	originalValue, valueExisted := common.OptionMap["StripeCheckoutAllowedHosts"]
	common.OptionMapRWMutex.Unlock()
	originalSetting := setting.StripeCheckoutAllowedHosts
	t.Cleanup(func() {
		setting.StripeCheckoutAllowedHosts = originalSetting
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		if optionMapWasNil {
			common.OptionMap = nil
		} else if valueExisted {
			common.OptionMap["StripeCheckoutAllowedHosts"] = originalValue
		} else {
			delete(common.OptionMap, "StripeCheckoutAllowedHosts")
		}
	})

	require.NoError(t, updateOptionMap(
		"StripeCheckoutAllowedHosts",
		" Pay.Example.com\ncheckout.example.net, pay.example.com ",
	))
	assert.Equal(t, "checkout.example.net,pay.example.com", setting.StripeCheckoutAllowedHosts)
	common.OptionMapRWMutex.RLock()
	assert.Equal(t, "checkout.example.net,pay.example.com", common.OptionMap["StripeCheckoutAllowedHosts"])
	common.OptionMapRWMutex.RUnlock()

	assert.Error(t, updateOptionMap("StripeCheckoutAllowedHosts", "*.example.com"))
	assert.Equal(t, "checkout.example.net,pay.example.com", setting.StripeCheckoutAllowedHosts)
	common.OptionMapRWMutex.RLock()
	assert.Equal(t, "checkout.example.net,pay.example.com", common.OptionMap["StripeCheckoutAllowedHosts"])
	common.OptionMapRWMutex.RUnlock()
}

func TestStripeCheckoutAllowedHostRemovalPreconditionRejectsActiveOrders(t *testing.T) {
	preparePaymentOptionCASTest(t)
	require.NoError(t, DB.AutoMigrate(&PaymentConfigurationAudit{}, &PaymentOrder{}))
	require.NoError(t, DB.Where(&Option{Key: "StripeCheckoutAllowedHosts"}).Delete(&Option{}).Error)
	t.Cleanup(func() {
		_ = DB.Where(&Option{Key: "StripeCheckoutAllowedHosts"}).Delete(&Option{}).Error
		_ = DB.Session(&gorm.Session{AllowGlobalUpdate: true, SkipHooks: true}).Delete(&PaymentConfigurationAudit{}).Error
	})
	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: "stripe-checkout-host-removal-precondition", UserID: 991920,
		OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderStripe,
		PaymentMethod: PaymentMethodStripe, RequestID: "stripe-checkout-host-removal-precondition",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 1,
		Status: PaymentOrderStatusPending, ExpiresAt: now + 600, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	t.Cleanup(func() { _ = DB.Delete(&PaymentOrder{}, order.ID).Error })

	version, err := UpdatePaymentOptionsAndRevokeCredentialsAuditedWithVersionLockHeld(
		map[string]string{"StripeCheckoutAllowedHosts": "checkout.example.net"},
		1,
		nil,
		&PaymentConfigurationPreconditions{RequireNoActiveStripeOrdersForHostRemoval: true},
		&PaymentConfigurationAuditInput{AdminID: 46, ActorIP: "203.0.113.14", Reason: "remove custom Checkout host"},
	)
	assert.ErrorIs(t, err, ErrPaymentConfigurationPrecondition)
	assert.Equal(t, int64(1), version)

	var optionCount int64
	require.NoError(t, DB.Model(&Option{}).Where(&Option{Key: "StripeCheckoutAllowedHosts"}).Count(&optionCount).Error)
	assert.Zero(t, optionCount)
}
