package service

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/clause"
)

func TestBeginWeChatPaymentAuthorizationBindsCredentialGeneration(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "xorpay-jsapi-binding-payment-key-at-least-32-bytes")
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}, &model.PaymentOrder{}, &model.SystemInstance{}))
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	require.NoError(t, ReportCurrentSystemInstance())
	configurationVersion, err := model.CurrentPaymentConfigurationVersion()
	require.NoError(t, err)
	require.NoError(t, model.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&model.Option{
		Key: model.PaymentConfigurationVersionOptionKey, Value: "1",
	}).Error)
	// Tests normally run with the initial configuration version. Keep the
	// database fence aligned with the process-local snapshot if another test
	// advanced it.
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", model.PaymentConfigurationVersionOptionKey).
		Update("value", strconv.FormatInt(configurationVersion, 10)).Error)

	originalAid, originalSecret := setting.XorPayAid, setting.XorPayAppSecret
	originalGeneration := setting.XorPayCredentialGeneration
	originalMethods := setting.XorPayEnabledMethods
	originalCallback := operation_setting.CustomCallbackAddress
	t.Cleanup(func() {
		setting.XorPayAid, setting.XorPayAppSecret = originalAid, originalSecret
		setting.XorPayCredentialGeneration = originalGeneration
		setting.XorPayEnabledMethods = originalMethods
		operation_setting.CustomCallbackAddress = originalCallback
	})
	setting.XorPayAid = "aid_jsapi_binding"
	setting.XorPayAppSecret = "xorpay_jsapi_binding_secret"
	setting.XorPayCredentialGeneration = 44
	setting.XorPayEnabledMethods = []string{setting.XorPayMethodJSAPI}
	operation_setting.CustomCallbackAddress = "https://payments.example.com"

	now := time.Now().Unix()
	order := &model.PaymentOrder{
		TradeNo: "PO_JSAPI_CREDENTIAL_BINDING", UserID: 979401,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderXorPay,
		PaymentMethod: model.PaymentMethodXorPayJSAPI, ConfigurationVersion: configurationVersion,
		RequestID: "jsapi-credential-binding", ExpectedAmountMinor: 100, Currency: "CNY",
		RequestedAmount: 1, CreditQuota: 500, Status: model.PaymentOrderStatusPending,
		ExpiresAt: now + 600, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, model.DB.Create(order).Error)
	t.Cleanup(func() { _ = model.DB.Delete(&model.PaymentOrder{}, order.ID).Error })

	location, err := BeginWeChatPaymentAuthorization(order.UserID, order.TradeNo)
	require.NoError(t, err)
	parsed, err := url.Parse(location)
	require.NoError(t, err)
	assert.Equal(t, "https", parsed.Scheme)
	assert.Equal(t, "xorpay.com", parsed.Hostname())
	assert.Equal(t, "/api/openid/aid_jsapi_binding", parsed.Path)
	callback := parsed.Query().Get("callback")
	assert.True(t, strings.HasPrefix(callback,
		"https://payments.example.com/api/payment/wechat/authorize/callback?state="))

	stored, err := model.GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, int64(44), stored.ProviderCredentialGeneration)
	require.NotNil(t, stored.BrowserAuthorizationDigest)
	assert.Len(t, *stored.BrowserAuthorizationDigest, 64)
	assert.Empty(t, stored.BrowserAuthorizationPayload)
}

func TestBeginWeChatPaymentAuthorizationRejectsStaleConfiguration(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "xorpay-jsapi-stale-config-key-at-least-32-bytes")
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}, &model.PaymentOrder{}, &model.SystemInstance{}))
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	require.NoError(t, ReportCurrentSystemInstance())
	configurationVersion, err := model.CurrentPaymentConfigurationVersion()
	require.NoError(t, err)
	require.NoError(t, model.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&model.Option{
		Key: model.PaymentConfigurationVersionOptionKey, Value: strconv.FormatInt(configurationVersion, 10),
	}).Error)
	originalMethods := setting.XorPayEnabledMethods
	setting.XorPayEnabledMethods = []string{setting.XorPayMethodJSAPI}
	t.Cleanup(func() { setting.XorPayEnabledMethods = originalMethods })
	now := time.Now().Unix()
	order := &model.PaymentOrder{
		TradeNo: "PO_JSAPI_STALE_CONFIGURATION", UserID: 979402,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderXorPay,
		PaymentMethod: model.PaymentMethodXorPayJSAPI, ConfigurationVersion: configurationVersion + 1,
		RequestID: "jsapi-stale-configuration", ExpectedAmountMinor: 100, Currency: "CNY",
		RequestedAmount: 1, CreditQuota: 500, Status: model.PaymentOrderStatusPending,
		ExpiresAt: now + 600, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, model.DB.Create(order).Error)
	t.Cleanup(func() { _ = model.DB.Delete(&model.PaymentOrder{}, order.ID).Error })

	_, err = BeginWeChatPaymentAuthorization(order.UserID, order.TradeNo)
	assert.ErrorIs(t, err, model.ErrPaymentBrowserAuthorizationInvalid)
	stored, err := model.GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	assert.Nil(t, stored.BrowserAuthorizationDigest)
	assert.Zero(t, stored.ProviderCredentialGeneration)
}
