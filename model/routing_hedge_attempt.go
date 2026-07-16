package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

const (
	RoutingHedgeAttemptAuditSchemaVersion = 4
	RoutingHedgeCostAuditSchemaVersion    = 2

	RoutingAttemptExecutionSerial = "serial"
	RoutingAttemptExecutionHedge  = "hedge"

	RoutingAttemptRoleSerial         = "serial"
	RoutingHedgeAttemptRolePrimary   = "primary"
	RoutingHedgeAttemptRoleSecondary = "secondary"

	RoutingHedgeAttemptStateStarted   = "started"
	RoutingHedgeAttemptStateCompleted = "completed"

	RoutingHedgeAttemptResultPending          = "pending"
	RoutingHedgeAttemptResultSuccess          = "success"
	RoutingHedgeAttemptResultUpstreamError    = "upstream_error"
	RoutingHedgeAttemptResultHedgeLost        = "hedge_lost"
	RoutingHedgeAttemptResultClientCanceled   = "client_canceled"
	RoutingHedgeAttemptResultResponseTooLarge = "response_too_large"
	RoutingHedgeAttemptResultInternalError    = "internal_error"

	routingHedgeAttemptRetentionBatch = 500
	routingHedgeAuditTextMaxRunes     = 512
	routingHedgeAuditJSONMaxBytes     = 16 << 10
	routingHedgeAttemptsPerDecision   = 16
	routingHedgeDecisionQueryBatch    = 100
)

var (
	ErrRoutingHedgeAttemptInvalid     = errors.New("invalid routing hedge attempt audit")
	ErrRoutingHedgeAttemptTransition  = errors.New("invalid routing hedge attempt audit transition")
	ErrRoutingHedgeAttemptCostInvalid = errors.New("invalid routing hedge attempt cost audit")
)

type RoutingHedgeAttemptAudit struct {
	ID                      int64   `json:"id" gorm:"primaryKey"`
	AttemptKey              string  `json:"attempt_key" gorm:"type:char(64);uniqueIndex;not null"`
	DecisionID              string  `json:"decision_id" gorm:"type:varchar(64);index"`
	RequestKey              string  `json:"request_key" gorm:"type:char(64);index;not null"`
	NodeEpochID             string  `json:"node_epoch_id" gorm:"type:char(32);index;not null"`
	StableNodeID            string  `json:"stable_node_id,omitempty" gorm:"type:varchar(128);index;not null"`
	StableNodeKnown         bool    `json:"stable_node_known" gorm:"not null"`
	SchemaVersion           int     `json:"schema_version" gorm:"not null"`
	PolicyRevision          int64   `json:"policy_revision" gorm:"bigint;index;not null"`
	AlgorithmVersion        string  `json:"algorithm_version" gorm:"type:varchar(128);index;not null"`
	PoolID                  int     `json:"pool_id" gorm:"index;not null"`
	MemberID                int     `json:"member_id" gorm:"index;not null"`
	ChannelID               int     `json:"channel_id" gorm:"index;not null"`
	CredentialReferenceHash string  `json:"credential_reference_hash" gorm:"type:char(64);not null"`
	ModelKey                string  `json:"model_key" gorm:"type:char(64);index;not null"`
	ExecutionMode           string  `json:"execution_mode" gorm:"type:varchar(16);index;not null"`
	AttemptIndex            int     `json:"attempt_index" gorm:"index;not null"`
	Role                    string  `json:"role" gorm:"type:varchar(16);index;not null"`
	State                   string  `json:"state" gorm:"type:varchar(16);index;not null"`
	Result                  string  `json:"result" gorm:"type:varchar(32);index;not null"`
	Winner                  bool    `json:"winner" gorm:"not null"`
	EndpointAuthority       string  `json:"endpoint_authority" gorm:"type:varchar(512);not null"`
	EndpointAuthorityHash   string  `json:"endpoint_authority_hash" gorm:"type:char(64);index;not null"`
	FailureDomainHash       string  `json:"failure_domain_hash" gorm:"type:char(64);index;not null"`
	Region                  string  `json:"region" gorm:"type:varchar(64);index;not null"`
	CrossRegion             bool    `json:"cross_region" gorm:"not null"`
	CostSchemaVersion       int     `json:"cost_schema_version" gorm:"not null"`
	CostKnown               bool    `json:"cost_known" gorm:"not null"`
	ExpectedCost            float64 `json:"expected_cost" gorm:"not null"`
	WorstCaseCost           float64 `json:"worst_case_cost" gorm:"not null"`
	EffectiveCost           float64 `json:"effective_cost" gorm:"not null"`
	CostCurrency            string  `json:"cost_currency" gorm:"type:varchar(16);not null"`
	CostUnit                string  `json:"cost_unit" gorm:"type:varchar(32);not null"`
	PricingBasis            string  `json:"pricing_basis" gorm:"type:varchar(64);not null"`
	PricingHash             string  `json:"pricing_hash" gorm:"type:char(64);index;not null"`
	PricingVersion          string  `json:"pricing_version" gorm:"type:varchar(128);index;not null"`
	PricingIdentity         string  `json:"pricing_identity,omitempty" gorm:"type:varchar(128);index"`
	CostUnknownReason       string  `json:"unknown_reason,omitempty" gorm:"type:varchar(128);index"`
	ConfigurationRevision   int64   `json:"configuration_revision,omitempty" gorm:"bigint;index"`
	UpstreamCostMultiplier  float64 `json:"upstream_cost_multiplier"`
	BaselineExpectedKnown   bool    `json:"baseline_expected_known"`
	BaselineExpectedCost    float64 `json:"baseline_expected_cost"`
	BaselineWorstCaseKnown  bool    `json:"baseline_worst_case_known"`
	BaselineWorstCaseCost   float64 `json:"baseline_worst_case_cost"`
	CostConfidenceScore     float64 `json:"cost_confidence_score" gorm:"not null"`
	CostFreshnessScore      float64 `json:"cost_freshness_score" gorm:"not null"`
	CostBreakdownJSON       string  `json:"-" gorm:"type:text;not null"`
	CostObservedTime        int64   `json:"cost_observed_time" gorm:"bigint;index;not null"`
	CostEffectiveTime       int64   `json:"cost_effective_time" gorm:"bigint;not null"`
	CostExpiresTime         int64   `json:"cost_expires_time" gorm:"bigint;index;not null"`
	// Retained only so historical schema-v3 connector rows remain readable.
	CostSourceSyncStatus     string  `json:"cost_source_sync_status,omitempty" gorm:"type:varchar(32);not null"`
	AccountSourceType        string  `json:"account_source_type,omitempty" gorm:"type:varchar(32);not null"`
	AccountReferenceHash     string  `json:"account_reference_hash,omitempty" gorm:"type:char(64);index;not null"`
	HTTPStatus               int     `json:"http_status" gorm:"not null"`
	ErrorClassification      string  `json:"error_classification" gorm:"type:varchar(64);index;not null"`
	ErrorResponsibility      string  `json:"error_responsibility" gorm:"type:varchar(32);index;not null"`
	ErrorRetryability        string  `json:"error_retryability" gorm:"type:varchar(32);not null"`
	ErrorCode                string  `json:"error_code" gorm:"type:varchar(64);not null"`
	UpstreamSent             bool    `json:"upstream_sent" gorm:"index;not null"`
	ClientCommitted          bool    `json:"client_committed" gorm:"not null"`
	WillRetry                bool    `json:"will_retry" gorm:"index;not null"`
	FinalAttempt             bool    `json:"final_attempt" gorm:"index;not null"`
	FirstByteTimeMs          int64   `json:"first_byte_time_ms" gorm:"bigint;index;not null"`
	ActualCostKnown          bool    `json:"actual_cost_known" gorm:"not null"`
	ActualCost               float64 `json:"actual_cost" gorm:"not null"`
	ActualPromptTokens       int64   `json:"actual_prompt_tokens" gorm:"bigint;not null"`
	ActualCompletionTokens   int64   `json:"actual_completion_tokens" gorm:"bigint;not null"`
	ActualTotalTokens        int64   `json:"actual_total_tokens" gorm:"bigint;not null"`
	ActualCacheReadTokens    int64   `json:"actual_cache_read_tokens" gorm:"bigint;not null"`
	ActualCacheWriteTokens   int64   `json:"actual_cache_write_tokens" gorm:"bigint;not null"`
	ActualCacheWrite1hTokens int64   `json:"actual_cache_write_1h_tokens" gorm:"column:actual_cache_write_1h_tokens;bigint;not null"`
	StartedTimeMs            int64   `json:"started_time_ms" gorm:"bigint;index;not null"`
	CompletedTimeMs          int64   `json:"completed_time_ms" gorm:"bigint;index;not null"`
	DurationMs               int64   `json:"duration_ms" gorm:"bigint;not null"`
	CreatedTimeMs            int64   `json:"created_time_ms" gorm:"bigint;index;not null"`
	UpdatedTimeMs            int64   `json:"updated_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingHedgeAttemptAudit) TableName() string {
	return "routing_hedge_attempt_audits"
}

type RoutingHedgeAttemptCostSpec struct {
	Known                  bool
	ExpectedCost           float64
	WorstCaseCost          float64
	EffectiveCost          float64
	Currency               string
	Unit                   string
	PricingBasis           string
	PricingHash            string
	PricingVersion         string
	PricingIdentity        string
	UnknownReason          string
	ConfigurationRevision  int64
	UpstreamCostMultiplier float64
	BaselineExpectedKnown  bool
	BaselineExpectedCost   float64
	BaselineWorstCaseKnown bool
	BaselineWorstCaseCost  float64
	ConfidenceScore        float64
	FreshnessScore         float64
	ExpectedBreakdown      RoutingCostBreakdown
	WorstSingleBreakdown   RoutingCostBreakdown
	ObservedTime           int64
	EffectiveTime          int64
	ExpiresTime            int64
}

type RoutingHedgeAttemptStartSpec struct {
	AttemptKey        string
	DecisionID        string
	RequestID         string
	NodeEpochID       string
	StableNodeID      string
	StableNodeKnown   bool
	PolicyRevision    uint64
	AlgorithmVersion  string
	PoolID            int
	MemberID          int
	ChannelID         int
	CredentialID      int
	ModelName         string
	ExecutionMode     string
	AttemptIndex      int
	Role              string
	EndpointAuthority string
	Region            string
	StartedTimeMs     int64
	Cost              RoutingHedgeAttemptCostSpec
}

type RoutingHedgeAttemptCompleteSpec struct {
	Result                   string
	Winner                   bool
	HTTPStatus               int
	ErrorClassification      string
	ErrorResponsibility      string
	ErrorRetryability        string
	ErrorCode                string
	UpstreamSent             bool
	ClientCommitted          bool
	WillRetry                bool
	FinalAttempt             bool
	FirstByteTimeMs          int64
	ActualCostKnown          bool
	ActualCost               float64
	ActualPromptTokens       int64
	ActualCompletionTokens   int64
	ActualTotalTokens        int64
	ActualCacheReadTokens    int64
	ActualCacheWriteTokens   int64
	ActualCacheWrite1hTokens int64
	CompletedTimeMs          int64
}

type RoutingHedgeAttemptSummary struct {
	DecisionID               string  `json:"-" gorm:"column:decision_id"`
	RequestKey               string  `json:"-" gorm:"column:request_key"`
	NodeEpochID              string  `json:"node_epoch_id"`
	StableNodeID             string  `json:"stable_node_id,omitempty"`
	StableNodeKnown          bool    `json:"stable_node_known"`
	PolicyRevision           int64   `json:"policy_revision"`
	AlgorithmVersion         string  `json:"algorithm_version"`
	ExecutionMode            string  `json:"execution_mode"`
	AttemptIndex             int     `json:"attempt_index"`
	Role                     string  `json:"role"`
	State                    string  `json:"state"`
	Result                   string  `json:"result"`
	Winner                   bool    `json:"winner"`
	MemberID                 int     `json:"member_id"`
	ChannelID                int     `json:"channel_id"`
	Region                   string  `json:"region"`
	EndpointAuthority        string  `json:"endpoint_authority"`
	FailureDomainHash        string  `json:"failure_domain_hash"`
	CostKnown                bool    `json:"cost_known"`
	ExpectedCost             float64 `json:"expected_cost,omitempty"`
	WorstCaseCost            float64 `json:"worst_case_cost,omitempty"`
	EffectiveCost            float64 `json:"effective_cost,omitempty"`
	CostCurrency             string  `json:"cost_currency,omitempty"`
	CostUnit                 string  `json:"cost_unit,omitempty"`
	PricingBasis             string  `json:"pricing_basis,omitempty"`
	PricingIdentity          string  `json:"pricing_identity,omitempty"`
	UnknownReason            string  `json:"unknown_reason,omitempty"`
	ConfigurationRevision    int64   `json:"configuration_revision,omitempty"`
	UpstreamCostMultiplier   float64 `json:"upstream_cost_multiplier"`
	BaselineExpectedKnown    bool    `json:"baseline_expected_known"`
	BaselineExpectedCost     float64 `json:"baseline_expected_cost,omitempty"`
	BaselineWorstCaseKnown   bool    `json:"baseline_worst_case_known"`
	BaselineWorstCaseCost    float64 `json:"baseline_worst_case_cost,omitempty"`
	ActualCostKnown          bool    `json:"actual_cost_known"`
	ActualCost               float64 `json:"actual_cost,omitempty"`
	ActualPromptTokens       int64   `json:"actual_prompt_tokens,omitempty"`
	ActualCompletionTokens   int64   `json:"actual_completion_tokens,omitempty"`
	ActualTotalTokens        int64   `json:"actual_total_tokens,omitempty"`
	ActualCacheReadTokens    int64   `json:"actual_cache_read_tokens,omitempty"`
	ActualCacheWriteTokens   int64   `json:"actual_cache_write_tokens,omitempty"`
	ActualCacheWrite1hTokens int64   `json:"actual_cache_write_1h_tokens,omitempty"`
	HTTPStatus               int     `json:"http_status,omitempty"`
	ErrorClassification      string  `json:"error_classification,omitempty"`
	ErrorResponsibility      string  `json:"error_responsibility,omitempty"`
	ErrorRetryability        string  `json:"error_retryability,omitempty"`
	ErrorCode                string  `json:"error_code,omitempty"`
	UpstreamSent             bool    `json:"upstream_sent"`
	ClientCommitted          bool    `json:"client_committed"`
	WillRetry                bool    `json:"will_retry"`
	FinalAttempt             bool    `json:"final_attempt"`
	FirstByteTimeMs          int64   `json:"first_byte_time_ms,omitempty"`
	StartedTimeMs            int64   `json:"started_time_ms"`
	CompletedTimeMs          int64   `json:"completed_time_ms,omitempty"`
	DurationMs               int64   `json:"duration_ms,omitempty"`
}

type RoutingHedgeDecisionAuditSummary struct {
	AttemptCount                int                          `json:"attempt_count"`
	AttemptsTruncated           bool                         `json:"attempts_truncated"`
	AllAttemptsCompleted        bool                         `json:"all_attempts_completed"`
	WinnerRole                  string                       `json:"winner_role,omitempty"`
	FinalMemberID               int                          `json:"final_member_id,omitempty"`
	FinalChannelID              int                          `json:"final_channel_id,omitempty"`
	FinalRegion                 string                       `json:"final_region,omitempty"`
	FinalNodeEpochID            string                       `json:"final_node_epoch_id,omitempty"`
	FinalStableNodeID           string                       `json:"final_stable_node_id,omitempty"`
	FinalStableNodeKnown        bool                         `json:"final_stable_node_known"`
	FinalResult                 string                       `json:"final_result,omitempty"`
	FinalHTTPStatus             int                          `json:"final_http_status,omitempty"`
	FinalErrorClassification    string                       `json:"final_error_classification,omitempty"`
	FinalErrorResponsibility    string                       `json:"final_error_responsibility,omitempty"`
	EstimatedTotalCostKnown     bool                         `json:"estimated_total_cost_known"`
	EstimatedTotalCost          float64                      `json:"estimated_total_cost,omitempty"`
	WorstCaseTotalCostKnown     bool                         `json:"worst_case_total_cost_known"`
	WorstCaseTotalCost          float64                      `json:"worst_case_total_cost,omitempty"`
	DuplicateExpectedCostKnown  bool                         `json:"duplicate_expected_cost_known"`
	DuplicateExpectedCost       float64                      `json:"duplicate_expected_cost,omitempty"`
	DuplicateWorstCaseCostKnown bool                         `json:"duplicate_worst_case_cost_known"`
	DuplicateWorstCaseCost      float64                      `json:"duplicate_worst_case_cost,omitempty"`
	ActualTotalCostKnown        bool                         `json:"actual_total_cost_known"`
	ActualTotalCost             float64                      `json:"actual_total_cost,omitempty"`
	DuplicateActualCostKnown    bool                         `json:"duplicate_actual_cost_known"`
	DuplicateActualCost         float64                      `json:"duplicate_actual_cost,omitempty"`
	CostCurrency                string                       `json:"cost_currency,omitempty"`
	CostUnit                    string                       `json:"cost_unit,omitempty"`
	Attempts                    []RoutingHedgeAttemptSummary `json:"attempts"`
}

type routingHedgeCostBreakdownPayload struct {
	SchemaVersion   int                  `json:"schema_version"`
	Expected        RoutingCostBreakdown `json:"expected"`
	WorstCaseSingle RoutingCostBreakdown `json:"worst_case_single"`
}

func StartRoutingHedgeAttemptAuditContext(
	ctx context.Context,
	spec RoutingHedgeAttemptStartSpec,
) (RoutingHedgeAttemptAudit, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return startRoutingHedgeAttemptAudit(DB.WithContext(ctx), spec)
}

func startRoutingHedgeAttemptAudit(
	db *gorm.DB,
	spec RoutingHedgeAttemptStartSpec,
) (RoutingHedgeAttemptAudit, error) {
	if db == nil {
		return RoutingHedgeAttemptAudit{}, ErrRoutingHedgeAttemptInvalid
	}
	audit, err := newRoutingHedgeAttemptAudit(spec)
	if err != nil {
		return RoutingHedgeAttemptAudit{}, err
	}
	if err := db.Create(&audit).Error; err != nil {
		var existing RoutingHedgeAttemptAudit
		if lookupErr := db.Where("attempt_key = ?", audit.AttemptKey).First(&existing).Error; lookupErr == nil {
			if sameRoutingHedgeAttemptStart(existing, audit) {
				return existing, nil
			}
			return RoutingHedgeAttemptAudit{}, ErrRoutingHedgeAttemptTransition
		}
		return RoutingHedgeAttemptAudit{}, err
	}
	return audit, nil
}

func ValidateRoutingHedgeAttemptStartSpec(spec RoutingHedgeAttemptStartSpec) error {
	_, err := newRoutingHedgeAttemptAudit(spec)
	return err
}

func ValidateRoutingHedgeAttemptCompleteSpec(spec RoutingHedgeAttemptCompleteSpec) error {
	if !validRoutingHedgeAttemptCompleteSpec(spec) {
		return ErrRoutingHedgeAttemptInvalid
	}
	return nil
}

func newRoutingHedgeAttemptAudit(spec RoutingHedgeAttemptStartSpec) (RoutingHedgeAttemptAudit, error) {
	if !validRoutingHedgeAttemptStartSpec(spec) {
		return RoutingHedgeAttemptAudit{}, ErrRoutingHedgeAttemptInvalid
	}
	authority, err := normalizeRoutingHedgeEndpointAuthority(spec.EndpointAuthority)
	if err != nil {
		return RoutingHedgeAttemptAudit{}, err
	}
	breakdown, err := common.Marshal(routingHedgeCostBreakdownPayload{
		SchemaVersion: RoutingHedgeCostAuditSchemaVersion,
		Expected:      spec.Cost.ExpectedBreakdown, WorstCaseSingle: spec.Cost.WorstSingleBreakdown,
	})
	if err != nil || len(breakdown) > routingHedgeAuditJSONMaxBytes {
		return RoutingHedgeAttemptAudit{}, ErrRoutingHedgeAttemptCostInvalid
	}
	attemptKey := spec.AttemptKey
	if attemptKey == "" {
		attemptToken := make([]byte, 32)
		if _, err := rand.Read(attemptToken); err != nil {
			return RoutingHedgeAttemptAudit{}, err
		}
		attemptKey = hex.EncodeToString(attemptToken)
	}
	startedAt := spec.StartedTimeMs
	if startedAt <= 0 {
		startedAt = time.Now().UnixMilli()
	}
	revision := int64(spec.PolicyRevision)
	if revision <= 0 || uint64(revision) != spec.PolicyRevision {
		return RoutingHedgeAttemptAudit{}, ErrRoutingHedgeAttemptInvalid
	}
	audit := RoutingHedgeAttemptAudit{
		AttemptKey:      attemptKey,
		DecisionID:      spec.DecisionID,
		RequestKey:      routingHedgeAuditHash("request", spec.RequestID),
		NodeEpochID:     spec.NodeEpochID,
		StableNodeID:    spec.StableNodeID,
		StableNodeKnown: spec.StableNodeKnown,
		SchemaVersion:   RoutingHedgeAttemptAuditSchemaVersion,
		PolicyRevision:  revision, AlgorithmVersion: spec.AlgorithmVersion,
		PoolID: spec.PoolID, MemberID: spec.MemberID,
		ChannelID: spec.ChannelID,
		CredentialReferenceHash: routingHedgeAuditHash(
			"credential", strings.Join([]string{
				strconv.FormatUint(spec.PolicyRevision, 10), strconv.Itoa(spec.ChannelID), strconv.Itoa(spec.CredentialID),
			}, "\x00"),
		),
		ModelKey:      routingHedgeAuditHash("model", spec.ModelName),
		ExecutionMode: spec.ExecutionMode, AttemptIndex: spec.AttemptIndex,
		Role: spec.Role, State: RoutingHedgeAttemptStateStarted,
		Result:                RoutingHedgeAttemptResultPending,
		EndpointAuthority:     authority,
		EndpointAuthorityHash: routingHedgeAuditHash("endpoint", authority),
		FailureDomainHash:     routingHedgeAuditHash("failure-domain", authority+"\x00"+spec.Region),
		Region:                spec.Region, CrossRegion: false,
		CostSchemaVersion: RoutingHedgeCostAuditSchemaVersion, CostKnown: spec.Cost.Known,
		ExpectedCost: spec.Cost.ExpectedCost, WorstCaseCost: spec.Cost.WorstCaseCost,
		EffectiveCost: spec.Cost.EffectiveCost, CostCurrency: spec.Cost.Currency,
		CostUnit: spec.Cost.Unit, PricingBasis: spec.Cost.PricingBasis,
		PricingHash: spec.Cost.PricingHash, PricingVersion: spec.Cost.PricingVersion,
		PricingIdentity: spec.Cost.PricingIdentity, CostUnknownReason: spec.Cost.UnknownReason,
		ConfigurationRevision:  spec.Cost.ConfigurationRevision,
		UpstreamCostMultiplier: spec.Cost.UpstreamCostMultiplier,
		BaselineExpectedKnown:  spec.Cost.BaselineExpectedKnown,
		BaselineExpectedCost:   spec.Cost.BaselineExpectedCost,
		BaselineWorstCaseKnown: spec.Cost.BaselineWorstCaseKnown,
		BaselineWorstCaseCost:  spec.Cost.BaselineWorstCaseCost,
		CostConfidenceScore:    spec.Cost.ConfidenceScore, CostFreshnessScore: spec.Cost.FreshnessScore,
		CostBreakdownJSON: string(breakdown), CostObservedTime: spec.Cost.ObservedTime,
		CostEffectiveTime: spec.Cost.EffectiveTime, CostExpiresTime: spec.Cost.ExpiresTime,
		StartedTimeMs: startedAt, CreatedTimeMs: startedAt, UpdatedTimeMs: startedAt,
	}
	return audit, nil
}

func sameRoutingHedgeAttemptStart(existing RoutingHedgeAttemptAudit, expected RoutingHedgeAttemptAudit) bool {
	existing.ID = 0
	existing.State = RoutingHedgeAttemptStateStarted
	existing.Result = RoutingHedgeAttemptResultPending
	existing.Winner = false
	existing.HTTPStatus = 0
	existing.ErrorClassification = ""
	existing.ErrorResponsibility = ""
	existing.ErrorRetryability = ""
	existing.ErrorCode = ""
	existing.UpstreamSent = false
	existing.ClientCommitted = false
	existing.WillRetry = false
	existing.FinalAttempt = false
	existing.FirstByteTimeMs = 0
	existing.ActualCostKnown = false
	existing.ActualCost = 0
	existing.ActualPromptTokens = 0
	existing.ActualCompletionTokens = 0
	existing.ActualTotalTokens = 0
	existing.ActualCacheReadTokens = 0
	existing.ActualCacheWriteTokens = 0
	existing.ActualCacheWrite1hTokens = 0
	existing.CompletedTimeMs = 0
	existing.DurationMs = 0
	existing.UpdatedTimeMs = existing.CreatedTimeMs
	expected.ID = 0
	return existing == expected
}

func CompleteRoutingHedgeAttemptAuditContext(
	ctx context.Context,
	id int64,
	spec RoutingHedgeAttemptCompleteSpec,
) (RoutingHedgeAttemptAudit, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return completeRoutingHedgeAttemptAudit(DB.WithContext(ctx), id, spec)
}

func completeRoutingHedgeAttemptAudit(
	db *gorm.DB,
	id int64,
	spec RoutingHedgeAttemptCompleteSpec,
) (RoutingHedgeAttemptAudit, error) {
	if db == nil || id <= 0 || !validRoutingHedgeAttemptCompleteSpec(spec) {
		return RoutingHedgeAttemptAudit{}, ErrRoutingHedgeAttemptInvalid
	}
	var current RoutingHedgeAttemptAudit
	if err := db.Where("id = ?", id).First(&current).Error; err != nil {
		return RoutingHedgeAttemptAudit{}, err
	}
	completedAt := spec.CompletedTimeMs
	if completedAt <= 0 {
		completedAt = time.Now().UnixMilli()
	}
	if completedAt < current.StartedTimeMs {
		completedAt = current.StartedTimeMs
	}
	if spec.FirstByteTimeMs > 0 &&
		(spec.FirstByteTimeMs < current.StartedTimeMs || spec.FirstByteTimeMs > completedAt) {
		return RoutingHedgeAttemptAudit{}, ErrRoutingHedgeAttemptInvalid
	}
	assignments := map[string]any{
		"state": RoutingHedgeAttemptStateCompleted, "result": spec.Result, "winner": spec.Winner,
		"http_status": spec.HTTPStatus, "error_classification": spec.ErrorClassification,
		"error_responsibility": spec.ErrorResponsibility,
		"error_retryability":   spec.ErrorRetryability, "error_code": spec.ErrorCode,
		"upstream_sent": spec.UpstreamSent, "client_committed": spec.ClientCommitted,
		"will_retry": spec.WillRetry, "final_attempt": spec.FinalAttempt,
		"first_byte_time_ms": spec.FirstByteTimeMs,
		"actual_cost_known":  spec.ActualCostKnown, "actual_cost": spec.ActualCost,
		"actual_prompt_tokens":         spec.ActualPromptTokens,
		"actual_completion_tokens":     spec.ActualCompletionTokens,
		"actual_total_tokens":          spec.ActualTotalTokens,
		"actual_cache_read_tokens":     spec.ActualCacheReadTokens,
		"actual_cache_write_tokens":    spec.ActualCacheWriteTokens,
		"actual_cache_write_1h_tokens": spec.ActualCacheWrite1hTokens,
		"completed_time_ms":            completedAt, "duration_ms": completedAt - current.StartedTimeMs,
		"updated_time_ms": completedAt,
	}
	result := db.Model(&RoutingHedgeAttemptAudit{}).
		Where("id = ? AND state = ?", id, RoutingHedgeAttemptStateStarted).
		Updates(assignments)
	if result.Error != nil {
		return RoutingHedgeAttemptAudit{}, result.Error
	}
	if result.RowsAffected == 0 {
		if err := db.Where("id = ?", id).First(&current).Error; err != nil {
			return RoutingHedgeAttemptAudit{}, err
		}
		if current.State == RoutingHedgeAttemptStateCompleted &&
			current.Result == spec.Result && current.Winner == spec.Winner &&
			current.HTTPStatus == spec.HTTPStatus &&
			current.ErrorClassification == spec.ErrorClassification &&
			current.ErrorResponsibility == spec.ErrorResponsibility &&
			current.ErrorRetryability == spec.ErrorRetryability && current.ErrorCode == spec.ErrorCode &&
			current.UpstreamSent == spec.UpstreamSent && current.ClientCommitted == spec.ClientCommitted &&
			current.WillRetry == spec.WillRetry && current.FinalAttempt == spec.FinalAttempt &&
			current.FirstByteTimeMs == spec.FirstByteTimeMs &&
			current.ActualCostKnown == spec.ActualCostKnown && current.ActualCost == spec.ActualCost &&
			current.ActualPromptTokens == spec.ActualPromptTokens &&
			current.ActualCompletionTokens == spec.ActualCompletionTokens &&
			current.ActualTotalTokens == spec.ActualTotalTokens &&
			current.ActualCacheReadTokens == spec.ActualCacheReadTokens &&
			current.ActualCacheWriteTokens == spec.ActualCacheWriteTokens &&
			current.ActualCacheWrite1hTokens == spec.ActualCacheWrite1hTokens {
			return current, nil
		}
		return RoutingHedgeAttemptAudit{}, ErrRoutingHedgeAttemptTransition
	}
	if err := db.Where("id = ?", id).First(&current).Error; err != nil {
		return RoutingHedgeAttemptAudit{}, err
	}
	return current, nil
}

func DeleteRoutingHedgeAttemptAuditsBeforeContext(ctx context.Context, cutoffMs int64) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cutoffMs <= 0 {
		return 0, nil
	}
	var deleted int64
	for {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		var ids []int64
		if err := DB.WithContext(ctx).Model(&RoutingHedgeAttemptAudit{}).
			Where("state = ? AND completed_time_ms > 0 AND completed_time_ms < ?",
				RoutingHedgeAttemptStateCompleted, cutoffMs).
			Order("id ASC").Limit(routingHedgeAttemptRetentionBatch).Pluck("id", &ids).Error; err != nil {
			return deleted, err
		}
		if len(ids) == 0 {
			return deleted, nil
		}
		result := DB.WithContext(ctx).Where("id IN ? AND state = ?", ids, RoutingHedgeAttemptStateCompleted).
			Delete(&RoutingHedgeAttemptAudit{})
		if result.Error != nil {
			return deleted, result.Error
		}
		deleted += result.RowsAffected
		if len(ids) < routingHedgeAttemptRetentionBatch {
			return deleted, nil
		}
	}
}

func GetRoutingHedgeDecisionAuditContext(
	ctx context.Context,
	decisionID string,
	requestID string,
) (RoutingHedgeDecisionAuditSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	decisionID = strings.TrimSpace(decisionID)
	requestID = strings.TrimSpace(requestID)
	if !validRoutingHedgeAuditText(decisionID, 64) || !validRoutingHedgeAuditText(requestID, 256) ||
		(decisionID == "" && requestID == "") {
		return RoutingHedgeDecisionAuditSummary{}, ErrRoutingHedgeAttemptInvalid
	}
	var rows []RoutingHedgeAttemptSummary
	var err error
	requestKey := ""
	if requestID != "" {
		requestKey = routingHedgeAuditHash("request", requestID)
	}
	if decisionID != "" && requestKey != "" {
		var mismatched int64
		if err := DB.WithContext(ctx).Model(&RoutingHedgeAttemptAudit{}).
			Where("decision_id = ? AND request_key <> ? AND request_key <> ?", decisionID, requestKey, "").
			Count(&mismatched).Error; err != nil {
			return RoutingHedgeDecisionAuditSummary{}, err
		}
		if mismatched > 0 {
			return RoutingHedgeDecisionAuditSummary{}, ErrRoutingHedgeAttemptInvalid
		}
	}
	useRequestTimeline := false
	if decisionID != "" && requestKey != "" {
		var matched int64
		if err := DB.WithContext(ctx).Model(&RoutingHedgeAttemptAudit{}).
			Where("decision_id = ? AND request_key = ?", decisionID, requestKey).
			Count(&matched).Error; err != nil {
			return RoutingHedgeDecisionAuditSummary{}, err
		}
		var decisionRows int64
		if err := DB.WithContext(ctx).Model(&RoutingHedgeAttemptAudit{}).
			Where("decision_id = ?", decisionID).Count(&decisionRows).Error; err != nil {
			return RoutingHedgeDecisionAuditSummary{}, err
		}
		useRequestTimeline = matched > 0 || decisionRows == 0
	} else if requestKey != "" {
		useRequestTimeline = true
	}
	if useRequestTimeline {
		rows, err = loadRoutingHedgeAttemptSummaries(
			DB.WithContext(ctx).Where("request_key = ?", requestKey),
			routingHedgeAttemptsPerDecision+1,
		)
		if err != nil {
			return RoutingHedgeDecisionAuditSummary{}, err
		}
	} else if decisionID != "" {
		rows, err = loadRoutingHedgeAttemptSummaries(
			DB.WithContext(ctx).Where("decision_id = ?", decisionID),
			routingHedgeAttemptsPerDecision+1,
		)
		if err != nil {
			return RoutingHedgeDecisionAuditSummary{}, err
		}
	}
	return buildRoutingHedgeDecisionAuditSummary(rows), nil
}

func GetRoutingHedgeDecisionAuditsContext(
	ctx context.Context,
	decisionIDs []string,
) (map[string]RoutingHedgeDecisionAuditSummary, error) {
	return getRoutingHedgeDecisionAuditsDBContext(ctx, DB, decisionIDs)
}

type routingAttemptTimelineReference struct {
	DecisionID string
	RequestKey string
}

func getRoutingAttemptTimelinesDBContext(
	ctx context.Context,
	db *gorm.DB,
	references []routingAttemptTimelineReference,
) (map[string]RoutingHedgeDecisionAuditSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil || len(references) > RoutingAuditExportMaxRecords {
		return nil, ErrRoutingHedgeAttemptInvalid
	}
	if len(references) == 0 {
		return map[string]RoutingHedgeDecisionAuditSummary{}, nil
	}

	requestKeys := make([]string, 0, len(references))
	decisionIDs := make([]string, 0, len(references))
	seenRequestKeys := make(map[string]struct{}, len(references))
	seenDecisionIDs := make(map[string]struct{}, len(references))
	for _, reference := range references {
		if strings.TrimSpace(reference.DecisionID) != reference.DecisionID ||
			reference.DecisionID == "" || !validRoutingHedgeAuditText(reference.DecisionID, 64) ||
			(reference.RequestKey != "" && !validRoutingHedgeAuditHash(reference.RequestKey)) {
			return nil, ErrRoutingHedgeAttemptInvalid
		}
		if reference.RequestKey != "" {
			if _, exists := seenRequestKeys[reference.RequestKey]; !exists {
				seenRequestKeys[reference.RequestKey] = struct{}{}
				requestKeys = append(requestKeys, reference.RequestKey)
			}
		}
		if _, exists := seenDecisionIDs[reference.DecisionID]; !exists {
			seenDecisionIDs[reference.DecisionID] = struct{}{}
			decisionIDs = append(decisionIDs, reference.DecisionID)
		}
	}

	requestRows := make(map[string][]RoutingHedgeAttemptSummary, len(requestKeys))
	for start := 0; start < len(requestKeys); start += routingHedgeDecisionQueryBatch {
		end := min(start+routingHedgeDecisionQueryBatch, len(requestKeys))
		batch := requestKeys[start:end]
		rows, err := loadRoutingHedgeAttemptSummaries(
			db.WithContext(ctx).
				Where("request_key IN ?", batch).
				Where(`(
					SELECT COUNT(*) FROM routing_hedge_attempt_audits AS prior
					WHERE prior.request_key = routing_hedge_attempt_audits.request_key
					AND (prior.started_time_ms < routing_hedge_attempt_audits.started_time_ms
						OR (prior.started_time_ms = routing_hedge_attempt_audits.started_time_ms
							AND prior.id <= routing_hedge_attempt_audits.id))
				) <= ?`, routingHedgeAttemptsPerDecision+1),
			len(batch)*(routingHedgeAttemptsPerDecision+1),
		)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			requestRows[row.RequestKey] = append(requestRows[row.RequestKey], row)
		}
	}

	fallbackDecisionIDs := make([]string, 0, len(decisionIDs))
	for _, reference := range references {
		if reference.RequestKey != "" && len(requestRows[reference.RequestKey]) > 0 {
			continue
		}
		if _, exists := seenDecisionIDs[reference.DecisionID]; exists {
			fallbackDecisionIDs = append(fallbackDecisionIDs, reference.DecisionID)
			delete(seenDecisionIDs, reference.DecisionID)
		}
	}
	decisionRows := make(map[string][]RoutingHedgeAttemptSummary, len(fallbackDecisionIDs))
	for start := 0; start < len(fallbackDecisionIDs); start += routingHedgeDecisionQueryBatch {
		end := min(start+routingHedgeDecisionQueryBatch, len(fallbackDecisionIDs))
		batch := fallbackDecisionIDs[start:end]
		rows, err := loadRoutingHedgeAttemptSummaries(
			db.WithContext(ctx).
				Where("decision_id IN ?", batch).
				Where(`(
					SELECT COUNT(*) FROM routing_hedge_attempt_audits AS prior
					WHERE prior.decision_id = routing_hedge_attempt_audits.decision_id
					AND (prior.started_time_ms < routing_hedge_attempt_audits.started_time_ms
						OR (prior.started_time_ms = routing_hedge_attempt_audits.started_time_ms
							AND prior.id <= routing_hedge_attempt_audits.id))
				) <= ?`, routingHedgeAttemptsPerDecision+1),
			len(batch)*(routingHedgeAttemptsPerDecision+1),
		)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			decisionRows[row.DecisionID] = append(decisionRows[row.DecisionID], row)
		}
	}

	result := make(map[string]RoutingHedgeDecisionAuditSummary, len(references))
	for _, reference := range references {
		rows := requestRows[reference.RequestKey]
		if len(rows) == 0 {
			rows = decisionRows[reference.DecisionID]
		}
		if len(rows) > 0 {
			result[reference.DecisionID] = buildRoutingHedgeDecisionAuditSummary(rows)
		}
	}
	return result, nil
}

func getRoutingHedgeDecisionAuditsDBContext(
	ctx context.Context,
	db *gorm.DB,
	decisionIDs []string,
) (map[string]RoutingHedgeDecisionAuditSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil {
		return nil, ErrRoutingHedgeAttemptInvalid
	}
	if len(decisionIDs) == 0 {
		return map[string]RoutingHedgeDecisionAuditSummary{}, nil
	}
	if len(decisionIDs) > RoutingAuditExportMaxRecords {
		return nil, ErrRoutingHedgeAttemptInvalid
	}
	unique := make([]string, 0, len(decisionIDs))
	seen := make(map[string]struct{}, len(decisionIDs))
	for _, decisionID := range decisionIDs {
		decisionID = strings.TrimSpace(decisionID)
		if decisionID == "" || !validRoutingHedgeAuditText(decisionID, 64) {
			return nil, ErrRoutingHedgeAttemptInvalid
		}
		if _, exists := seen[decisionID]; exists {
			continue
		}
		seen[decisionID] = struct{}{}
		unique = append(unique, decisionID)
	}
	grouped := make(map[string][]RoutingHedgeAttemptSummary, len(unique))
	truncated := make(map[string]bool)
	for start := 0; start < len(unique); start += routingHedgeDecisionQueryBatch {
		end := min(start+routingHedgeDecisionQueryBatch, len(unique))
		batch := unique[start:end]
		rows, err := loadRoutingHedgeAttemptSummaries(
			db.WithContext(ctx).Where("decision_id IN ?", batch),
			len(batch)*(routingHedgeAttemptsPerDecision+1),
		)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			items := grouped[row.DecisionID]
			if len(items) >= routingHedgeAttemptsPerDecision {
				truncated[row.DecisionID] = true
				continue
			}
			grouped[row.DecisionID] = append(items, row)
		}
	}
	result := make(map[string]RoutingHedgeDecisionAuditSummary, len(grouped))
	for decisionID, rows := range grouped {
		summary := buildRoutingHedgeDecisionAuditSummary(rows)
		summary.AttemptsTruncated = summary.AttemptsTruncated || truncated[decisionID]
		if summary.AttemptsTruncated {
			summary.AllAttemptsCompleted = false
			summary.EstimatedTotalCostKnown = false
			summary.EstimatedTotalCost = 0
			summary.WorstCaseTotalCostKnown = false
			summary.WorstCaseTotalCost = 0
			summary.ActualTotalCostKnown = false
			summary.ActualTotalCost = 0
		}
		result[decisionID] = summary
	}
	return result, nil
}

func loadRoutingHedgeAttemptSummaries(query *gorm.DB, limit int) ([]RoutingHedgeAttemptSummary, error) {
	if query == nil || limit < 1 {
		return nil, ErrRoutingHedgeAttemptInvalid
	}
	var rows []RoutingHedgeAttemptSummary
	err := query.Model(&RoutingHedgeAttemptAudit{}).
		Select([]string{
			"decision_id", "request_key", "node_epoch_id", "stable_node_id", "stable_node_known", "policy_revision",
			"algorithm_version", "execution_mode", "attempt_index", "role", "state", "result", "winner",
			"member_id", "channel_id", "region", "endpoint_authority", "failure_domain_hash",
			"cost_known", "expected_cost", "worst_case_cost", "effective_cost", "cost_currency", "cost_unit",
			"pricing_basis", "pricing_identity", "cost_unknown_reason AS unknown_reason", "configuration_revision",
			"upstream_cost_multiplier", "baseline_expected_known", "baseline_expected_cost",
			"baseline_worst_case_known", "baseline_worst_case_cost",
			"actual_cost_known", "actual_cost", "actual_prompt_tokens", "actual_completion_tokens",
			"actual_total_tokens", "actual_cache_read_tokens", "actual_cache_write_tokens",
			"actual_cache_write_1h_tokens", "http_status", "error_classification", "error_retryability",
			"error_responsibility", "error_code", "upstream_sent", "client_committed", "will_retry",
			"final_attempt", "first_byte_time_ms", "started_time_ms", "completed_time_ms", "duration_ms",
		}).
		Order("started_time_ms asc").Order("id asc").Order("decision_id asc").Limit(limit).Scan(&rows).Error
	return rows, err
}

func buildRoutingHedgeDecisionAuditSummary(rows []RoutingHedgeAttemptSummary) RoutingHedgeDecisionAuditSummary {
	summary := RoutingHedgeDecisionAuditSummary{
		Attempts:                make([]RoutingHedgeAttemptSummary, 0, min(len(rows), routingHedgeAttemptsPerDecision)),
		AllAttemptsCompleted:    len(rows) > 0,
		EstimatedTotalCostKnown: len(rows) > 0,
		WorstCaseTotalCostKnown: len(rows) > 0,
		ActualTotalCostKnown:    len(rows) > 0,
	}
	for index, row := range rows {
		if index >= routingHedgeAttemptsPerDecision {
			summary.AttemptsTruncated = true
			break
		}
		row.DecisionID = ""
		row.RequestKey = ""
		summary.Attempts = append(summary.Attempts, row)
		summary.AttemptCount++
		summary.AllAttemptsCompleted = summary.AllAttemptsCompleted && row.State == RoutingHedgeAttemptStateCompleted
		if !row.CostKnown || (summary.CostCurrency != "" && summary.CostCurrency != row.CostCurrency) ||
			(summary.CostUnit != "" && summary.CostUnit != row.CostUnit) {
			summary.EstimatedTotalCostKnown = false
			summary.WorstCaseTotalCostKnown = false
		}
		if summary.CostCurrency == "" {
			summary.CostCurrency = row.CostCurrency
		}
		if summary.CostUnit == "" {
			summary.CostUnit = row.CostUnit
		}
		summary.EstimatedTotalCost += row.ExpectedCost
		summary.WorstCaseTotalCost += row.WorstCaseCost
		if !row.ActualCostKnown {
			summary.ActualTotalCostKnown = false
		}
		summary.ActualTotalCost += row.ActualCost
		if row.Winner {
			summary.WinnerRole = row.Role
			setRoutingAttemptFinalSummary(&summary, row)
		} else if row.FinalAttempt && summary.WinnerRole == "" {
			setRoutingAttemptFinalSummary(&summary, row)
		}
		if row.Role == RoutingHedgeAttemptRoleSecondary {
			summary.DuplicateExpectedCostKnown = row.CostKnown
			summary.DuplicateExpectedCost = row.ExpectedCost
			summary.DuplicateWorstCaseCostKnown = row.CostKnown
			summary.DuplicateWorstCaseCost = row.WorstCaseCost
			summary.DuplicateActualCostKnown = row.ActualCostKnown
			if row.ActualCostKnown {
				summary.DuplicateActualCost = row.ActualCost
			}
		}
	}
	if summary.AttemptsTruncated {
		summary.AllAttemptsCompleted = false
		summary.EstimatedTotalCostKnown = false
		summary.WorstCaseTotalCostKnown = false
		summary.ActualTotalCostKnown = false
	}
	if !summary.EstimatedTotalCostKnown {
		summary.EstimatedTotalCost = 0
	}
	if !summary.WorstCaseTotalCostKnown {
		summary.WorstCaseTotalCost = 0
	}
	if !summary.ActualTotalCostKnown {
		summary.ActualTotalCost = 0
	}
	if !summary.DuplicateActualCostKnown {
		summary.DuplicateActualCost = 0
	}
	return summary
}

func setRoutingAttemptFinalSummary(
	summary *RoutingHedgeDecisionAuditSummary,
	row RoutingHedgeAttemptSummary,
) {
	if summary == nil {
		return
	}
	summary.FinalMemberID = row.MemberID
	summary.FinalChannelID = row.ChannelID
	summary.FinalRegion = row.Region
	summary.FinalNodeEpochID = row.NodeEpochID
	summary.FinalStableNodeID = row.StableNodeID
	summary.FinalStableNodeKnown = row.StableNodeKnown
	summary.FinalResult = row.Result
	summary.FinalHTTPStatus = row.HTTPStatus
	summary.FinalErrorClassification = row.ErrorClassification
	summary.FinalErrorResponsibility = row.ErrorResponsibility
}

func validRoutingHedgeAttemptStartSpec(spec RoutingHedgeAttemptStartSpec) bool {
	return (spec.AttemptKey == "" || validRoutingHedgeAuditHash(spec.AttemptKey)) &&
		validRoutingHedgeAuditText(spec.DecisionID, 64) &&
		strings.TrimSpace(spec.RequestID) != "" && validRoutingHedgeAuditText(spec.RequestID, 256) &&
		validRoutingNodeEpoch(spec.NodeEpochID) && validRoutingStableNode(spec.StableNodeID, spec.StableNodeKnown) &&
		spec.PolicyRevision > 0 && spec.PoolID > 0 && spec.MemberID > 0 && spec.ChannelID > 0 &&
		strings.TrimSpace(spec.AlgorithmVersion) != "" && validRoutingHedgeAuditText(spec.AlgorithmVersion, 128) &&
		spec.CredentialID >= 0 && strings.TrimSpace(spec.ModelName) != "" &&
		validRoutingHedgeAuditText(spec.ModelName, 128) && validRoutingAttemptExecution(spec.ExecutionMode) &&
		spec.AttemptIndex >= 0 && spec.AttemptIndex < routingHedgeAttemptsPerDecision &&
		validRoutingHedgeAttemptRole(spec.ExecutionMode, spec.Role) &&
		validRoutingHedgeAuditText(spec.Region, 64) && strings.TrimSpace(spec.Region) != "" &&
		validRoutingHedgeCostSpec(spec.Cost)
}

func validRoutingHedgeCostSpec(spec RoutingHedgeAttemptCostSpec) bool {
	values := []float64{
		spec.ExpectedCost, spec.WorstCaseCost, spec.EffectiveCost,
		spec.UpstreamCostMultiplier, spec.BaselineExpectedCost, spec.BaselineWorstCaseCost,
		spec.ConfidenceScore, spec.FreshnessScore,
	}
	for _, value := range values {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	if spec.ConfigurationRevision < 0 || spec.UpstreamCostMultiplier > RoutingChannelUpstreamCostMultiplierMaximum ||
		(!spec.BaselineExpectedKnown && spec.BaselineExpectedCost != 0) ||
		(!spec.BaselineWorstCaseKnown && spec.BaselineWorstCaseCost != 0) ||
		(spec.BaselineExpectedKnown && spec.BaselineWorstCaseKnown &&
			spec.BaselineWorstCaseCost < spec.BaselineExpectedCost) ||
		(spec.Currency != "" && !validRoutingHedgeAuditText(spec.Currency, 16)) ||
		(spec.Unit != "" && !validRoutingHedgeAuditText(spec.Unit, 32)) ||
		(spec.PricingBasis != "" && !validRoutingHedgeAuditText(spec.PricingBasis, 64)) ||
		(spec.PricingHash != "" && !validRoutingHedgeAuditHash(spec.PricingHash)) ||
		(spec.PricingVersion != "" && !validRoutingHedgeAuditText(spec.PricingVersion, 128)) ||
		(spec.PricingIdentity != "" && !validRoutingHedgeAuditText(spec.PricingIdentity, 128)) ||
		(spec.UnknownReason != "" && !validRoutingHedgeAuditText(spec.UnknownReason, 128)) ||
		spec.ConfidenceScore > 1 || spec.FreshnessScore > 1 {
		return false
	}
	timeMetadataPresent := spec.ObservedTime != 0 || spec.EffectiveTime != 0 || spec.ExpiresTime != 0
	if timeMetadataPresent && (spec.ObservedTime <= 0 || spec.EffectiveTime <= 0 || spec.ExpiresTime < spec.EffectiveTime) {
		return false
	}
	if !spec.Known {
		if spec == (RoutingHedgeAttemptCostSpec{}) {
			return true
		}
		return spec.ExpectedCost == 0 && spec.WorstCaseCost == 0 && spec.EffectiveCost == 0 &&
			spec.ExpectedBreakdown == (RoutingCostBreakdown{}) && spec.WorstSingleBreakdown == (RoutingCostBreakdown{}) &&
			strings.TrimSpace(spec.UnknownReason) != "" &&
			(spec.PricingIdentity == "" || spec.ConfigurationRevision > 0)
	}
	return spec.WorstCaseCost >= spec.ExpectedCost && spec.UnknownReason == "" &&
		validRoutingHedgeAuditText(spec.Currency, 16) && strings.TrimSpace(spec.Currency) != "" &&
		validRoutingHedgeAuditText(spec.Unit, 32) && strings.TrimSpace(spec.Unit) != "" &&
		validRoutingHedgeAuditText(spec.PricingBasis, 64) && strings.TrimSpace(spec.PricingBasis) != "" &&
		validRoutingHedgeAuditHash(spec.PricingHash) &&
		validRoutingHedgeAuditText(spec.PricingVersion, 128) && strings.TrimSpace(spec.PricingVersion) != "" &&
		validRoutingHedgeAuditText(spec.PricingIdentity, 128) && strings.TrimSpace(spec.PricingIdentity) != "" &&
		spec.ConfigurationRevision > 0 &&
		spec.ConfidenceScore <= 1 && spec.FreshnessScore <= 1 && spec.ObservedTime > 0 &&
		spec.EffectiveTime > 0 && spec.ExpiresTime >= spec.EffectiveTime
}

func validRoutingHedgeAttemptCompleteSpec(spec RoutingHedgeAttemptCompleteSpec) bool {
	if !validRoutingHedgeAttemptResult(spec.Result) || spec.HTTPStatus < 0 || spec.HTTPStatus > 599 ||
		!validRoutingHedgeAuditText(spec.ErrorClassification, 64) ||
		!validRoutingHedgeAuditText(spec.ErrorResponsibility, 32) ||
		!validRoutingHedgeAuditText(spec.ErrorRetryability, 32) ||
		!validRoutingHedgeAuditText(spec.ErrorCode, 64) || spec.FirstByteTimeMs < 0 ||
		(spec.WillRetry && (spec.ClientCommitted || spec.FinalAttempt || spec.Result == RoutingHedgeAttemptResultSuccess)) ||
		(spec.Winner && (!spec.UpstreamSent || spec.Result != RoutingHedgeAttemptResultSuccess)) ||
		(spec.Result == RoutingHedgeAttemptResultSuccess && !spec.UpstreamSent) {
		return false
	}
	const maxActualTokens = int64(1_000_000_000_000)
	tokens := []int64{
		spec.ActualPromptTokens, spec.ActualCompletionTokens, spec.ActualTotalTokens,
		spec.ActualCacheReadTokens, spec.ActualCacheWriteTokens, spec.ActualCacheWrite1hTokens,
	}
	for _, value := range tokens {
		if value < 0 || value > maxActualTokens {
			return false
		}
	}
	if !spec.ActualCostKnown {
		return spec.ActualCost == 0 && spec.ActualPromptTokens == 0 && spec.ActualCompletionTokens == 0 &&
			spec.ActualTotalTokens == 0 && spec.ActualCacheReadTokens == 0 &&
			spec.ActualCacheWriteTokens == 0 && spec.ActualCacheWrite1hTokens == 0
	}
	return spec.UpstreamSent && !math.IsNaN(spec.ActualCost) && !math.IsInf(spec.ActualCost, 0) && spec.ActualCost >= 0 &&
		spec.ActualPromptTokens <= maxActualTokens-spec.ActualCompletionTokens &&
		spec.ActualTotalTokens == spec.ActualPromptTokens+spec.ActualCompletionTokens &&
		spec.ActualCacheReadTokens <= spec.ActualPromptTokens &&
		spec.ActualCacheWriteTokens <= spec.ActualPromptTokens &&
		spec.ActualCacheWrite1hTokens <= spec.ActualPromptTokens
}

func normalizeRoutingHedgeEndpointAuthority(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || (parsed.Path != "" && parsed.Path != "/") {
		return "", ErrRoutingHedgeAttemptInvalid
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), nil
}

func routingHedgeAuditHash(kind string, value string) string {
	return hex.EncodeToString(common.Sha256Raw([]byte("routing-hedge-audit:v1\x00" + kind + "\x00" + value)))
}

func validRoutingAttemptExecution(mode string) bool {
	return mode == RoutingAttemptExecutionSerial || mode == RoutingAttemptExecutionHedge
}

func validRoutingHedgeAttemptRole(mode string, role string) bool {
	if mode == RoutingAttemptExecutionSerial {
		return role == RoutingAttemptRoleSerial
	}
	return mode == RoutingAttemptExecutionHedge &&
		(role == RoutingHedgeAttemptRolePrimary || role == RoutingHedgeAttemptRoleSecondary)
}

func validRoutingNodeEpoch(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validRoutingStableNode(value string, known bool) bool {
	if !known {
		return value == ""
	}
	if strings.TrimSpace(value) == "" || !validRoutingHedgeAuditText(value, 128) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
}

func validRoutingHedgeAttemptResult(result string) bool {
	switch result {
	case RoutingHedgeAttemptResultSuccess, RoutingHedgeAttemptResultUpstreamError,
		RoutingHedgeAttemptResultHedgeLost, RoutingHedgeAttemptResultClientCanceled,
		RoutingHedgeAttemptResultResponseTooLarge, RoutingHedgeAttemptResultInternalError:
		return true
	default:
		return false
	}
}

func validRoutingHedgeAuditHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validRoutingHedgeAuditText(value string, maxRunes int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes &&
		!strings.ContainsRune(value, '\x00') && maxRunes <= routingHedgeAuditTextMaxRunes
}
