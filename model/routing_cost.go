package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"

	"gorm.io/gorm"
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

	RoutingCostFreshnessFresh        = "fresh"
	RoutingCostFreshnessStale        = "stale"
	RoutingCostFreshnessExpired      = "expired"
	RoutingCostFreshnessUnknown      = "unknown"
	RoutingCostUnknownMissingContext = "missing_context"

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

	RoutingCostLifecycleScopeGeneration     = "generation"
	RoutingCostLifecycleScopeLegacyUnscoped = "legacy-unscoped"

	routingCostSnapshotVersionSchema           = 1
	routingCostSnapshotGenerationVersionSchema = 2
	routingCostJSONMaxBytes                    = 60 << 10
	routingCostTextMaxBytes                    = 4 << 10
	routingCostMigrationBatch                  = 500
	routingCostRetentionBatch                  = 500
)

var (
	ErrRoutingCostInvalid          = errors.New("invalid versioned routing cost snapshot")
	ErrRoutingCostHistoryImmutable = errors.New("routing cost history is immutable")
	ErrRoutingCostVersionCorrupt   = errors.New("routing cost snapshot version is corrupt")
	ErrRoutingCostGenerationStale  = errors.New("routing cost snapshot generation is stale")
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

type RoutingCostSnapshotVersion struct {
	ID                int64   `json:"id" gorm:"primaryKey"`
	SchemaVersion     int     `json:"schema_version" gorm:"not null"`
	PricingHash       string  `json:"pricing_hash" gorm:"type:char(64);uniqueIndex;not null"`
	ContentHash       string  `json:"content_hash" gorm:"type:char(64);index"`
	ApplyToken        string  `json:"-" gorm:"type:char(32);not null"`
	AccountID         int     `json:"account_id" gorm:"index;not null"`
	AccountKey        string  `json:"-" gorm:"type:char(64);index;not null"`
	SourceType        string  `json:"source_type" gorm:"type:varchar(32);index;not null"`
	ChannelID         int     `json:"channel_id" gorm:"index;not null"`
	RoutingIdentity   string  `json:"routing_identity,omitempty" gorm:"type:varchar(32);index"`
	RoutingGeneration string  `json:"routing_generation,omitempty" gorm:"type:varchar(32);index"`
	LifecycleScope    string  `json:"lifecycle_scope" gorm:"type:varchar(32);index"`
	UpstreamGroup     string  `json:"upstream_group" gorm:"type:varchar(128);not null"`
	UpstreamGroupKey  string  `json:"-" gorm:"type:char(64);index;not null"`
	UpstreamModel     string  `json:"upstream_model" gorm:"type:varchar(128);not null"`
	UpstreamModelKey  string  `json:"-" gorm:"type:char(64);index;not null"`
	LocalModel        string  `json:"local_model" gorm:"type:varchar(128);not null"`
	LocalModelKey     string  `json:"-" gorm:"type:char(64);index;not null"`
	ObservedTime      int64   `json:"observed_time" gorm:"bigint;index;not null"`
	EffectiveTime     int64   `json:"effective_time" gorm:"bigint;index;not null"`
	ExpiresTime       int64   `json:"expires_time" gorm:"bigint;index;not null"`
	PricingVersion    string  `json:"pricing_version" gorm:"type:varchar(128);index;not null"`
	PricingJSON       string  `json:"-" gorm:"type:text;not null"`
	Confidence        string  `json:"confidence" gorm:"type:varchar(32);index;not null"`
	ConfidenceScore   float64 `json:"confidence_score" gorm:"not null"`
	Freshness         string  `json:"freshness" gorm:"type:varchar(32);index;not null"`
	FreshnessScore    float64 `json:"freshness_score" gorm:"not null"`
	SourceSyncStatus  string  `json:"source_sync_status" gorm:"type:varchar(32);index;not null"`
	SourceSyncError   string  `json:"source_sync_error" gorm:"type:text;not null"`
	CreatedTime       int64   `json:"created_time" gorm:"bigint;index;not null"`
}

func (RoutingCostSnapshotVersion) TableName() string {
	return "routing_cost_snapshot_versions"
}

func (version *RoutingCostSnapshotVersion) BeforeCreate(tx *gorm.DB) error {
	if version == nil {
		return ErrRoutingCostInvalid
	}
	switch version.SchemaVersion {
	case routingCostSnapshotVersionSchema:
		if version.RoutingIdentity != "" || version.RoutingGeneration != "" ||
			(version.LifecycleScope != "" && version.LifecycleScope != RoutingCostLifecycleScopeLegacyUnscoped) {
			return ErrRoutingCostInvalid
		}
		version.LifecycleScope = RoutingCostLifecycleScopeLegacyUnscoped
		return nil
	case routingCostSnapshotGenerationVersionSchema:
		if version.LifecycleScope == "" {
			version.LifecycleScope = RoutingCostLifecycleScopeGeneration
		}
		if version.ChannelID <= 0 || version.LifecycleScope != RoutingCostLifecycleScopeGeneration ||
			!validRoutingIdentity(version.RoutingIdentity) || !validRoutingIdentity(version.RoutingGeneration) ||
			tx == nil {
			return ErrRoutingCostInvalid
		}
		var channel Channel
		err := tx.Select("id", "routing_identity", "routing_generation").
			Where("id = ?", version.ChannelID).First(&channel).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || err == nil &&
			(channel.RoutingIdentity != version.RoutingIdentity || channel.RoutingGeneration != version.RoutingGeneration) {
			return ErrRoutingCostGenerationStale
		}
		if err != nil {
			return err
		}
		var lifecycle RoutingChannelLifecycle
		if err := tx.Select("id", "status").Where(
			"channel_id = ? AND routing_identity = ? AND routing_generation = ? AND status = ?",
			version.ChannelID, version.RoutingIdentity, version.RoutingGeneration,
			RoutingChannelLifecycleStatusActive,
		).First(&lifecycle).Error; err != nil {
			return err
		}
		return nil
	default:
		return ErrRoutingCostInvalid
	}
}

func (*RoutingCostSnapshotVersion) BeforeUpdate(*gorm.DB) error {
	return ErrRoutingCostHistoryImmutable
}

func (*RoutingCostSnapshotVersion) BeforeDelete(*gorm.DB) error {
	return ErrRoutingCostHistoryImmutable
}

// CreateRoutingCostSnapshotVersionContext persists a generation-scoped cost
// fact. A delayed result for a retired generation is acknowledged and ignored
// so an asynchronous retry cannot attach historical cost to a reused channel
// number.
func CreateRoutingCostSnapshotVersionContext(
	ctx context.Context,
	version *RoutingCostSnapshotVersion,
) (bool, error) {
	if version == nil || version.SchemaVersion != routingCostSnapshotGenerationVersionSchema || DB == nil {
		return false, ErrRoutingCostInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := DB.WithContext(ctx).Create(version).Error
	if errors.Is(err, ErrRoutingCostGenerationStale) {
		return false, nil
	}
	return err == nil, err
}

type RoutingNormalizedPricing struct {
	QuotaType                  int                       `json:"quota_type,omitempty"`
	BillingMode                string                    `json:"billing_mode"`
	Currency                   string                    `json:"currency"`
	Unit                       string                    `json:"unit"`
	GroupRatio                 *float64                  `json:"group_ratio,omitempty"`
	BaseRatio                  *float64                  `json:"base_ratio,omitempty"`
	CompletionRatio            *float64                  `json:"completion_ratio,omitempty"`
	ModelPrice                 *float64                  `json:"model_price,omitempty"`
	InputCostPerMillion        *float64                  `json:"input_cost_per_million,omitempty"`
	OutputCostPerMillion       *float64                  `json:"output_cost_per_million,omitempty"`
	CacheReadCostPerMillion    *float64                  `json:"cache_read_cost_per_million,omitempty"`
	CacheWriteCostPerMillion   *float64                  `json:"cache_write_cost_per_million,omitempty"`
	CacheWrite1hCostPerMillion *float64                  `json:"cache_write_1h_cost_per_million,omitempty"`
	ImageInputCostPerMillion   *float64                  `json:"image_input_cost_per_million,omitempty"`
	ImageOutputCostPerMillion  *float64                  `json:"image_output_cost_per_million,omitempty"`
	ImageCost                  *float64                  `json:"image_cost,omitempty"`
	PerImageCost               *float64                  `json:"per_image_cost,omitempty"`
	AudioInputCostPerMillion   *float64                  `json:"audio_input_cost_per_million,omitempty"`
	AudioOutputCostPerMillion  *float64                  `json:"audio_output_cost_per_million,omitempty"`
	AudioCostPerSecond         *float64                  `json:"audio_cost_per_second,omitempty"`
	VideoCostPerSecond         *float64                  `json:"video_cost_per_second,omitempty"`
	PerTaskCost                *float64                  `json:"per_task_cost,omitempty"`
	PerRequestCost             *float64                  `json:"per_request_cost,omitempty"`
	BillingExpression          string                    `json:"billing_expression,omitempty"`
	Tiers                      json.RawMessage           `json:"tiers,omitempty"`
	Extras                     json.RawMessage           `json:"extras,omitempty"`
	ContractV2                 *RoutingPricingContractV2 `json:"contract_v2,omitempty"`
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
	AudioSeconds                  float64
	VideoSeconds                  float64
	TaskUnits                     float64
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
	CacheWriteOneHourTokensKnown  bool
	ImageInputTokensKnown         bool
	ImageOutputTokensKnown        bool
	ImageUnitsKnown               bool
	AudioInputTokensKnown         bool
	AudioOutputTokensKnown        bool
	AudioDurationKnown            bool
	VideoDurationKnown            bool
	TaskUnitsKnown                bool
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
	AudioSeconds float64 `json:"audio_seconds"`
	VideoSeconds float64 `json:"video_seconds"`
	TaskUnits    float64 `json:"task_units"`
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
	UnknownReason            string               `json:"unknown_reason,omitempty"`
	MissingContext           []string             `json:"missing_context,omitempty"`
}

type routingCostSnapshotManifest struct {
	SchemaVersion     int
	AccountID         int
	ChannelID         int
	RoutingIdentity   string
	RoutingGeneration string
	LifecycleScope    string
	UpstreamGroup     string
	UpstreamModel     string
	LocalModel        string
	ObservedTime      int64
	EffectiveTime     int64
	ExpiresTime       int64
	PricingVersion    string
	Confidence        string
	ConfidenceScore   float64
	Freshness         string
	FreshnessScore    float64
	SourceSyncStatus  string
	SourceSyncError   string
	Pricing           RoutingNormalizedPricing
}

func RoutingCostModelKey(modelName string) string {
	return routingCostHash([]byte(modelName))
}

func LoadRoutingCostSnapshotVersionContext(ctx context.Context, pricingHash string) (RoutingCostSnapshotVersion, RoutingNormalizedPricing, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(pricingHash) != 64 {
		return RoutingCostSnapshotVersion{}, RoutingNormalizedPricing{}, ErrRoutingCostInvalid
	}
	var version RoutingCostSnapshotVersion
	if err := DB.WithContext(ctx).Where("pricing_hash = ?", pricingHash).First(&version).Error; err != nil {
		return RoutingCostSnapshotVersion{}, RoutingNormalizedPricing{}, err
	}
	if !validRoutingCostSnapshotLifecycle(version) {
		return RoutingCostSnapshotVersion{}, RoutingNormalizedPricing{}, ErrRoutingCostVersionCorrupt
	}
	if version.SchemaVersion == routingCostSnapshotGenerationVersionSchema {
		if !DB.WithContext(ctx).Migrator().HasTable(&RoutingChannelLifecycle{}) {
			return RoutingCostSnapshotVersion{}, RoutingNormalizedPricing{}, ErrRoutingCostVersionCorrupt
		}
		var lifecycle RoutingChannelLifecycle
		if err := DB.WithContext(ctx).Select("id").Where(
			"channel_id = ? AND routing_identity = ? AND routing_generation = ?",
			version.ChannelID, version.RoutingIdentity, version.RoutingGeneration,
		).First(&lifecycle).Error; err != nil {
			return RoutingCostSnapshotVersion{}, RoutingNormalizedPricing{}, ErrRoutingCostVersionCorrupt
		}
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
	manifest := routingCostSnapshotManifest{
		SchemaVersion:     version.SchemaVersion,
		AccountID:         version.AccountID,
		ChannelID:         version.ChannelID,
		RoutingIdentity:   version.RoutingIdentity,
		RoutingGeneration: version.RoutingGeneration,
		LifecycleScope:    version.LifecycleScope,
		UpstreamGroup:     version.UpstreamGroup,
		UpstreamModel:     version.UpstreamModel,
		LocalModel:        version.LocalModel,
		ObservedTime:      version.ObservedTime,
		EffectiveTime:     version.EffectiveTime,
		ExpiresTime:       version.ExpiresTime,
		PricingVersion:    version.PricingVersion,
		Confidence:        version.Confidence,
		ConfidenceScore:   version.ConfidenceScore,
		Freshness:         version.Freshness,
		FreshnessScore:    version.FreshnessScore,
		SourceSyncStatus:  version.SourceSyncStatus,
		SourceSyncError:   version.SourceSyncError,
		Pricing:           normalized,
	}
	expectedHash, err := routingCostPricingHash(account, manifest, pricingJSON)
	expectedContentHash, contentErr := routingCostContentHash(account, manifest, pricingJSON)
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
		return ErrRoutingCostInvalid
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
	cacheWriteOneHourTokensKnown := profile.CacheTokensKnown || profile.CacheWriteOneHourTokensKnown
	expectedKnown := true
	worstKnown := true
	if profile.KnowledgeSpecified {
		missing := make([]string, 0, 12)
		if dependencies.request && !profile.RequestInputKnown {
			missing = append(missing, "request_fields")
		}
		if dependencies.cacheRead && !cacheReadTokensKnown {
			missing = append(missing, "cache_read_tokens")
		}
		if dependencies.cacheWrite && !cacheWriteTokensKnown {
			missing = append(missing, "cache_write_tokens")
		}
		if dependencies.cacheWriteOneHour && !cacheWriteOneHourTokensKnown {
			missing = append(missing, "cache_write_1h_tokens")
		}
		if dependencies.imageInput && !profile.ImageInputTokensKnown {
			missing = append(missing, "image_input_tokens")
		}
		if dependencies.imageOutput && !profile.ImageOutputTokensKnown {
			missing = append(missing, "image_output_tokens")
		}
		if dependencies.imageUnits && !profile.ImageUnitsKnown {
			missing = append(missing, "image_units")
		}
		if dependencies.audioInput && !profile.AudioInputTokensKnown {
			missing = append(missing, "audio_input_tokens")
		}
		if dependencies.audioOutput && !profile.AudioOutputTokensKnown {
			missing = append(missing, "audio_output_tokens")
		}
		if dependencies.audioDuration && !profile.AudioDurationKnown {
			missing = append(missing, "audio_seconds")
		}
		if dependencies.videoDuration && !profile.VideoDurationKnown {
			missing = append(missing, "video_seconds")
		}
		if dependencies.taskUnits && !profile.TaskUnitsKnown {
			missing = append(missing, "task_units")
		}
		if dependencies.input && !profile.InputTokensKnown {
			missing = append(missing, "input_tokens")
			estimate.ConfidenceScore *= 0.6
		}
		if dependencies.output && !profile.MaximumCompletionKnown {
			missing = append(missing, "completion_tokens")
		}
		if len(missing) > 0 {
			expectedKnown = false
			worstKnown = false
			estimate.UnknownReason = RoutingCostUnknownMissingContext
			estimate.MissingContext = missing
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
		return RoutingCostEstimate{}, ErrRoutingCostInvalid
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
			return ErrRoutingCostInvalid
		}
	}
	if profile.InputTokensKnown && profile.MaximumPromptTokens < profile.PromptTokens {
		return ErrRoutingCostInvalid
	}
	if profile.MaxAttempts < 0 || profile.MaxAttempts > 16 ||
		!routingCostFinite(profile.RetryProbability) || profile.RetryProbability < 0 || profile.RetryProbability > 1 ||
		!routingCostFinite(profile.HedgeProbability) || profile.HedgeProbability < 0 || profile.HedgeProbability > 1 ||
		!routingCostFinite(profile.ImageUnits) || profile.ImageUnits < 0 || profile.ImageUnits > float64(maxCostDimension) ||
		!routingCostFinite(profile.AudioSeconds) || profile.AudioSeconds < 0 || profile.AudioSeconds > float64(maxCostDimension) ||
		!routingCostFinite(profile.VideoSeconds) || profile.VideoSeconds < 0 || profile.VideoSeconds > float64(maxCostDimension) ||
		!routingCostFinite(profile.TaskUnits) || profile.TaskUnits < 0 || profile.TaskUnits > float64(maxCostDimension) {
		return ErrRoutingCostInvalid
	}
	return nil
}

func ValidateRoutingCostRequestProfile(profile RoutingCostRequestProfile) error {
	return validateRoutingCostRequestProfile(profile)
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
			return 0, RoutingCostBreakdown{}, false, ErrRoutingCostInvalid
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
	outputRate := 0.0
	if pricing.OutputCostPerMillion != nil {
		outputRate = *pricing.OutputCostPerMillion
	} else if pricing.CompletionRatio != nil && (pricing.InputCostPerMillion != nil || pricing.BaseRatio != nil) {
		outputRate = inputRate * *pricing.CompletionRatio
	}
	cacheReadRate := 0.0
	if pricing.CacheReadCostPerMillion != nil {
		cacheReadRate = *pricing.CacheReadCostPerMillion
	}
	cacheWriteRate := 0.0
	if pricing.CacheWriteCostPerMillion != nil {
		cacheWriteRate = *pricing.CacheWriteCostPerMillion
	}
	cacheWriteOneHourRate := 0.0
	if pricing.CacheWrite1hCostPerMillion != nil {
		cacheWriteOneHourRate = *pricing.CacheWrite1hCostPerMillion
	}
	imageInputRate := 0.0
	if pricing.ImageInputCostPerMillion != nil {
		imageInputRate = *pricing.ImageInputCostPerMillion
	}
	imageOutputRate := 0.0
	if pricing.ImageOutputCostPerMillion != nil {
		imageOutputRate = *pricing.ImageOutputCostPerMillion
	}
	audioInputRate := 0.0
	if pricing.AudioInputCostPerMillion != nil {
		audioInputRate = *pricing.AudioInputCostPerMillion
	}
	audioOutputRate := 0.0
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
		AudioSeconds: profile.AudioSeconds * routingCostPointerValue(pricing.AudioCostPerSecond),
		VideoSeconds: profile.VideoSeconds * routingCostPointerValue(pricing.VideoCostPerSecond),
		TaskUnits:    profile.TaskUnits * routingCostPointerValue(pricing.PerTaskCost),
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
			return RoutingCostRequestProfile{}, ErrRoutingCostInvalid
		}
	}
	if (!usage.ClaudeUsageSemantic && (usage.CacheReadTokens > usage.PromptTokens ||
		usage.CacheWriteTokens > usage.PromptTokens || usage.CacheWriteOneHourTokens > usage.PromptTokens)) ||
		usage.ImageInputTokens > usage.PromptTokens ||
		usage.AudioInputTokens > usage.PromptTokens || usage.ImageOutputTokens > usage.CompletionTokens ||
		usage.AudioOutputTokens > usage.CompletionTokens {
		return RoutingCostRequestProfile{}, ErrRoutingCostInvalid
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
				return RoutingCostRequestProfile{}, ErrRoutingCostInvalid
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
			return RoutingCostRequestProfile{}, ErrRoutingCostInvalid
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
	profile.CacheWriteOneHourTokensKnown = true
	profile.ImageInputTokensKnown = true
	profile.ImageOutputTokensKnown = true
	profile.AudioInputTokensKnown = true
	profile.AudioOutputTokensKnown = true
	profile.RequestInputKnown = true
	return profile, nil
}

type routingCostDependencies struct {
	input             bool
	output            bool
	cacheRead         bool
	cacheWrite        bool
	cacheWriteOneHour bool
	imageInput        bool
	imageOutput       bool
	imageUnits        bool
	audioInput        bool
	audioOutput       bool
	audioDuration     bool
	videoDuration     bool
	taskUnits         bool
	request           bool
}

func routingCostPricingDependencies(pricing RoutingNormalizedPricing) routingCostDependencies {
	expression := strings.TrimSpace(pricing.BillingExpression)
	if expression == "" {
		expression = routingCostTierExpression(pricing.Tiers)
	}
	if expression != "" {
		used := billingexpr.UsedVars(expression)
		return routingCostDependencies{
			input:             used["p"] || used["len"],
			output:            used["c"],
			cacheRead:         used["cr"],
			cacheWrite:        used["cc"],
			cacheWriteOneHour: used["cc1h"],
			imageInput:        used["img"],
			imageOutput:       used["img_o"],
			audioInput:        used["ai"],
			audioOutput:       used["ao"],
			request:           used["header"] || used["param"],
		}
	}
	inputRate := 0.0
	if pricing.InputCostPerMillion != nil {
		inputRate = *pricing.InputCostPerMillion
	} else if pricing.BaseRatio != nil {
		inputRate = *pricing.BaseRatio * 1_000_000 / common.QuotaPerUnit
	}
	outputRate := 0.0
	if pricing.OutputCostPerMillion != nil {
		outputRate = *pricing.OutputCostPerMillion
	} else if pricing.CompletionRatio != nil && (pricing.InputCostPerMillion != nil || pricing.BaseRatio != nil) {
		outputRate = inputRate * *pricing.CompletionRatio
	}
	return routingCostDependencies{
		input:             inputRate > 0,
		output:            outputRate > 0,
		cacheRead:         routingCostPositivePointer(pricing.CacheReadCostPerMillion),
		cacheWrite:        routingCostPositivePointer(pricing.CacheWriteCostPerMillion),
		cacheWriteOneHour: routingCostPositivePointer(pricing.CacheWrite1hCostPerMillion),
		imageInput:        routingCostPositivePointer(pricing.ImageInputCostPerMillion),
		imageOutput:       routingCostPositivePointer(pricing.ImageOutputCostPerMillion),
		imageUnits: routingCostPositivePointer(pricing.PerImageCost) ||
			routingCostPositivePointer(pricing.ImageCost),
		audioInput:    routingCostPositivePointer(pricing.AudioInputCostPerMillion),
		audioOutput:   routingCostPositivePointer(pricing.AudioOutputCostPerMillion),
		audioDuration: routingCostPositivePointer(pricing.AudioCostPerSecond),
		videoDuration: routingCostPositivePointer(pricing.VideoCostPerSecond),
		taskUnits:     routingCostPositivePointer(pricing.PerTaskCost),
	}
}

func routingCostPositivePointer(value *float64) bool {
	return value != nil && *value > 0
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
	breakdown.AudioSeconds *= ratio
	breakdown.VideoSeconds *= ratio
	breakdown.TaskUnits *= ratio
	breakdown.PerRequest *= ratio
	breakdown.Expression *= ratio
	breakdown.Total = breakdown.Input + breakdown.Output + breakdown.CacheRead + breakdown.CacheWrite +
		breakdown.CacheWrite1h + breakdown.ImageInput + breakdown.ImageOutput + breakdown.ImageUnits +
		breakdown.AudioInput + breakdown.AudioOutput + breakdown.AudioSeconds + breakdown.VideoSeconds +
		breakdown.TaskUnits + breakdown.PerRequest + breakdown.Expression
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

func normalizeRoutingNormalizedPricing(pricing RoutingNormalizedPricing) (RoutingNormalizedPricing, []byte, error) {
	pricing.BillingMode = strings.TrimSpace(pricing.BillingMode)
	pricing.Currency = strings.ToUpper(strings.TrimSpace(pricing.Currency))
	pricing.Unit = strings.ToLower(strings.TrimSpace(pricing.Unit))
	pricing.BillingExpression = strings.TrimSpace(pricing.BillingExpression)
	if pricing.ContractV2 != nil {
		contract, contractErr := NormalizeRoutingPricingContractV2(*pricing.ContractV2)
		if contractErr != nil {
			return RoutingNormalizedPricing{}, nil, ErrRoutingCostInvalid
		}
		pricing.ContractV2 = &contract
	}
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
		return RoutingNormalizedPricing{}, nil, ErrRoutingCostInvalid
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
	if pricing.ContractV2 != nil {
		contract := *pricing.ContractV2
		multiplier := 1.0
		if pricing.GroupRatio != nil {
			multiplier = *pricing.GroupRatio
		}
		expected := contract.ToRoutingNormalizedPricing(pricing.BillingMode, multiplier)
		if contract.Currency != pricing.Currency || expected.Unit != pricing.Unit ||
			!routingCostOptionalFloatEqual(expected.InputCostPerMillion, pricing.InputCostPerMillion) ||
			!routingCostOptionalFloatEqual(expected.OutputCostPerMillion, pricing.OutputCostPerMillion) ||
			!routingCostOptionalFloatEqual(expected.CacheReadCostPerMillion, pricing.CacheReadCostPerMillion) ||
			!routingCostOptionalFloatEqual(expected.CacheWriteCostPerMillion, pricing.CacheWriteCostPerMillion) ||
			!routingCostOptionalFloatEqual(expected.CacheWrite1hCostPerMillion, pricing.CacheWrite1hCostPerMillion) ||
			!routingCostOptionalFloatEqual(expected.ImageInputCostPerMillion, pricing.ImageInputCostPerMillion) ||
			!routingCostOptionalFloatEqual(expected.ImageOutputCostPerMillion, pricing.ImageOutputCostPerMillion) ||
			!routingCostOptionalFloatEqual(expected.PerImageCost, pricing.PerImageCost) ||
			!routingCostOptionalFloatEqual(expected.AudioInputCostPerMillion, pricing.AudioInputCostPerMillion) ||
			!routingCostOptionalFloatEqual(expected.AudioOutputCostPerMillion, pricing.AudioOutputCostPerMillion) ||
			!routingCostOptionalFloatEqual(expected.AudioCostPerSecond, pricing.AudioCostPerSecond) ||
			!routingCostOptionalFloatEqual(expected.VideoCostPerSecond, pricing.VideoCostPerSecond) ||
			!routingCostOptionalFloatEqual(expected.PerTaskCost, pricing.PerTaskCost) ||
			!routingCostOptionalFloatEqual(expected.PerRequestCost, pricing.PerRequestCost) ||
			!routingCostOptionalFloatEqual(expected.ModelPrice, pricing.ModelPrice) ||
			expected.BillingExpression != pricing.BillingExpression ||
			(strings.TrimSpace(string(pricing.Tiers)) != "" && strings.TrimSpace(string(pricing.Tiers)) != "{}") {
			return RoutingNormalizedPricing{}, nil, ErrRoutingCostInvalid
		}
	}
	if pricing.QuotaType < 0 || pricing.QuotaType > 1 || !validRoutingCostText(pricing.BillingMode, 64) || pricing.BillingMode == "" ||
		!validRoutingCostText(pricing.Currency, 8) || !validRoutingCostUnit(pricing.Unit) ||
		!validRoutingCostText(pricing.BillingExpression, 16_384) {
		return RoutingNormalizedPricing{}, nil, ErrRoutingCostInvalid
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
		pricing.AudioCostPerSecond,
		pricing.VideoCostPerSecond,
		pricing.PerTaskCost,
		pricing.PerRequestCost,
	}
	for _, value := range values {
		if value != nil && (!routingCostFinite(*value) || *value < 0) {
			return RoutingNormalizedPricing{}, nil, ErrRoutingCostInvalid
		}
	}
	var err error
	pricing.Tiers, err = normalizeRoutingCostJSON(pricing.Tiers)
	if err != nil {
		return RoutingNormalizedPricing{}, nil, err
	}
	if pricing.BillingExpression != "" {
		if _, err := validateRoutingCostExpression(pricing.BillingExpression); err != nil {
			return RoutingNormalizedPricing{}, nil, ErrRoutingCostInvalid
		}
	}
	if _, err := validateRoutingCostTiers(pricing.Tiers); err != nil {
		return RoutingNormalizedPricing{}, nil, ErrRoutingCostInvalid
	}
	pricing.Extras, err = normalizeRoutingCostJSON(pricing.Extras)
	if err != nil {
		return RoutingNormalizedPricing{}, nil, err
	}
	pricingJSON, err := common.Marshal(pricing)
	if err != nil || len(pricingJSON) > routingCostJSONMaxBytes {
		return RoutingNormalizedPricing{}, nil, ErrRoutingCostInvalid
	}
	return pricing, pricingJSON, nil
}

func routingCostOptionalFloatEqual(left *float64, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func normalizeRoutingCostJSON(value json.RawMessage) (json.RawMessage, error) {
	if len(strings.TrimSpace(string(value))) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(value) > routingCostJSONMaxBytes {
		return nil, ErrRoutingCostInvalid
	}
	jsonType := common.GetJsonType(value)
	if jsonType != "object" && jsonType != "array" {
		return nil, ErrRoutingCostInvalid
	}
	var decoded any
	if err := common.Unmarshal(value, &decoded); err != nil || decoded == nil {
		return nil, ErrRoutingCostInvalid
	}
	canonical, err := common.Marshal(decoded)
	if err != nil || len(canonical) > routingCostJSONMaxBytes {
		return nil, ErrRoutingCostInvalid
	}
	return json.RawMessage(canonical), nil
}

func routingCostPricingHash(account RoutingUpstreamAccount, write routingCostSnapshotManifest, pricingJSON []byte) (string, error) {
	schemaVersion, routingIdentity, routingGeneration, lifecycleScope, err := routingCostSnapshotHashLifecycle(write)
	if err != nil {
		return "", err
	}
	manifest := struct {
		SchemaVersion     int             `json:"schema_version"`
		AccountKey        string          `json:"account_key"`
		SourceType        string          `json:"source_type"`
		ChannelID         int             `json:"channel_id"`
		RoutingIdentity   string          `json:"routing_identity,omitempty"`
		RoutingGeneration string          `json:"routing_generation,omitempty"`
		LifecycleScope    string          `json:"lifecycle_scope,omitempty"`
		UpstreamGroup     string          `json:"upstream_group"`
		UpstreamModel     string          `json:"upstream_model"`
		LocalModel        string          `json:"local_model"`
		ObservedTime      int64           `json:"observed_time"`
		EffectiveTime     int64           `json:"effective_time"`
		ExpiresTime       int64           `json:"expires_time"`
		PricingVersion    string          `json:"pricing_version"`
		Confidence        string          `json:"confidence"`
		ConfidenceScore   float64         `json:"confidence_score"`
		Freshness         string          `json:"freshness"`
		FreshnessScore    float64         `json:"freshness_score"`
		SourceSyncStatus  string          `json:"source_sync_status"`
		SourceSyncError   string          `json:"source_sync_error"`
		Pricing           json.RawMessage `json:"pricing"`
	}{
		SchemaVersion:     schemaVersion,
		AccountKey:        account.AccountKey,
		SourceType:        account.SourceType,
		ChannelID:         write.ChannelID,
		RoutingIdentity:   routingIdentity,
		RoutingGeneration: routingGeneration,
		LifecycleScope:    lifecycleScope,
		UpstreamGroup:     write.UpstreamGroup,
		UpstreamModel:     write.UpstreamModel,
		LocalModel:        write.LocalModel,
		ObservedTime:      write.ObservedTime,
		EffectiveTime:     write.EffectiveTime,
		ExpiresTime:       write.ExpiresTime,
		PricingVersion:    write.PricingVersion,
		Confidence:        write.Confidence,
		ConfidenceScore:   write.ConfidenceScore,
		Freshness:         write.Freshness,
		FreshnessScore:    write.FreshnessScore,
		SourceSyncStatus:  write.SourceSyncStatus,
		SourceSyncError:   write.SourceSyncError,
		Pricing:           json.RawMessage(pricingJSON),
	}
	encoded, err := common.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return routingCostHash(encoded), nil
}

func routingCostContentHash(account RoutingUpstreamAccount, write routingCostSnapshotManifest, pricingJSON []byte) (string, error) {
	schemaVersion, routingIdentity, routingGeneration, lifecycleScope, err := routingCostSnapshotHashLifecycle(write)
	if err != nil {
		return "", err
	}
	manifest := struct {
		SchemaVersion     int             `json:"schema_version"`
		AccountKey        string          `json:"account_key"`
		SourceType        string          `json:"source_type"`
		ChannelID         int             `json:"channel_id"`
		RoutingIdentity   string          `json:"routing_identity,omitempty"`
		RoutingGeneration string          `json:"routing_generation,omitempty"`
		LifecycleScope    string          `json:"lifecycle_scope,omitempty"`
		UpstreamGroup     string          `json:"upstream_group"`
		UpstreamModel     string          `json:"upstream_model"`
		LocalModel        string          `json:"local_model"`
		PricingVersion    string          `json:"pricing_version"`
		Confidence        string          `json:"confidence"`
		ConfidenceScore   float64         `json:"confidence_score"`
		Freshness         string          `json:"freshness"`
		FreshnessScore    float64         `json:"freshness_score"`
		SourceSyncStatus  string          `json:"source_sync_status"`
		SourceSyncError   string          `json:"source_sync_error"`
		Pricing           json.RawMessage `json:"pricing"`
	}{
		SchemaVersion:     schemaVersion,
		AccountKey:        account.AccountKey,
		SourceType:        account.SourceType,
		ChannelID:         write.ChannelID,
		RoutingIdentity:   routingIdentity,
		RoutingGeneration: routingGeneration,
		LifecycleScope:    lifecycleScope,
		UpstreamGroup:     write.UpstreamGroup,
		UpstreamModel:     write.UpstreamModel,
		LocalModel:        write.LocalModel,
		PricingVersion:    write.PricingVersion,
		Confidence:        write.Confidence,
		ConfidenceScore:   write.ConfidenceScore,
		Freshness:         write.Freshness,
		FreshnessScore:    write.FreshnessScore,
		SourceSyncStatus:  write.SourceSyncStatus,
		SourceSyncError:   write.SourceSyncError,
		Pricing:           json.RawMessage(pricingJSON),
	}
	encoded, err := common.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return routingCostHash(encoded), nil
}

func routingCostSnapshotHashLifecycle(write routingCostSnapshotManifest) (int, string, string, string, error) {
	schemaVersion := write.SchemaVersion
	if schemaVersion == 0 {
		schemaVersion = routingCostSnapshotVersionSchema
	}
	switch schemaVersion {
	case routingCostSnapshotVersionSchema:
		return schemaVersion, "", "", "", nil
	case routingCostSnapshotGenerationVersionSchema:
		if write.LifecycleScope != RoutingCostLifecycleScopeGeneration ||
			!validRoutingIdentity(write.RoutingIdentity) || !validRoutingIdentity(write.RoutingGeneration) {
			return 0, "", "", "", ErrRoutingCostInvalid
		}
		return schemaVersion, write.RoutingIdentity, write.RoutingGeneration, write.LifecycleScope, nil
	default:
		return 0, "", "", "", ErrRoutingCostInvalid
	}
}

func validRoutingCostSnapshotLifecycle(version RoutingCostSnapshotVersion) bool {
	switch version.SchemaVersion {
	case routingCostSnapshotVersionSchema:
		return version.RoutingIdentity == "" && version.RoutingGeneration == "" &&
			(version.LifecycleScope == "" || version.LifecycleScope == RoutingCostLifecycleScopeLegacyUnscoped)
	case routingCostSnapshotGenerationVersionSchema:
		return version.LifecycleScope == RoutingCostLifecycleScopeGeneration &&
			validRoutingIdentity(version.RoutingIdentity) && validRoutingIdentity(version.RoutingGeneration)
	default:
		return false
	}
}

func routingNormalizedPricingHasKnownCost(pricing RoutingNormalizedPricing) bool {
	if pricing.ContractV2 != nil && pricing.ContractV2.Mode == RoutingPricingContractModeFree {
		return true
	}
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
		pricing.AudioCostPerSecond,
		pricing.VideoCostPerSecond,
		pricing.PerTaskCost,
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
				return false, ErrRoutingCostInvalid
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
					return false, ErrRoutingCostInvalid
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
					return false, ErrRoutingCostInvalid
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
		return false, ErrRoutingCostInvalid
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

func validRoutingCostUnit(unit string) bool {
	switch unit {
	case "million_tokens", "request", "image", "mixed", "expression":
		return true
	default:
		return false
	}
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
