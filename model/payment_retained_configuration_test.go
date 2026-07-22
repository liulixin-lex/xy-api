package model

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func retainedPaymentMethodForTest(provider string) string {
	switch provider {
	case PaymentProviderCreem:
		return PaymentMethodCreem
	case PaymentProviderWaffo:
		return PaymentMethodWaffo
	case PaymentProviderWaffoPancake:
		return PaymentMethodWaffoPancake
	default:
		return provider
	}
}

func retainedConfigurationOrderForTest(provider, suffix, status string, userID int, now int64) PaymentOrder {
	return PaymentOrder{
		TradeNo: "PO_" + provider + "_" + suffix, UserID: userID, OrderKind: PaymentOrderKindTopUp,
		Provider: provider, PaymentMethod: retainedPaymentMethodForTest(provider), RequestID: "request_" + provider + "_" + suffix,
		ExpectedAmountMinor: 1000, Currency: "USD", RequestedAmount: 1, CreditQuota: 100,
		Status: status, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
}

func TestRetainedConfigurationDependencyCountsCanonicalAndStandaloneLegacy(t *testing.T) {
	for index, provider := range []string{PaymentProviderCreem, PaymentProviderWaffo, PaymentProviderWaffoPancake} {
		t.Run(provider, func(t *testing.T) {
			truncateTables(t)
			now := common.GetTimestamp()
			outsideRecovery := now - int64(PaymentCallbackRecoveryWindow/time.Second) - 1
			orders := []PaymentOrder{
				retainedConfigurationOrderForTest(provider, "pending", PaymentOrderStatusPending, 990400+index*10, now-100),
				retainedConfigurationOrderForTest(provider, "recent_failed", PaymentOrderStatusFailed, 990401+index*10, now-100),
				retainedConfigurationOrderForTest(provider, "stale_failed", PaymentOrderStatusFailed, 990402+index*10, outsideRecovery),
			}
			orders[1].StartedAt = now - 90
			orders[2].StartedAt = outsideRecovery
			require.NoError(t, DB.Create(&orders).Error)
			zeroOrderID := int64(0)
			legacy := []TopUp{
				{PaymentOrderId: &zeroOrderID, UserId: 990403 + index*10, Amount: 1, Money: 10, TradeNo: "LEGACY_" + provider + "_pending",
					PaymentMethod: retainedPaymentMethodForTest(provider), PaymentProvider: provider, CreateTime: now - 100, Status: common.TopUpStatusPending},
				{UserId: 990404 + index*10, Amount: 1, Money: 10, TradeNo: "LEGACY_" + provider + "_recent_failed",
					PaymentMethod: retainedPaymentMethodForTest(provider), PaymentProvider: provider, CreateTime: now - 100,
					CompleteTime: now - 50, Status: common.TopUpStatusFailed},
				{UserId: 990405 + index*10, Amount: 1, Money: 10, TradeNo: "LEGACY_" + provider + "_stale_failed",
					PaymentMethod: retainedPaymentMethodForTest(provider), PaymentProvider: provider, CreateTime: outsideRecovery,
					CompleteTime: outsideRecovery, Status: common.TopUpStatusFailed},
			}
			require.NoError(t, DB.Create(&legacy).Error)

			activeCount, err := CountActivePaymentOrdersForProvider(provider)
			require.NoError(t, err)
			assert.EqualValues(t, 2, activeCount)
			recoveryCount, err := CountPaymentOrdersDependingOnCallbackOrigin(provider, now)
			require.NoError(t, err)
			assert.EqualValues(t, 4, recoveryCount)
		})
	}
}

func TestRetainedProviderListPreconditionsDistinguishActiveAndRecoveryScopes(t *testing.T) {
	for _, test := range []struct {
		name          string
		preconditions *PaymentConfigurationPreconditions
		wantBlocked   bool
	}{
		{
			name: "active scope allows a recent started failure",
			preconditions: &PaymentConfigurationPreconditions{
				RequireNoActiveProviderOrders: []string{PaymentProviderCreem},
			},
		},
		{
			name: "recovery scope blocks a recent started failure",
			preconditions: &PaymentConfigurationPreconditions{
				RequireNoCallbackDependentProviderOrders: []string{PaymentProviderCreem},
			},
			wantBlocked: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			truncateTables(t)
			preparePaymentOptionCASTest(t)
			now := common.GetTimestamp()
			order := retainedConfigurationOrderForTest(PaymentProviderCreem, "precondition_recent_failed", PaymentOrderStatusFailed, 990450, now-60)
			order.StartedAt = now - 50
			require.NoError(t, DB.Create(&order).Error)

			version, err := UpdatePaymentOptionsAndRevokeCredentialsAuditedWithVersionLockHeld(
				map[string]string{paymentOptionCASTestFirst: "retained-precondition"}, 1, nil, test.preconditions,
				&PaymentConfigurationAuditInput{AdminID: 54, ActorIP: "192.0.2.54", Reason: "retained configuration dependency test"},
			)
			if test.wantBlocked {
				assert.ErrorIs(t, err, ErrPaymentConfigurationPrecondition)
				assert.Equal(t, int64(1), version)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, int64(2), version)
		})
	}
}

func TestCurrentOnlyCredentialDisablePreviewMatchesRecoveryQuarantine(t *testing.T) {
	for index, provider := range []string{PaymentProviderCreem, PaymentProviderWaffo, PaymentProviderWaffoPancake} {
		t.Run(provider, func(t *testing.T) {
			truncateTables(t)
			preparePaymentOptionCASTest(t)
			now := common.GetTimestamp()
			outsideRecovery := now - int64(PaymentCallbackRecoveryWindow/time.Second) - 1
			orders := []PaymentOrder{
				retainedConfigurationOrderForTest(provider, "disable_pending", PaymentOrderStatusPending, 990500+index*10, now-100),
				retainedConfigurationOrderForTest(provider, "disable_recent_failed", PaymentOrderStatusFailed, 990501+index*10, now-100),
				retainedConfigurationOrderForTest(provider, "disable_stale_failed", PaymentOrderStatusFailed, 990502+index*10, outsideRecovery),
				retainedConfigurationOrderForTest(provider, "disable_unstarted_expired", PaymentOrderStatusExpired, 990503+index*10, now-100),
			}
			orders[1].StartedAt = now - 90
			orders[2].StartedAt = outsideRecovery
			require.NoError(t, DB.Create(&orders).Error)
			legacy := []TopUp{
				{UserId: 990504 + index*10, Amount: 1, Money: 10, TradeNo: "LEGACY_" + provider + "_disable_pending",
					PaymentMethod: retainedPaymentMethodForTest(provider), PaymentProvider: provider, CreateTime: now - 100, Status: common.TopUpStatusPending},
				{UserId: 990505 + index*10, Amount: 1, Money: 10, TradeNo: "LEGACY_" + provider + "_disable_recent_expired",
					PaymentMethod: retainedPaymentMethodForTest(provider), PaymentProvider: provider, CreateTime: now - 100,
					CompleteTime: now - 50, Status: common.TopUpStatusExpired},
				{UserId: 990506 + index*10, Amount: 1, Money: 10, TradeNo: "LEGACY_" + provider + "_disable_stale_expired",
					PaymentMethod: retainedPaymentMethodForTest(provider), PaymentProvider: provider, CreateTime: outsideRecovery,
					CompleteTime: outsideRecovery, Status: common.TopUpStatusExpired},
			}
			require.NoError(t, DB.Create(&legacy).Error)

			impact, err := PreviewPaymentCredentialRevocation(
				provider, PaymentCredentialRevocationModeAllActive, []int64{0}, true, now,
			)
			require.NoError(t, err)
			assert.EqualValues(t, 2, impact.CanonicalAffectedOrders)
			assert.EqualValues(t, 2, impact.CanonicalUnfinishedOrders)
			assert.EqualValues(t, 2, impact.LegacyPendingTopUps)
			assert.EqualValues(t, 4, impact.TotalAffectedOrders)
			assert.EqualValues(t, 4, impact.TotalUnfinishedOrders)

			version, err := UpdatePaymentOptionsAndRevokeCredentialsAuditedWithVersionLockHeld(
				map[string]string{paymentOptionCASTestFirst: "disabled-" + provider}, 1,
				[]PaymentCredentialRevocation{{Provider: provider, Generation: 0, ValidBefore: now, AllActiveOrders: true}}, nil,
				&PaymentConfigurationAuditInput{AdminID: 55, ActorIP: "192.0.2.55", Reason: "disable compromised current-only credential"},
			)
			require.NoError(t, err)
			assert.Equal(t, int64(2), version)

			for _, orderIndex := range []int{0, 1} {
				require.NoError(t, DB.First(&orders[orderIndex], orders[orderIndex].ID).Error)
				assert.Equal(t, PaymentOrderStatusManualReview, orders[orderIndex].Status)
				assert.True(t, orders[orderIndex].CredentialIncident)
				assert.Zero(t, orders[orderIndex].CredentialIncidentGeneration)
				assert.Contains(t, orders[orderIndex].CredentialIncidentReason, "current-only credential disabled")
			}
			for _, orderIndex := range []int{2, 3} {
				require.NoError(t, DB.First(&orders[orderIndex], orders[orderIndex].ID).Error)
				assert.False(t, orders[orderIndex].CredentialIncident)
			}
			for _, legacyIndex := range []int{0, 1} {
				require.NoError(t, DB.First(&legacy[legacyIndex], legacy[legacyIndex].Id).Error)
				assert.Equal(t, common.TopUpStatusManualReview, legacy[legacyIndex].Status)
			}
			require.NoError(t, DB.First(&legacy[2], legacy[2].Id).Error)
			assert.Equal(t, common.TopUpStatusExpired, legacy[2].Status)

			var audit PaymentConfigurationAudit
			require.NoError(t, DB.Order("id desc").First(&audit).Error)
			assert.EqualValues(t, 4, audit.AffectedOrders, fmt.Sprintf("provider %s preview and audit must match", provider))
		})
	}
}
