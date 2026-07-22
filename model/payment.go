package model

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PaymentOrder is the canonical payment contract used by new payment flows.
// TopUp and SubscriptionOrder remain compatibility projections for existing
// clients and historical data, but settlement must use this immutable snapshot.
type PaymentOrder struct {
	ID                            int64   `json:"id" gorm:"primaryKey"`
	TradeNo                       string  `json:"trade_no" gorm:"uniqueIndex;type:varchar(128)"`
	UserID                        int     `json:"user_id" gorm:"index;uniqueIndex:idx_payment_user_request,priority:1"`
	OrderKind                     string  `json:"order_kind" gorm:"type:varchar(32);index"`
	Provider                      string  `json:"provider" gorm:"type:varchar(32);index"`
	PaymentMethod                 string  `json:"payment_method" gorm:"type:varchar(64)"`
	ConfigurationVersion          int64   `json:"-" gorm:"index"`
	CreationFenceToken            int64   `json:"-"`
	ProviderCredentialGeneration  int64   `json:"-" gorm:"index"`
	ProviderLivemode              *bool   `json:"-"`
	QuoteID                       string  `json:"-" gorm:"type:varchar(128)"`
	RequestID                     string  `json:"request_id" gorm:"type:varchar(128);uniqueIndex:idx_payment_user_request,priority:2"`
	ProviderOrderKey              *string `json:"provider_order_key,omitempty" gorm:"type:varchar(320);uniqueIndex:idx_payment_orders_provider_order_key"`
	ProviderPaymentKey            *string `json:"provider_payment_key,omitempty" gorm:"type:varchar(320);uniqueIndex:idx_payment_orders_provider_payment_key"`
	ExpectedAmountMinor           int64   `json:"expected_amount_minor"`
	PaidAmountMinor               int64   `json:"paid_amount_minor"`
	Currency                      string  `json:"currency" gorm:"type:varchar(8)"`
	RequestedAmount               int64   `json:"requested_amount"`
	CreditQuota                   int64   `json:"credit_quota"`
	PricingSnapshot               string  `json:"-" gorm:"type:text"`
	ProductSnapshot               string  `json:"-" gorm:"type:text"`
	StartFlow                     string  `json:"-" gorm:"type:varchar(32)"`
	StartPayload                  string  `json:"-" gorm:"type:text"`
	BrowserAuthorizationDigest    *string `json:"-" gorm:"type:varchar(64);uniqueIndex"`
	BrowserAuthorizationPayload   string  `json:"-" gorm:"type:text"`
	BrowserAuthorizationExpiresAt int64   `json:"-" gorm:"index"`
	BrowserAuthorizedAt           int64   `json:"-"`
	StartedAt                     int64   `json:"started_at"`
	ProviderCheckedAt             int64   `json:"-" gorm:"index"`
	LegacyRecordType              string  `json:"legacy_record_type,omitempty" gorm:"type:varchar(32)"`
	LegacyRecordID                int     `json:"legacy_record_id,omitempty" gorm:"index"`
	Status                        string  `json:"status" gorm:"type:varchar(32);index"`
	StatusReason                  string  `json:"status_reason,omitempty" gorm:"type:varchar(512)"`
	CredentialIncident            bool    `json:"credential_incident" gorm:"index"`
	CredentialIncidentState       string  `json:"credential_incident_state,omitempty" gorm:"type:varchar(32);index"`
	CredentialIncidentGeneration  int64   `json:"credential_incident_generation,omitempty" gorm:"index"`
	CredentialIncidentReason      string  `json:"credential_incident_reason,omitempty" gorm:"type:varchar(512)"`
	CredentialIncidentAt          int64   `json:"credential_incident_at,omitempty" gorm:"index"`
	CredentialIncidentReviewedAt  int64   `json:"credential_incident_reviewed_at,omitempty"`
	CredentialIncidentReviewedBy  int     `json:"credential_incident_reviewed_by,omitempty"`
	CredentialIncidentReviewNote  string  `json:"credential_incident_review_note,omitempty" gorm:"type:varchar(512)"`
	ExpiresAt                     int64   `json:"expires_at" gorm:"index"`
	SettledAt                     int64   `json:"settled_at"`
	RefundedAmountMinor           int64   `json:"refunded_amount_minor"`
	DisputedAmountMinor           int64   `json:"disputed_amount_minor"`
	ReversedAmountMinor           int64   `json:"reversed_amount_minor"`
	ReversedQuota                 int64   `json:"reversed_quota"`
	CreatedAt                     int64   `json:"created_at" gorm:"index"`
	UpdatedAt                     int64   `json:"updated_at"`
	Version                       int64   `json:"version"`
}

// PaymentQuote is a short-lived, single-use server-side pricing snapshot.
type PaymentQuote struct {
	ID                  int64  `json:"id" gorm:"primaryKey"`
	QuoteID             string `json:"quote_id" gorm:"uniqueIndex;type:varchar(128)"`
	UserID              int    `json:"user_id" gorm:"index"`
	OrderKind           string `json:"order_kind" gorm:"type:varchar(32)"`
	Provider            string `json:"provider" gorm:"type:varchar(32)"`
	PaymentMethod       string `json:"payment_method" gorm:"type:varchar(64)"`
	ProviderLivemode    *bool  `json:"-"`
	RequestedAmount     int64  `json:"requested_amount"`
	CreditQuota         int64  `json:"credit_quota"`
	ExpectedAmountMinor int64  `json:"expected_amount_minor"`
	Currency            string `json:"currency" gorm:"type:varchar(8)"`
	PricingSnapshot     string `json:"-" gorm:"type:text"`
	ProductSnapshot     string `json:"-" gorm:"type:text"`
	ExpiresAt           int64  `json:"expires_at" gorm:"index"`
	ConsumedAt          int64  `json:"consumed_at"`
	CreatedAt           int64  `json:"created_at" gorm:"index"`
}

// PaymentUserGuard serializes quote creation and canonical order creation for
// one authenticated user. The upsert used to acquire it is a real write on all
// supported databases, so capacity checks cannot race even where SELECT FOR
// UPDATE is unavailable (SQLite).
type PaymentUserGuard struct {
	UserID    int   `json:"-" gorm:"primaryKey"`
	UpdatedAt int64 `json:"-"`
	Blocked   bool  `json:"-"`
}

// PaymentEvent is the durable webhook inbox record. Raw provider bodies and
// signatures are deliberately not persisted.
type PaymentEvent struct {
	ID                           int64  `json:"id" gorm:"primaryKey"`
	Provider                     string `json:"provider" gorm:"type:varchar(32);index;uniqueIndex:idx_payment_event_key,priority:1"`
	EventKey                     string `json:"event_key" gorm:"type:varchar(255);uniqueIndex:idx_payment_event_key,priority:2"`
	EventType                    string `json:"event_type" gorm:"type:varchar(128)"`
	TradeNo                      string `json:"trade_no" gorm:"type:varchar(128);index"`
	PaymentOrderID               int64  `json:"payment_order_id,omitempty" gorm:"index"`
	ProviderOrderKey             string `json:"provider_order_key,omitempty" gorm:"type:varchar(320);index"`
	ProviderPaymentKey           string `json:"provider_payment_key,omitempty" gorm:"type:varchar(320);index"`
	ProviderResourceKey          string `json:"provider_resource_key,omitempty" gorm:"type:varchar(320);index"`
	ProviderCredentialGeneration int64  `json:"-" gorm:"index"`
	ProviderLivemode             *bool  `json:"provider_livemode,omitempty" gorm:"index"`
	CustomerID                   string `json:"customer_id,omitempty" gorm:"type:varchar(255)"`
	ProviderCreatedAt            int64  `json:"provider_created_at,omitempty" gorm:"index"`
	ProviderState                string `json:"provider_state,omitempty" gorm:"type:varchar(64)"`
	PaidAmountMinor              int64  `json:"paid_amount_minor"`
	RefundedAmountMinor          int64  `json:"refunded_amount_minor"`
	DisputedAmountMinor          int64  `json:"disputed_amount_minor"`
	Currency                     string `json:"currency,omitempty" gorm:"type:varchar(8)"`
	PaymentMethod                string `json:"payment_method,omitempty" gorm:"type:varchar(64)"`
	Paid                         bool   `json:"paid"`
	Failed                       bool   `json:"failed"`
	Expired                      bool   `json:"expired"`
	Refunded                     bool   `json:"refunded"`
	Disputed                     bool   `json:"disputed"`
	DisputeResolved              bool   `json:"dispute_resolved"`
	DisputeWon                   bool   `json:"dispute_won"`
	PermanentFailure             bool   `json:"permanent_failure"`
	ManualReview                 bool   `json:"manual_review"`
	PayloadDigest                string `json:"payload_digest" gorm:"type:varchar(128)"`
	NormalizedPayload            string `json:"-" gorm:"type:text"`
	Status                       string `json:"status" gorm:"type:varchar(32);index"`
	ReviewCode                   string `json:"review_code,omitempty" gorm:"type:varchar(64);index"`
	Attempts                     int    `json:"attempts"`
	LastError                    string `json:"last_error,omitempty" gorm:"type:varchar(1024)"`
	CreatedAt                    int64  `json:"created_at" gorm:"index"`
	ProcessedAt                  int64  `json:"processed_at"`
	UpdatedAt                    int64  `json:"updated_at"`
}

// PaymentLedgerEntry is the append-only audit trail for purchased quota,
// reversals, debt and entitlement changes. It deliberately stores normalized
// accounting facts only; provider payloads and secrets never belong here.
type PaymentLedgerEntry struct {
	ID             int64  `json:"id" gorm:"primaryKey"`
	PaymentOrderID int64  `json:"payment_order_id" gorm:"index;uniqueIndex:idx_payment_ledger_event,priority:1"`
	PaymentEventID int64  `json:"payment_event_id" gorm:"index;uniqueIndex:idx_payment_ledger_event,priority:2"`
	UserID         int    `json:"user_id" gorm:"index"`
	EntryType      string `json:"entry_type" gorm:"type:varchar(48);index;uniqueIndex:idx_payment_ledger_event,priority:3"`
	AmountMinor    int64  `json:"amount_minor"`
	QuotaDelta     int64  `json:"quota_delta"`
	Currency       string `json:"currency" gorm:"type:varchar(8)"`
	Description    string `json:"description,omitempty" gorm:"type:varchar(255)"`
	CreatedAt      int64  `json:"created_at" gorm:"index"`
}

func (*PaymentLedgerEntry) BeforeUpdate(_ *gorm.DB) error {
	return ErrFinancialHistoryImmutable
}

func (*PaymentLedgerEntry) BeforeDelete(_ *gorm.DB) error {
	return ErrFinancialHistoryImmutable
}

// PaymentDebt records quota that could not be recovered after a refund or
// dispute. A row is unique per affected user and payment order so retries only
// increase the same durable balance instead of creating duplicate debt.
type PaymentDebt struct {
	ID                     int64  `json:"id" gorm:"primaryKey"`
	PaymentOrderID         int64  `json:"payment_order_id" gorm:"index;uniqueIndex:idx_payment_debt_subject,priority:1"`
	UserID                 int    `json:"user_id" gorm:"index;uniqueIndex:idx_payment_debt_subject,priority:2"`
	DebtKind               string `json:"debt_kind" gorm:"type:varchar(32);uniqueIndex:idx_payment_debt_subject,priority:3"`
	Currency               string `json:"currency" gorm:"type:varchar(8)"`
	OriginalAmountMinor    int64  `json:"original_amount_minor"`
	OutstandingAmountMinor int64  `json:"outstanding_amount_minor"`
	OriginalQuota          int64  `json:"original_quota"`
	OutstandingQuota       int64  `json:"outstanding_quota"`
	RecoveredQuota         int64  `json:"recovered_quota"`
	PreviousUserStatus     int    `json:"previous_user_status"`
	FreezeApplied          bool   `json:"freeze_applied"`
	Status                 string `json:"status" gorm:"type:varchar(32);index"`
	CreatedAt              int64  `json:"created_at" gorm:"index"`
	UpdatedAt              int64  `json:"updated_at"`
	ResolvedAt             int64  `json:"resolved_at"`
	Resolution             string `json:"resolution,omitempty" gorm:"type:varchar(32)"`
	ResolutionNote         string `json:"resolution_note,omitempty" gorm:"type:varchar(512)"`
	ResolvedBy             int    `json:"resolved_by,omitempty"`
}

// PaymentCustomerBinding is the database authority for provider customer
// ownership. The two unique constraints prevent both cross-user customer reuse
// and multiple customers for the same user/provider under concurrent webhook
// processing, without imposing a migration constraint on legacy users rows.
type PaymentCustomerBinding struct {
	ID          int64  `json:"id" gorm:"primaryKey"`
	Provider    string `json:"provider" gorm:"type:varchar(32);uniqueIndex:idx_payment_customer_provider_key,priority:1;uniqueIndex:idx_payment_customer_provider_user,priority:1"`
	CustomerKey string `json:"customer_key" gorm:"type:varchar(64);uniqueIndex:idx_payment_customer_provider_key,priority:2"`
	UserID      int    `json:"user_id" gorm:"index;uniqueIndex:idx_payment_customer_provider_user,priority:2"`
	CreatedAt   int64  `json:"created_at" gorm:"index"`
	UpdatedAt   int64  `json:"updated_at"`
	Version     int64  `json:"version"`
}

func (binding *PaymentCustomerBinding) BeforeCreate(_ *gorm.DB) error {
	now := common.GetTimestamp()
	if binding.CreatedAt == 0 {
		binding.CreatedAt = now
	}
	binding.UpdatedAt = now
	if binding.Version == 0 {
		binding.Version = 1
	}
	return nil
}

const (
	paymentTradeNoPrefix         = "new-api-ref-"
	paymentTradeNoAlphabet       = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	paymentTradeNoCreateAttempts = 5

	// PaymentCallbackRecoveryWindow keeps provider callback origins stable long
	// enough for delayed Epay and XORPay payment notifications to settle an
	// order that was already marked failed or expired locally. Thirty days is a
	// deliberately conservative operational boundary; unresolved orders older
	// than this require explicit reconciliation instead of silently pinning a
	// callback origin forever.
	PaymentCallbackRecoveryWindow = 30 * 24 * time.Hour
	// PaymentProviderAuthorityKeyMaxLength leaves room for a documented
	// 255-character provider object ID plus a provider namespace prefix.
	PaymentProviderAuthorityKeyMaxLength = 320
	// Stripe requires Checkout Session expiration to remain at least 30 minutes
	// in the future. The local order owns a 40-minute window so the durable
	// worker has bounded queueing time without allowing Stripe to extend it.
	PaymentStripeOrderTTLSeconds = int64(40 * 60)
	// Retained hosted checkouts historically use a 45-minute payment window.
	// Keep that window server-owned so reservations and background recovery
	// share the same durable expiry on every node.
	PaymentRetainedOrderTTLSeconds = int64(45 * 60)

	PaymentOrderKindTopUp        = "topup"
	PaymentOrderKindSubscription = "subscription"

	PaymentOrderStatusPending       = "pending"
	PaymentOrderStatusProcessing    = "processing"
	PaymentOrderStatusPaid          = "paid"
	PaymentOrderStatusFulfilled     = "fulfilled"
	PaymentOrderStatusFailed        = "failed"
	PaymentOrderStatusExpired       = "expired"
	PaymentOrderStatusManualReview  = "manual_review"
	PaymentOrderStatusRefundPending = "refund_pending"
	PaymentOrderStatusRefunded      = "refunded"
	PaymentOrderStatusDisputed      = "disputed"
	PaymentOrderStatusDebt          = "debt"

	PaymentEventStatusReceived          = "received"
	PaymentEventStatusProcessing        = "processing"
	PaymentEventStatusProcessed         = "processed"
	PaymentEventStatusManualReview      = "manual_review"
	PaymentEventStatusFailed            = "failed"
	PaymentEventStatusCredentialRevoked = "credential_revoked"

	// A paid subscription-mode Checkout Session can only come from the retired
	// recurring Stripe flow. It is authoritative payment evidence, but the local
	// entitlement contract is not safe to reconstruct automatically after an
	// upgrade, so both the provider state and review classification remain
	// explicit and durable.
	PaymentProviderStateStripeLegacyRecurringCheckoutPaid = "stripe_legacy_recurring_checkout_paid"
	PaymentReviewCodeStripeLegacyRecurringCheckoutPaid    = PaymentProviderStateStripeLegacyRecurringCheckoutPaid

	PaymentLedgerEntryCredit               = "credit"
	PaymentLedgerEntryRefundReversal       = "refund_reversal"
	PaymentLedgerEntryDisputeReversal      = "dispute_reversal"
	PaymentLedgerEntryReversalRestored     = "reversal_restored"
	PaymentLedgerEntrySubscriptionGranted  = "subscription_granted"
	PaymentLedgerEntrySubscriptionCanceled = "subscription_canceled"
	PaymentLedgerEntryAffiliateReversal    = "affiliate_reversal"
	PaymentLedgerEntryDebtResolved         = "debt_resolved"

	PaymentDebtKindBuyer      = "buyer"
	PaymentDebtKindAffiliate  = "affiliate"
	PaymentDebtStatusOpen     = "open"
	PaymentDebtStatusResolved = "resolved"

	PaymentCredentialIncidentOpen         = "open"
	PaymentCredentialIncidentAcknowledged = "acknowledged"
	PaymentCredentialIncidentResolved     = "resolved"

	// Abuse-control limits are deliberately enforced in the model transaction,
	// in addition to HTTP rate limiting. This protects alternate/internal entry
	// points and every application node sharing the database.
	PaymentMaxActiveQuotesPerUser                 = 10
	PaymentMaxInFlightOrdersPerUserProvider       = 5
	PaymentQuoteAuditRetentionSeconds       int64 = 24 * 60 * 60
)

var (
	ErrPaymentQuoteNotFound       = errors.New("payment quote not found")
	ErrPaymentQuoteExpired        = errors.New("payment quote expired")
	ErrPaymentQuoteConsumed       = errors.New("payment quote already consumed")
	ErrPaymentOrderNotFound       = errors.New("payment order not found")
	ErrPaymentEventDuplicate      = errors.New("payment event already exists")
	ErrPaymentEventConflict       = errors.New("payment event payload conflict")
	ErrPaymentManualReview        = errors.New("payment requires manual review")
	ErrPaymentAmountMismatch      = errors.New("payment amount mismatch")
	ErrPaymentCurrencyMismatch    = errors.New("payment currency mismatch")
	ErrPaymentProviderMismatch    = errors.New("payment provider identity mismatch")
	ErrPaymentIdempotencyConflict = errors.New("payment request id was reused for a different quote")
	ErrPaymentActiveQuoteLimit    = errors.New("too many active payment quotes")
	ErrPaymentInFlightOrderLimit  = errors.New("too many in-flight payment orders for this provider")
	ErrPaymentUserUnavailable     = errors.New("payment user is no longer available")
	ErrPaymentTradeNoCollision    = errors.New("payment trade number collision limit reached")
)

func GeneratePaymentTradeNo() (string, error) {
	return generatePaymentTradeNo(time.Now().UTC(), rand.Reader)
}

func generatePaymentTradeNo(now time.Time, entropy io.Reader) (string, error) {
	if entropy == nil {
		return "", errors.New("payment trade number entropy is unavailable")
	}
	random := make([]byte, 10)
	if _, err := io.ReadFull(entropy, random); err != nil {
		return "", err
	}
	suffix := make([]byte, 0, 16)
	var bits uint32
	var bitCount uint
	for _, value := range random {
		bits = bits<<8 | uint32(value)
		bitCount += 8
		for bitCount >= 5 {
			bitCount -= 5
			suffix = append(suffix, paymentTradeNoAlphabet[(bits>>bitCount)&31])
		}
		if bitCount == 0 {
			bits = 0
		} else {
			bits &= (1 << bitCount) - 1
		}
	}
	if len(suffix) != 16 {
		return "", errors.New("invalid payment trade number entropy length")
	}
	return paymentTradeNoPrefix + now.UTC().Format("20060102T150405Z") + "-" + string(suffix), nil
}

func createPaymentOrderWithUniqueTradeNoTx(tx *gorm.DB, order *PaymentOrder, generator func() (string, error)) error {
	if tx == nil || order == nil || generator == nil || strings.TrimSpace(order.TradeNo) != "" {
		return errors.New("invalid payment order trade number creation")
	}
	const savepoint = "payment_trade_no_create"
	for attempt := 0; attempt < paymentTradeNoCreateAttempts; attempt++ {
		tradeNo, err := generator()
		if err != nil {
			return err
		}
		var existingCount int64
		if err := tx.Model(&PaymentOrder{}).Where("trade_no = ?", tradeNo).Count(&existingCount).Error; err != nil {
			return err
		}
		if existingCount > 0 {
			continue
		}
		order.ID = 0
		order.TradeNo = tradeNo
		if err := tx.SavePoint(savepoint).Error; err != nil {
			return err
		}
		createErr := tx.Create(order).Error
		if createErr == nil {
			return nil
		}
		if rollbackErr := tx.RollbackTo(savepoint).Error; rollbackErr != nil {
			return fmt.Errorf("create payment order: %w; rollback savepoint: %v", createErr, rollbackErr)
		}
		order.ID = 0
		var collisionCount int64
		if err := tx.Model(&PaymentOrder{}).Where("trade_no = ?", tradeNo).Count(&collisionCount).Error; err != nil {
			return err
		}
		if collisionCount == 0 {
			return createErr
		}
		order.TradeNo = ""
	}
	order.TradeNo = ""
	return ErrPaymentTradeNoCollision
}

func GeneratePaymentQuoteID() (string, error) {
	key, err := common.GenerateRandomCharsKey(32)
	if err != nil {
		return "", err
	}
	return "Q" + key, nil
}

func CreatePaymentQuote(quote *PaymentQuote) error {
	if quote == nil || quote.QuoteID == "" || quote.UserID <= 0 {
		return errors.New("invalid payment quote")
	}
	if paymentProviderUsesEnvironmentBinding(quote.Provider) && quote.ProviderLivemode == nil {
		return fmt.Errorf("%s payment quote mode is required", quote.Provider)
	}
	if !paymentProviderUsesEnvironmentBinding(quote.Provider) && quote.ProviderLivemode != nil {
		return errors.New("payment quote mode is only valid for environment-bound providers")
	}
	if quote.CreatedAt == 0 {
		quote.CreatedAt = time.Now().Unix()
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := lockPaymentUserGuardTx(tx, quote.UserID); err != nil {
			return err
		}
		if err := ensurePaymentUserGuardActiveTx(tx, quote.UserID); err != nil {
			return err
		}
		now := time.Now().Unix()
		if err := cleanupPaymentQuotesTx(tx, quote.UserID, now); err != nil {
			return err
		}
		var activeCount int64
		if err := tx.Model(&PaymentQuote{}).
			Where("user_id = ? AND consumed_at = 0 AND (expires_at = 0 OR expires_at > ?)", quote.UserID, now).
			Count(&activeCount).Error; err != nil {
			return err
		}
		if activeCount >= PaymentMaxActiveQuotesPerUser {
			return ErrPaymentActiveQuoteLimit
		}
		return tx.Create(quote).Error
	})
}

func lockPaymentUserGuardTx(tx *gorm.DB, userID int) error {
	if tx == nil || userID <= 0 {
		return errors.New("invalid payment user guard")
	}
	guard := PaymentUserGuard{UserID: userID, UpdatedAt: time.Now().UnixNano()}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"updated_at"}),
	}).Create(&guard).Error
}

func ensurePaymentUserGuardActiveTx(tx *gorm.DB, userID int) error {
	var guard PaymentUserGuard
	if err := lockForUpdate(tx).Where("user_id = ?", userID).First(&guard).Error; err != nil {
		return err
	}
	if guard.Blocked {
		return ErrPaymentUserUnavailable
	}
	return nil
}

// cleanupPaymentQuotesTx retains expired and consumed quotes for a short audit
// window, then removes them opportunistically during that user's next payment
// mutation. Canonical orders retain their own immutable contract snapshots.
func cleanupPaymentQuotesTx(tx *gorm.DB, userID int, now int64) error {
	if tx == nil || userID <= 0 || now <= 0 {
		return errors.New("invalid payment quote cleanup request")
	}
	cutoff := now - PaymentQuoteAuditRetentionSeconds
	return tx.Where(
		"user_id = ? AND ((consumed_at > 0 AND consumed_at <= ?) OR (consumed_at = 0 AND expires_at > 0 AND expires_at <= ?))",
		userID, cutoff, cutoff,
	).Delete(&PaymentQuote{}).Error
}

type PaymentQuoteCleanupResult struct {
	Scanned int   `json:"scanned"`
	Deleted int64 `json:"deleted"`
}

// CleanupPaymentQuotes removes one global batch after the audit-retention
// window, including quotes belonging to users who never initiate payment
// again. The deletion predicate is rechecked atomically so an active quote can
// never be removed merely because it appeared in an earlier candidate scan.
func CleanupPaymentQuotes(ctx context.Context, now int64, batchSize int) (PaymentQuoteCleanupResult, error) {
	result := PaymentQuoteCleanupResult{}
	if ctx == nil {
		ctx = context.Background()
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	if batchSize <= 0 {
		batchSize = 200
	}
	if batchSize > 1000 {
		batchSize = 1000
	}
	cutoff := now - PaymentQuoteAuditRetentionSeconds
	var ids []int64
	if err := DB.WithContext(ctx).Model(&PaymentQuote{}).
		Where("(consumed_at > 0 AND consumed_at <= ?) OR (consumed_at = 0 AND expires_at > 0 AND expires_at <= ?)", cutoff, cutoff).
		Order("id ASC").Limit(batchSize).Pluck("id", &ids).Error; err != nil {
		return result, err
	}
	result.Scanned = len(ids)
	if len(ids) == 0 {
		return result, nil
	}
	deleted := DB.WithContext(ctx).Where("id IN ?", ids).
		Where("(consumed_at > 0 AND consumed_at <= ?) OR (consumed_at = 0 AND expires_at > 0 AND expires_at <= ?)", cutoff, cutoff).
		Delete(&PaymentQuote{})
	result.Deleted = deleted.RowsAffected
	return result, deleted.Error
}

// CreatePaymentOrderFromQuote atomically consumes a quote and creates one
// pending order. Retrying the same request ID returns the original order.
func CreatePaymentOrderFromQuote(userID int, quoteID, requestID string) (*PaymentOrder, error) {
	return createPaymentOrderFromQuote(userID, quoteID, requestID, 0)
}

func CreatePaymentOrderFromQuoteWithConfigurationVersion(userID int, quoteID, requestID string, expectedConfigurationVersion int64) (*PaymentOrder, error) {
	if expectedConfigurationVersion <= 0 {
		return nil, errors.New("payment configuration version must be positive")
	}
	return createPaymentOrderFromQuote(userID, quoteID, requestID, expectedConfigurationVersion)
}

func createPaymentOrderFromQuote(userID int, quoteID, requestID string, expectedConfigurationVersion int64) (*PaymentOrder, error) {
	if userID <= 0 || strings.TrimSpace(quoteID) == "" || strings.TrimSpace(requestID) == "" {
		return nil, errors.New("invalid payment order request")
	}
	var order PaymentOrder
	err := DB.Transaction(func(tx *gorm.DB) error {
		if expectedConfigurationVersion > 0 {
			configurationVersion, err := lockPaymentConfigurationFenceTx(tx)
			if err != nil {
				return err
			}
			if configurationVersion != expectedConfigurationVersion {
				return ErrPaymentConfigurationVersionConflict
			}
		}
		if err := lockPaymentUserGuardTx(tx, userID); err != nil {
			return err
		}
		if err := ensurePaymentUserGuardActiveTx(tx, userID); err != nil {
			return err
		}
		var activeUser User
		if err := lockForUpdate(tx).Select("id").Where("id = ? AND status = ?", userID, common.UserStatusEnabled).First(&activeUser).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentUserUnavailable
			}
			return err
		}
		now := time.Now().Unix()
		// The user guard makes it safe to check the idempotency key before the
		// quote. This also lets retries return the original order after the quote
		// has aged out of the short audit-retention window.
		// The payment user guard already serializes every order mutation for this
		// user. Avoid SELECT FOR UPDATE for a missing idempotency key: on MySQL
		// 5.7, concurrent first orders for different users can otherwise acquire
		// overlapping next-key gap locks on the empty composite unique index and
		// deadlock when both transactions insert.
		if err := tx.Where("user_id = ? AND request_id = ?", userID, requestID).First(&order).Error; err == nil {
			if order.QuoteID == quoteID {
				return nil
			}
			var retryQuote PaymentQuote
			if err := lockForUpdate(tx).Where("quote_id = ? AND user_id = ?", quoteID, userID).First(&retryQuote).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					// Pre-quote canonical orders cannot prove a quote identity. The
					// existing request ID is still authoritative: returning it is the
					// only behavior that cannot create a second upstream charge.
					if order.QuoteID == "" {
						return nil
					}
					return ErrPaymentIdempotencyConflict
				}
				return err
			}
			if !paymentOrderMatchesQuote(&order, &retryQuote) {
				return ErrPaymentIdempotencyConflict
			}
			if order.QuoteID == "" {
				if err := tx.Model(&PaymentOrder{}).Where("id = ? AND quote_id = ''", order.ID).
					Update("quote_id", retryQuote.QuoteID).Error; err != nil {
					return err
				}
				order.QuoteID = retryQuote.QuoteID
			}
			if retryQuote.ConsumedAt == 0 {
				if err := tx.Model(&PaymentQuote{}).Where("id = ? AND consumed_at = 0", retryQuote.ID).
					Update("consumed_at", now).Error; err != nil {
					return err
				}
			}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var quote PaymentQuote
		if err := lockForUpdate(tx).Where("quote_id = ? AND user_id = ?", quoteID, userID).First(&quote).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentQuoteNotFound
			}
			return err
		}
		if quote.ExpiresAt > 0 && quote.ExpiresAt <= now {
			return ErrPaymentQuoteExpired
		}
		if quote.ConsumedAt != 0 {
			return ErrPaymentQuoteConsumed
		}
		inFlightLimit := PaymentMaxInFlightOrdersPerUserProvider
		if quote.Provider == PaymentProviderStripe {
			var user User
			if err := lockForUpdate(tx).Select("id", "stripe_customer").Where("id = ?", userID).First(&user).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrPaymentUserUnavailable
				}
				return err
			}
			// Until Stripe has a reusable Customer, every Checkout Session created
			// with customer_creation=always can mint a different Customer. Keep a
			// single canonical order in flight so concurrent starts cannot create
			// mutually conflicting ownership bindings during webhook settlement.
			if strings.TrimSpace(user.StripeCustomer) == "" {
				inFlightLimit = 1
			}
		}
		var inFlightCount int64
		if err := tx.Model(&PaymentOrder{}).
			Where("user_id = ? AND provider = ? AND status IN ?", userID, quote.Provider, paymentInFlightOrderStatuses()).
			Count(&inFlightCount).Error; err != nil {
			return err
		}
		if inFlightCount >= int64(inFlightLimit) {
			return ErrPaymentInFlightOrderLimit
		}
		orderExpiresAt := quote.ExpiresAt
		if quote.Provider == PaymentProviderStripe {
			orderExpiresAt = now + PaymentStripeOrderTTLSeconds
		} else if quote.Provider == PaymentProviderCreem || quote.Provider == PaymentProviderWaffo ||
			quote.Provider == PaymentProviderWaffoPancake {
			orderExpiresAt = now + PaymentRetainedOrderTTLSeconds
		}
		var err error
		orderExpiresAt, err = boundPaymentOrderExpiryForLimitTx(tx, &quote, now, orderExpiresAt)
		if err != nil {
			return err
		}
		order = PaymentOrder{
			TradeNo:              "",
			UserID:               userID,
			OrderKind:            quote.OrderKind,
			Provider:             quote.Provider,
			PaymentMethod:        quote.PaymentMethod,
			ConfigurationVersion: expectedConfigurationVersion,
			ProviderLivemode:     copyPaymentLivemode(quote.ProviderLivemode),
			QuoteID:              quote.QuoteID,
			RequestID:            requestID,
			ExpectedAmountMinor:  quote.ExpectedAmountMinor,
			Currency:             quote.Currency,
			RequestedAmount:      quote.RequestedAmount,
			CreditQuota:          quote.CreditQuota,
			PricingSnapshot:      quote.PricingSnapshot,
			ProductSnapshot:      quote.ProductSnapshot,
			Status:               PaymentOrderStatusPending,
			ExpiresAt:            orderExpiresAt,
			CreatedAt:            now,
			UpdatedAt:            now,
			Version:              1,
		}
		if err := createPaymentOrderWithUniqueTradeNoTx(tx, &order, GeneratePaymentTradeNo); err != nil {
			return err
		}
		if _, err := ensurePaymentTaskTx(tx, order.ID, PaymentTaskOperationCreate, now); err != nil {
			return err
		}
		if err := reservePaymentLimitTxAt(tx, &order, now); err != nil {
			return err
		}
		if order.OrderKind == PaymentOrderKindTopUp {
			money := float64(order.ExpectedAmountMinor) / float64(common.PaymentProviderCurrencyMinorUnit(order.Provider, order.Currency))
			topUp := &TopUp{
				PaymentOrderId:      &order.ID,
				UserId:              order.UserID,
				Amount:              order.RequestedAmount,
				Money:               money,
				TradeNo:             order.TradeNo,
				PaymentMethod:       order.PaymentMethod,
				PaymentProvider:     order.Provider,
				Currency:            order.Currency,
				ExpectedAmountMinor: order.ExpectedAmountMinor,
				CreditQuotaSnapshot: order.CreditQuota,
				CreateTime:          now,
				Status:              common.TopUpStatusPending,
			}
			if err := tx.Create(topUp).Error; err != nil {
				return err
			}
			order.LegacyRecordType = PaymentOrderKindTopUp
			order.LegacyRecordID = topUp.Id
			if err := tx.Model(&order).Updates(map[string]interface{}{
				"legacy_record_type": PaymentOrderKindTopUp,
				"legacy_record_id":   topUp.Id,
			}).Error; err != nil {
				return err
			}
		} else if order.OrderKind == PaymentOrderKindSubscription {
			var snapshot SubscriptionPlanSnapshot
			if err := common.UnmarshalJsonStr(order.ProductSnapshot, &snapshot); err != nil {
				return ErrSubscriptionOrderSnapshotMissing
			}
			if _, err := snapshot.SubscriptionPlan(); err != nil {
				return err
			}
			if err := reserveSubscriptionPurchaseTx(tx, userID, &snapshot); err != nil {
				return err
			}
			subscriptionOrder := &SubscriptionOrder{
				PaymentOrderId:      &order.ID,
				UserId:              userID,
				PlanId:              snapshot.PlanId,
				Money:               float64(order.ExpectedAmountMinor) / float64(common.PaymentProviderCurrencyMinorUnit(order.Provider, order.Currency)),
				PlanSnapshot:        order.ProductSnapshot,
				ExpectedAmountMinor: order.ExpectedAmountMinor,
				PaymentCurrency:     order.Currency,
				ReserveUntil:        order.ExpiresAt,
				TradeNo:             order.TradeNo,
				PaymentMethod:       order.PaymentMethod,
				PaymentProvider:     order.Provider,
				Status:              common.TopUpStatusPending,
				CreateTime:          now,
			}
			if err := tx.Create(subscriptionOrder).Error; err != nil {
				return err
			}
			order.LegacyRecordType = PaymentOrderKindSubscription
			order.LegacyRecordID = subscriptionOrder.Id
			if err := tx.Model(&order).Updates(map[string]interface{}{
				"legacy_record_type": PaymentOrderKindSubscription,
				"legacy_record_id":   subscriptionOrder.Id,
			}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&quote).Updates(map[string]interface{}{"consumed_at": now}).Error
	})
	if err != nil {
		// The unique index remains the final arbiter for rows created by older
		// nodes that do not acquire PaymentUserGuard. Return a matching durable
		// order instead of leaking a dialect-specific duplicate-key error.
		var existing PaymentOrder
		if lookupErr := DB.Where("user_id = ? AND request_id = ?", userID, requestID).First(&existing).Error; lookupErr == nil {
			if existing.QuoteID == quoteID {
				return &existing, nil
			}
			var quote PaymentQuote
			if quoteErr := DB.Where("quote_id = ? AND user_id = ?", quoteID, userID).First(&quote).Error; quoteErr == nil && paymentOrderMatchesQuote(&existing, &quote) {
				_ = DB.Model(&PaymentQuote{}).Where("id = ? AND consumed_at = 0", quote.ID).
					Update("consumed_at", time.Now().Unix()).Error
				return &existing, nil
			}
			// A pre-quote legacy order with the same request ID remains the safer
			// result when its original quote no longer exists: never create a second
			// upstream charge merely because old audit state was cleaned up.
			if existing.QuoteID == "" && !errors.Is(err, ErrPaymentIdempotencyConflict) {
				return &existing, nil
			}
			return nil, ErrPaymentIdempotencyConflict
		}
		return nil, err
	}
	return &order, nil
}

func paymentOrderMatchesQuote(order *PaymentOrder, quote *PaymentQuote) bool {
	if order == nil || quote == nil {
		return false
	}
	return order.UserID == quote.UserID && order.OrderKind == quote.OrderKind && order.Provider == quote.Provider &&
		order.PaymentMethod == quote.PaymentMethod && order.ExpectedAmountMinor == quote.ExpectedAmountMinor &&
		strings.EqualFold(order.Currency, quote.Currency) && order.RequestedAmount == quote.RequestedAmount &&
		order.CreditQuota == quote.CreditQuota && order.PricingSnapshot == quote.PricingSnapshot &&
		order.ProductSnapshot == quote.ProductSnapshot && paymentLivemodeEqual(order.ProviderLivemode, quote.ProviderLivemode)
}

func copyPaymentLivemode(value *bool) *bool {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func paymentLivemodeEqual(left, right *bool) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func paymentInFlightOrderStatuses() []string {
	return []string{
		PaymentOrderStatusPending,
		PaymentOrderStatusProcessing,
		PaymentOrderStatusManualReview,
	}
}

func GetPaymentOrderByTradeNo(tradeNo string) (*PaymentOrder, error) {
	var order PaymentOrder
	if err := DB.Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPaymentOrderNotFound
		}
		return nil, err
	}
	return &order, nil
}

func GetPaymentOrderByID(id int64) (*PaymentOrder, error) {
	if id <= 0 {
		return nil, ErrPaymentOrderNotFound
	}
	var order PaymentOrder
	if err := DB.Where("id = ?", id).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPaymentOrderNotFound
		}
		return nil, err
	}
	return &order, nil
}

func GetPaymentOrderForUser(userID int, tradeNo string) (*PaymentOrder, error) {
	var order PaymentOrder
	if err := DB.Where("user_id = ? AND trade_no = ?", userID, tradeNo).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPaymentOrderNotFound
		}
		return nil, err
	}
	return &order, nil
}

func GetPaymentOrderByRequestID(userID int, requestID string) (*PaymentOrder, error) {
	requestID = strings.TrimSpace(requestID)
	if userID <= 0 || requestID == "" {
		return nil, ErrPaymentOrderNotFound
	}
	var order PaymentOrder
	if err := DB.Where("user_id = ? AND request_id = ?", userID, requestID).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPaymentOrderNotFound
		}
		return nil, err
	}
	return &order, nil
}

func GetPaymentOrderByProviderPaymentKey(providerPaymentKey string) (*PaymentOrder, error) {
	if strings.TrimSpace(providerPaymentKey) == "" {
		return nil, ErrPaymentOrderNotFound
	}
	var order PaymentOrder
	if err := DB.Where("provider_payment_key = ?", providerPaymentKey).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPaymentOrderNotFound
		}
		return nil, err
	}
	return &order, nil
}

func CountPaymentOrdersForProvider(provider string, statuses []string) (int64, error) {
	query := DB.Model(&PaymentOrder{}).Where("provider = ?", strings.TrimSpace(provider))
	if len(statuses) > 0 {
		query = query.Where("status IN ?", statuses)
	}
	var count int64
	err := query.Count(&count).Error
	return count, err
}

// CountPaymentOrdersDependingOnCallbackOrigin reports canonical and
// standalone legacy orders that still require the currently configured
// callback origin or another configuration value that is bound at provider
// creation time. Providers with recoverable callback-delivery windows retain
// recently started failed or expired orders; Stripe only needs active orders
// here because its webhook endpoint is configured outside each Checkout.
func CountPaymentOrdersDependingOnCallbackOrigin(provider string, now int64) (int64, error) {
	return countPaymentOrdersDependingOnCallbackOriginTx(DB, provider, now)
}

func countLegacyActivePaymentProjectionsTx(tx *gorm.DB, provider string, includeEmptyProvider bool) (int64, error) {
	return countLegacyPaymentProjectionsTx(tx, provider, includeEmptyProvider, 0)
}

func CountActivePaymentOrdersForProvider(provider string) (int64, error) {
	return countActivePaymentOrdersForProviderTx(DB, provider)
}

func countActivePaymentOrdersForProviderTx(tx *gorm.DB, provider string) (int64, error) {
	provider = strings.TrimSpace(provider)
	if tx == nil || !paymentConfigurationPreconditionProviderSupported(provider) {
		return 0, errors.New("invalid active payment order lookup")
	}
	activeStatuses := []string{PaymentOrderStatusPending, PaymentOrderStatusProcessing, PaymentOrderStatusManualReview}
	var canonicalCount int64
	if err := tx.Model(&PaymentOrder{}).Where("provider = ? AND status IN ?", provider, activeStatuses).
		Count(&canonicalCount).Error; err != nil {
		return 0, err
	}
	legacyCount, err := countLegacyActivePaymentProjectionsTx(tx, provider, provider == PaymentProviderEpay)
	if err != nil {
		return 0, err
	}
	return canonicalCount + legacyCount, nil
}

func paymentConfigurationPreconditionProviderSupported(provider string) bool {
	switch strings.TrimSpace(provider) {
	case PaymentProviderEpay, PaymentProviderStripe, PaymentProviderXorPay, PaymentProviderCreem,
		PaymentProviderWaffo, PaymentProviderWaffoPancake:
		return true
	default:
		return false
	}
}

func paymentProviderUsesCurrentOnlyCredentials(provider string) bool {
	switch strings.TrimSpace(provider) {
	case PaymentProviderCreem, PaymentProviderWaffo, PaymentProviderWaffoPancake:
		return true
	default:
		return false
	}
}

func paymentProviderUsesEnvironmentBinding(provider string) bool {
	switch strings.TrimSpace(provider) {
	case PaymentProviderStripe, PaymentProviderCreem, PaymentProviderWaffoPancake:
		return true
	default:
		return false
	}
}

func countPaymentOrdersDependingOnCallbackOriginTx(tx *gorm.DB, provider string, now int64) (int64, error) {
	query, recoveryCutoff, err := paymentOrdersDependingOnConfigurationQueryTx(tx, provider, now)
	if err != nil {
		return 0, err
	}
	var canonicalCount int64
	if err := query.Count(&canonicalCount).Error; err != nil {
		return 0, err
	}
	legacyCount, err := countLegacyPaymentProjectionsTx(tx, strings.TrimSpace(provider), strings.TrimSpace(provider) == PaymentProviderEpay, recoveryCutoff)
	if err != nil {
		return 0, err
	}
	return canonicalCount + legacyCount, nil
}

func paymentOrdersDependingOnConfigurationQueryTx(tx *gorm.DB, provider string, now int64) (*gorm.DB, int64, error) {
	provider = strings.TrimSpace(provider)
	if tx == nil || now <= 0 || !paymentConfigurationPreconditionProviderSupported(provider) {
		return nil, 0, errors.New("invalid callback-dependent payment order lookup")
	}
	activeStatuses := []string{PaymentOrderStatusPending, PaymentOrderStatusProcessing, PaymentOrderStatusManualReview}
	query := tx.Model(&PaymentOrder{}).Where("provider = ?", provider)
	if provider == PaymentProviderStripe {
		return query.Where("status IN ?", activeStatuses), 0, nil
	}
	recoveryCutoff := now - int64(PaymentCallbackRecoveryWindow/time.Second)
	if recoveryCutoff <= 0 {
		recoveryCutoff = 1
	}
	return query.Where(
		"(status IN ? OR (status IN ? AND started_at > 0 AND (updated_at >= ? OR expires_at >= ? OR created_at >= ?)))",
		activeStatuses,
		[]string{PaymentOrderStatusFailed, PaymentOrderStatusExpired},
		recoveryCutoff, recoveryCutoff, recoveryCutoff,
	), recoveryCutoff, nil
}

func countLegacyPaymentProjectionsTx(tx *gorm.DB, provider string, includeEmptyProvider bool, recoveryCutoff int64) (int64, error) {
	if tx == nil || strings.TrimSpace(provider) == "" {
		return 0, errors.New("invalid legacy payment projection lookup")
	}
	activeStatuses := []string{common.TopUpStatusPending, PaymentOrderStatusProcessing, common.TopUpStatusManualReview}
	countProjection := func(modelValue interface{}) (int64, error) {
		if !tx.Migrator().HasTable(modelValue) {
			return 0, nil
		}
		query := tx.Model(modelValue).Where("(payment_order_id IS NULL OR payment_order_id = 0)")
		if recoveryCutoff > 0 {
			query = query.Where(
				"(status IN ? OR (status IN ? AND (complete_time >= ? OR create_time >= ?)))",
				activeStatuses,
				[]string{common.TopUpStatusFailed, common.TopUpStatusExpired},
				recoveryCutoff, recoveryCutoff,
			)
		} else {
			query = query.Where("status IN ?", activeStatuses)
		}
		switch provider {
		case PaymentProviderEpay:
			if includeEmptyProvider {
				query = query.Where(
					"(payment_provider = ? OR (payment_provider = '' AND payment_method <> '' AND payment_method NOT IN ?))",
					provider,
					[]string{
						PaymentMethodStripe, PaymentMethodCreem, PaymentMethodWaffo,
						PaymentMethodWaffoPancake, PaymentMethodXorPayNative,
						PaymentMethodXorPayAlipay, PaymentMethodXorPayJSAPI, PaymentMethodBalance,
					},
				)
			} else {
				query = query.Where("payment_provider = ?", provider)
			}
		case PaymentProviderStripe:
			query = query.Where("(payment_provider = ? OR payment_method = ?)", provider, PaymentMethodStripe)
		case PaymentProviderXorPay:
			query = query.Where(
				"(payment_provider = ? OR payment_method IN ?)",
				provider, []string{PaymentMethodXorPayNative, PaymentMethodXorPayAlipay, PaymentMethodXorPayJSAPI},
			)
		default:
			query = query.Where("payment_provider = ?", provider)
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return 0, err
		}
		return count, nil
	}
	topUpCount, err := countProjection(&TopUp{})
	if err != nil {
		return 0, err
	}
	subscriptionCount, err := countProjection(&SubscriptionOrder{})
	if err != nil {
		return 0, err
	}
	return topUpCount + subscriptionCount, nil
}

func CountRecentPaymentOrdersForProvider(provider string, createdSince int64) (int64, error) {
	if createdSince <= 0 {
		return 0, errors.New("invalid payment order retention boundary")
	}
	var count int64
	err := DB.Model(&PaymentOrder{}).
		Where("provider = ? AND created_at >= ?", strings.TrimSpace(provider), createdSince).
		Count(&count).Error
	return count, err
}

func BindPaymentOrderCredentialGeneration(tradeNo string, generation, expectedConfigurationVersion int64) error {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" || generation <= 0 || expectedConfigurationVersion <= 0 {
		return errors.New("invalid payment credential generation binding")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		configurationVersion, err := lockPaymentConfigurationFenceTx(tx)
		if err != nil {
			return err
		}
		if configurationVersion != expectedConfigurationVersion {
			return ErrPaymentConfigurationVersionConflict
		}
		var order PaymentOrder
		if err := lockForUpdate(tx).Select("id", "status", "start_payload", "provider_credential_generation").
			Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentOrderNotFound
			}
			return err
		}
		if order.StartPayload != "" || order.Status != PaymentOrderStatusPending && order.Status != PaymentOrderStatusProcessing {
			return fmt.Errorf("payment order is no longer startable: %s", order.Status)
		}
		if order.ProviderCredentialGeneration == generation {
			return nil
		}
		if order.ProviderCredentialGeneration != 0 {
			return errors.New("payment order is already bound to another credential generation")
		}
		updated := tx.Model(&PaymentOrder{}).
			Where("id = ? AND provider_credential_generation = ? AND status IN ? AND start_payload = ?", order.ID, 0,
				[]string{PaymentOrderStatusPending, PaymentOrderStatusProcessing}, "").
			Update("provider_credential_generation", generation)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("payment order credential generation binding was superseded")
		}
		return nil
	})
}

func lockPaymentConfigurationFenceTx(tx *gorm.DB) (int64, error) {
	if tx == nil {
		return 0, errors.New("payment configuration transaction is required")
	}
	initial := Option{Key: PaymentConfigurationVersionOptionKey, Value: strconv.FormatInt(initialPaymentConfigurationVersion, 10)}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoNothing: true,
	}).Create(&initial).Error; err != nil {
		return 0, err
	}
	var version Option
	if err := lockForUpdate(tx).Where(fmt.Sprintf("%s = ?", optionKeyColumn()), PaymentConfigurationVersionOptionKey).
		First(&version).Error; err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(version.Value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid stored payment configuration version %q", version.Value)
	}
	return parsed, nil
}

func paymentCredentialGenerationAvailableTx(tx *gorm.DB, provider string, generation, createdAt, now int64) (bool, error) {
	if tx == nil || generation <= 0 || createdAt <= 0 || now <= 0 {
		return false, nil
	}
	var currentKey, previousKey, previousValidBeforeKey, previousExpiresAtKey string
	switch strings.TrimSpace(provider) {
	case PaymentProviderEpay:
		currentKey = "EpayCredentialGeneration"
		previousKey = "EpayPreviousCredentialGeneration"
		previousValidBeforeKey = "EpayPreviousValidBefore"
		previousExpiresAtKey = "EpayPreviousExpiresAt"
	case PaymentProviderXorPay:
		currentKey = "XorPayCredentialGeneration"
		previousKey = "XorPayPreviousCredentialGeneration"
		previousValidBeforeKey = "XorPayPreviousValidBefore"
		previousExpiresAtKey = "XorPayPreviousExpiresAt"
	case PaymentProviderStripe:
		currentKey = "StripeWebhookCredentialGeneration"
		previousKey = "StripeWebhookPreviousCredentialGeneration"
		previousValidBeforeKey = "StripeWebhookPreviousValidBefore"
		previousExpiresAtKey = "StripeWebhookSecretPreviousExpiresAt"
	default:
		return true, nil
	}
	keys := []string{currentKey, previousKey, previousValidBeforeKey, previousExpiresAtKey}
	var options []Option
	if err := tx.Where(fmt.Sprintf("%s IN ?", optionKeyColumn()), keys).Find(&options).Error; err != nil {
		return false, err
	}
	values := make(map[string]int64, len(options))
	found := make(map[string]bool, len(options))
	for _, option := range options {
		parsed, err := strconv.ParseInt(strings.TrimSpace(option.Value), 10, 64)
		if err != nil || parsed < 0 {
			return false, fmt.Errorf("invalid stored payment credential boundary %s", option.Key)
		}
		values[option.Key] = parsed
		found[option.Key] = true
	}
	if provider == PaymentProviderStripe && !found[currentKey] {
		// Upgrade compatibility: installations that already have a Stripe
		// webhook secret predate persisted generations. Current v2 stays
		// distinct from a still-active legacy previous secret at v1.
		values[currentKey] = 2
	}
	if provider == PaymentProviderStripe && !found[previousKey] && values[previousExpiresAtKey] > 0 {
		values[previousKey] = 1
	}
	if provider == PaymentProviderStripe && !found[previousValidBeforeKey] && values[previousExpiresAtKey] > 0 {
		legacyValidBefore := values[previousExpiresAtKey] - int64(setting.StripeWebhookSecretOverlap/time.Second)
		if legacyValidBefore > 0 {
			values[previousValidBeforeKey] = legacyValidBefore
		}
	}
	if generation == values[currentKey] && generation > 0 {
		return true, nil
	}
	return generation == values[previousKey] && generation > 0 && createdAt <= values[previousValidBeforeKey] && now < values[previousExpiresAtKey], nil
}

func PaymentCredentialGenerationAvailable(provider string, generation, createdAt int64) (bool, error) {
	available := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if _, err := lockPaymentConfigurationFenceTx(tx); err != nil {
			return err
		}
		var err error
		available, err = paymentCredentialGenerationAvailableTx(tx, provider, generation, createdAt, common.GetTimestamp())
		return err
	})
	return available, err
}

func MarkPaymentOrderCredentialGenerationManualReview(tradeNo string) error {
	return MarkPaymentOrderManualReview(tradeNo, "provider credential generation is no longer available; verify payment manually")
}

func MarkPaymentOrderManualReview(tradeNo, reason string) error {
	return markPaymentOrderManualReview(tradeNo, reason, 0, nil, "")
}

func MarkPaymentOrderManualReviewFenced(tradeNo, reason string, creationFenceToken int64) error {
	if creationFenceToken <= 0 {
		return errors.New("payment creation fence is required")
	}
	return markPaymentOrderManualReview(tradeNo, reason, creationFenceToken, nil, "")
}

// MarkPaymentOrderManualReviewForTask changes an order only while the caller
// still owns the durable task lease. Lease validation and the order mutation
// share one transaction, preventing a stale reconcile worker from blocking a
// newer worker that has already observed a successful payment.
func MarkPaymentOrderManualReviewForTask(tradeNo, reason string, task *PaymentTask, runnerID string) error {
	return markPaymentOrderManualReview(tradeNo, reason, 0, task, runnerID)
}

func markPaymentOrderManualReview(tradeNo, reason string, creationFenceToken int64, task *PaymentTask, runnerID string) error {
	tradeNo = strings.TrimSpace(tradeNo)
	reason = strings.TrimSpace(reason)
	if tradeNo == "" || reason == "" || len(reason) > 512 {
		return ErrPaymentOrderNotFound
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if task != nil {
			if err := assertPaymentTaskLeaseTx(tx, task, runnerID); err != nil {
				return err
			}
		}
		var order PaymentOrder
		if err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentOrderNotFound
			}
			return err
		}
		if order.Status != PaymentOrderStatusPending && order.Status != PaymentOrderStatusProcessing {
			return nil
		}
		if task != nil {
			if task.PaymentOrderID != order.ID {
				return ErrPaymentTaskLeaseLost
			}
			if task.Operation == PaymentTaskOperationCreate {
				if task.FenceToken <= 0 || order.CreationFenceToken != task.FenceToken {
					return ErrPaymentTaskLeaseLost
				}
				creationFenceToken = task.FenceToken
			}
		}
		if creationFenceToken > 0 && order.CreationFenceToken != creationFenceToken {
			return ErrPaymentTaskLeaseLost
		}
		now := common.GetTimestamp()
		query := tx.Model(&PaymentOrder{}).Where("id = ?", order.ID)
		if creationFenceToken > 0 {
			query = query.Where("creation_fence_token = ?", creationFenceToken)
		}
		updated := query.Updates(map[string]interface{}{
			"status": PaymentOrderStatusManualReview, "status_reason": reason,
			"start_payload": "", "browser_authorization_digest": nil,
			"browser_authorization_payload": "", "browser_authorization_expires_at": 0,
			"browser_authorized_at": 0, "updated_at": now, "version": gorm.Expr("version + ?", 1),
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 && creationFenceToken > 0 {
			return ErrPaymentTaskLeaseLost
		}
		order.Status = PaymentOrderStatusManualReview
		order.StatusReason = reason
		return syncPaymentProjectionStatusTx(tx, &order)
	})
}

func StripeCustomerForCheckout(userID int, candidate string) (string, error) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return "", nil
	}
	if userID <= 0 || !strings.HasPrefix(candidate, "cus_") || len(candidate) > 64 {
		return "", ErrPaymentManualReview
	}
	resolved := ""
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).Select("id", "stripe_customer").Where("id = ?", userID).First(&user).Error; err != nil {
			return err
		}
		if strings.TrimSpace(user.StripeCustomer) != candidate {
			return ErrPaymentManualReview
		}

		var userBinding PaymentCustomerBinding
		userBindingQuery := lockForUpdate(tx).
			Where("provider = ? AND user_id = ?", PaymentProviderStripe, userID).
			Limit(1).Find(&userBinding)
		if userBindingQuery.Error != nil {
			return userBindingQuery.Error
		}
		if userBindingQuery.RowsAffected > 0 {
			if userBinding.CustomerKey != candidate {
				return ErrPaymentManualReview
			}
			var owner PaymentCustomerBinding
			ownerQuery := tx.Where("provider = ? AND customer_key = ?", PaymentProviderStripe, candidate).
				Limit(1).Find(&owner)
			if ownerQuery.Error != nil {
				return ownerQuery.Error
			}
			if ownerQuery.RowsAffected != 1 || owner.UserID != userID {
				return ErrPaymentManualReview
			}
			resolved = candidate
			return nil
		}

		var otherUser User
		otherUserQuery := tx.Select("id").Where("stripe_customer = ? AND id <> ?", candidate, userID).
			Limit(1).Find(&otherUser)
		if otherUserQuery.Error != nil {
			return otherUserQuery.Error
		}
		if otherUserQuery.RowsAffected > 0 {
			return ErrPaymentManualReview
		}
		binding := PaymentCustomerBinding{Provider: PaymentProviderStripe, CustomerKey: candidate, UserID: userID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&binding).Error; err != nil {
			return err
		}
		var stored PaymentCustomerBinding
		storedQuery := lockForUpdate(tx).Where("provider = ? AND customer_key = ?", PaymentProviderStripe, candidate).
			Limit(1).Find(&stored)
		if storedQuery.Error != nil {
			return storedQuery.Error
		}
		if storedQuery.RowsAffected != 1 || stored.UserID != userID {
			return ErrPaymentManualReview
		}
		var byUser PaymentCustomerBinding
		byUserQuery := lockForUpdate(tx).Where("provider = ? AND user_id = ?", PaymentProviderStripe, userID).
			Limit(1).Find(&byUser)
		if byUserQuery.Error != nil {
			return byUserQuery.Error
		}
		if byUserQuery.RowsAffected != 1 || byUser.CustomerKey != candidate {
			return ErrPaymentManualReview
		}
		resolved = candidate
		return nil
	})
	return resolved, err
}

// LegacyPendingPaymentCreatedAt returns the creation time of an old payment
// projection that predates canonical PaymentOrder rows. It is used only to
// scope previous-credential verification during migration.
func LegacyPendingPaymentCreatedAt(provider, tradeNo string) (int64, bool, error) {
	provider = strings.TrimSpace(provider)
	tradeNo = strings.TrimSpace(tradeNo)
	if provider == "" || tradeNo == "" {
		return 0, false, errors.New("invalid legacy payment lookup")
	}
	if DB.Migrator().HasTable(&TopUp{}) {
		var topUp TopUp
		topUpQuery := DB.Select("create_time").Where("trade_no = ? AND status = ?", tradeNo, common.TopUpStatusPending)
		if provider == PaymentProviderEpay {
			topUpQuery = topUpQuery.Where("payment_provider = ? OR payment_provider = ''", provider)
		} else {
			topUpQuery = topUpQuery.Where("payment_provider = ?", provider)
		}
		if err := topUpQuery.First(&topUp).Error; err == nil {
			return topUp.CreateTime, true, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, false, err
		}
	}

	if DB.Migrator().HasTable(&SubscriptionOrder{}) {
		var subscription SubscriptionOrder
		subscriptionQuery := DB.Select("create_time").Where("trade_no = ? AND status = ?", tradeNo, common.TopUpStatusPending)
		if provider == PaymentProviderEpay {
			subscriptionQuery = subscriptionQuery.Where("payment_provider = ? OR payment_provider = ''", provider)
		} else {
			subscriptionQuery = subscriptionQuery.Where("payment_provider = ?", provider)
		}
		if err := subscriptionQuery.First(&subscription).Error; err == nil {
			return subscription.CreateTime, true, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, false, err
		}
	}
	return 0, false, nil
}

const paymentOrderStartLeaseSeconds int64 = 60

func ClaimPaymentOrderStart(tradeNo string) (bool, error) {
	now := time.Now().Unix()
	result := DB.Model(&PaymentOrder{}).
		Where("trade_no = ? AND start_payload = '' AND (status = ? OR (status = ? AND started_at <= ?))",
			tradeNo, PaymentOrderStatusPending, PaymentOrderStatusProcessing, now-paymentOrderStartLeaseSeconds).
		Updates(map[string]interface{}{
			"status":     PaymentOrderStatusProcessing,
			"started_at": now,
			"updated_at": now,
			"version":    gorm.Expr("version + ?", 1),
		})
	return result.RowsAffected == 1, result.Error
}

func ClaimPaymentOrderQuery(tradeNo string, minimumInterval time.Duration) (bool, error) {
	intervalSeconds := int64(minimumInterval / time.Second)
	if intervalSeconds < 1 {
		intervalSeconds = 1
	}
	now := time.Now().Unix()
	result := DB.Model(&PaymentOrder{}).
		Where("trade_no = ? AND status IN ? AND provider_checked_at <= ?", tradeNo,
			[]string{PaymentOrderStatusPending, PaymentOrderStatusProcessing}, now-intervalSeconds).
		Update("provider_checked_at", now)
	return result.RowsAffected == 1, result.Error
}

func SavePaymentOrderStart(tradeNo, flow, payload string, expiresAt int64) error {
	return SavePaymentOrderStartWithProviderIdentity(tradeNo, flow, payload, expiresAt, "", "")
}

// SavePaymentOrderStartWithProviderIdentity atomically commits the provider
// identity, provider-adjusted expiry and encrypted client start snapshot. A
// process crash or local database failure therefore cannot leave a durable
// provider identity without the QR/form/redirect data needed to resume the
// same payment intent.
func SavePaymentOrderStartWithProviderIdentity(tradeNo, flow, payload string, expiresAt int64,
	providerOrderKey, providerPaymentKey string) error {
	return savePaymentOrderStartWithProviderIdentity(tradeNo, flow, payload, expiresAt,
		providerOrderKey, providerPaymentKey, 0)
}

// SavePaymentOrderStartWithProviderIdentityFenced is the asynchronous worker
// variant. The creation fence is advanced whenever another node takes over the
// durable task; an old worker therefore cannot publish stale QR or redirect
// data after its lease has expired.
func SavePaymentOrderStartWithProviderIdentityFenced(tradeNo, flow, payload string, expiresAt int64,
	providerOrderKey, providerPaymentKey string, creationFenceToken int64) error {
	if creationFenceToken <= 0 {
		return errors.New("payment creation fence is required")
	}
	return savePaymentOrderStartWithProviderIdentity(tradeNo, flow, payload, expiresAt,
		providerOrderKey, providerPaymentKey, creationFenceToken)
}

func savePaymentOrderStartWithProviderIdentity(tradeNo, flow, payload string, expiresAt int64,
	providerOrderKey, providerPaymentKey string, creationFenceToken int64) error {
	if strings.TrimSpace(flow) == "" || strings.TrimSpace(payload) == "" || len(payload) > 32<<10 {
		return errors.New("invalid payment start snapshot")
	}
	providerOrderKey = strings.TrimSpace(providerOrderKey)
	providerPaymentKey = strings.TrimSpace(providerPaymentKey)
	if len(providerOrderKey) > PaymentProviderAuthorityKeyMaxLength ||
		len(providerPaymentKey) > PaymentProviderAuthorityKeyMaxLength ||
		expiresAt > 0 && expiresAt <= time.Now().Unix() {
		return errors.New("invalid payment provider start identity")
	}
	encryptedPayload, err := EncryptPaymentOrderStartPayload(tradeNo, payload)
	if err != nil {
		return err
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var order PaymentOrder
		if err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentOrderNotFound
			}
			return err
		}
		if order.Status == PaymentOrderStatusFulfilled || order.Status == PaymentOrderStatusRefunded ||
			order.Status == PaymentOrderStatusRefundPending || order.Status == PaymentOrderStatusDisputed || order.Status == PaymentOrderStatusDebt {
			return nil
		}
		if order.Status != PaymentOrderStatusPending && order.Status != PaymentOrderStatusProcessing {
			return ErrPaymentOrderNotFound
		}
		if creationFenceToken > 0 && order.CreationFenceToken != creationFenceToken {
			return ErrPaymentTaskLeaseLost
		}
		if order.ExpiresAt > 0 && expiresAt > order.ExpiresAt {
			// Provider responses are state, not permission to lengthen the
			// server-authoritative payment window or its limit reservation.
			expiresAt = order.ExpiresAt
		}
		if err := bindAndValidateProviderIdentityTx(tx, &order, PaymentEventInput{
			ProviderOrderKey: providerOrderKey, ProviderPaymentKey: providerPaymentKey,
		}); err != nil {
			return err
		}
		now := time.Now().Unix()
		updates := map[string]interface{}{
			"status": PaymentOrderStatusPending, "start_flow": flow,
			"start_payload": encryptedPayload, "browser_authorization_digest": nil,
			"browser_authorization_payload": "", "browser_authorization_expires_at": 0,
			"browser_authorized_at": 0, "updated_at": now,
		}
		if expiresAt > 0 {
			updates["expires_at"] = expiresAt
		}
		query := tx.Model(&PaymentOrder{}).Where("id = ? AND status IN ?", order.ID,
			[]string{PaymentOrderStatusPending, PaymentOrderStatusProcessing})
		if creationFenceToken > 0 {
			query = query.Where("creation_fence_token = ?", creationFenceToken)
		}
		updated := query.Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 && creationFenceToken > 0 {
			return ErrPaymentTaskLeaseLost
		}
		if updated.RowsAffected == 0 {
			return ErrPaymentOrderNotFound
		}
		if expiresAt > 0 && order.OrderKind == PaymentOrderKindSubscription {
			if err := tx.Model(&SubscriptionOrder{}).
				Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
				Where("status = ?", common.TopUpStatusPending).
				Update("reserve_until", expiresAt).Error; err != nil {
				return err
			}
		}
		if expiresAt > 0 {
			if err := updatePaymentLimitReservationExpiryTx(tx, order.ID, expiresAt); err != nil {
				return err
			}
		}
		return nil
	})
}

func ExpirePaymentOrderIfDue(userID int, tradeNo string) (*PaymentOrder, error) {
	return expirePaymentOrderIfDue(userID, tradeNo, nil, "")
}

// ExpirePaymentOrderIfDueForTask keeps expiry and task ownership atomic for
// worker-driven order transitions.
func ExpirePaymentOrderIfDueForTask(userID int, tradeNo string, task *PaymentTask, runnerID string) (*PaymentOrder, error) {
	return expirePaymentOrderIfDue(userID, tradeNo, task, runnerID)
}

func expirePaymentOrderIfDue(userID int, tradeNo string, task *PaymentTask, runnerID string) (*PaymentOrder, error) {
	var order PaymentOrder
	err := DB.Transaction(func(tx *gorm.DB) error {
		if task != nil {
			if err := assertPaymentTaskLeaseTx(tx, task, runnerID); err != nil {
				return err
			}
		}
		if err := lockForUpdate(tx).Where("user_id = ? AND trade_no = ?", userID, tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentOrderNotFound
			}
			return err
		}
		_, err := expirePaymentOrderTx(tx, &order, time.Now().Unix())
		return err
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

type PaymentOrderExpirySweepResult struct {
	Scanned int `json:"scanned"`
	Expired int `json:"expired"`
	Skipped int `json:"skipped"`
}

// ExpireDuePaymentOrders expires one bounded batch of unattended canonical
// orders. Each candidate is rechecked and updated transactionally, so a paid
// callback racing the sweep wins without being overwritten. Expired orders are
// intentionally retained: a later verified paid event can still recover them.
func ExpireDuePaymentOrders(ctx context.Context, now int64, batchSize int) (PaymentOrderExpirySweepResult, error) {
	result := PaymentOrderExpirySweepResult{}
	if ctx == nil {
		ctx = context.Background()
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	if batchSize > 500 {
		batchSize = 500
	}
	var ids []int64
	if err := DB.WithContext(ctx).Model(&PaymentOrder{}).
		Where("status IN ? AND expires_at > 0 AND expires_at <= ?", []string{PaymentOrderStatusPending, PaymentOrderStatusProcessing}, now).
		Order("id ASC").Limit(batchSize).Pluck("id", &ids).Error; err != nil {
		return result, err
	}
	result.Scanned = len(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		expired := false
		err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var order PaymentOrder
			if err := lockForUpdate(tx).Where("id = ?", id).First(&order).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			var err error
			expired, err = expirePaymentOrderTx(tx, &order, now)
			return err
		})
		if err != nil {
			return result, err
		}
		if expired {
			result.Expired++
		} else {
			result.Skipped++
		}
	}
	return result, nil
}

func expirePaymentOrderTx(tx *gorm.DB, order *PaymentOrder, now int64) (bool, error) {
	if tx == nil || order == nil || now <= 0 {
		return false, errors.New("invalid payment expiry request")
	}
	if order.ExpiresAt <= 0 || order.ExpiresAt > now ||
		(order.Status != PaymentOrderStatusPending && order.Status != PaymentOrderStatusProcessing) {
		return false, nil
	}
	statusReason := "payment session expired"
	updated := tx.Model(&PaymentOrder{}).
		Where("id = ? AND status IN ? AND expires_at > 0 AND expires_at <= ?", order.ID,
			[]string{PaymentOrderStatusPending, PaymentOrderStatusProcessing}, now).
		Updates(map[string]interface{}{
			"status": PaymentOrderStatusExpired, "status_reason": statusReason,
			"start_flow": "", "start_payload": "", "browser_authorization_digest": nil,
			"browser_authorization_payload": "", "browser_authorization_expires_at": 0,
			"browser_authorized_at": 0, "updated_at": now,
			"version": gorm.Expr("version + ?", 1),
		})
	if updated.Error != nil {
		return false, updated.Error
	}
	if updated.RowsAffected == 0 {
		return false, nil
	}
	order.Status = PaymentOrderStatusExpired
	order.StatusReason = statusReason
	order.StartFlow = ""
	order.StartPayload = ""
	order.UpdatedAt = now
	order.Version++
	if err := releasePaymentLimitReservationTx(tx, order, now); err != nil {
		return false, err
	}
	if order.OrderKind == PaymentOrderKindTopUp {
		if err := tx.Model(&TopUp{}).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
			Where("status = ?", common.TopUpStatusPending).
			Updates(map[string]interface{}{"status": common.TopUpStatusExpired, "complete_time": now}).Error; err != nil {
			return false, err
		}
		return true, nil
	}
	if order.OrderKind == PaymentOrderKindSubscription {
		if err := tx.Model(&SubscriptionOrder{}).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
			Where("status = ?", common.TopUpStatusPending).
			Updates(map[string]interface{}{"status": common.TopUpStatusExpired, "complete_time": now}).Error; err != nil {
			return false, err
		}
	}
	return true, nil
}

func MarkPaymentOrderFailed(tradeNo, reason string) error {
	return markPaymentOrderFailed(tradeNo, reason, 0)
}

func MarkPaymentOrderFailedFenced(tradeNo, reason string, creationFenceToken int64) error {
	if creationFenceToken <= 0 {
		return errors.New("payment creation fence is required")
	}
	return markPaymentOrderFailed(tradeNo, reason, creationFenceToken)
}

func markPaymentOrderFailed(tradeNo, reason string, creationFenceToken int64) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var order PaymentOrder
		if err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentOrderNotFound
			}
			return err
		}
		if order.Status != PaymentOrderStatusPending && order.Status != PaymentOrderStatusProcessing {
			return ErrPaymentOrderNotFound
		}
		if creationFenceToken > 0 && order.CreationFenceToken != creationFenceToken {
			return ErrPaymentTaskLeaseLost
		}
		order.Status = PaymentOrderStatusFailed
		order.StatusReason = reason
		order.UpdatedAt = time.Now().Unix()
		order.Version++
		query := tx.Model(&PaymentOrder{}).Where("id = ?", order.ID)
		if creationFenceToken > 0 {
			query = query.Where("creation_fence_token = ?", creationFenceToken)
		}
		updated := query.Updates(map[string]interface{}{
			"status": order.Status, "status_reason": order.StatusReason, "start_payload": "",
			"browser_authorization_digest": nil, "browser_authorization_payload": "",
			"browser_authorization_expires_at": 0, "browser_authorized_at": 0,
			"updated_at": order.UpdatedAt, "version": order.Version,
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 && creationFenceToken > 0 {
			return ErrPaymentTaskLeaseLost
		}
		if err := releasePaymentLimitReservationTx(tx, &order, order.UpdatedAt); err != nil {
			return err
		}
		return syncPaymentProjectionStatusTx(tx, &order)
	})
}

func UpsertPaymentEvent(tx *gorm.DB, event *PaymentEvent) (*PaymentEvent, bool, error) {
	if tx == nil {
		tx = DB
	}
	if event == nil || event.Provider == "" || event.EventKey == "" {
		return nil, false, errors.New("invalid payment event")
	}
	if event.CreatedAt == 0 {
		event.CreatedAt = time.Now().Unix()
	}
	if event.UpdatedAt == 0 {
		event.UpdatedAt = event.CreatedAt
	}
	if event.Status == "" {
		event.Status = PaymentEventStatusReceived
	}
	createResult := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "provider"}, {Name: "event_key"}},
		DoNothing: true,
	}).Create(event)
	if createResult.Error != nil {
		return nil, false, createResult.Error
	}
	if createResult.RowsAffected == 1 {
		return event, true, nil
	}

	// ON CONFLICT DO NOTHING keeps PostgreSQL transactions usable after a
	// duplicate delivery, unlike retrying after a unique-constraint error.
	var existing PaymentEvent
	if err := lockForUpdate(tx).Where("provider = ? AND event_key = ?", event.Provider, event.EventKey).First(&existing).Error; err != nil {
		return nil, false, err
	}
	return &existing, false, nil
}

func UpdatePaymentEvent(tx *gorm.DB, eventID int64, status, lastError string) error {
	if tx == nil {
		tx = DB
	}
	updates := map[string]interface{}{
		"status":     status,
		"last_error": lastError,
		"attempts":   gorm.Expr("attempts + ?", 1),
		"updated_at": time.Now().Unix(),
	}
	if status == PaymentEventStatusProcessed {
		updates["processed_at"] = time.Now().Unix()
	}
	return tx.Model(&PaymentEvent{}).Where("id = ?", eventID).Updates(updates).Error
}

func PaymentPayloadDigest(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func PaymentEventKey(provider, eventType, providerOrderKey, tradeNo, normalized string) string {
	if providerOrderKey == "" {
		providerOrderKey = tradeNo
	}
	if normalized == "" {
		normalized = eventType + ":" + providerOrderKey + ":" + tradeNo
	}
	canonical := fmt.Sprintf(
		"%d:%s%d:%s%d:%s%d:%s%d:%s",
		len(provider), provider,
		len(eventType), eventType,
		len(providerOrderKey), providerOrderKey,
		len(tradeNo), tradeNo,
		len(normalized), normalized,
	)
	return "event:" + PaymentPayloadDigest(canonical)
}
