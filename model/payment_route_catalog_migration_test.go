package model

import (
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMigrateLegacyPaymentRouteCatalogPersistsImplicitRoutesOnce(t *testing.T) {
	db := newPaymentRouteCatalogMigrationTestDB(t)
	require.NoError(t, db.Create(&[]Option{
		{Key: PaymentConfigurationVersionOptionKey, Value: "7"},
		{Key: "StripeApiSecret", Value: "enc:v2:configured"},
		{Key: "CreemProducts", Value: `[{"productId":"configured"}]`},
		{Key: "WaffoEnabled", Value: "true"},
		{Key: "WaffoPancakeMerchantID", Value: "configured"},
		{Key: "XorPayAid", Value: "configured"},
		{Key: "XorPayEnabledMethods", Value: `["native","alipay","jsapi"]`},
	}).Error)

	require.NoError(t, migrateLegacyPaymentRouteCatalogOn(db))
	assertPaymentRouteCatalogMigrationOption(t, db, PaymentRouteCatalogVersionOptionKey, "1")
	assertPaymentRouteCatalogMigrationOption(t, db, PaymentConfigurationVersionOptionKey, "8")

	var payMethods Option
	require.NoError(t, db.Where(&Option{Key: "PayMethods"}).First(&payMethods).Error)
	assert.NotContains(t, payMethods.Value, "支付宝")
	assert.Contains(t, payMethods.Value, `\u652f\u4ed8\u5b9d`)
	parsed, err := operation_setting.ParsePayMethodsByJsonString(payMethods.Value)
	require.NoError(t, err)
	identities := make(map[string]struct{}, len(parsed))
	for _, method := range parsed {
		identities[method["provider"]+"\x00"+method["type"]] = struct{}{}
	}
	for _, identity := range []string{
		"epay\x00alipay", "epay\x00wxpay", "epay\x00custom1",
		"stripe\x00stripe", "creem\x00creem", "waffo\x00waffo", "waffo_pancake\x00waffo_pancake",
		"xorpay\x00xorpay_native", "xorpay\x00xorpay_alipay", "xorpay\x00xorpay_jsapi",
	} {
		assert.Contains(t, identities, identity)
	}

	require.NoError(t, migrateLegacyPaymentRouteCatalogOn(db))
	assertPaymentRouteCatalogMigrationOption(t, db, PaymentConfigurationVersionOptionKey, "8")
	var afterSecondRun Option
	require.NoError(t, db.Where(&Option{Key: "PayMethods"}).First(&afterSecondRun).Error)
	assert.Equal(t, payMethods.Value, afterSecondRun.Value)
}

func TestMigrateLegacyPaymentRouteCatalogHonorsExplicitlyDisabledXorPayMethods(t *testing.T) {
	db := newPaymentRouteCatalogMigrationTestDB(t)
	require.NoError(t, db.Create(&[]Option{
		{Key: PaymentConfigurationVersionOptionKey, Value: "3"},
		{Key: "XorPayAid", Value: "configured"},
		{Key: "XorPayAppSecret", Value: "enc:v2:configured"},
		{Key: "XorPayEnabledMethods", Value: `[]`},
	}).Error)

	require.NoError(t, migrateLegacyPaymentRouteCatalogOn(db))
	var payMethods Option
	require.NoError(t, db.Where(&Option{Key: "PayMethods"}).First(&payMethods).Error)
	parsed, err := operation_setting.ParsePayMethodsByJsonString(payMethods.Value)
	require.NoError(t, err)
	for _, method := range parsed {
		assert.NotEqual(t, "xorpay", method["provider"])
	}
}

func TestMigrateLegacyPaymentRouteCatalogRollsBackMalformedCatalog(t *testing.T) {
	db := newPaymentRouteCatalogMigrationTestDB(t)
	malformed := `[{"name":"Reserved","type":"stripe","provider":"epay"}]`
	require.NoError(t, db.Create(&[]Option{
		{Key: PaymentConfigurationVersionOptionKey, Value: "4"},
		{Key: "PayMethods", Value: malformed},
		{Key: "StripeApiSecret", Value: "enc:v2:configured"},
	}).Error)

	err := migrateLegacyPaymentRouteCatalogOn(db)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse legacy PayMethods")
	assertPaymentRouteCatalogMigrationOption(t, db, PaymentConfigurationVersionOptionKey, "4")
	assertPaymentRouteCatalogMigrationOption(t, db, "PayMethods", malformed)

	var markerCount int64
	require.NoError(t, db.Model(&Option{}).Where(&Option{Key: PaymentRouteCatalogVersionOptionKey}).Count(&markerCount).Error)
	assert.Zero(t, markerCount)
}

func TestMigrateLegacyPaymentRouteCatalogPreservesHistoricalCustomEpayTypes(t *testing.T) {
	db := newPaymentRouteCatalogMigrationTestDB(t)
	legacyMethods := `[
		{"name":"Legacy product checkout","type":"creem","provider":"epay"},
		{"name":"Legacy payment options","type":"waffo","provider":"epay"}
	]`
	require.NoError(t, db.Create(&[]Option{
		{Key: PaymentConfigurationVersionOptionKey, Value: "5"},
		{Key: "PayMethods", Value: legacyMethods},
		{Key: "CreemApiKey", Value: "enc:v2:configured"},
		{Key: "WaffoEnabled", Value: "true"},
	}).Error)

	require.NoError(t, migrateLegacyPaymentRouteCatalogOn(db))
	assertPaymentRouteCatalogMigrationOption(t, db, PaymentConfigurationVersionOptionKey, "6")

	var payMethods Option
	require.NoError(t, db.Where(&Option{Key: "PayMethods"}).First(&payMethods).Error)
	parsed, err := operation_setting.ParsePayMethodsByJsonString(payMethods.Value)
	require.NoError(t, err)
	identities := make(map[string]struct{}, len(parsed))
	for _, method := range parsed {
		identities[method["provider"]+"\x00"+method["type"]] = struct{}{}
	}
	assert.Contains(t, identities, "epay\x00creem")
	assert.Contains(t, identities, "epay\x00waffo")
	assert.Contains(t, identities, "creem\x00creem")
	assert.Contains(t, identities, "waffo\x00waffo")
}

func newPaymentRouteCatalogMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "route-catalog.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	return db
}

func assertPaymentRouteCatalogMigrationOption(t *testing.T, db *gorm.DB, key, expected string) {
	t.Helper()
	var option Option
	require.NoError(t, db.Where(&Option{Key: key}).First(&option).Error)
	assert.Equal(t, expected, option.Value)
}
