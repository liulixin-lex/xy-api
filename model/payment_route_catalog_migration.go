package model

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// PaymentRouteCatalogVersionOptionKey is an internal one-time data
	// migration marker. It is intentionally separate from the configuration
	// CAS version, which is incremented so already-running nodes reload the
	// migrated PayMethods snapshot.
	PaymentRouteCatalogVersionOptionKey = "PaymentRouteCatalogVersion"
	currentPaymentRouteCatalogVersion   = int64(1)
)

// migrateLegacyPaymentRouteCatalogOn makes PayMethods the durable source of
// public route truth without dropping gateways that older releases published
// implicitly. Provider settings are only evidence for the one-time migration;
// live publication still passes through the ordinary provider-readiness checks.
func migrateLegacyPaymentRouteCatalogOn(db *gorm.DB) error {
	if db == nil {
		return errors.New("payment route catalog migration database is required")
	}

	applied := false
	addedRoutes := 0
	committedVersion := int64(0)
	err := db.Transaction(func(tx *gorm.DB) error {
		initialVersion := Option{
			Key:   PaymentConfigurationVersionOptionKey,
			Value: strconv.FormatInt(initialPaymentConfigurationVersion, 10),
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoNothing: true,
		}).Create(&initialVersion).Error; err != nil {
			return fmt.Errorf("initialize payment configuration version for route catalog migration: %w", err)
		}

		versionQuery := tx
		if tx.Dialector.Name() != "sqlite" {
			versionQuery = versionQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var storedVersion Option
		if err := versionQuery.Where(&Option{Key: PaymentConfigurationVersionOptionKey}).First(&storedVersion).Error; err != nil {
			return fmt.Errorf("lock payment configuration version for route catalog migration: %w", err)
		}
		currentVersion, err := parsePaymentConfigurationVersion(storedVersion.Value, "stored")
		if err != nil {
			return err
		}

		var marker Option
		markerResult := tx.Where(&Option{Key: PaymentRouteCatalogVersionOptionKey}).Limit(1).Find(&marker)
		if markerResult.Error != nil {
			return fmt.Errorf("read payment route catalog migration marker: %w", markerResult.Error)
		}
		if markerResult.RowsAffected > 0 {
			markerVersion, err := strconv.ParseInt(strings.TrimSpace(marker.Value), 10, 64)
			if err != nil || markerVersion <= 0 {
				return fmt.Errorf("invalid payment route catalog migration version %q", marker.Value)
			}
			if markerVersion >= currentPaymentRouteCatalogVersion {
				return nil
			}
		}

		var options []Option
		if err := tx.Find(&options).Error; err != nil {
			return fmt.Errorf("read legacy payment settings for route catalog migration: %w", err)
		}
		values := make(map[string]string, len(options))
		for _, option := range options {
			values[option.Key] = option.Value
		}

		methods := operation_setting.DefaultPayMethods()
		if raw, exists := values["PayMethods"]; exists {
			methods, err = operation_setting.ParsePayMethodsByJsonString(raw)
			if err != nil {
				return fmt.Errorf("parse legacy PayMethods for route catalog migration: %w", err)
			}
		}

		legacy := operation_setting.LegacyImplicitPaymentRoutes{
			Stripe: legacyPaymentProviderConfigured(values,
				"StripeApiSecret", "StripeWebhookSecret", "StripePriceId", "StripeAccountId", "StripeCredentialAccountId"),
			Creem: legacyPaymentProviderConfigured(values,
				"CreemApiKey", "CreemWebhookSecret", "CreemProducts"),
			Waffo: strings.EqualFold(strings.TrimSpace(values["WaffoEnabled"]), "true") || legacyPaymentProviderConfigured(values,
				"WaffoApiKey", "WaffoPrivateKey", "WaffoPublicCert",
				"WaffoSandboxApiKey", "WaffoSandboxPrivateKey", "WaffoSandboxPublicCert", "WaffoMerchantId"),
			WaffoPancake: legacyPaymentProviderConfigured(values,
				"WaffoPancakeMerchantID", "WaffoPancakePrivateKey", "WaffoPancakeStoreID", "WaffoPancakeProductID"),
		}
		if legacyPaymentProviderConfigured(values, "XorPayAid", "XorPayAppSecret") {
			enabledMethods := []string{setting.XorPayMethodNative, setting.XorPayMethodAlipay}
			if raw, exists := values["XorPayEnabledMethods"]; exists {
				enabledMethods, err = setting.ParseXorPayEnabledMethods(raw)
				if err != nil {
					return fmt.Errorf("parse legacy XORPay methods for route catalog migration: %w", err)
				}
			}
			legacy.XorPay = make([]string, 0, len(enabledMethods))
			for _, method := range enabledMethods {
				switch method {
				case setting.XorPayMethodNative:
					legacy.XorPay = append(legacy.XorPay, PaymentMethodXorPayNative)
				case setting.XorPayMethodAlipay:
					legacy.XorPay = append(legacy.XorPay, PaymentMethodXorPayAlipay)
				case setting.XorPayMethodJSAPI:
					legacy.XorPay = append(legacy.XorPay, PaymentMethodXorPayJSAPI)
				}
			}
		}

		canonical, added, err := operation_setting.MergeLegacyImplicitPaymentRoutes(methods, legacy)
		if err != nil {
			return fmt.Errorf("merge legacy payment routes into PayMethods: %w", err)
		}
		encodedMethods, err := common.Marshal(canonical)
		if err != nil {
			return fmt.Errorf("encode migrated PayMethods: %w", err)
		}
		if currentVersion == math.MaxInt64 {
			return errors.New("payment configuration version is exhausted")
		}
		committedVersion = currentVersion + 1

		for _, option := range []Option{
			{Key: "PayMethods", Value: string(encodedMethods)},
			{Key: PaymentRouteCatalogVersionOptionKey, Value: strconv.FormatInt(currentPaymentRouteCatalogVersion, 10)},
		} {
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"value"}),
			}).Create(&option).Error; err != nil {
				return fmt.Errorf("persist payment route catalog migration option %s: %w", option.Key, err)
			}
		}
		result := tx.Model(&Option{}).
			Where(&Option{Key: PaymentConfigurationVersionOptionKey}).
			Where("value = ?", storedVersion.Value).
			Update("value", strconv.FormatInt(committedVersion, 10))
		if result.Error != nil {
			return fmt.Errorf("commit payment route catalog configuration version: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrPaymentConfigurationVersionConflict
		}

		applied = true
		addedRoutes = added
		return nil
	})
	if err != nil {
		return err
	}
	if applied {
		common.SysLog(fmt.Sprintf(
			"payment route catalog migration applied: added_routes=%d configuration_version=%d",
			addedRoutes, committedVersion,
		))
	}
	return nil
}

func legacyPaymentProviderConfigured(values map[string]string, keys ...string) bool {
	for _, key := range keys {
		value := strings.TrimSpace(values[key])
		switch strings.ToLower(value) {
		case "", "null", "[]", "{}":
			continue
		default:
			return true
		}
	}
	return false
}
