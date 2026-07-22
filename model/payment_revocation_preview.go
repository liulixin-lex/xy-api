package model

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	PaymentCredentialRevocationModePrevious  = "previous"
	PaymentCredentialRevocationModeAllActive = "all_active"
)

// PaymentCredentialRevocationImpact is an administrator-only count snapshot.
// It contains no credential material, provider payload, customer identity, or
// order identifier.
type PaymentCredentialRevocationImpact struct {
	Provider                   string `json:"provider"`
	Mode                       string `json:"mode"`
	CanonicalAffectedOrders    int64  `json:"canonical_affected_orders"`
	CanonicalUnfinishedOrders  int64  `json:"canonical_unfinished_orders"`
	LegacyPendingTopUps        int64  `json:"legacy_pending_topups"`
	LegacyPendingSubscriptions int64  `json:"legacy_pending_subscriptions"`
	UnmatchedEconomicEvents    int64  `json:"unmatched_economic_events"`
	TotalAffectedOrders        int64  `json:"total_affected_orders"`
	TotalUnfinishedOrders      int64  `json:"total_unfinished_orders"`
}

// PreviewPaymentCredentialRevocation mirrors the durable quarantine scopes in
// updatePaymentOptionsWithVersionLockHeld. The preview is intentionally
// read-only and accepts generation numbers only from trusted server-side
// configuration resolution, never from an HTTP client.
func PreviewPaymentCredentialRevocation(provider, mode string, generations []int64, allActiveOrders bool, validBefore int64) (*PaymentCredentialRevocationImpact, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	mode = strings.ToLower(strings.TrimSpace(mode))
	currentOnlyDisable := paymentProviderUsesCurrentOnlyCredentials(provider) &&
		mode == PaymentCredentialRevocationModeAllActive && allActiveOrders
	if validBefore <= 0 || !paymentConfigurationPreconditionProviderSupported(provider) ||
		(mode != PaymentCredentialRevocationModePrevious && mode != PaymentCredentialRevocationModeAllActive) {
		return nil, errors.New("invalid payment credential revocation preview")
	}
	if paymentProviderUsesCurrentOnlyCredentials(provider) && !currentOnlyDisable {
		return nil, errors.New("current-only provider credentials can only be disabled with all-active quarantine")
	}
	generationSet := make(map[int64]struct{}, len(generations))
	for _, generation := range generations {
		if generation > 0 || currentOnlyDisable && generation == 0 {
			generationSet[generation] = struct{}{}
		}
	}
	if currentOnlyDisable {
		generationSet[0] = struct{}{}
	}
	if len(generationSet) == 0 {
		return nil, errors.New("payment credential revocation preview has no active generation")
	}
	generations = generations[:0]
	for generation := range generationSet {
		generations = append(generations, generation)
	}
	sort.Slice(generations, func(i, j int) bool { return generations[i] < generations[j] })

	impact := &PaymentCredentialRevocationImpact{Provider: provider, Mode: mode}
	affectedCanonical := make(map[int64]struct{})
	activeStatuses := map[string]struct{}{
		PaymentOrderStatusPending: {}, PaymentOrderStatusProcessing: {}, PaymentOrderStatusManualReview: {},
	}
	for index, generation := range generations {
		candidateByID := make(map[int64]PaymentOrder)
		var candidates []PaymentOrder
		candidateQuery := DB.Where("provider = ?", provider).
			Where("provider_credential_generation = ? OR (provider_credential_generation = 0 AND created_at <= ?)", generation, validBefore)
		if currentOnlyDisable {
			var err error
			candidateQuery, _, err = paymentOrdersDependingOnConfigurationQueryTx(DB, provider, validBefore)
			if err != nil {
				return nil, err
			}
		}
		if err := candidateQuery.Find(&candidates).Error; err != nil {
			return nil, err
		}
		for _, order := range candidates {
			candidateByID[order.ID] = order
		}
		if provider == PaymentProviderStripe {
			if DB.Migrator().HasTable(&PaymentEvent{}) {
				var linkedOrderIDs []int64
				if err := DB.Model(&PaymentEvent{}).
					Where("provider = ? AND provider_credential_generation = ? AND payment_order_id > 0", provider, generation).
					Where("paid = ? OR refunded = ? OR disputed = ? OR dispute_resolved = ? OR paid_amount_minor > 0 OR refunded_amount_minor > 0 OR disputed_amount_minor > 0",
						true, true, true, true).
					Distinct().Pluck("payment_order_id", &linkedOrderIDs).Error; err != nil {
					return nil, err
				}
				if len(linkedOrderIDs) > 0 {
					var linkedOrders []PaymentOrder
					if err := DB.Where("id IN ? AND provider = ?", linkedOrderIDs, provider).Find(&linkedOrders).Error; err != nil {
						return nil, err
					}
					for _, order := range linkedOrders {
						candidateByID[order.ID] = order
					}
				}
			}
			if allActiveOrders && index == 0 {
				var activeOrders []PaymentOrder
				if err := DB.Where("provider = ? AND status IN ?", provider, paymentInFlightOrderStatuses()).Find(&activeOrders).Error; err != nil {
					return nil, err
				}
				for _, order := range activeOrders {
					candidateByID[order.ID] = order
				}
			}
		}
		for _, order := range candidateByID {
			if _, seen := affectedCanonical[order.ID]; seen {
				continue
			}
			incidentGeneration := generation
			if order.ProviderCredentialGeneration == 0 {
				incidentGeneration = 0
			}
			if order.CredentialIncident && order.CredentialIncidentGeneration == incidentGeneration &&
				(order.CredentialIncidentState == PaymentCredentialIncidentOpen ||
					order.CredentialIncidentState == PaymentCredentialIncidentAcknowledged) {
				continue
			}
			affectedCanonical[order.ID] = struct{}{}
			if _, active := activeStatuses[order.Status]; active || currentOnlyDisable {
				impact.CanonicalUnfinishedOrders++
			}
		}
	}
	impact.CanonicalAffectedOrders = int64(len(affectedCanonical))

	countLegacy := func(modelValue any, statuses []string) (int64, error) {
		if !DB.Migrator().HasTable(modelValue) {
			return 0, nil
		}
		query := DB.Model(modelValue).Where("(payment_order_id IS NULL OR payment_order_id = 0)")
		if provider == PaymentProviderStripe {
			query = query.Where("status IN ?", statuses)
		} else if currentOnlyDisable {
			recoveryCutoff := validBefore - int64(PaymentCallbackRecoveryWindow/time.Second)
			if recoveryCutoff <= 0 {
				recoveryCutoff = 1
			}
			query = query.Where(
				"(status IN ? OR (status IN ? AND (complete_time >= ? OR create_time >= ?)))",
				statuses,
				[]string{common.TopUpStatusFailed, common.TopUpStatusExpired},
				recoveryCutoff, recoveryCutoff,
			)
		} else {
			query = query.Where("status = ? AND create_time <= ?", common.TopUpStatusPending, validBefore)
		}
		if provider == PaymentProviderEpay {
			query = query.Where("payment_provider = ? OR payment_provider = ''", provider)
		} else {
			query = query.Where("payment_provider = ?", provider)
		}
		var count int64
		return count, query.Count(&count).Error
	}
	var err error
	impact.LegacyPendingTopUps, err = countLegacy(&TopUp{}, []string{
		common.TopUpStatusPending, PaymentOrderStatusProcessing, common.TopUpStatusManualReview,
	})
	if err != nil {
		return nil, err
	}
	impact.LegacyPendingSubscriptions, err = countLegacy(&SubscriptionOrder{}, []string{
		common.TopUpStatusPending, PaymentOrderStatusProcessing, SubscriptionOrderStatusManualReview,
	})
	if err != nil {
		return nil, err
	}

	if DB.Migrator().HasTable(&PaymentEvent{}) {
		if err := DB.Model(&PaymentEvent{}).
			Where("provider = ? AND provider_credential_generation IN ? AND payment_order_id = ?", provider, generations, 0).
			Where("(status IS NULL OR status NOT IN ?)", []string{
				PaymentEventStatusProcessed, PaymentEventStatusDismissed, PaymentEventStatusCredentialRevoked,
			}).
			Where("paid = ? OR refunded = ? OR disputed = ? OR dispute_resolved = ? OR paid_amount_minor > 0 OR refunded_amount_minor > 0 OR disputed_amount_minor > 0",
				true, true, true, true).
			Count(&impact.UnmatchedEconomicEvents).Error; err != nil {
			return nil, err
		}
	}
	impact.TotalAffectedOrders = impact.CanonicalAffectedOrders + impact.LegacyPendingTopUps + impact.LegacyPendingSubscriptions
	impact.TotalUnfinishedOrders = impact.CanonicalUnfinishedOrders + impact.LegacyPendingTopUps + impact.LegacyPendingSubscriptions
	return impact, nil
}
