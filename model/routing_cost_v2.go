package model

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingUpstreamAccountStatusActive   = "active"
	RoutingUpstreamAccountStatusDegraded = "degraded"
	RoutingUpstreamAccountStatusDisabled = "disabled"
	RoutingUpstreamAccountStatusUnknown  = "unknown"

	RoutingUpstreamSyncStatusSuccess = "success"
	RoutingUpstreamSyncStatusPartial = "partial"
	RoutingUpstreamSyncStatusFailed  = "failed"
	RoutingUpstreamSyncStatusUnknown = "unknown"

	RoutingCostConfidenceExact   = "exact"
	RoutingCostConfidenceDerived = "derived"

	RoutingCostFreshnessFresh   = "fresh"
	RoutingCostFreshnessStale   = "stale"
	RoutingCostFreshnessExpired = "expired"
	RoutingCostFreshnessUnknown = "unknown"

	// RoutingCostSub2APIIntervalUnmatchedTier marks request profiles that fall
	// outside the interval prices exposed by Sub2API. The upstream falls back to
	// a private base catalog in that case, so routing must treat the cost as
	// unknown instead of accepting the expression's zero placeholder.
	RoutingCostSub2APIIntervalUnmatchedTier = "__routing_cost_sub2api_interval_unmatched_v1__"

	// These scopes identify upstream price directories that intentionally omit
	// request-dependent billing dimensions. The estimator keeps their useful
	// base prices and fails closed only when the current request needs a missing
	// dimension.
	RoutingCostCatalogScopeNewAPIPricing = "newapi_pricing_v1"
	// RoutingCostSub2APIDisplayContractV1 identifies pricing derived from
	// Sub2API's user-facing channels directory. That directory intentionally
	// omits parts of the effective BillingService contract, so request-specific
	// safety checks must run before the snapshot can be treated as known.
	RoutingCostSub2APIDisplayContractV1 = "display_v1"

	routingCostSnapshotVersionSchema = 1
	routingCostJSONMaxBytes          = 60 << 10
	routingCostTextMaxBytes          = 4 << 10
	routingCostMigrationBatch        = 500
	routingCostRetentionBatch        = 500
	routingCostContentObservations   = 2
	routingCostMaxFutureClockSkew    = 5 * time.Minute
)

var (
	ErrRoutingCostV2Invalid        = errors.New("invalid versioned routing cost snapshot")
	ErrRoutingCostSnapshotExpired  = errors.New("routing cost snapshot is expired")
	ErrRoutingCostHistoryImmutable = errors.New("routing cost history is immutable")
	ErrRoutingCostVersionCorrupt   = errors.New("routing cost snapshot version is corrupt")
)

type RoutingUpstreamAccount struct {
	ID               int     `json:"id" gorm:"primaryKey"`
	AccountKey       string  `json:"-" gorm:"type:char(64);uniqueIndex;not null"`
	SourceType       string  `json:"source_type" gorm:"type:varchar(32);index;not null"`
	MaskedIdentity   string  `json:"masked_identity" gorm:"type:varchar(256);not null"`
	Status           string  `json:"status" gorm:"type:varchar(32);index;not null"`
	BalanceKnown     bool    `json:"balance_known" gorm:"not null"`
	Balance          float64 `json:"balance" gorm:"not null"`
	BalanceUpdatedAt int64   `json:"balance_updated_at" gorm:"bigint;not null"`
	LastSyncStatus   string  `json:"last_sync_status" gorm:"type:varchar(32);index;not null"`
	LastSyncError    string  `json:"last_sync_error" gorm:"type:text;not null"`
	CreatedTime      int64   `json:"created_time" gorm:"bigint;not null"`
	UpdatedTime      int64   `json:"updated_time" gorm:"bigint;index;not null"`
}

func (RoutingUpstreamAccount) TableName() string {
	return "routing_upstream_accounts"
}

type RoutingUpstreamAccountSpec struct {
	SourceType                  string
	StableIdentity              string
	MaskedIdentity              string
	Status                      string
	PreserveBalance             bool
	BalanceKnown                bool
	Balance                     float64
	BalanceUpdatedAt            int64
	ChannelBalanceNotApplicable bool
	LastSyncStatus              string
	LastSyncError               string
}

type RoutingCostSnapshotVersion struct {
	ID               int64   `json:"id" gorm:"primaryKey"`
	SchemaVersion    int     `json:"schema_version" gorm:"not null"`
	PricingHash      string  `json:"pricing_hash" gorm:"type:char(64);uniqueIndex;not null"`
	ContentHash      string  `json:"content_hash" gorm:"type:char(64);index"`
	ApplyToken       string  `json:"-" gorm:"type:char(32);not null"`
	AccountID        int     `json:"account_id" gorm:"index;not null"`
	AccountKey       string  `json:"-" gorm:"type:char(64);index;not null"`
	SourceType       string  `json:"source_type" gorm:"type:varchar(32);index;not null"`
	ChannelID        int     `json:"channel_id" gorm:"index;not null"`
	UpstreamGroup    string  `json:"upstream_group" gorm:"type:varchar(128);not null"`
	UpstreamGroupKey string  `json:"-" gorm:"type:char(64);index;not null"`
	UpstreamModel    string  `json:"upstream_model" gorm:"type:varchar(128);not null"`
	UpstreamModelKey string  `json:"-" gorm:"type:char(64);index;not null"`
	LocalModel       string  `json:"local_model" gorm:"type:varchar(128);not null"`
	LocalModelKey    string  `json:"-" gorm:"type:char(64);index;not null"`
	ObservedTime     int64   `json:"observed_time" gorm:"bigint;index;not null"`
	EffectiveTime    int64   `json:"effective_time" gorm:"bigint;index;not null"`
	ExpiresTime      int64   `json:"expires_time" gorm:"bigint;index;not null"`
	PricingVersion   string  `json:"pricing_version" gorm:"type:varchar(128);index;not null"`
	PricingJSON      string  `json:"-" gorm:"type:text;not null"`
	Confidence       string  `json:"confidence" gorm:"type:varchar(32);index;not null"`
	ConfidenceScore  float64 `json:"confidence_score" gorm:"not null"`
	Freshness        string  `json:"freshness" gorm:"type:varchar(32);index;not null"`
	FreshnessScore   float64 `json:"freshness_score" gorm:"not null"`
	SourceSyncStatus string  `json:"source_sync_status" gorm:"type:varchar(32);index;not null"`
	SourceSyncError  string  `json:"source_sync_error" gorm:"type:text;not null"`
	CreatedTime      int64   `json:"created_time" gorm:"bigint;index;not null"`
}

func (RoutingCostSnapshotVersion) TableName() string {
	return "routing_cost_snapshot_versions"
}

func (*RoutingCostSnapshotVersion) BeforeUpdate(*gorm.DB) error {
	return ErrRoutingCostHistoryImmutable
}

func (*RoutingCostSnapshotVersion) BeforeDelete(*gorm.DB) error {
	return ErrRoutingCostHistoryImmutable
}

type RoutingNormalizedPricing struct {
	QuotaType                  int             `json:"quota_type"`
	BillingMode                string          `json:"billing_mode"`
	Currency                   string          `json:"currency"`
	Unit                       string          `json:"unit"`
	GroupRatio                 *float64        `json:"group_ratio"`
	BaseRatio                  *float64        `json:"base_ratio"`
	CompletionRatio            *float64        `json:"completion_ratio"`
	ModelPrice                 *float64        `json:"model_price"`
	InputCostPerMillion        *float64        `json:"input_cost_per_million"`
	OutputCostPerMillion       *float64        `json:"output_cost_per_million"`
	CacheReadCostPerMillion    *float64        `json:"cache_read_cost_per_million"`
	CacheWriteCostPerMillion   *float64        `json:"cache_write_cost_per_million"`
	CacheWrite1hCostPerMillion *float64        `json:"cache_write_1h_cost_per_million"`
	ImageInputCostPerMillion   *float64        `json:"image_input_cost_per_million"`
	ImageOutputCostPerMillion  *float64        `json:"image_output_cost_per_million"`
	ImageCost                  *float64        `json:"image_cost"`
	PerImageCost               *float64        `json:"per_image_cost"`
	AudioInputCostPerMillion   *float64        `json:"audio_input_cost_per_million"`
	AudioOutputCostPerMillion  *float64        `json:"audio_output_cost_per_million"`
	PerRequestCost             *float64        `json:"per_request_cost"`
	BillingExpression          string          `json:"billing_expression"`
	Tiers                      json.RawMessage `json:"tiers"`
	Extras                     json.RawMessage `json:"extras"`
}

// RoutingCostRequestProfile describes one logical request for platform-cost
// estimation. RetryProbability is the chance that each additional attempt is
// needed; HedgeProbability is the chance of one concurrent hedge. These
// values never participate in user quota or settlement.
type RoutingCostRequestProfile struct {
	PromptTokens                  int64
	MaximumPromptTokens           int64
	ExpectedCompletionTokens      int64
	MaximumCompletionTokens       int64
	CacheReadTokens               int64
	CacheWriteTokens              int64
	CacheWriteOneHourTokens       int64
	ImageInputTokens              int64
	ImageOutputTokens             int64
	AudioInputTokens              int64
	AudioOutputTokens             int64
	ImageUnits                    float64
	MaxAttempts                   int
	RetryProbability              float64
	HedgeProbability              float64
	HedgeAllowed                  bool
	KnowledgeSpecified            bool
	InputTokensKnown              bool
	MaximumCompletionKnown        bool
	CacheTokensKnown              bool
	CacheReadTokensKnown          bool
	CacheWriteTokensKnown         bool
	MediaDimensionsKnown          bool
	RequestInputKnown             bool
	RequestPricingFeaturesKnown   bool
	UncataloguedSurchargePossible bool
	Request                       billingexpr.RequestInput `json:"-"`
	ActualUsage                   *RoutingCostActualUsage  `json:"-"`
	actualTokenParams             *billingexpr.TokenParams
}

type RoutingCostActualUsage struct {
	PromptTokens            int64
	CompletionTokens        int64
	CacheReadTokens         int64
	CacheWriteTokens        int64
	CacheWriteOneHourTokens int64
	ImageInputTokens        int64
	ImageOutputTokens       int64
	AudioInputTokens        int64
	AudioOutputTokens       int64
	ClaudeUsageSemantic     bool
}

type RoutingCostBreakdown struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cache_read"`
	CacheWrite   float64 `json:"cache_write"`
	CacheWrite1h float64 `json:"cache_write_1h"`
	ImageInput   float64 `json:"image_input"`
	ImageOutput  float64 `json:"image_output"`
	ImageUnits   float64 `json:"image_units"`
	AudioInput   float64 `json:"audio_input"`
	AudioOutput  float64 `json:"audio_output"`
	PerRequest   float64 `json:"per_request"`
	Expression   float64 `json:"expression"`
	Total        float64 `json:"total"`
}

type RoutingCostEstimate struct {
	Known                    bool                 `json:"known"`
	ExpectedKnown            bool                 `json:"expected_known"`
	WorstCaseKnown           bool                 `json:"worst_case_known"`
	ExpectedEffectiveKnown   bool                 `json:"expected_effective_known"`
	ExpectedCost             float64              `json:"expected_cost"`
	WorstCaseCost            float64              `json:"worst_case_cost"`
	ExpectedEffectiveCost    float64              `json:"expected_effective_cost"`
	Currency                 string               `json:"currency"`
	Unit                     string               `json:"unit"`
	ConfidenceScore          float64              `json:"confidence_score"`
	FreshnessScore           float64              `json:"freshness_score"`
	ExpectedBreakdown        RoutingCostBreakdown `json:"expected_breakdown"`
	WorstCaseSingleBreakdown RoutingCostBreakdown `json:"worst_case_single_breakdown"`
}

type RoutingCostSnapshotVersionWrite struct {
	AccountID        int
	ChannelID        int
	UpstreamGroup    string
	UpstreamModel    string
	LocalModel       string
	ObservedTime     int64
	EffectiveTime    int64
	ExpiresTime      int64
	PricingVersion   string
	Confidence       string
	ConfidenceScore  float64
	Freshness        string
	FreshnessScore   float64
	SourceSyncStatus string
	SourceSyncError  string
	Pricing          RoutingNormalizedPricing
}

type RoutingCostSnapshotVersionWriteResult struct {
	Version RoutingCostSnapshotVersion `json:"version"`
	Latest  RoutingCostSnapshot        `json:"latest"`
	Created bool                       `json:"created"`
}

type RoutingCostVersionSyncResult struct {
	Account  RoutingUpstreamAccount                  `json:"account"`
	Versions []RoutingCostSnapshotVersionWriteResult `json:"versions"`
	Latest   []RoutingCostSnapshot                   `json:"latest"`
}

func RoutingUpstreamAccountKey(sourceType string, stableIdentity string) string {
	sourceType = strings.TrimSpace(sourceType)
	stableIdentity = strings.TrimSpace(stableIdentity)
	return routingCostHash([]byte("routing-upstream-account:v1\x00" + sourceType + "\x00" + stableIdentity))
}

func RoutingCostModelKey(modelName string) string {
	return routingCostHash([]byte(modelName))
}

func UpsertRoutingUpstreamAccountContext(ctx context.Context, spec RoutingUpstreamAccountSpec) (RoutingUpstreamAccount, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return upsertRoutingUpstreamAccount(DB.WithContext(ctx), spec)
}

// UpsertRoutingUpstreamAccountStatusForBindingContext creates or updates an
// account only while the binding that authenticated its stable identity is
// still current. Callers must derive StableIdentity from provider-authoritative
// account data before using this on failure or partial-status paths.
func UpsertRoutingUpstreamAccountStatusForBindingContext(
	ctx context.Context,
	expected RoutingChannelBinding,
	spec RoutingUpstreamAccountSpec,
) (RoutingUpstreamAccount, error) {
	return UpsertRoutingUpstreamAccountStatusForBindingsContext(
		ctx,
		[]RoutingChannelBinding{expected},
		spec,
	)
}

// UpsertRoutingUpstreamAccountStatusForBindingsContext applies one account
// status only when every binding that contributed to that status is still
// current in the same transaction.
func UpsertRoutingUpstreamAccountStatusForBindingsContext(
	ctx context.Context,
	expected []RoutingChannelBinding,
	spec RoutingUpstreamAccountSpec,
) (RoutingUpstreamAccount, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(expected) == 0 {
		return RoutingUpstreamAccount{}, ErrRoutingBindingChanged
	}
	if !spec.PreserveBalance || spec.BalanceKnown {
		return RoutingUpstreamAccount{}, ErrRoutingCostV2Invalid
	}
	ordered := append([]RoutingChannelBinding(nil), expected...)
	sort.Slice(ordered, func(left int, right int) bool {
		if ordered[left].ID == ordered[right].ID {
			return ordered[left].ChannelID < ordered[right].ChannelID
		}
		return ordered[left].ID < ordered[right].ID
	})
	var account RoutingUpstreamAccount
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seen := make(map[int]struct{}, len(ordered))
		for _, binding := range ordered {
			if binding.ID <= 0 || binding.ChannelID <= 0 {
				return ErrRoutingBindingChanged
			}
			if _, exists := seen[binding.ID]; exists {
				continue
			}
			seen[binding.ID] = struct{}{}
			if _, err := currentRoutingBindingForSync(tx, binding); err != nil {
				return err
			}
		}
		var err error
		account, err = upsertRoutingUpstreamAccount(tx, spec)
		return err
	})
	return account, err
}

// UpdateRoutingUpstreamAccountStatusForBindingContext updates an existing
// account only while the binding used by the sync is still current. Failure
// and partial-status paths use this instead of upsert so a stale sync cannot
// create an account or degrade one after credentials or topology changed.
func UpdateRoutingUpstreamAccountStatusForBindingContext(
	ctx context.Context,
	expected RoutingChannelBinding,
	spec RoutingUpstreamAccountSpec,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if expected.ID <= 0 || expected.ChannelID <= 0 {
		return false, ErrRoutingBindingChanged
	}
	spec.SourceType = strings.TrimSpace(spec.SourceType)
	spec.StableIdentity = strings.TrimSpace(spec.StableIdentity)
	spec.MaskedIdentity = strings.TrimSpace(spec.MaskedIdentity)
	spec.Status = strings.TrimSpace(spec.Status)
	spec.LastSyncStatus = strings.TrimSpace(spec.LastSyncStatus)
	if !spec.PreserveBalance || spec.BalanceKnown ||
		!validRoutingCostText(spec.SourceType, 32) ||
		!validRoutingCostText(spec.StableIdentity, 512) ||
		!validRoutingCostText(spec.MaskedIdentity, 256) ||
		spec.SourceType == "" || spec.StableIdentity == "" || spec.MaskedIdentity == "" ||
		spec.MaskedIdentity == spec.StableIdentity || !validRoutingUpstreamType(spec.SourceType) ||
		!validRoutingUpstreamAccountStatus(spec.Status) ||
		!validRoutingUpstreamSyncStatus(spec.LastSyncStatus) {
		return false, ErrRoutingCostV2Invalid
	}

	accountKey := RoutingUpstreamAccountKey(spec.SourceType, spec.StableIdentity)
	lastSyncError := truncateRoutingCostText(
		common.SanitizeErrorMessage(spec.LastSyncError, spec.StableIdentity),
		1_024,
	)
	applied := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := currentRoutingBindingForSync(tx, expected); err != nil {
			return err
		}

		var account RoutingUpstreamAccount
		if err := tx.Where("account_key = ? AND source_type = ?", accountKey, spec.SourceType).
			First(&account).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		now := common.GetTimestamp()
		updates := map[string]any{
			"masked_identity":  spec.MaskedIdentity,
			"status":           spec.Status,
			"last_sync_status": spec.LastSyncStatus,
			"last_sync_error":  lastSyncError,
			"updated_time":     now,
		}
		updated := tx.Model(&RoutingUpstreamAccount{}).
			Where("id = ? AND account_key = ?", account.ID, accountKey).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		account.MaskedIdentity = spec.MaskedIdentity
		account.Status = spec.Status
		account.LastSyncStatus = spec.LastSyncStatus
		account.LastSyncError = lastSyncError
		account.UpdatedTime = now
		if tx.Migrator().HasTable(&RoutingCostSnapshot{}) {
			if err := tx.Model(&RoutingCostSnapshot{}).Where("account_id = ?", account.ID).Updates(map[string]any{
				"account_source_type": account.SourceType,
				"account_key_hash":    account.AccountKey,
				"account_masked_id":   account.MaskedIdentity,
				"account_status":      account.Status,
				"account_sync_status": account.LastSyncStatus,
				"account_sync_error":  account.LastSyncError,
			}).Error; err != nil {
				return err
			}
		}
		applied = true
		return nil
	})
	return applied, err
}

func upsertRoutingUpstreamAccount(db *gorm.DB, spec RoutingUpstreamAccountSpec) (RoutingUpstreamAccount, error) {
	if db == nil {
		return RoutingUpstreamAccount{}, ErrRoutingCostV2Invalid
	}
	spec.SourceType = strings.TrimSpace(spec.SourceType)
	spec.StableIdentity = strings.TrimSpace(spec.StableIdentity)
	spec.MaskedIdentity = strings.TrimSpace(spec.MaskedIdentity)
	spec.Status = strings.TrimSpace(spec.Status)
	spec.LastSyncStatus = strings.TrimSpace(spec.LastSyncStatus)
	if !validRoutingCostText(spec.SourceType, 32) || !validRoutingCostText(spec.StableIdentity, 512) ||
		!validRoutingCostText(spec.MaskedIdentity, 256) || spec.SourceType == "" || spec.StableIdentity == "" ||
		spec.MaskedIdentity == "" || spec.MaskedIdentity == spec.StableIdentity || !validRoutingUpstreamType(spec.SourceType) ||
		!validRoutingUpstreamAccountStatus(spec.Status) || !validRoutingUpstreamSyncStatus(spec.LastSyncStatus) ||
		(spec.PreserveBalance && spec.BalanceKnown) ||
		(spec.BalanceKnown && (!routingCostFinite(spec.Balance) || spec.BalanceUpdatedAt <= 0)) {
		return RoutingUpstreamAccount{}, ErrRoutingCostV2Invalid
	}
	if !spec.BalanceKnown && !spec.PreserveBalance {
		spec.Balance = 0
		spec.BalanceUpdatedAt = 0
	}
	now := common.GetTimestamp()
	account := RoutingUpstreamAccount{
		AccountKey:       RoutingUpstreamAccountKey(spec.SourceType, spec.StableIdentity),
		SourceType:       spec.SourceType,
		MaskedIdentity:   spec.MaskedIdentity,
		Status:           spec.Status,
		BalanceKnown:     spec.BalanceKnown,
		Balance:          spec.Balance,
		BalanceUpdatedAt: spec.BalanceUpdatedAt,
		LastSyncStatus:   spec.LastSyncStatus,
		LastSyncError:    truncateRoutingCostText(common.SanitizeErrorMessage(spec.LastSyncError, spec.StableIdentity), 1_024),
		CreatedTime:      now,
		UpdatedTime:      now,
	}
	assignments := map[string]any{
		"masked_identity":  account.MaskedIdentity,
		"status":           account.Status,
		"last_sync_status": account.LastSyncStatus,
		"last_sync_error":  account.LastSyncError,
		"updated_time":     account.UpdatedTime,
	}
	if !spec.PreserveBalance {
		assignments["balance_known"] = account.BalanceKnown
		assignments["balance"] = account.Balance
		assignments["balance_updated_at"] = account.BalanceUpdatedAt
	}
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "account_key"}},
		DoUpdates: clause.Assignments(assignments),
	}).Create(&account).Error; err != nil {
		return RoutingUpstreamAccount{}, err
	}
	if err := db.Where("account_key = ?", account.AccountKey).First(&account).Error; err != nil {
		return RoutingUpstreamAccount{}, err
	}
	if db.Migrator().HasTable(&RoutingCostSnapshot{}) {
		if err := db.Model(&RoutingCostSnapshot{}).Where("account_id = ?", account.ID).Updates(map[string]any{
			"account_source_type":   account.SourceType,
			"account_key_hash":      account.AccountKey,
			"account_masked_id":     account.MaskedIdentity,
			"account_status":        account.Status,
			"account_balance_known": account.BalanceKnown,
			"account_balance":       account.Balance,
			"account_balance_at":    account.BalanceUpdatedAt,
			"account_sync_status":   account.LastSyncStatus,
			"account_sync_error":    account.LastSyncError,
		}).Error; err != nil {
			return RoutingUpstreamAccount{}, err
		}
	}
	return account, nil
}

func WriteRoutingCostSnapshotVersionContext(
	ctx context.Context,
	write RoutingCostSnapshotVersionWrite,
) (RoutingCostSnapshotVersionWriteResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return RoutingCostSnapshotVersionWriteResult{}, err
	}

	var result RoutingCostSnapshotVersionWriteResult
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = writeRoutingCostSnapshotVersion(tx, write)
		return err
	})
	return result, err
}

func writeRoutingCostSnapshotVersion(tx *gorm.DB, write RoutingCostSnapshotVersionWrite) (RoutingCostSnapshotVersionWriteResult, error) {
	normalized, pricingJSON, err := normalizeRoutingCostSnapshotVersionWrite(write)
	if err != nil {
		return RoutingCostSnapshotVersionWriteResult{}, err
	}
	var account RoutingUpstreamAccount
	if err := lockForUpdate(tx).Where("id = ?", normalized.AccountID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RoutingCostSnapshotVersionWriteResult{}, ErrRoutingCostV2Invalid
		}
		return RoutingCostSnapshotVersionWriteResult{}, err
	}

	pricingHash, err := routingCostPricingHash(account, normalized, pricingJSON)
	if err != nil {
		return RoutingCostSnapshotVersionWriteResult{}, err
	}
	contentHash, err := routingCostContentHash(account, normalized, pricingJSON)
	if err != nil {
		return RoutingCostSnapshotVersionWriteResult{}, err
	}
	version := RoutingCostSnapshotVersion{
		SchemaVersion:    routingCostSnapshotVersionSchema,
		PricingHash:      pricingHash,
		ContentHash:      contentHash,
		AccountID:        account.ID,
		AccountKey:       account.AccountKey,
		SourceType:       account.SourceType,
		ChannelID:        normalized.ChannelID,
		UpstreamGroup:    normalized.UpstreamGroup,
		UpstreamGroupKey: routingCostHash([]byte(normalized.UpstreamGroup)),
		UpstreamModel:    normalized.UpstreamModel,
		UpstreamModelKey: RoutingCostModelKey(normalized.UpstreamModel),
		LocalModel:       normalized.LocalModel,
		LocalModelKey:    RoutingCostModelKey(normalized.LocalModel),
		ObservedTime:     normalized.ObservedTime,
		EffectiveTime:    normalized.EffectiveTime,
		ExpiresTime:      normalized.ExpiresTime,
		PricingVersion:   normalized.PricingVersion,
		PricingJSON:      string(pricingJSON),
		Confidence:       normalized.Confidence,
		ConfidenceScore:  normalized.ConfidenceScore,
		Freshness:        normalized.Freshness,
		FreshnessScore:   normalized.FreshnessScore,
		SourceSyncStatus: normalized.SourceSyncStatus,
		SourceSyncError:  normalized.SourceSyncError,
		CreatedTime:      common.GetTimestamp(),
	}
	var applyToken [16]byte
	if _, err := rand.Read(applyToken[:]); err != nil {
		return RoutingCostSnapshotVersionWriteResult{}, err
	}
	version.ApplyToken = hex.EncodeToString(applyToken[:])
	create := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "pricing_hash"}},
		DoNothing: true,
	}).Create(&version)
	if create.Error != nil {
		return RoutingCostSnapshotVersionWriteResult{}, create.Error
	}
	var existing RoutingCostSnapshotVersion
	if err := lockForUpdate(tx).Where("pricing_hash = ?", pricingHash).First(&existing).Error; err != nil {
		return RoutingCostSnapshotVersionWriteResult{}, err
	}
	created := existing.ApplyToken == version.ApplyToken
	if !routingCostVersionMatches(existing, version) {
		return RoutingCostSnapshotVersionWriteResult{}, ErrRoutingCostVersionCorrupt
	}
	if existing.ContentHash == "" {
		if err := backfillRoutingCostSnapshotContentHash(tx, existing.ID, contentHash); err != nil {
			return RoutingCostSnapshotVersionWriteResult{}, err
		}
		existing.ContentHash = contentHash
	}
	version = existing

	now := common.GetTimestamp()
	latest := RoutingCostSnapshot{}
	if normalized.EffectiveTime <= now {
		latest = routingCostLatestSnapshot(normalized, pricingJSON, version, account)
		if err := upsertRoutingCostLatestV2(tx, &latest); err != nil {
			return RoutingCostSnapshotVersionWriteResult{}, err
		}
	} else {
		modelKey := RoutingCostModelKey(normalized.LocalModel)
		err := tx.Where("channel_id = ? AND model_key = ?", normalized.ChannelID, modelKey).First(&latest).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return RoutingCostSnapshotVersionWriteResult{}, err
		}
	}
	if created && normalized.EffectiveTime <= now {
		if err := compactRoutingCostSnapshotContent(tx, version, now); err != nil {
			return RoutingCostSnapshotVersionWriteResult{}, err
		}
	}
	return RoutingCostSnapshotVersionWriteResult{Version: version, Latest: latest, Created: created}, nil
}

func compactRoutingCostSnapshotContent(tx *gorm.DB, current RoutingCostSnapshotVersion, now int64) error {
	if tx == nil || current.ID <= 0 || len(current.ContentHash) != sha256.Size*2 || now <= 0 {
		return ErrRoutingCostV2Invalid
	}
	for {
		var ids []int64
		if err := tx.Model(&RoutingCostSnapshotVersion{}).
			Where("content_hash = ? AND id <> ? AND effective_time <= ?", current.ContentHash, current.ID, now).
			Order("observed_time desc").
			Order("id desc").
			Offset(routingCostContentObservations-1).
			Limit(routingCostRetentionBatch).
			Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		result := tx.Session(&gorm.Session{SkipHooks: true}).Where("id IN ?", ids).Delete(&RoutingCostSnapshotVersion{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
	}
}

// CompleteRoutingCostVersionSyncContext fences the binding configuration and
// atomically writes the upstream account, immutable history, compatibility
// latest rows, optional balance, and successful binding sync state.
func CompleteRoutingCostVersionSyncContext(
	ctx context.Context,
	expected RoutingChannelBinding,
	accountSpec RoutingUpstreamAccountSpec,
	writes []RoutingCostSnapshotVersionWrite,
) (RoutingCostVersionSyncResult, error) {
	return CompleteRoutingCostVersionSyncWithAccountFencesContext(
		ctx,
		expected,
		nil,
		accountSpec,
		writes,
	)
}

// CompleteRoutingCostVersionSyncWithAccountFencesContext atomically applies a
// successful binding sync while fencing every current failure/backoff binding
// that contributed to the account status written in the same transaction.
func CompleteRoutingCostVersionSyncWithAccountFencesContext(
	ctx context.Context,
	expected RoutingChannelBinding,
	accountFences []RoutingChannelBinding,
	accountSpec RoutingUpstreamAccountSpec,
	writes []RoutingCostSnapshotVersionWrite,
) (RoutingCostVersionSyncResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if expected.ID <= 0 || expected.ChannelID <= 0 || len(writes) > 4_096 || len(accountFences) > 4_096 {
		return RoutingCostVersionSyncResult{}, ErrRoutingBindingChanged
	}
	if err := ctx.Err(); err != nil {
		return RoutingCostVersionSyncResult{}, err
	}

	result := RoutingCostVersionSyncResult{
		Versions: make([]RoutingCostSnapshotVersionWriteResult, 0, len(writes)),
		Latest:   make([]RoutingCostSnapshot, 0, len(writes)),
	}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fences := append([]RoutingChannelBinding{expected}, accountFences...)
		currentFences, err := currentRoutingBindingsForSync(tx, fences)
		if err != nil {
			return err
		}
		current, exists := currentFences[expected.ID]
		if !exists {
			return ErrRoutingBindingChanged
		}
		account, err := upsertRoutingUpstreamAccount(tx, accountSpec)
		if err != nil {
			return err
		}
		result.Account = account

		for index := range writes {
			if err := ctx.Err(); err != nil {
				return err
			}
			write := writes[index]
			if write.ChannelID != 0 && write.ChannelID != expected.ChannelID ||
				strings.TrimSpace(write.UpstreamGroup) != strings.TrimSpace(expected.UpstreamGroup) {
				return ErrRoutingBindingChanged
			}
			write.AccountID = account.ID
			write.ChannelID = expected.ChannelID
			version, err := writeRoutingCostSnapshotVersion(tx, write)
			if err != nil {
				return err
			}
			result.Versions = append(result.Versions, version)
			if version.Latest.ID > 0 && version.Latest.ChannelID == expected.ChannelID {
				result.Latest = append(result.Latest, version.Latest)
			}
		}
		authoritativeModelKeys := make([]string, 0, len(result.Versions))
		for index := range result.Versions {
			authoritativeModelKeys = append(authoritativeModelKeys, result.Versions[index].Version.LocalModelKey)
		}
		if err := reconcileRoutingCostLatestModels(tx, expected.ChannelID, authoritativeModelKeys); err != nil {
			return err
		}

		if accountSpec.ChannelBalanceNotApplicable {
			if err := clearRoutingChannelBalance(tx, expected.ChannelID, accountSpec.BalanceUpdatedAt); err != nil {
				return err
			}
		} else if accountSpec.BalanceKnown {
			if _, err := upsertRoutingChannelBalance(tx, expected.ChannelID, accountSpec.Balance, accountSpec.BalanceUpdatedAt); err != nil {
				return err
			}
		}
		update := routingBindingSyncSourceQuery(tx.Model(&RoutingChannelBinding{}), expected).Updates(map[string]any{
			"last_sync_error":    nil,
			"sync_failure_count": 0,
			"sync_backoff_until": 0,
			"updated_time":       nextRoutingBindingUpdatedTime(current.UpdatedTime),
		})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 0 {
			var verified RoutingChannelBinding
			if err := tx.Where("id = ?", expected.ID).First(&verified).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrRoutingBindingChanged
				}
				return err
			}
			if !routingBindingSyncSourceEqual(verified, expected) || !verified.Enabled {
				return ErrRoutingBindingChanged
			}
		}
		return nil
	})
	if err != nil {
		return RoutingCostVersionSyncResult{}, err
	}
	return result, nil
}

func LoadRoutingCostSnapshotVersionContext(ctx context.Context, pricingHash string) (RoutingCostSnapshotVersion, RoutingNormalizedPricing, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(pricingHash) != 64 {
		return RoutingCostSnapshotVersion{}, RoutingNormalizedPricing{}, ErrRoutingCostV2Invalid
	}
	var version RoutingCostSnapshotVersion
	if err := DB.WithContext(ctx).Where("pricing_hash = ?", pricingHash).First(&version).Error; err != nil {
		return RoutingCostSnapshotVersion{}, RoutingNormalizedPricing{}, err
	}
	var pricing RoutingNormalizedPricing
	if err := common.UnmarshalJsonStr(version.PricingJSON, &pricing); err != nil {
		return RoutingCostSnapshotVersion{}, RoutingNormalizedPricing{}, ErrRoutingCostVersionCorrupt
	}
	normalized, pricingJSON, err := normalizeRoutingNormalizedPricing(pricing)
	if err != nil || string(pricingJSON) != version.PricingJSON {
		return RoutingCostSnapshotVersion{}, RoutingNormalizedPricing{}, ErrRoutingCostVersionCorrupt
	}
	account := RoutingUpstreamAccount{ID: version.AccountID, AccountKey: version.AccountKey, SourceType: version.SourceType}
	write := RoutingCostSnapshotVersionWrite{
		AccountID:        version.AccountID,
		ChannelID:        version.ChannelID,
		UpstreamGroup:    version.UpstreamGroup,
		UpstreamModel:    version.UpstreamModel,
		LocalModel:       version.LocalModel,
		ObservedTime:     version.ObservedTime,
		EffectiveTime:    version.EffectiveTime,
		ExpiresTime:      version.ExpiresTime,
		PricingVersion:   version.PricingVersion,
		Confidence:       version.Confidence,
		ConfidenceScore:  version.ConfidenceScore,
		Freshness:        version.Freshness,
		FreshnessScore:   version.FreshnessScore,
		SourceSyncStatus: version.SourceSyncStatus,
		SourceSyncError:  version.SourceSyncError,
		Pricing:          normalized,
	}
	expectedHash, err := routingCostPricingHash(account, write, pricingJSON)
	expectedContentHash, contentErr := routingCostContentHash(account, write, pricingJSON)
	if err != nil || contentErr != nil || expectedHash != version.PricingHash ||
		(version.ContentHash != "" && expectedContentHash != version.ContentHash) ||
		version.UpstreamGroupKey != routingCostHash([]byte(version.UpstreamGroup)) ||
		version.UpstreamModelKey != RoutingCostModelKey(version.UpstreamModel) ||
		version.LocalModelKey != RoutingCostModelKey(version.LocalModel) {
		return RoutingCostSnapshotVersion{}, RoutingNormalizedPricing{}, ErrRoutingCostVersionCorrupt
	}
	if version.ContentHash == "" {
		if err := backfillRoutingCostSnapshotContentHash(DB.WithContext(ctx), version.ID, expectedContentHash); err != nil {
			return RoutingCostSnapshotVersion{}, RoutingNormalizedPricing{}, err
		}
		version.ContentHash = expectedContentHash
	}
	return version, normalized, nil
}

func DecodeRoutingCostSnapshotPricing(snapshot RoutingCostSnapshot) (RoutingNormalizedPricing, bool, error) {
	encoded := snapshot.PricingJSON
	if encoded == nil || strings.TrimSpace(*encoded) == "" {
		encoded = snapshot.ExtrasJSON
	}
	if encoded == nil || strings.TrimSpace(*encoded) == "" {
		return RoutingNormalizedPricing{}, false, nil
	}
	var pricing RoutingNormalizedPricing
	if err := common.UnmarshalJsonStr(*encoded, &pricing); err != nil {
		return RoutingNormalizedPricing{}, false, ErrRoutingCostVersionCorrupt
	}
	normalized, _, err := normalizeRoutingNormalizedPricing(pricing)
	if err != nil {
		return RoutingNormalizedPricing{}, false, ErrRoutingCostVersionCorrupt
	}
	return normalized, routingNormalizedPricingHasKnownCost(normalized), nil
}

func backfillRoutingCostSnapshotContentHash(db *gorm.DB, versionID int64, contentHash string) error {
	if db == nil || versionID <= 0 || len(contentHash) != sha256.Size*2 {
		return ErrRoutingCostV2Invalid
	}
	return db.Session(&gorm.Session{SkipHooks: true}).Model(&RoutingCostSnapshotVersion{}).
		Where("id = ? AND (content_hash IS NULL OR content_hash = '')", versionID).
		UpdateColumn("content_hash", contentHash).Error
}

func DeleteRoutingCostSnapshotVersionsBeforeContext(ctx context.Context, cutoff int64) (int64, error) {
	if cutoff <= 0 {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var ids []int64
		if err := DB.WithContext(ctx).Model(&RoutingCostSnapshotVersion{}).
			Where("created_time < ? AND effective_time < ?", cutoff, cutoff).
			Order("created_time asc").
			Order("id asc").
			Limit(routingCostRetentionBatch).
			Pluck("id", &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		result := DB.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Where("id IN ?", ids).Delete(&RoutingCostSnapshotVersion{})
		if result.Error != nil {
			return total, result.Error
		}
		total += result.RowsAffected
		if result.RowsAffected == 0 || len(ids) < routingCostRetentionBatch {
			return total, nil
		}
	}
}

// EstimateRoutingCostSnapshot computes platform cost only. ExpectedCost is one
// normal upstream attempt, WorstCaseCost covers MaxAttempts plus one optional
// hedge at MaximumCompletionTokens, and ExpectedEffectiveCost applies the
// retry/hedge probabilities to the expected single-attempt cost.
func EstimateRoutingCostSnapshot(
	version RoutingCostSnapshotVersion,
	pricing RoutingNormalizedPricing,
	profile RoutingCostRequestProfile,
	atUnix int64,
) (RoutingCostEstimate, error) {
	if atUnix <= 0 {
		atUnix = common.GetTimestamp()
	}
	pricing, _, err := normalizeRoutingNormalizedPricing(pricing)
	if err != nil {
		return RoutingCostEstimate{}, err
	}
	if profile.ActualUsage != nil {
		profile, err = normalizeRoutingActualCostProfile(pricing, profile)
		if err != nil {
			return RoutingCostEstimate{}, err
		}
	}
	if profile.MaximumPromptTokens == 0 {
		profile.MaximumPromptTokens = profile.PromptTokens
	}
	if err := validateRoutingCostRequestProfile(profile); err != nil {
		return RoutingCostEstimate{}, err
	}
	estimate := RoutingCostEstimate{
		Currency:        pricing.Currency,
		Unit:            pricing.Unit,
		ConfidenceScore: version.ConfidenceScore,
		FreshnessScore:  routingCostFreshnessAt(version, atUnix),
	}
	if version.Confidence == RoutingCostConfidenceUnknown || version.Freshness == RoutingCostFreshnessUnknown ||
		version.EffectiveTime > atUnix || version.ExpiresTime <= atUnix || estimate.FreshnessScore <= 0 ||
		!routingNormalizedPricingHasKnownCost(pricing) {
		return estimate, nil
	}
	if pricing.GroupRatio != nil && *pricing.GroupRatio == 0 {
		estimate.Known = true
		estimate.ExpectedKnown = true
		estimate.WorstCaseKnown = true
		estimate.ExpectedEffectiveKnown = true
		return estimate, nil
	}
	if !routingCostCatalogCoversRequest(version.SourceType, pricing, profile) {
		return estimate, nil
	}

	dependencies := routingCostPricingDependencies(pricing)
	cacheReadTokensKnown := profile.CacheTokensKnown || profile.CacheReadTokensKnown
	cacheWriteTokensKnown := profile.CacheTokensKnown || profile.CacheWriteTokensKnown
	expectedKnown := true
	worstKnown := true
	if profile.KnowledgeSpecified {
		if dependencies.request && !profile.RequestInputKnown ||
			dependencies.cacheRead && !cacheReadTokensKnown ||
			dependencies.cacheWrite && !cacheWriteTokensKnown ||
			dependencies.media && !profile.MediaDimensionsKnown {
			expectedKnown = false
			worstKnown = false
		}
		if dependencies.input && !profile.InputTokensKnown {
			worstKnown = false
			if profile.PromptTokens == 0 {
				expectedKnown = false
			}
			estimate.ConfidenceScore *= 0.6
		}
		if dependencies.output && !profile.MaximumCompletionKnown {
			worstKnown = false
		}
	}
	if expectedKnown {
		expected, breakdown, known, err := routingCostSingleAttempt(
			version.SourceType, pricing, profile, profile.ExpectedCompletionTokens,
		)
		if err != nil {
			return RoutingCostEstimate{}, err
		}
		if known {
			estimate.ExpectedKnown = true
			estimate.ExpectedEffectiveKnown = true
			estimate.ExpectedCost = expected
			estimate.ExpectedBreakdown = breakdown
		}
	}

	maxAttempts := profile.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	expectedAttemptFactor := 1.0
	retryPower := profile.RetryProbability
	for attempt := 1; attempt < maxAttempts; attempt++ {
		expectedAttemptFactor += retryPower
		retryPower *= profile.RetryProbability
	}
	worstAttemptCount := maxAttempts
	if profile.HedgeAllowed {
		expectedAttemptFactor += profile.HedgeProbability
		worstAttemptCount++
	}

	if estimate.ExpectedKnown {
		estimate.ExpectedEffectiveCost = estimate.ExpectedCost * expectedAttemptFactor
	}
	if worstKnown {
		maximumCompletion := profile.MaximumCompletionTokens
		if maximumCompletion < profile.ExpectedCompletionTokens {
			maximumCompletion = profile.ExpectedCompletionTokens
		}
		worstProfile := profile
		worstProfile.PromptTokens = profile.MaximumPromptTokens
		worstSingle, breakdown, known, err := routingCostSingleAttempt(
			version.SourceType, pricing, worstProfile, maximumCompletion,
		)
		if err != nil {
			return RoutingCostEstimate{}, err
		}
		if known {
			estimate.WorstCaseKnown = true
			estimate.WorstCaseCost = worstSingle * float64(worstAttemptCount)
			estimate.WorstCaseSingleBreakdown = breakdown
		}
	}
	estimate.Known = estimate.ExpectedKnown
	if !routingCostFinite(estimate.ExpectedCost) || !routingCostFinite(estimate.WorstCaseCost) ||
		!routingCostFinite(estimate.ExpectedEffectiveCost) || estimate.ExpectedCost < 0 ||
		estimate.WorstCaseCost < 0 || estimate.ExpectedEffectiveCost < 0 {
		return RoutingCostEstimate{}, ErrRoutingCostV2Invalid
	}
	return estimate, nil
}

type routingCostCatalogMetadata struct {
	CatalogScope                string `json:"catalog_scope"`
	AlwaysUncataloguedSurcharge bool   `json:"always_uncatalogued_surcharge"`
}

func routingCostCatalogCoversRequest(
	sourceType string,
	pricing RoutingNormalizedPricing,
	profile RoutingCostRequestProfile,
) bool {
	requiresNewAPIContract := strings.TrimSpace(sourceType) == RoutingUpstreamTypeNewAPI
	if len(pricing.Extras) == 0 || common.GetJsonType(pricing.Extras) != "object" {
		return !requiresNewAPIContract
	}
	var metadata routingCostCatalogMetadata
	if err := common.Unmarshal(pricing.Extras, &metadata); err != nil {
		return false
	}
	catalogScope := strings.TrimSpace(metadata.CatalogScope)
	if requiresNewAPIContract && catalogScope != RoutingCostCatalogScopeNewAPIPricing {
		return false
	}
	switch catalogScope {
	case RoutingCostCatalogScopeNewAPIPricing:
		return profile.KnowledgeSpecified && profile.RequestPricingFeaturesKnown &&
			!profile.UncataloguedSurchargePossible && !metadata.AlwaysUncataloguedSurcharge
	}
	return true
}

func validateRoutingCostRequestProfile(profile RoutingCostRequestProfile) error {
	values := []int64{
		profile.PromptTokens,
		profile.MaximumPromptTokens,
		profile.ExpectedCompletionTokens,
		profile.MaximumCompletionTokens,
		profile.CacheReadTokens,
		profile.CacheWriteTokens,
		profile.CacheWriteOneHourTokens,
		profile.ImageInputTokens,
		profile.ImageOutputTokens,
		profile.AudioInputTokens,
		profile.AudioOutputTokens,
	}
	const maxCostDimension = int64(1_000_000_000_000)
	for _, value := range values {
		if value < 0 || value > maxCostDimension {
			return ErrRoutingCostV2Invalid
		}
	}
	if profile.InputTokensKnown && profile.MaximumPromptTokens < profile.PromptTokens {
		return ErrRoutingCostV2Invalid
	}
	if profile.MaxAttempts < 0 || profile.MaxAttempts > 16 ||
		!routingCostFinite(profile.RetryProbability) || profile.RetryProbability < 0 || profile.RetryProbability > 1 ||
		!routingCostFinite(profile.HedgeProbability) || profile.HedgeProbability < 0 || profile.HedgeProbability > 1 ||
		!routingCostFinite(profile.ImageUnits) || profile.ImageUnits < 0 || profile.ImageUnits > float64(maxCostDimension) {
		return ErrRoutingCostV2Invalid
	}
	return nil
}

func routingCostFreshnessAt(version RoutingCostSnapshotVersion, atUnix int64) float64 {
	if version.ExpiresTime <= atUnix || version.ExpiresTime <= version.ObservedTime || version.FreshnessScore <= 0 {
		return 0
	}
	if atUnix <= version.ObservedTime {
		return version.FreshnessScore
	}
	remaining := float64(version.ExpiresTime - atUnix)
	window := float64(version.ExpiresTime - version.ObservedTime)
	return math.Max(0, math.Min(version.FreshnessScore, version.FreshnessScore*remaining/window))
}

func routingCostSingleAttempt(
	sourceType string,
	pricing RoutingNormalizedPricing,
	profile RoutingCostRequestProfile,
	completionTokens int64,
) (float64, RoutingCostBreakdown, bool, error) {
	groupRatio := 1.0
	if pricing.GroupRatio != nil {
		groupRatio = *pricing.GroupRatio
	}
	params := billingexpr.TokenParams{
		P:    float64(profile.PromptTokens),
		C:    float64(completionTokens),
		Len:  float64(profile.PromptTokens + profile.CacheReadTokens + profile.CacheWriteTokens + profile.CacheWriteOneHourTokens),
		CR:   float64(profile.CacheReadTokens),
		CC:   float64(profile.CacheWriteTokens),
		CC1h: float64(profile.CacheWriteOneHourTokens),
		Img:  float64(profile.ImageInputTokens),
		ImgO: float64(profile.ImageOutputTokens),
		AI:   float64(profile.AudioInputTokens),
		AO:   float64(profile.AudioOutputTokens),
	}
	if profile.actualTokenParams != nil {
		params = *profile.actualTokenParams
	}
	if !routingCostSub2APIDisplayPricingKnownForRequest(sourceType, pricing, profile, params) {
		return 0, RoutingCostBreakdown{}, false, nil
	}
	expression := strings.TrimSpace(pricing.BillingExpression)
	if expression == "" {
		expression = routingCostTierExpression(pricing.Tiers)
	}
	if expression != "" {
		raw, trace, err := billingexpr.RunExprWithRequest(expression, params, profile.Request)
		if err != nil || !routingCostFinite(raw) || raw < 0 {
			return 0, RoutingCostBreakdown{}, false, ErrRoutingCostV2Invalid
		}
		if trace.MatchedTier == RoutingCostSub2APIIntervalUnmatchedTier &&
			(params.P > 0 || params.C > 0 || params.CR > 0 || params.CC > 0 ||
				params.CC1h > 0 || params.Img > 0 || params.ImgO > 0 ||
				params.AI > 0 || params.AO > 0) {
			return 0, RoutingCostBreakdown{}, false, nil
		}
		cost := raw / 1_000_000 * groupRatio
		return cost, RoutingCostBreakdown{Expression: cost, Total: cost}, true, nil
	}

	inputRate := 0.0
	if pricing.InputCostPerMillion != nil {
		inputRate = *pricing.InputCostPerMillion
	} else if pricing.BaseRatio != nil {
		inputRate = *pricing.BaseRatio * 1_000_000 / common.QuotaPerUnit
	}
	outputRate := inputRate
	if pricing.OutputCostPerMillion != nil {
		outputRate = *pricing.OutputCostPerMillion
	} else if pricing.CompletionRatio != nil {
		outputRate = inputRate * *pricing.CompletionRatio
	}
	cacheReadRate := inputRate
	if pricing.CacheReadCostPerMillion != nil {
		cacheReadRate = *pricing.CacheReadCostPerMillion
	}
	cacheWriteRate := inputRate
	if pricing.CacheWriteCostPerMillion != nil {
		cacheWriteRate = *pricing.CacheWriteCostPerMillion
	}
	cacheWriteOneHourRate := cacheWriteRate
	if pricing.CacheWrite1hCostPerMillion != nil {
		cacheWriteOneHourRate = *pricing.CacheWrite1hCostPerMillion
	}
	imageInputRate := inputRate
	if pricing.ImageInputCostPerMillion != nil {
		imageInputRate = *pricing.ImageInputCostPerMillion
	}
	imageOutputRate := outputRate
	if pricing.ImageOutputCostPerMillion != nil {
		imageOutputRate = *pricing.ImageOutputCostPerMillion
	}
	audioInputRate := inputRate
	if pricing.AudioInputCostPerMillion != nil {
		audioInputRate = *pricing.AudioInputCostPerMillion
	}
	audioOutputRate := outputRate
	if pricing.AudioOutputCostPerMillion != nil {
		audioOutputRate = *pricing.AudioOutputCostPerMillion
	}

	perImageRate := 0.0
	if pricing.PerImageCost != nil {
		perImageRate = *pricing.PerImageCost
	} else if pricing.ImageCost != nil {
		perImageRate = *pricing.ImageCost
	}
	breakdown := RoutingCostBreakdown{
		Input:        float64(profile.PromptTokens) * inputRate / 1_000_000,
		Output:       float64(completionTokens) * outputRate / 1_000_000,
		CacheRead:    float64(profile.CacheReadTokens) * cacheReadRate / 1_000_000,
		CacheWrite:   float64(profile.CacheWriteTokens) * cacheWriteRate / 1_000_000,
		CacheWrite1h: float64(profile.CacheWriteOneHourTokens) * cacheWriteOneHourRate / 1_000_000,
		ImageInput:   float64(profile.ImageInputTokens) * imageInputRate / 1_000_000,
		ImageOutput:  float64(profile.ImageOutputTokens) * imageOutputRate / 1_000_000,
		ImageUnits:   profile.ImageUnits * perImageRate,
		AudioInput:   float64(profile.AudioInputTokens) * audioInputRate / 1_000_000,
		AudioOutput:  float64(profile.AudioOutputTokens) * audioOutputRate / 1_000_000,
		PerRequest:   routingCostPointerValue(pricing.PerRequestCost),
	}
	breakdown = scaleRoutingCostBreakdown(breakdown, groupRatio)
	return breakdown.Total, breakdown, true, nil
}

// routingCostSub2APIDisplayPricingKnownForRequest enforces the information
// boundary of Sub2API's /api/v1/channels/available response. Flat display
// prices do not expose priority, 1h cache-write, or long-context overrides;
// interval prices are explicit channel prices and cover priority semantics.
func routingCostSub2APIDisplayPricingKnownForRequest(
	sourceType string,
	pricing RoutingNormalizedPricing,
	profile RoutingCostRequestProfile,
	params billingexpr.TokenParams,
) bool {
	requiresSub2APIContract := strings.TrimSpace(sourceType) == RoutingUpstreamTypeSub2API
	if requiresSub2APIContract && (!profile.KnowledgeSpecified ||
		!profile.RequestPricingFeaturesKnown || profile.UncataloguedSurchargePossible) {
		return false
	}
	billingMode := strings.ToLower(strings.TrimSpace(pricing.BillingMode))
	if !requiresSub2APIContract && billingMode != "token" && billingMode != "tiered_expr" {
		return true
	}
	if len(pricing.Extras) == 0 || common.GetJsonType(pricing.Extras) != "object" {
		return !requiresSub2APIContract
	}

	var rawMetadata map[string]json.RawMessage
	if err := common.Unmarshal(pricing.Extras, &rawMetadata); err != nil {
		return false
	}
	contractJSON, exists := rawMetadata["sub2api_contract"]
	if !exists {
		return !requiresSub2APIContract
	}
	var contract string
	if err := common.Unmarshal(contractJSON, &contract); err != nil ||
		strings.TrimSpace(contract) != RoutingCostSub2APIDisplayContractV1 {
		return false
	}
	if billingMode != "token" && billingMode != "tiered_expr" {
		return true
	}
	var metadata struct {
		Platform          string `json:"platform"`
		SourceBillingMode string `json:"source_billing_mode"`
		HasIntervals      *bool  `json:"has_intervals"`
	}
	if err := common.Unmarshal(pricing.Extras, &metadata); err != nil ||
		strings.ToLower(strings.TrimSpace(metadata.SourceBillingMode)) != "token" ||
		metadata.HasIntervals == nil || !profile.RequestInputKnown {
		return false
	}

	serviceTier := ""
	if len(profile.Request.Body) > 0 {
		var request map[string]json.RawMessage
		if err := common.Unmarshal(profile.Request.Body, &request); err != nil {
			return false
		}
		if serviceTierJSON, found := request["service_tier"]; found {
			if err := common.Unmarshal(serviceTierJSON, &serviceTier); err != nil {
				return false
			}
			serviceTier = strings.ToLower(strings.TrimSpace(serviceTier))
		}
	}
	platform := strings.ToLower(strings.TrimSpace(metadata.Platform))
	if platform == "" {
		return false
	}
	if platform == "openai" {
		for key, value := range profile.Request.Headers {
			if !strings.EqualFold(strings.TrimSpace(key), "anthropic-beta") {
				continue
			}
			for _, token := range strings.Split(value, ",") {
				if strings.EqualFold(strings.TrimSpace(token), "fast-mode-2026-02-01") {
					serviceTier = "priority"
					break
				}
			}
		}
	}

	hasIntervals := *metadata.HasIntervals
	switch serviceTier {
	case "", "standard", "auto", "default", "scale":
	case "priority", "fast":
		if !hasIntervals {
			return false
		}
	case "flex":
		// Sub2API applies a 0.5 multiplier that display expressions do not yet
		// encode. Keep both flat and interval estimates unknown for accuracy.
		return false
	default:
		return false
	}
	if hasIntervals {
		if !profile.InputTokensKnown {
			return false
		}
		return true
	}
	if params.CC1h > 0 {
		return false
	}
	switch platform {
	case "openai":
		// The display contract omits account/catalog long-context policies.
		return profile.InputTokensKnown && params.Len <= 272_000
	case "gemini":
		// Gemini bills the input plus cache-read portion above 200K using a
		// hidden long-context rule when no explicit channel interval exists.
		return profile.InputTokensKnown && params.P+params.CR <= 200_000
	default:
		return true
	}
}

func normalizeRoutingActualCostProfile(
	pricing RoutingNormalizedPricing,
	profile RoutingCostRequestProfile,
) (RoutingCostRequestProfile, error) {
	usage := profile.ActualUsage
	if usage == nil {
		return profile, nil
	}
	values := []int64{
		usage.PromptTokens, usage.CompletionTokens, usage.CacheReadTokens,
		usage.CacheWriteTokens, usage.CacheWriteOneHourTokens,
		usage.ImageInputTokens, usage.ImageOutputTokens,
		usage.AudioInputTokens, usage.AudioOutputTokens,
	}
	const maxCostDimension = int64(1_000_000_000_000)
	for _, value := range values {
		if value < 0 || value > maxCostDimension {
			return RoutingCostRequestProfile{}, ErrRoutingCostV2Invalid
		}
	}
	if (!usage.ClaudeUsageSemantic && (usage.CacheReadTokens > usage.PromptTokens ||
		usage.CacheWriteTokens > usage.PromptTokens || usage.CacheWriteOneHourTokens > usage.PromptTokens)) ||
		usage.ImageInputTokens > usage.PromptTokens ||
		usage.AudioInputTokens > usage.PromptTokens || usage.ImageOutputTokens > usage.CompletionTokens ||
		usage.AudioOutputTokens > usage.CompletionTokens {
		return RoutingCostRequestProfile{}, ErrRoutingCostV2Invalid
	}

	expression := strings.TrimSpace(pricing.BillingExpression)
	if expression == "" {
		expression = routingCostTierExpression(pricing.Tiers)
	}
	promptTokens := usage.PromptTokens
	completionTokens := usage.CompletionTokens
	if expression != "" {
		usedVars := billingexpr.UsedVars(expression)
		params := billingexpr.TokenParams{
			P: float64(promptTokens), C: float64(completionTokens),
			CR: float64(usage.CacheReadTokens), CC: float64(usage.CacheWriteTokens),
			CC1h: float64(usage.CacheWriteOneHourTokens), Img: float64(usage.ImageInputTokens),
			ImgO: float64(usage.ImageOutputTokens), AI: float64(usage.AudioInputTokens),
			AO: float64(usage.AudioOutputTokens), Len: float64(promptTokens),
		}
		if usage.ClaudeUsageSemantic {
			if promptTokens > maxCostDimension-usage.CacheReadTokens ||
				promptTokens+usage.CacheReadTokens > maxCostDimension-usage.CacheWriteTokens ||
				promptTokens+usage.CacheReadTokens+usage.CacheWriteTokens > maxCostDimension-usage.CacheWriteOneHourTokens {
				return RoutingCostRequestProfile{}, ErrRoutingCostV2Invalid
			}
			params.Len = float64(promptTokens + usage.CacheReadTokens + usage.CacheWriteTokens + usage.CacheWriteOneHourTokens)
		} else {
			if usedVars["cr"] {
				params.P -= params.CR
			}
			if usedVars["cc"] {
				params.P -= params.CC
			}
			if usedVars["cc1h"] {
				params.P -= params.CC1h
			}
			if usedVars["img"] {
				params.P -= params.Img
			}
			if usedVars["ai"] {
				params.P -= params.AI
			}
			if usedVars["img_o"] {
				params.C -= params.ImgO
			}
			if usedVars["ao"] {
				params.C -= params.AO
			}
			params.P = math.Max(params.P, 0)
			params.C = math.Max(params.C, 0)
		}
		promptTokens = int64(params.P)
		completionTokens = int64(params.C)
		profile.actualTokenParams = &params
	} else if !usage.ClaudeUsageSemantic {
		inputSubcategories := usage.CacheReadTokens + usage.CacheWriteTokens + usage.CacheWriteOneHourTokens +
			usage.ImageInputTokens + usage.AudioInputTokens
		outputSubcategories := usage.ImageOutputTokens + usage.AudioOutputTokens
		if inputSubcategories > promptTokens || outputSubcategories > completionTokens {
			return RoutingCostRequestProfile{}, ErrRoutingCostV2Invalid
		}
		promptTokens -= inputSubcategories
		completionTokens -= outputSubcategories
	}

	profile.PromptTokens = promptTokens
	profile.MaximumPromptTokens = promptTokens
	profile.ExpectedCompletionTokens = completionTokens
	profile.MaximumCompletionTokens = completionTokens
	profile.CacheReadTokens = usage.CacheReadTokens
	profile.CacheWriteTokens = usage.CacheWriteTokens
	profile.CacheWriteOneHourTokens = usage.CacheWriteOneHourTokens
	profile.ImageInputTokens = usage.ImageInputTokens
	profile.ImageOutputTokens = usage.ImageOutputTokens
	profile.AudioInputTokens = usage.AudioInputTokens
	profile.AudioOutputTokens = usage.AudioOutputTokens
	profile.MaxAttempts = 1
	profile.RetryProbability = 0
	profile.HedgeProbability = 0
	profile.HedgeAllowed = false
	profile.KnowledgeSpecified = true
	profile.InputTokensKnown = true
	profile.MaximumCompletionKnown = true
	profile.CacheTokensKnown = true
	profile.CacheReadTokensKnown = true
	profile.CacheWriteTokensKnown = true
	profile.MediaDimensionsKnown = true
	profile.RequestInputKnown = true
	return profile, nil
}

type routingCostDependencies struct {
	input      bool
	output     bool
	cacheRead  bool
	cacheWrite bool
	media      bool
	request    bool
}

func routingCostPricingDependencies(pricing RoutingNormalizedPricing) routingCostDependencies {
	expression := strings.TrimSpace(pricing.BillingExpression)
	if expression == "" {
		expression = routingCostTierExpression(pricing.Tiers)
	}
	if expression != "" {
		used := billingexpr.UsedVars(expression)
		return routingCostDependencies{
			input:      used["p"] || used["len"],
			output:     used["c"],
			cacheRead:  used["cr"],
			cacheWrite: used["cc"] || used["cc1h"],
			media:      used["img"] || used["img_o"] || used["ai"] || used["ao"],
			request:    used["header"] || used["param"],
		}
	}
	inputRate := 0.0
	if pricing.InputCostPerMillion != nil {
		inputRate = *pricing.InputCostPerMillion
	} else if pricing.BaseRatio != nil {
		inputRate = *pricing.BaseRatio * 1_000_000 / common.QuotaPerUnit
	}
	outputRate := inputRate
	if pricing.OutputCostPerMillion != nil {
		outputRate = *pricing.OutputCostPerMillion
	} else if pricing.CompletionRatio != nil {
		outputRate = inputRate * *pricing.CompletionRatio
	}
	return routingCostDependencies{
		input:  inputRate > 0,
		output: outputRate > 0,
		// Missing subtype prices inherit p/c and therefore need no separate
		// quantity. Pointer presence, including an explicit free price, opts the
		// subtype into separate accounting.
		cacheRead: pricing.CacheReadCostPerMillion != nil,
		cacheWrite: pricing.CacheWriteCostPerMillion != nil ||
			pricing.CacheWrite1hCostPerMillion != nil,
		media: pricing.ImageInputCostPerMillion != nil || pricing.ImageOutputCostPerMillion != nil ||
			pricing.PerImageCost != nil || pricing.ImageCost != nil ||
			pricing.AudioInputCostPerMillion != nil || pricing.AudioOutputCostPerMillion != nil,
	}
}

func scaleRoutingCostBreakdown(breakdown RoutingCostBreakdown, ratio float64) RoutingCostBreakdown {
	breakdown.Input *= ratio
	breakdown.Output *= ratio
	breakdown.CacheRead *= ratio
	breakdown.CacheWrite *= ratio
	breakdown.CacheWrite1h *= ratio
	breakdown.ImageInput *= ratio
	breakdown.ImageOutput *= ratio
	breakdown.ImageUnits *= ratio
	breakdown.AudioInput *= ratio
	breakdown.AudioOutput *= ratio
	breakdown.PerRequest *= ratio
	breakdown.Expression *= ratio
	breakdown.Total = breakdown.Input + breakdown.Output + breakdown.CacheRead + breakdown.CacheWrite +
		breakdown.CacheWrite1h + breakdown.ImageInput + breakdown.ImageOutput + breakdown.ImageUnits +
		breakdown.AudioInput + breakdown.AudioOutput + breakdown.PerRequest + breakdown.Expression
	return breakdown
}

func routingCostTierExpression(tiers json.RawMessage) string {
	if len(tiers) == 0 || common.GetJsonType(tiers) != "object" {
		return ""
	}
	var object map[string]any
	if err := common.Unmarshal(tiers, &object); err != nil {
		return ""
	}
	for _, key := range []string{"expr", "billing_expression"} {
		if expression, ok := object[key].(string); ok {
			return strings.TrimSpace(expression)
		}
	}
	return ""
}

func migrateRoutingCostSnapshotModelKeys(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&RoutingCostSnapshot{}) {
		return nil
	}
	for {
		var rows []RoutingCostSnapshot
		if err := db.Select("id", "model_name").
			Where("model_key IS NULL OR model_key = ?", "").
			Order("id asc").
			Limit(routingCostMigrationBatch).
			Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			for index := range rows {
				modelKey := RoutingCostModelKey(rows[index].ModelName)
				if err := tx.Model(&RoutingCostSnapshot{}).
					Where("id = ? AND (model_key IS NULL OR model_key = ?)", rows[index].ID, "").
					Update("model_key", modelKey).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if len(rows) < routingCostMigrationBatch {
			break
		}
	}
	if db.Migrator().HasIndex(&RoutingCostSnapshot{}, "idx_routing_cost_channel_model") {
		if err := db.Migrator().DropIndex(&RoutingCostSnapshot{}, "idx_routing_cost_channel_model"); err != nil {
			return err
		}
	}
	if !db.Migrator().HasIndex(&RoutingCostSnapshot{}, "idx_routing_cost_channel_model_key") {
		if err := db.Migrator().CreateIndex(&RoutingCostSnapshot{}, "idx_routing_cost_channel_model_key"); err != nil {
			return err
		}
	}
	return nil
}

func upsertRoutingCostLatestV2(tx *gorm.DB, snapshot *RoutingCostSnapshot) error {
	modelKey := RoutingCostModelKey(snapshot.ModelName)
	snapshot.ModelKey = &modelKey
	create := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}, {Name: "model_key"}},
		DoNothing: true,
	}).Create(snapshot)
	if create.Error != nil {
		return create.Error
	}
	update := tx.Model(&RoutingCostSnapshot{}).
		Where("channel_id = ? AND model_key = ? AND snapshot_ts <= ?", snapshot.ChannelID, modelKey, snapshot.SnapshotTS).
		Updates(map[string]any{
			"account_id":            snapshot.AccountID,
			"model_name":            snapshot.ModelName,
			"quota_type":            snapshot.QuotaType,
			"group_ratio":           snapshot.GroupRatio,
			"base_ratio":            snapshot.BaseRatio,
			"completion_ratio":      snapshot.CompletionRatio,
			"model_price":           snapshot.ModelPrice,
			"billing_mode":          snapshot.BillingMode,
			"tiers_json":            snapshot.TiersJSON,
			"extras_json":           snapshot.ExtrasJSON,
			"confidence":            snapshot.Confidence,
			"snapshot_ts":           snapshot.SnapshotTS,
			"pricing_version":       snapshot.PricingVersion,
			"pricing_hash":          snapshot.PricingHash,
			"pricing_json":          snapshot.PricingJSON,
			"upstream_group":        snapshot.UpstreamGroup,
			"upstream_model":        snapshot.UpstreamModel,
			"observed_time":         snapshot.ObservedTime,
			"effective_time":        snapshot.EffectiveTime,
			"expires_time":          snapshot.ExpiresTime,
			"version_confidence":    snapshot.VersionConfidence,
			"confidence_score":      snapshot.ConfidenceScore,
			"freshness":             snapshot.Freshness,
			"freshness_score":       snapshot.FreshnessScore,
			"source_sync_status":    snapshot.SourceSyncStatus,
			"source_sync_error":     snapshot.SourceSyncError,
			"account_source_type":   snapshot.AccountSourceType,
			"account_key_hash":      snapshot.AccountKeyHash,
			"account_masked_id":     snapshot.AccountMaskedID,
			"account_status":        snapshot.AccountStatus,
			"account_balance_known": snapshot.AccountBalanceKnown,
			"account_balance":       snapshot.AccountBalance,
			"account_balance_at":    snapshot.AccountBalanceAt,
			"account_sync_status":   snapshot.AccountSyncStatus,
			"account_sync_error":    snapshot.AccountSyncError,
		})
	if update.Error != nil {
		return update.Error
	}
	return tx.Where("channel_id = ? AND model_key = ?", snapshot.ChannelID, modelKey).First(snapshot).Error
}

func normalizeRoutingCostSnapshotVersionWrite(
	write RoutingCostSnapshotVersionWrite,
) (RoutingCostSnapshotVersionWrite, []byte, error) {
	write.UpstreamGroup = strings.TrimSpace(write.UpstreamGroup)
	write.UpstreamModel = strings.TrimSpace(write.UpstreamModel)
	write.LocalModel = strings.TrimSpace(write.LocalModel)
	write.PricingVersion = strings.TrimSpace(write.PricingVersion)
	write.Confidence = strings.TrimSpace(write.Confidence)
	write.Freshness = strings.TrimSpace(write.Freshness)
	write.SourceSyncStatus = strings.TrimSpace(write.SourceSyncStatus)
	write.SourceSyncError = truncateRoutingCostText(common.SanitizeErrorMessage(write.SourceSyncError), 1_024)
	now := common.GetTimestamp()
	maxFutureObserved := now + int64(routingCostMaxFutureClockSkew/time.Second)
	if (write.ExpiresTime > 0 && write.ExpiresTime <= now) || write.Freshness == RoutingCostFreshnessExpired {
		return RoutingCostSnapshotVersionWrite{}, nil, ErrRoutingCostSnapshotExpired
	}
	if write.AccountID <= 0 || write.ChannelID <= 0 || !validRoutingCostText(write.UpstreamGroup, 128) || write.UpstreamGroup == "" ||
		!validRoutingCostText(write.UpstreamModel, 128) || write.UpstreamModel == "" ||
		!validRoutingCostText(write.LocalModel, 128) || write.LocalModel == "" ||
		!validRoutingCostText(write.PricingVersion, 128) || write.PricingVersion == "" ||
		write.ObservedTime <= 0 || write.ObservedTime > maxFutureObserved || write.EffectiveTime <= 0 || write.ExpiresTime <= write.ObservedTime ||
		write.ExpiresTime <= write.EffectiveTime || !validRoutingCostConfidence(write.Confidence) ||
		!validRoutingCostFreshness(write.Freshness) || !validRoutingUpstreamSyncStatus(write.SourceSyncStatus) ||
		!validRoutingCostScore(write.ConfidenceScore) || !validRoutingCostScore(write.FreshnessScore) {
		return RoutingCostSnapshotVersionWrite{}, nil, ErrRoutingCostV2Invalid
	}
	if write.Confidence == RoutingCostConfidenceUnknown {
		if write.ConfidenceScore != 0 {
			return RoutingCostSnapshotVersionWrite{}, nil, ErrRoutingCostV2Invalid
		}
	} else if write.ConfidenceScore <= 0 {
		return RoutingCostSnapshotVersionWrite{}, nil, ErrRoutingCostV2Invalid
	}
	if write.Freshness == RoutingCostFreshnessUnknown {
		if write.FreshnessScore != 0 {
			return RoutingCostSnapshotVersionWrite{}, nil, ErrRoutingCostV2Invalid
		}
	} else if write.FreshnessScore <= 0 {
		return RoutingCostSnapshotVersionWrite{}, nil, ErrRoutingCostV2Invalid
	}

	pricing, pricingJSON, err := normalizeRoutingNormalizedPricing(write.Pricing)
	if err != nil {
		return RoutingCostSnapshotVersionWrite{}, nil, err
	}
	if write.Confidence != RoutingCostConfidenceUnknown && write.Freshness != RoutingCostFreshnessUnknown && !routingNormalizedPricingHasKnownCost(pricing) {
		return RoutingCostSnapshotVersionWrite{}, nil, ErrRoutingCostV2Invalid
	}
	write.Pricing = pricing
	return write, pricingJSON, nil
}

func normalizeRoutingNormalizedPricing(pricing RoutingNormalizedPricing) (RoutingNormalizedPricing, []byte, error) {
	pricing.BillingMode = strings.TrimSpace(pricing.BillingMode)
	pricing.Currency = strings.ToUpper(strings.TrimSpace(pricing.Currency))
	pricing.Unit = strings.ToLower(strings.TrimSpace(pricing.Unit))
	pricing.BillingExpression = strings.TrimSpace(pricing.BillingExpression)
	if pricing.Currency == "" {
		pricing.Currency = "USD"
	}
	if pricing.PerImageCost == nil && pricing.ImageCost != nil {
		value := *pricing.ImageCost
		pricing.PerImageCost = &value
	}
	if pricing.ImageCost == nil && pricing.PerImageCost != nil {
		value := *pricing.PerImageCost
		pricing.ImageCost = &value
	}
	if pricing.ImageCost != nil && pricing.PerImageCost != nil && *pricing.ImageCost != *pricing.PerImageCost {
		return RoutingNormalizedPricing{}, nil, ErrRoutingCostV2Invalid
	}
	if pricing.Unit == "" {
		switch {
		case pricing.BillingExpression != "" || len(strings.TrimSpace(string(pricing.Tiers))) > 0:
			pricing.Unit = "expression"
		case strings.EqualFold(pricing.BillingMode, "per_request"):
			pricing.Unit = "request"
		default:
			pricing.Unit = "mixed"
		}
	}
	if pricing.QuotaType < 0 || pricing.QuotaType > 1 || !validRoutingCostText(pricing.BillingMode, 32) || pricing.BillingMode == "" ||
		!validRoutingCostText(pricing.Currency, 8) || !validRoutingCostUnit(pricing.Unit) ||
		!validRoutingCostText(pricing.BillingExpression, 16_384) {
		return RoutingNormalizedPricing{}, nil, ErrRoutingCostV2Invalid
	}
	values := []*float64{
		pricing.GroupRatio,
		pricing.BaseRatio,
		pricing.CompletionRatio,
		pricing.ModelPrice,
		pricing.InputCostPerMillion,
		pricing.OutputCostPerMillion,
		pricing.CacheReadCostPerMillion,
		pricing.CacheWriteCostPerMillion,
		pricing.CacheWrite1hCostPerMillion,
		pricing.ImageInputCostPerMillion,
		pricing.ImageOutputCostPerMillion,
		pricing.ImageCost,
		pricing.PerImageCost,
		pricing.AudioInputCostPerMillion,
		pricing.AudioOutputCostPerMillion,
		pricing.PerRequestCost,
	}
	for _, value := range values {
		if value != nil && (!routingCostFinite(*value) || *value < 0) {
			return RoutingNormalizedPricing{}, nil, ErrRoutingCostV2Invalid
		}
	}
	var err error
	pricing.Tiers, err = normalizeRoutingCostJSON(pricing.Tiers)
	if err != nil {
		return RoutingNormalizedPricing{}, nil, err
	}
	if pricing.BillingExpression != "" {
		if _, err := validateRoutingCostExpression(pricing.BillingExpression); err != nil {
			return RoutingNormalizedPricing{}, nil, ErrRoutingCostV2Invalid
		}
	}
	if _, err := validateRoutingCostTiers(pricing.Tiers); err != nil {
		return RoutingNormalizedPricing{}, nil, ErrRoutingCostV2Invalid
	}
	pricing.Extras, err = normalizeRoutingCostJSON(pricing.Extras)
	if err != nil {
		return RoutingNormalizedPricing{}, nil, err
	}
	pricingJSON, err := common.Marshal(pricing)
	if err != nil || len(pricingJSON) > routingCostJSONMaxBytes {
		return RoutingNormalizedPricing{}, nil, ErrRoutingCostV2Invalid
	}
	return pricing, pricingJSON, nil
}

func normalizeRoutingCostJSON(value json.RawMessage) (json.RawMessage, error) {
	if len(strings.TrimSpace(string(value))) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(value) > routingCostJSONMaxBytes {
		return nil, ErrRoutingCostV2Invalid
	}
	jsonType := common.GetJsonType(value)
	if jsonType != "object" && jsonType != "array" {
		return nil, ErrRoutingCostV2Invalid
	}
	var decoded any
	if err := common.Unmarshal(value, &decoded); err != nil || decoded == nil {
		return nil, ErrRoutingCostV2Invalid
	}
	canonical, err := common.Marshal(decoded)
	if err != nil || len(canonical) > routingCostJSONMaxBytes {
		return nil, ErrRoutingCostV2Invalid
	}
	return json.RawMessage(canonical), nil
}

func routingCostPricingHash(account RoutingUpstreamAccount, write RoutingCostSnapshotVersionWrite, pricingJSON []byte) (string, error) {
	manifest := struct {
		SchemaVersion    int             `json:"schema_version"`
		AccountKey       string          `json:"account_key"`
		SourceType       string          `json:"source_type"`
		ChannelID        int             `json:"channel_id"`
		UpstreamGroup    string          `json:"upstream_group"`
		UpstreamModel    string          `json:"upstream_model"`
		LocalModel       string          `json:"local_model"`
		ObservedTime     int64           `json:"observed_time"`
		EffectiveTime    int64           `json:"effective_time"`
		ExpiresTime      int64           `json:"expires_time"`
		PricingVersion   string          `json:"pricing_version"`
		Confidence       string          `json:"confidence"`
		ConfidenceScore  float64         `json:"confidence_score"`
		Freshness        string          `json:"freshness"`
		FreshnessScore   float64         `json:"freshness_score"`
		SourceSyncStatus string          `json:"source_sync_status"`
		SourceSyncError  string          `json:"source_sync_error"`
		Pricing          json.RawMessage `json:"pricing"`
	}{
		SchemaVersion:    routingCostSnapshotVersionSchema,
		AccountKey:       account.AccountKey,
		SourceType:       account.SourceType,
		ChannelID:        write.ChannelID,
		UpstreamGroup:    write.UpstreamGroup,
		UpstreamModel:    write.UpstreamModel,
		LocalModel:       write.LocalModel,
		ObservedTime:     write.ObservedTime,
		EffectiveTime:    write.EffectiveTime,
		ExpiresTime:      write.ExpiresTime,
		PricingVersion:   write.PricingVersion,
		Confidence:       write.Confidence,
		ConfidenceScore:  write.ConfidenceScore,
		Freshness:        write.Freshness,
		FreshnessScore:   write.FreshnessScore,
		SourceSyncStatus: write.SourceSyncStatus,
		SourceSyncError:  write.SourceSyncError,
		Pricing:          json.RawMessage(pricingJSON),
	}
	encoded, err := common.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return routingCostHash(encoded), nil
}

func routingCostContentHash(account RoutingUpstreamAccount, write RoutingCostSnapshotVersionWrite, pricingJSON []byte) (string, error) {
	manifest := struct {
		SchemaVersion    int             `json:"schema_version"`
		AccountKey       string          `json:"account_key"`
		SourceType       string          `json:"source_type"`
		ChannelID        int             `json:"channel_id"`
		UpstreamGroup    string          `json:"upstream_group"`
		UpstreamModel    string          `json:"upstream_model"`
		LocalModel       string          `json:"local_model"`
		PricingVersion   string          `json:"pricing_version"`
		Confidence       string          `json:"confidence"`
		ConfidenceScore  float64         `json:"confidence_score"`
		Freshness        string          `json:"freshness"`
		FreshnessScore   float64         `json:"freshness_score"`
		SourceSyncStatus string          `json:"source_sync_status"`
		SourceSyncError  string          `json:"source_sync_error"`
		Pricing          json.RawMessage `json:"pricing"`
	}{
		SchemaVersion:    routingCostSnapshotVersionSchema,
		AccountKey:       account.AccountKey,
		SourceType:       account.SourceType,
		ChannelID:        write.ChannelID,
		UpstreamGroup:    write.UpstreamGroup,
		UpstreamModel:    write.UpstreamModel,
		LocalModel:       write.LocalModel,
		PricingVersion:   write.PricingVersion,
		Confidence:       write.Confidence,
		ConfidenceScore:  write.ConfidenceScore,
		Freshness:        write.Freshness,
		FreshnessScore:   write.FreshnessScore,
		SourceSyncStatus: write.SourceSyncStatus,
		SourceSyncError:  write.SourceSyncError,
		Pricing:          json.RawMessage(pricingJSON),
	}
	encoded, err := common.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return routingCostHash(encoded), nil
}

func routingCostLatestSnapshot(
	write RoutingCostSnapshotVersionWrite,
	pricingJSON []byte,
	version RoutingCostSnapshotVersion,
	account RoutingUpstreamAccount,
) RoutingCostSnapshot {
	pricing := write.Pricing
	modelKey := RoutingCostModelKey(write.LocalModel)
	tiersJSON := string(pricing.Tiers)
	extrasJSON := string(pricingJSON)
	modelPrice := routingCostPointerValue(pricing.ModelPrice)
	if modelPrice == 0 && (pricing.QuotaType == 1 || strings.EqualFold(pricing.BillingMode, "per_request")) {
		modelPrice = routingCostPointerValue(pricing.PerRequestCost)
	}
	confidence := RoutingCostConfidenceGroupOnly
	if write.Confidence == RoutingCostConfidenceUnknown || write.Freshness == RoutingCostFreshnessUnknown {
		confidence = RoutingCostConfidenceUnknown
	} else if write.Confidence == RoutingCostConfidenceExact && write.Freshness == RoutingCostFreshnessFresh {
		confidence = RoutingCostConfidenceFull
	}
	return RoutingCostSnapshot{
		AccountID:           write.AccountID,
		ChannelID:           write.ChannelID,
		ModelName:           write.LocalModel,
		ModelKey:            &modelKey,
		QuotaType:           pricing.QuotaType,
		GroupRatio:          routingCostPointerValue(pricing.GroupRatio),
		BaseRatio:           routingCostPointerValue(pricing.BaseRatio),
		CompletionRatio:     routingCostPointerValue(pricing.CompletionRatio),
		ModelPrice:          modelPrice,
		BillingMode:         pricing.BillingMode,
		TiersJSON:           &tiersJSON,
		ExtrasJSON:          &extrasJSON,
		Confidence:          confidence,
		SnapshotTS:          write.ObservedTime,
		PricingVersion:      write.PricingVersion,
		PricingHash:         version.PricingHash,
		PricingJSON:         &extrasJSON,
		UpstreamGroup:       write.UpstreamGroup,
		UpstreamModel:       write.UpstreamModel,
		ObservedTime:        write.ObservedTime,
		EffectiveTime:       write.EffectiveTime,
		ExpiresTime:         write.ExpiresTime,
		VersionConfidence:   write.Confidence,
		ConfidenceScore:     write.ConfidenceScore,
		Freshness:           write.Freshness,
		FreshnessScore:      write.FreshnessScore,
		SourceSyncStatus:    write.SourceSyncStatus,
		SourceSyncError:     write.SourceSyncError,
		AccountSourceType:   account.SourceType,
		AccountKeyHash:      account.AccountKey,
		AccountMaskedID:     account.MaskedIdentity,
		AccountStatus:       account.Status,
		AccountBalanceKnown: account.BalanceKnown,
		AccountBalance:      account.Balance,
		AccountBalanceAt:    account.BalanceUpdatedAt,
		AccountSyncStatus:   account.LastSyncStatus,
		AccountSyncError:    account.LastSyncError,
	}
}

func routingCostVersionMatches(existing RoutingCostSnapshotVersion, candidate RoutingCostSnapshotVersion) bool {
	return existing.SchemaVersion == candidate.SchemaVersion && existing.PricingHash == candidate.PricingHash &&
		(existing.ContentHash == "" || existing.ContentHash == candidate.ContentHash) &&
		existing.AccountID == candidate.AccountID && existing.AccountKey == candidate.AccountKey &&
		existing.SourceType == candidate.SourceType && existing.ChannelID == candidate.ChannelID &&
		existing.UpstreamGroup == candidate.UpstreamGroup &&
		existing.UpstreamGroupKey == candidate.UpstreamGroupKey && existing.UpstreamModel == candidate.UpstreamModel &&
		existing.UpstreamModelKey == candidate.UpstreamModelKey && existing.LocalModel == candidate.LocalModel &&
		existing.LocalModelKey == candidate.LocalModelKey && existing.PricingVersion == candidate.PricingVersion &&
		existing.PricingJSON == candidate.PricingJSON && existing.ObservedTime == candidate.ObservedTime &&
		existing.EffectiveTime == candidate.EffectiveTime && existing.ExpiresTime == candidate.ExpiresTime &&
		existing.Confidence == candidate.Confidence && existing.ConfidenceScore == candidate.ConfidenceScore &&
		existing.Freshness == candidate.Freshness && existing.FreshnessScore == candidate.FreshnessScore &&
		existing.SourceSyncStatus == candidate.SourceSyncStatus && existing.SourceSyncError == candidate.SourceSyncError
}

func routingNormalizedPricingHasKnownCost(pricing RoutingNormalizedPricing) bool {
	if pricing.GroupRatio != nil && *pricing.GroupRatio == 0 {
		return true
	}
	for _, value := range []*float64{
		pricing.BaseRatio,
		pricing.ModelPrice,
		pricing.InputCostPerMillion,
		pricing.OutputCostPerMillion,
		pricing.CacheReadCostPerMillion,
		pricing.CacheWriteCostPerMillion,
		pricing.CacheWrite1hCostPerMillion,
		pricing.ImageInputCostPerMillion,
		pricing.ImageOutputCostPerMillion,
		pricing.ImageCost,
		pricing.PerImageCost,
		pricing.AudioInputCostPerMillion,
		pricing.AudioOutputCostPerMillion,
		pricing.PerRequestCost,
	} {
		if value != nil {
			return true
		}
	}
	if pricing.BillingExpression != "" {
		known, err := validateRoutingCostExpression(pricing.BillingExpression)
		if err == nil && known {
			return true
		}
	}
	known, err := validateRoutingCostTiers(pricing.Tiers)
	return err == nil && known
}

func validateRoutingCostExpression(expression string) (bool, error) {
	if _, err := billingexpr.CompileFromCache(expression); err != nil {
		return false, err
	}
	known := false
	vectors := []billingexpr.TokenParams{
		{},
		{P: 1_000, C: 1_000, Len: 1_000},
		{P: 1_000_000, C: 1_000_000, Len: 1_000_000},
	}
	requests := []billingexpr.RequestInput{
		{},
		{
			Headers: map[string]string{"anthropic-beta": "fast-mode-2026-02-01"},
			Body:    []byte(`{"service_tier":"fast","stream_options":{"include_usage":true}}`),
		},
	}
	for _, vector := range vectors {
		for _, request := range requests {
			result, _, err := billingexpr.RunExprWithRequest(expression, vector, request)
			if err != nil || !routingCostFinite(result) || result < 0 {
				return false, ErrRoutingCostV2Invalid
			}
			if result > 0 {
				known = true
			}
		}
	}
	return known, nil
}

func validateRoutingCostTiers(tiers json.RawMessage) (bool, error) {
	if len(tiers) == 0 {
		return false, nil
	}
	var decoded any
	if err := common.Unmarshal(tiers, &decoded); err != nil {
		return false, err
	}
	return inspectRoutingCostTierValue(decoded)
}

func inspectRoutingCostTierValue(value any) (bool, error) {
	switch typed := value.(type) {
	case map[string]any:
		known := false
		for key, item := range typed {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			if normalizedKey == "expr" || normalizedKey == "billing_expression" {
				expression, ok := item.(string)
				if !ok || strings.TrimSpace(expression) == "" {
					return false, ErrRoutingCostV2Invalid
				}
				expressionKnown, err := validateRoutingCostExpression(strings.TrimSpace(expression))
				if err != nil {
					return false, err
				}
				known = known || expressionKnown
				continue
			}
			if routingCostTierCostKey(normalizedKey) {
				number, ok := item.(float64)
				if !ok || !routingCostFinite(number) || number < 0 {
					return false, ErrRoutingCostV2Invalid
				}
				known = known || number > 0
				continue
			}
			nestedKnown, err := inspectRoutingCostTierValue(item)
			if err != nil {
				return false, err
			}
			known = known || nestedKnown
		}
		return known, nil
	case []any:
		known := false
		for _, item := range typed {
			itemKnown, err := inspectRoutingCostTierValue(item)
			if err != nil {
				return false, err
			}
			known = known || itemKnown
		}
		return known, nil
	case nil, string, bool, float64:
		return false, nil
	default:
		return false, ErrRoutingCostV2Invalid
	}
}

func routingCostTierCostKey(key string) bool {
	switch key {
	case "cost", "price", "model_price", "per_request_cost", "base_ratio",
		"input_cost_per_million", "output_cost_per_million", "cache_read_cost_per_million",
		"cache_write_cost_per_million", "image_cost", "audio_input_cost_per_million",
		"audio_output_cost_per_million":
		return true
	default:
		return false
	}
}

func validRoutingUpstreamType(sourceType string) bool {
	return sourceType == RoutingUpstreamTypeNewAPI || sourceType == RoutingUpstreamTypeSub2API
}

func validRoutingUpstreamAccountStatus(status string) bool {
	switch status {
	case RoutingUpstreamAccountStatusActive, RoutingUpstreamAccountStatusDegraded,
		RoutingUpstreamAccountStatusDisabled, RoutingUpstreamAccountStatusUnknown:
		return true
	default:
		return false
	}
}

func validRoutingUpstreamSyncStatus(status string) bool {
	switch status {
	case RoutingUpstreamSyncStatusSuccess, RoutingUpstreamSyncStatusPartial,
		RoutingUpstreamSyncStatusFailed, RoutingUpstreamSyncStatusUnknown:
		return true
	default:
		return false
	}
}

func validRoutingCostConfidence(confidence string) bool {
	switch confidence {
	case RoutingCostConfidenceExact, RoutingCostConfidenceDerived,
		RoutingCostConfidenceGroupOnly, RoutingCostConfidenceUnknown:
		return true
	default:
		return false
	}
}

func validRoutingCostFreshness(freshness string) bool {
	switch freshness {
	case RoutingCostFreshnessFresh, RoutingCostFreshnessStale,
		RoutingCostFreshnessExpired, RoutingCostFreshnessUnknown:
		return true
	default:
		return false
	}
}

func validRoutingCostUnit(unit string) bool {
	switch unit {
	case "million_tokens", "request", "image", "mixed", "expression":
		return true
	default:
		return false
	}
}

func validRoutingCostScore(score float64) bool {
	return routingCostFinite(score) && score >= 0 && score <= 1
}

func validRoutingCostText(value string, maxRunes int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes && len(value) <= routingCostTextMaxBytes
}

func routingCostFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func routingCostPointerValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func routingCostHash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func truncateRoutingCostText(value string, maxRunes int) string {
	if !utf8.ValidString(value) {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}
