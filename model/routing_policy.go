package model

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingPolicySchemaVersion        = 1
	RoutingPolicyMaxMembersPerPool    = 4_096
	RoutingPolicyCanaryMinBasisPoints = 100
	RoutingPolicyCanaryMaxBasisPoints = 500

	RoutingDeploymentStageObserve = "observe"
	RoutingDeploymentStageShadow  = "shadow"
	RoutingDeploymentStageCanary  = "canary"
	RoutingDeploymentStageActive  = "active"

	RoutingPolicyProfileBalanced         = "balanced"
	RoutingPolicyProfileReliabilityFirst = "reliability_first"
	RoutingPolicyProfileCostAware        = "cost_aware"
	RoutingPolicyProfileEnterpriseSLO    = "enterprise_slo"
	RoutingPolicyProfileCustom           = "custom"

	RoutingConfigEventPolicyRevision = "routing_policy_revision"

	routingPolicyHeadID                    = 1
	routingPolicyMaxCanonicalBytes         = 64 << 20
	routingPolicyMaxConfigBytes            = 32 << 10
	routingPolicyMaxCredentialRefs         = 1_000_000
	routingPolicyMaxCredentialIDsPerMember = 4_096
	routingPolicyPoolInsertBatch           = 200
	routingPolicyMemberInsertBatch         = 500
	routingPolicyInsertBatchMaxBytes       = 1 << 20
	routingPolicyReasonMaxRunes            = 512
	routingPolicyCredentialIDsMaxBytes     = 60 << 10
	routingConfigOutboxPayloadMaxBytes     = 60 << 10
	routingCheckpointPayloadMaxBytes       = 60 << 10
	routingControlPlaneRetentionBatch      = 500
)

var (
	ErrRoutingPolicyInvalid            = errors.New("invalid routing policy")
	ErrRoutingPolicyRevisionConflict   = errors.New("routing policy revision conflict")
	ErrRoutingPolicyRevisionNotFound   = errors.New("routing policy revision not found")
	ErrRoutingPolicyHistoryImmutable   = errors.New("routing policy history is immutable")
	ErrRoutingPolicyContentCorrupt     = errors.New("routing policy content is corrupt")
	ErrRoutingPolicyPoolIdentity       = errors.New("routing policy pool identity cannot be rebound")
	ErrRoutingPolicyMemberIdentity     = errors.New("routing policy member identity cannot be rebound")
	ErrRoutingRuntimeCheckpointInvalid = errors.New("invalid routing runtime checkpoint")
	ErrRoutingConfigOutboxClaimLost    = errors.New("routing config outbox claim lost")
	errRoutingPolicyDatabaseNil        = errors.New("routing policy database is nil")
)

type RoutingPolicyPoolLimitError struct {
	PoolID      int    `json:"pool_id"`
	GroupName   string `json:"group_name"`
	MemberCount int    `json:"member_count"`
	Limit       int    `json:"limit"`
}

func (err *RoutingPolicyPoolLimitError) Error() string {
	if err == nil {
		return ErrRoutingPolicyInvalid.Error()
	}
	return fmt.Sprintf(
		"routing policy pool %d (%q) has %d members; limit is %d",
		err.PoolID,
		err.GroupName,
		err.MemberCount,
		err.Limit,
	)
}

func (err *RoutingPolicyPoolLimitError) Unwrap() error {
	return ErrRoutingPolicyInvalid
}

type RoutingPolicyHead struct {
	ID                  int    `json:"id" gorm:"primaryKey;autoIncrement:false"`
	CurrentRevision     int64  `json:"current_revision" gorm:"bigint;not null"`
	CurrentActivationID int64  `json:"current_activation_id" gorm:"bigint;not null"`
	CurrentHash         string `json:"current_hash" gorm:"type:char(64);not null"`
	CurrentStage        string `json:"current_stage" gorm:"type:varchar(16);not null"`
	CreatedTime         int64  `json:"created_time" gorm:"bigint;not null"`
	UpdatedTime         int64  `json:"updated_time" gorm:"bigint;not null"`
}

func (RoutingPolicyHead) TableName() string {
	return "routing_policy_heads"
}

type RoutingPolicyRevision struct {
	Revision           int64  `json:"revision" gorm:"primaryKey;autoIncrement:false"`
	ParentRevision     int64  `json:"parent_revision" gorm:"bigint;index;not null"`
	RollbackOfRevision int64  `json:"rollback_of_revision" gorm:"bigint;index;not null"`
	SchemaVersion      int    `json:"schema_version" gorm:"not null"`
	ContentHash        string `json:"content_hash" gorm:"type:char(64);index;not null"`
	PoolCount          int    `json:"pool_count" gorm:"not null"`
	MemberCount        int    `json:"member_count" gorm:"not null"`
	ActorID            int    `json:"actor_id" gorm:"index;not null"`
	Reason             string `json:"reason" gorm:"type:varchar(512);not null"`
	CreatedTime        int64  `json:"created_time" gorm:"bigint;index;not null"`
}

func (RoutingPolicyRevision) TableName() string {
	return "routing_policy_revisions"
}

func (*RoutingPolicyRevision) BeforeUpdate(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

func (*RoutingPolicyRevision) BeforeDelete(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

type RoutingPolicyPoolRevision struct {
	ID              int64  `json:"id" gorm:"primaryKey"`
	Revision        int64  `json:"revision" gorm:"bigint;not null;uniqueIndex:idx_routing_policy_pool_revision,priority:1;index"`
	PoolID          int    `json:"pool_id" gorm:"not null;uniqueIndex:idx_routing_policy_pool_revision,priority:2;index"`
	GroupKey        string `json:"-" gorm:"type:char(64);not null;index"`
	GroupName       string `json:"group_name" gorm:"type:varchar(64);not null"`
	DisplayName     string `json:"display_name" gorm:"type:varchar(128);not null"`
	DeploymentStage string `json:"deployment_stage" gorm:"type:varchar(16);not null;index"`
	PolicyProfile   string `json:"policy_profile" gorm:"type:varchar(32);not null;index"`
	PolicyJSON      string `json:"-" gorm:"type:text;not null"`
}

func (RoutingPolicyPoolRevision) TableName() string {
	return "routing_policy_pool_revisions"
}

func (*RoutingPolicyPoolRevision) BeforeUpdate(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

func (*RoutingPolicyPoolRevision) BeforeDelete(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

type RoutingPolicyMemberRevision struct {
	ID                int64  `json:"id" gorm:"primaryKey"`
	Revision          int64  `json:"revision" gorm:"bigint;not null;uniqueIndex:idx_routing_policy_member_revision,priority:1;uniqueIndex:idx_routing_policy_member_channel,priority:1;index:idx_routing_policy_member_pool,priority:1"`
	PoolID            int    `json:"pool_id" gorm:"not null;uniqueIndex:idx_routing_policy_member_channel,priority:2;index:idx_routing_policy_member_pool,priority:2"`
	MemberID          int    `json:"member_id" gorm:"not null;uniqueIndex:idx_routing_policy_member_revision,priority:2;index"`
	ChannelID         int    `json:"channel_id" gorm:"not null;uniqueIndex:idx_routing_policy_member_channel,priority:3;index"`
	Enabled           bool   `json:"enabled" gorm:"not null"`
	Priority          int64  `json:"priority" gorm:"bigint;not null"`
	Weight            int64  `json:"weight" gorm:"bigint;not null"`
	CredentialIDsJSON string `json:"-" gorm:"type:text;not null"`
	OverridesJSON     string `json:"-" gorm:"type:text;not null"`
}

func (RoutingPolicyMemberRevision) TableName() string {
	return "routing_policy_member_revisions"
}

func (*RoutingPolicyMemberRevision) BeforeUpdate(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

func (*RoutingPolicyMemberRevision) BeforeDelete(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

type RoutingPolicyActivation struct {
	ID                 int64  `json:"id" gorm:"primaryKey"`
	Revision           int64  `json:"revision" gorm:"bigint;not null;index"`
	PreviousRevision   int64  `json:"previous_revision" gorm:"bigint;not null;index"`
	RollbackOfRevision int64  `json:"rollback_of_revision" gorm:"bigint;not null;index"`
	Stage              string `json:"stage" gorm:"type:varchar(16);not null;index"`
	TrafficBasisPoints int    `json:"traffic_basis_points" gorm:"not null"`
	ActorID            int    `json:"actor_id" gorm:"index;not null"`
	Reason             string `json:"reason" gorm:"type:varchar(512);not null"`
	CreatedTime        int64  `json:"created_time" gorm:"bigint;index;not null"`
}

func (RoutingPolicyActivation) TableName() string {
	return "routing_policy_activations"
}

func (*RoutingPolicyActivation) BeforeUpdate(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

func (*RoutingPolicyActivation) BeforeDelete(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

type RoutingConfigOutbox struct {
	ID              int64  `json:"id" gorm:"primaryKey"`
	EventID         string `json:"event_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	Revision        int64  `json:"revision" gorm:"bigint;index;not null"`
	EventType       string `json:"event_type" gorm:"type:varchar(64);index;not null"`
	PayloadJSON     string `json:"-" gorm:"type:text;not null"`
	PayloadHash     string `json:"payload_hash" gorm:"type:char(64);not null"`
	CreatedTime     int64  `json:"created_time" gorm:"bigint;index;not null"`
	PublishedTime   int64  `json:"published_time" gorm:"bigint;index;not null"`
	Attempts        int    `json:"attempts" gorm:"not null"`
	NextAttemptTime int64  `json:"next_attempt_time" gorm:"bigint;index;not null"`
	ClaimToken      string `json:"-" gorm:"type:char(32);index;not null"`
	ClaimedUntil    int64  `json:"claimed_until" gorm:"bigint;index;not null"`
	LastError       string `json:"last_error" gorm:"type:text;not null"`
}

func (RoutingConfigOutbox) TableName() string {
	return "routing_config_outbox"
}

type RoutingRuntimeCheckpoint struct {
	ID             int64  `json:"id" gorm:"primaryKey"`
	IdentityKey    string `json:"-" gorm:"type:char(64);uniqueIndex;not null"`
	NodeID         string `json:"node_id" gorm:"type:varchar(128);index;not null"`
	CheckpointKind string `json:"checkpoint_kind" gorm:"type:varchar(32);index;not null"`
	Scope          string `json:"scope" gorm:"type:text;not null"`
	ScopeHash      string `json:"-" gorm:"type:char(64);index;not null"`
	PolicyRevision int64  `json:"policy_revision" gorm:"bigint;index;not null"`
	Sequence       int64  `json:"sequence" gorm:"bigint;not null"`
	PayloadJSON    string `json:"-" gorm:"type:text;not null"`
	PayloadHash    string `json:"payload_hash" gorm:"type:char(64);not null"`
	ObservedTime   int64  `json:"observed_time" gorm:"bigint;not null"`
	ExpiresTime    int64  `json:"expires_time" gorm:"bigint;index;not null"`
	CreatedTime    int64  `json:"created_time" gorm:"bigint;not null"`
	UpdatedTime    int64  `json:"updated_time" gorm:"bigint;index;not null"`
}

func (RoutingRuntimeCheckpoint) TableName() string {
	return "routing_runtime_checkpoints"
}

type RoutingPolicyDocument struct {
	SchemaVersion int                        `json:"schema_version"`
	Pools         []RoutingPolicyPoolContent `json:"pools"`
}

type RoutingPolicyPoolContent struct {
	PoolID          int                          `json:"pool_id"`
	GroupName       string                       `json:"group_name"`
	DisplayName     string                       `json:"display_name"`
	DeploymentStage string                       `json:"deployment_stage"`
	PolicyProfile   string                       `json:"policy_profile"`
	Policy          json.RawMessage              `json:"policy"`
	Members         []RoutingPolicyMemberContent `json:"members"`
}

type RoutingPolicyMemberContent struct {
	MemberID      int             `json:"member_id"`
	ChannelID     int             `json:"channel_id"`
	Enabled       bool            `json:"enabled"`
	Priority      int64           `json:"priority"`
	Weight        int64           `json:"weight"`
	CredentialIDs []int           `json:"credential_ids"`
	Overrides     json.RawMessage `json:"overrides"`
}

type RoutingPolicyActivationSpec struct {
	Stage              string `json:"stage"`
	TrafficBasisPoints int    `json:"traffic_basis_points"`
	ActorID            int    `json:"actor_id"`
	Reason             string `json:"reason"`
}

func (spec RoutingPolicyActivationSpec) Validate() error {
	if !validRoutingPolicyActivationSpec(spec) {
		return ErrRoutingPolicyInvalid
	}
	return nil
}

type RoutingPolicyPublishResult struct {
	Revision   RoutingPolicyRevision   `json:"revision"`
	Activation RoutingPolicyActivation `json:"activation"`
	Outbox     RoutingConfigOutbox     `json:"outbox"`
}

type RoutingConfigEvent struct {
	SchemaVersion      int    `json:"schema_version"`
	EventID            string `json:"event_id"`
	Revision           int64  `json:"revision"`
	PreviousRevision   int64  `json:"previous_revision"`
	ActivationID       int64  `json:"activation_id"`
	DeploymentStage    string `json:"deployment_stage"`
	TrafficBasisPoints int    `json:"traffic_basis_points"`
	ContentHash        string `json:"content_hash"`
	ChangedPoolIDs     []int  `json:"changed_pool_ids"`
	RollbackOfRevision int64  `json:"rollback_of_revision"`
	CreatedTime        int64  `json:"created_time"`
}

type RoutingPolicyRevisionConflictError struct {
	ExpectedRevision int64
	ActualRevision   int64
	ActualHash       string
}

func (err *RoutingPolicyRevisionConflictError) Error() string {
	return fmt.Sprintf("%v: expected %d, current %d", ErrRoutingPolicyRevisionConflict, err.ExpectedRevision, err.ActualRevision)
}

func (err *RoutingPolicyRevisionConflictError) Unwrap() error {
	return ErrRoutingPolicyRevisionConflict
}

func EnsureRoutingPolicyHead() error {
	return EnsureRoutingPolicyHeadContext(context.Background())
}

func EnsureRoutingPolicyHeadContext(ctx context.Context) error {
	return EnsureRoutingPolicyHeadDBContext(ctx, DB)
}

func EnsureRoutingPolicyHeadDBContext(ctx context.Context, db *gorm.DB) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil {
		return errRoutingPolicyDatabaseNil
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := ensureRoutingPolicyHeadTx(tx)
		return err
	})
}

func GetRoutingPolicyHeadContext(ctx context.Context) (RoutingPolicyHead, error) {
	return GetRoutingPolicyHeadDBContext(ctx, DB)
}

func GetRoutingPolicyHeadDBContext(ctx context.Context, db *gorm.DB) (RoutingPolicyHead, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil {
		return RoutingPolicyHead{}, errRoutingPolicyDatabaseNil
	}
	var head RoutingPolicyHead
	err := db.WithContext(ctx).Where("id = ?", routingPolicyHeadID).First(&head).Error
	return head, err
}

func PublishRoutingPolicyRevisionContext(
	ctx context.Context,
	expectedRevision int64,
	document RoutingPolicyDocument,
	activation RoutingPolicyActivationSpec,
) (RoutingPolicyPublishResult, error) {
	return PublishRoutingPolicyRevisionDBContext(ctx, DB, expectedRevision, document, activation)
}

func PublishRoutingPolicyRevisionDBContext(
	ctx context.Context,
	db *gorm.DB,
	expectedRevision int64,
	document RoutingPolicyDocument,
	activation RoutingPolicyActivationSpec,
) (RoutingPolicyPublishResult, error) {
	normalized, contentHash, err := normalizeRoutingPolicyDocument(document)
	if err != nil {
		return RoutingPolicyPublishResult{}, err
	}
	return publishNormalizedRoutingPolicyRevisionContext(ctx, db, expectedRevision, 0, normalized, contentHash, activation)
}

func NormalizeRoutingPolicyDocument(document RoutingPolicyDocument) (RoutingPolicyDocument, string, error) {
	return normalizeRoutingPolicyDocument(document)
}

func RollbackRoutingPolicyRevisionContext(
	ctx context.Context,
	expectedRevision int64,
	sourceRevision int64,
	activation RoutingPolicyActivationSpec,
) (RoutingPolicyPublishResult, error) {
	return RollbackRoutingPolicyRevisionDBContext(ctx, DB, expectedRevision, sourceRevision, activation)
}

func RollbackRoutingPolicyRevisionDBContext(
	ctx context.Context,
	db *gorm.DB,
	expectedRevision int64,
	sourceRevision int64,
	activation RoutingPolicyActivationSpec,
) (RoutingPolicyPublishResult, error) {
	if sourceRevision <= 0 || sourceRevision >= expectedRevision {
		return RoutingPolicyPublishResult{}, ErrRoutingPolicyInvalid
	}
	document, revision, err := LoadRoutingPolicyRevisionDBContext(ctx, db, sourceRevision)
	if err != nil {
		return RoutingPolicyPublishResult{}, err
	}
	return publishNormalizedRoutingPolicyRevisionContext(
		ctx,
		db,
		expectedRevision,
		sourceRevision,
		document,
		revision.ContentHash,
		activation,
	)
}

func LoadRoutingPolicyRevisionContext(ctx context.Context, revisionNumber int64) (RoutingPolicyDocument, RoutingPolicyRevision, error) {
	return LoadRoutingPolicyRevisionDBContext(ctx, DB, revisionNumber)
}

func LoadRoutingPolicyRevisionDBContext(ctx context.Context, db *gorm.DB, revisionNumber int64) (RoutingPolicyDocument, RoutingPolicyRevision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil {
		return RoutingPolicyDocument{}, RoutingPolicyRevision{}, errRoutingPolicyDatabaseNil
	}
	if revisionNumber <= 0 {
		return RoutingPolicyDocument{}, RoutingPolicyRevision{}, ErrRoutingPolicyRevisionNotFound
	}
	db = db.WithContext(ctx)
	var revision RoutingPolicyRevision
	if err := db.Where("revision = ?", revisionNumber).First(&revision).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RoutingPolicyDocument{}, RoutingPolicyRevision{}, ErrRoutingPolicyRevisionNotFound
		}
		return RoutingPolicyDocument{}, RoutingPolicyRevision{}, err
	}

	var poolRows []RoutingPolicyPoolRevision
	if err := db.Where("revision = ?", revisionNumber).Order("pool_id asc").Find(&poolRows).Error; err != nil {
		return RoutingPolicyDocument{}, RoutingPolicyRevision{}, err
	}
	var memberRows []RoutingPolicyMemberRevision
	if err := db.Where("revision = ?", revisionNumber).Order("pool_id asc").Order("member_id asc").Find(&memberRows).Error; err != nil {
		return RoutingPolicyDocument{}, RoutingPolicyRevision{}, err
	}
	if len(poolRows) != revision.PoolCount || len(memberRows) != revision.MemberCount {
		return RoutingPolicyDocument{}, RoutingPolicyRevision{}, fmt.Errorf("%w: row count mismatch", ErrRoutingPolicyContentCorrupt)
	}

	document := RoutingPolicyDocument{SchemaVersion: revision.SchemaVersion, Pools: make([]RoutingPolicyPoolContent, len(poolRows))}
	poolIndexes := make(map[int]int, len(poolRows))
	for index := range poolRows {
		row := poolRows[index]
		if row.GroupKey != routingGroupKey(row.GroupName) {
			return RoutingPolicyDocument{}, RoutingPolicyRevision{}, fmt.Errorf("%w: group hash mismatch", ErrRoutingPolicyContentCorrupt)
		}
		poolIndexes[row.PoolID] = index
		document.Pools[index] = RoutingPolicyPoolContent{
			PoolID:          row.PoolID,
			GroupName:       row.GroupName,
			DisplayName:     row.DisplayName,
			DeploymentStage: row.DeploymentStage,
			PolicyProfile:   row.PolicyProfile,
			Policy:          json.RawMessage(row.PolicyJSON),
			Members:         make([]RoutingPolicyMemberContent, 0),
		}
	}
	for index := range memberRows {
		row := memberRows[index]
		poolIndex, exists := poolIndexes[row.PoolID]
		if !exists {
			return RoutingPolicyDocument{}, RoutingPolicyRevision{}, fmt.Errorf("%w: member pool missing", ErrRoutingPolicyContentCorrupt)
		}
		var credentialIDs []int
		if err := common.UnmarshalJsonStr(row.CredentialIDsJSON, &credentialIDs); err != nil {
			return RoutingPolicyDocument{}, RoutingPolicyRevision{}, fmt.Errorf("%w: credential ids", ErrRoutingPolicyContentCorrupt)
		}
		document.Pools[poolIndex].Members = append(document.Pools[poolIndex].Members, RoutingPolicyMemberContent{
			MemberID:      row.MemberID,
			ChannelID:     row.ChannelID,
			Enabled:       row.Enabled,
			Priority:      row.Priority,
			Weight:        row.Weight,
			CredentialIDs: credentialIDs,
			Overrides:     json.RawMessage(row.OverridesJSON),
		})
	}

	normalized, contentHash, err := normalizeRoutingPolicyDocument(document)
	if err != nil || contentHash != revision.ContentHash {
		return RoutingPolicyDocument{}, RoutingPolicyRevision{}, fmt.Errorf("%w: manifest hash mismatch", ErrRoutingPolicyContentCorrupt)
	}
	return normalized, revision, nil
}

func (outbox RoutingConfigOutbox) DecodePayload(target any) error {
	if target == nil || routingPolicyHash([]byte(outbox.PayloadJSON)) != outbox.PayloadHash {
		return ErrRoutingPolicyContentCorrupt
	}
	if err := common.UnmarshalJsonStr(outbox.PayloadJSON, target); err != nil {
		return fmt.Errorf("%w: outbox payload", ErrRoutingPolicyContentCorrupt)
	}
	return nil
}

func ClaimRoutingConfigOutboxContext(ctx context.Context, now int64, leaseSeconds int64) (*RoutingConfigOutbox, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now <= 0 || leaseSeconds <= 0 || leaseSeconds > 300 || now > math.MaxInt64-leaseSeconds {
		return nil, ErrRoutingPolicyInvalid
	}
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, err
	}
	claimToken := hex.EncodeToString(tokenBytes[:])
	var claimed RoutingConfigOutbox
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := lockForUpdate(tx.WithContext(ctx)).Where(
			"published_time = ? AND next_attempt_time <= ? AND claimed_until <= ?", 0, now, now,
		).Order("revision asc").Order("id asc")
		if err := query.First(&claimed).Error; err != nil {
			return err
		}
		if claimed.Attempts == int(^uint(0)>>1) {
			return ErrRoutingPolicyInvalid
		}
		result := tx.WithContext(ctx).Model(&RoutingConfigOutbox{}).
			Where("id = ? AND published_time = ? AND claimed_until <= ?", claimed.ID, 0, now).
			Updates(map[string]any{
				"claim_token":   claimToken,
				"claimed_until": now + leaseSeconds,
				"attempts":      claimed.Attempts + 1,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRoutingConfigOutboxClaimLost
		}
		return tx.WithContext(ctx).Where("id = ? AND claim_token = ?", claimed.ID, claimToken).First(&claimed).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := claimed.DecodePayload(&RoutingConfigEvent{}); err != nil {
		return nil, err
	}
	return &claimed, nil
}

func MarkRoutingConfigOutboxPublishedContext(ctx context.Context, id int64, claimToken string, publishedTime int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 || len(claimToken) != 32 || publishedTime <= 0 {
		return ErrRoutingPolicyInvalid
	}
	result := DB.WithContext(ctx).Model(&RoutingConfigOutbox{}).
		Where("id = ? AND claim_token = ? AND published_time = ?", id, claimToken, 0).
		Updates(map[string]any{
			"published_time":    publishedTime,
			"claim_token":       "",
			"claimed_until":     0,
			"next_attempt_time": 0,
			"last_error":        "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRoutingConfigOutboxClaimLost
	}
	return nil
}

func ReleaseRoutingConfigOutboxClaimContext(ctx context.Context, id int64, claimToken string, nextAttemptTime int64, publishErr error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 || len(claimToken) != 32 || nextAttemptTime <= 0 || publishErr == nil {
		return ErrRoutingPolicyInvalid
	}
	result := DB.WithContext(ctx).Model(&RoutingConfigOutbox{}).
		Where("id = ? AND claim_token = ? AND published_time = ?", id, claimToken, 0).
		Updates(map[string]any{
			"claim_token":       "",
			"claimed_until":     0,
			"next_attempt_time": nextAttemptTime,
			"last_error":        truncateRoutingPolicyText(common.SanitizeErrorMessage(publishErr.Error()), routingPolicyReasonMaxRunes),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRoutingConfigOutboxClaimLost
	}
	return nil
}

func UpsertRoutingRuntimeCheckpointContext(ctx context.Context, checkpoint RoutingRuntimeCheckpoint) (RoutingRuntimeCheckpoint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := checkpoint.Validate(); err != nil {
		return RoutingRuntimeCheckpoint{}, err
	}
	var stored RoutingRuntimeCheckpoint
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "identity_key"}},
			DoNothing: true,
		}).Create(&checkpoint).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx.WithContext(ctx)).Where("identity_key = ?", checkpoint.IdentityKey).First(&stored).Error; err != nil {
			return err
		}
		if stored.PolicyRevision > checkpoint.PolicyRevision ||
			(stored.PolicyRevision == checkpoint.PolicyRevision && stored.Sequence > checkpoint.Sequence) {
			return nil
		}
		if stored.PolicyRevision == checkpoint.PolicyRevision && stored.Sequence == checkpoint.Sequence {
			if stored.PayloadHash != checkpoint.PayloadHash || stored.NodeID != checkpoint.NodeID ||
				stored.CheckpointKind != checkpoint.CheckpointKind || stored.Scope != checkpoint.Scope {
				return ErrRoutingRuntimeCheckpointInvalid
			}
			return nil
		}
		if err := tx.WithContext(ctx).Model(&RoutingRuntimeCheckpoint{}).Where("id = ?", stored.ID).Updates(map[string]any{
			"policy_revision": checkpoint.PolicyRevision,
			"sequence":        checkpoint.Sequence,
			"payload_json":    checkpoint.PayloadJSON,
			"payload_hash":    checkpoint.PayloadHash,
			"observed_time":   checkpoint.ObservedTime,
			"expires_time":    checkpoint.ExpiresTime,
			"updated_time":    checkpoint.UpdatedTime,
		}).Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).Where("id = ?", stored.ID).First(&stored).Error
	})
	return stored, err
}

func GetRoutingRuntimeCheckpointContext(ctx context.Context, nodeID string, kind string, scope string) (RoutingRuntimeCheckpoint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	identityKey := routingPolicyHash([]byte(nodeID + "\x00" + kind + "\x00" + scope))
	var checkpoint RoutingRuntimeCheckpoint
	err := DB.WithContext(ctx).Where("identity_key = ?", identityKey).First(&checkpoint).Error
	if err == nil {
		err = checkpoint.Validate()
	}
	return checkpoint, err
}

func DeletePublishedRoutingConfigOutboxBeforeContext(ctx context.Context, cutoff int64) (int64, error) {
	if cutoff <= 0 {
		return 0, nil
	}
	return deleteRoutingControlPlaneRowsContext(
		ctx,
		&RoutingConfigOutbox{},
		"published_time > ? AND published_time < ?",
		[]any{0, cutoff},
		"published_time asc",
	)
}

func DeleteExpiredRoutingRuntimeCheckpointsContext(ctx context.Context, now int64) (int64, error) {
	if now <= 0 {
		return 0, nil
	}
	return deleteRoutingControlPlaneRowsContext(
		ctx,
		&RoutingRuntimeCheckpoint{},
		"expires_time > ? AND expires_time < ?",
		[]any{0, now},
		"expires_time asc",
	)
}

func deleteRoutingControlPlaneRowsContext(ctx context.Context, table any, where string, args []any, order string) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var ids []int64
		if err := DB.WithContext(ctx).Model(table).Where(where, args...).Order(order).Order("id asc").
			Limit(routingControlPlaneRetentionBatch).Pluck("id", &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		result := DB.WithContext(ctx).Model(table).Where("id IN ?", ids).Delete(table)
		if result.Error != nil {
			return total, result.Error
		}
		total += result.RowsAffected
		if len(ids) < routingControlPlaneRetentionBatch {
			return total, nil
		}
	}
}

func truncateRoutingPolicyText(value string, maxRunes int) string {
	if maxRunes <= 0 || !utf8.ValidString(value) {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes)
}

func NewRoutingRuntimeCheckpoint(
	nodeID string,
	kind string,
	scope string,
	policyRevision int64,
	sequence int64,
	payload any,
	observedTime int64,
	expiresTime int64,
) (RoutingRuntimeCheckpoint, error) {
	payloadBytes, err := common.Marshal(payload)
	if err != nil || len(payloadBytes) == 0 || len(payloadBytes) > routingCheckpointPayloadMaxBytes {
		return RoutingRuntimeCheckpoint{}, ErrRoutingRuntimeCheckpointInvalid
	}
	now := common.GetTimestamp()
	checkpoint := RoutingRuntimeCheckpoint{
		IdentityKey:    routingPolicyHash([]byte(nodeID + "\x00" + kind + "\x00" + scope)),
		NodeID:         nodeID,
		CheckpointKind: kind,
		Scope:          scope,
		ScopeHash:      routingPolicyHash([]byte(scope)),
		PolicyRevision: policyRevision,
		Sequence:       sequence,
		PayloadJSON:    string(payloadBytes),
		PayloadHash:    routingPolicyHash(payloadBytes),
		ObservedTime:   observedTime,
		ExpiresTime:    expiresTime,
		CreatedTime:    now,
		UpdatedTime:    now,
	}
	if err := checkpoint.Validate(); err != nil {
		return RoutingRuntimeCheckpoint{}, err
	}
	return checkpoint, nil
}

func (checkpoint RoutingRuntimeCheckpoint) Validate() error {
	if !validRoutingPolicyText(checkpoint.NodeID, 128) || checkpoint.NodeID == "" ||
		!validRoutingPolicyText(checkpoint.CheckpointKind, 32) || checkpoint.CheckpointKind == "" ||
		!utf8.ValidString(checkpoint.Scope) || checkpoint.Scope == "" || len(checkpoint.Scope) > 4<<10 ||
		checkpoint.PolicyRevision < 0 || checkpoint.Sequence < 0 || checkpoint.ObservedTime < 0 || checkpoint.ExpiresTime < 0 ||
		len(checkpoint.PayloadJSON) == 0 || len(checkpoint.PayloadJSON) > routingCheckpointPayloadMaxBytes ||
		checkpoint.IdentityKey != routingPolicyHash([]byte(checkpoint.NodeID+"\x00"+checkpoint.CheckpointKind+"\x00"+checkpoint.Scope)) ||
		checkpoint.ScopeHash != routingPolicyHash([]byte(checkpoint.Scope)) ||
		checkpoint.PayloadHash != routingPolicyHash([]byte(checkpoint.PayloadJSON)) {
		return ErrRoutingRuntimeCheckpointInvalid
	}
	var payload any
	if err := common.UnmarshalJsonStr(checkpoint.PayloadJSON, &payload); err != nil {
		return ErrRoutingRuntimeCheckpointInvalid
	}
	return nil
}

func (checkpoint RoutingRuntimeCheckpoint) DecodePayload(target any) error {
	if target == nil {
		return ErrRoutingRuntimeCheckpointInvalid
	}
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	return common.UnmarshalJsonStr(checkpoint.PayloadJSON, target)
}

func publishNormalizedRoutingPolicyRevisionContext(
	ctx context.Context,
	db *gorm.DB,
	expectedRevision int64,
	rollbackOfRevision int64,
	document RoutingPolicyDocument,
	contentHash string,
	activationSpec RoutingPolicyActivationSpec,
) (RoutingPolicyPublishResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil {
		return RoutingPolicyPublishResult{}, errRoutingPolicyDatabaseNil
	}
	if expectedRevision < 0 || expectedRevision == math.MaxInt64 || rollbackOfRevision < 0 ||
		activationSpec.Validate() != nil || len(contentHash) != 64 {
		return RoutingPolicyPublishResult{}, ErrRoutingPolicyInvalid
	}
	if err := ValidateRoutingPolicyActivationDocument(document, activationSpec); err != nil {
		return RoutingPolicyPublishResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return RoutingPolicyPublishResult{}, err
	}

	changedPoolIDs := make([]int, len(document.Pools))
	for index := range document.Pools {
		changedPoolIDs[index] = document.Pools[index].PoolID
	}
	now := common.GetTimestamp()
	var result RoutingPolicyPublishResult
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = publishNormalizedRoutingPolicyRevisionTx(
			ctx,
			tx,
			expectedRevision,
			rollbackOfRevision,
			document,
			contentHash,
			activationSpec,
			changedPoolIDs,
			now,
		)
		return err
	})
	return result, err
}

func publishNormalizedRoutingPolicyRevisionTx(
	ctx context.Context,
	tx *gorm.DB,
	expectedRevision int64,
	rollbackOfRevision int64,
	document RoutingPolicyDocument,
	contentHash string,
	activationSpec RoutingPolicyActivationSpec,
	changedPoolIDs []int,
	now int64,
) (RoutingPolicyPublishResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if tx == nil {
		return RoutingPolicyPublishResult{}, errRoutingPolicyDatabaseNil
	}
	if expectedRevision < 0 || expectedRevision == math.MaxInt64 || rollbackOfRevision < 0 ||
		activationSpec.Validate() != nil || len(contentHash) != 64 || now <= 0 {
		return RoutingPolicyPublishResult{}, ErrRoutingPolicyInvalid
	}
	if err := ValidateRoutingPolicyActivationDocument(document, activationSpec); err != nil {
		return RoutingPolicyPublishResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return RoutingPolicyPublishResult{}, err
	}

	poolIDs := make(map[int]struct{}, len(document.Pools))
	for index := range document.Pools {
		poolIDs[document.Pools[index].PoolID] = struct{}{}
	}
	changedPoolIDs = append([]int(nil), changedPoolIDs...)
	sort.Ints(changedPoolIDs)
	for index := range changedPoolIDs {
		if _, exists := poolIDs[changedPoolIDs[index]]; !exists ||
			(index > 0 && changedPoolIDs[index] == changedPoolIDs[index-1]) {
			return RoutingPolicyPublishResult{}, ErrRoutingPolicyInvalid
		}
	}

	tx = tx.WithContext(ctx)
	head, err := ensureRoutingPolicyHeadTx(tx)
	if err != nil {
		return RoutingPolicyPublishResult{}, err
	}
	if head.CurrentRevision != expectedRevision {
		return RoutingPolicyPublishResult{}, newRoutingPolicyRevisionConflict(expectedRevision, head)
	}
	if err := validateRoutingPolicyPoolIdentitiesTx(tx, document); err != nil {
		return RoutingPolicyPublishResult{}, err
	}
	if err := validateRoutingPolicyMemberIdentitiesTx(tx, document); err != nil {
		return RoutingPolicyPublishResult{}, err
	}

	nextRevision := expectedRevision + 1
	cas := tx.Model(&RoutingPolicyHead{}).
		Where("id = ? AND current_revision = ?", routingPolicyHeadID, expectedRevision).
		Updates(map[string]any{
			"current_revision": nextRevision,
			"current_hash":     contentHash,
			"current_stage":    activationSpec.Stage,
			"updated_time":     now,
		})
	if cas.Error != nil {
		return RoutingPolicyPublishResult{}, cas.Error
	}
	if cas.RowsAffected != 1 {
		var actual RoutingPolicyHead
		if err := tx.Where("id = ?", routingPolicyHeadID).First(&actual).Error; err != nil {
			return RoutingPolicyPublishResult{}, err
		}
		return RoutingPolicyPublishResult{}, newRoutingPolicyRevisionConflict(expectedRevision, actual)
	}

	revision := RoutingPolicyRevision{
		Revision:           nextRevision,
		ParentRevision:     expectedRevision,
		RollbackOfRevision: rollbackOfRevision,
		SchemaVersion:      document.SchemaVersion,
		ContentHash:        contentHash,
		PoolCount:          len(document.Pools),
		MemberCount:        routingPolicyDocumentMemberCount(document),
		ActorID:            activationSpec.ActorID,
		Reason:             activationSpec.Reason,
		CreatedTime:        now,
	}
	if err := tx.Create(&revision).Error; err != nil {
		return RoutingPolicyPublishResult{}, err
	}

	poolRows, memberRows, err := routingPolicyRevisionRows(nextRevision, document)
	if err != nil {
		return RoutingPolicyPublishResult{}, err
	}
	if len(poolRows) > 0 {
		if err := createRoutingPolicyRowsInBatches(tx, poolRows, routingPolicyPoolInsertBatch, routingPolicyPoolRowEncodedSize); err != nil {
			return RoutingPolicyPublishResult{}, err
		}
	}
	if len(memberRows) > 0 {
		if err := createRoutingPolicyRowsInBatches(tx, memberRows, routingPolicyMemberInsertBatch, routingPolicyMemberRowEncodedSize); err != nil {
			return RoutingPolicyPublishResult{}, err
		}
	}

	activation := RoutingPolicyActivation{
		Revision:           nextRevision,
		PreviousRevision:   expectedRevision,
		RollbackOfRevision: rollbackOfRevision,
		Stage:              activationSpec.Stage,
		TrafficBasisPoints: activationSpec.TrafficBasisPoints,
		ActorID:            activationSpec.ActorID,
		Reason:             activationSpec.Reason,
		CreatedTime:        now,
	}
	if err := tx.Create(&activation).Error; err != nil {
		return RoutingPolicyPublishResult{}, err
	}

	eventID := fmt.Sprintf("routing-policy-revision:%020d", nextRevision)
	event := RoutingConfigEvent{
		SchemaVersion:      RoutingPolicySchemaVersion,
		EventID:            eventID,
		Revision:           nextRevision,
		PreviousRevision:   expectedRevision,
		ActivationID:       activation.ID,
		DeploymentStage:    activation.Stage,
		TrafficBasisPoints: activation.TrafficBasisPoints,
		ContentHash:        contentHash,
		ChangedPoolIDs:     changedPoolIDs,
		RollbackOfRevision: rollbackOfRevision,
		CreatedTime:        now,
	}
	payload, err := common.Marshal(event)
	if err != nil {
		return RoutingPolicyPublishResult{}, err
	}
	if len(payload) > routingConfigOutboxPayloadMaxBytes {
		return RoutingPolicyPublishResult{}, ErrRoutingPolicyInvalid
	}
	outbox := RoutingConfigOutbox{
		EventID:     eventID,
		Revision:    nextRevision,
		EventType:   RoutingConfigEventPolicyRevision,
		PayloadJSON: string(payload),
		PayloadHash: routingPolicyHash(payload),
		CreatedTime: now,
	}
	if err := tx.Create(&outbox).Error; err != nil {
		return RoutingPolicyPublishResult{}, err
	}

	finalize := tx.Model(&RoutingPolicyHead{}).
		Where("id = ? AND current_revision = ? AND current_hash = ?", routingPolicyHeadID, nextRevision, contentHash).
		Update("current_activation_id", activation.ID)
	if finalize.Error != nil {
		return RoutingPolicyPublishResult{}, finalize.Error
	}
	if finalize.RowsAffected != 1 {
		return RoutingPolicyPublishResult{}, ErrRoutingPolicyRevisionConflict
	}
	return RoutingPolicyPublishResult{Revision: revision, Activation: activation, Outbox: outbox}, nil
}

func ensureRoutingPolicyHeadTx(tx *gorm.DB) (RoutingPolicyHead, error) {
	var head RoutingPolicyHead
	err := tx.Where("id = ?", routingPolicyHeadID).First(&head).Error
	if err == nil {
		return head, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return RoutingPolicyHead{}, err
	}

	var latestRevision int64
	if err := tx.Model(&RoutingPolicyRevision{}).Select("COALESCE(MAX(revision), 0)").Scan(&latestRevision).Error; err != nil {
		return RoutingPolicyHead{}, err
	}
	now := common.GetTimestamp()
	candidate := RoutingPolicyHead{ID: routingPolicyHeadID, CurrentRevision: latestRevision, CreatedTime: now, UpdatedTime: now}
	if latestRevision > 0 {
		var revision RoutingPolicyRevision
		if err := tx.Where("revision = ?", latestRevision).First(&revision).Error; err != nil {
			return RoutingPolicyHead{}, err
		}
		candidate.CurrentHash = revision.ContentHash
		var activation RoutingPolicyActivation
		err := tx.Where("revision = ?", latestRevision).Order("id desc").First(&activation).Error
		if err == nil {
			candidate.CurrentActivationID = activation.ID
			candidate.CurrentStage = activation.Stage
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return RoutingPolicyHead{}, err
		}
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoNothing: true,
	}).Create(&candidate).Error; err != nil {
		return RoutingPolicyHead{}, err
	}
	if err := tx.Where("id = ?", routingPolicyHeadID).First(&head).Error; err != nil {
		return RoutingPolicyHead{}, err
	}
	return head, nil
}

func normalizeRoutingPolicyDocument(document RoutingPolicyDocument) (RoutingPolicyDocument, string, error) {
	if document.SchemaVersion == 0 {
		document.SchemaVersion = RoutingPolicySchemaVersion
	}
	if document.SchemaVersion != RoutingPolicySchemaVersion || len(document.Pools) > routingTopologyMaxPools {
		return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
	}

	normalized := RoutingPolicyDocument{SchemaVersion: document.SchemaVersion, Pools: make([]RoutingPolicyPoolContent, len(document.Pools))}
	poolIDs := make(map[int]struct{}, len(document.Pools))
	groupNames := make(map[string]struct{}, len(document.Pools))
	memberIDs := make(map[int]struct{})
	totalMembers := 0
	totalCredentialRefs := 0
	for poolIndex := range document.Pools {
		pool := document.Pools[poolIndex]
		if pool.PoolID <= 0 || !validRoutingPolicyText(pool.GroupName, 64) || pool.GroupName == "" ||
			!validRoutingPolicyText(pool.DisplayName, 128) || !validRoutingDeploymentStage(pool.DeploymentStage) ||
			!validRoutingPolicyProfile(pool.PolicyProfile) {
			return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
		}
		if pool.DisplayName == "" {
			pool.DisplayName = pool.GroupName
		}
		if _, exists := poolIDs[pool.PoolID]; exists {
			return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
		}
		if _, exists := groupNames[pool.GroupName]; exists {
			return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
		}
		poolIDs[pool.PoolID] = struct{}{}
		groupNames[pool.GroupName] = struct{}{}

		policy, err := normalizeRoutingPolicyJSONObject(pool.Policy)
		if err != nil {
			return RoutingPolicyDocument{}, "", err
		}
		if err := validateRoutingPolicyCanaryConfiguration(policy); err != nil {
			return RoutingPolicyDocument{}, "", err
		}
		pool.Policy = policy
		pool.Members = append([]RoutingPolicyMemberContent(nil), pool.Members...)
		if len(pool.Members) > RoutingPolicyMaxMembersPerPool {
			return RoutingPolicyDocument{}, "", &RoutingPolicyPoolLimitError{
				PoolID:      pool.PoolID,
				GroupName:   pool.GroupName,
				MemberCount: len(pool.Members),
				Limit:       RoutingPolicyMaxMembersPerPool,
			}
		}
		totalMembers += len(pool.Members)
		if totalMembers > routingTopologyMaxMembers {
			return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
		}
		channels := make(map[int]struct{}, len(pool.Members))
		for memberIndex := range pool.Members {
			member := &pool.Members[memberIndex]
			if member.MemberID <= 0 || member.ChannelID <= 0 || member.Weight < 0 {
				return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
			}
			if _, exists := memberIDs[member.MemberID]; exists {
				return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
			}
			if _, exists := channels[member.ChannelID]; exists {
				return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
			}
			memberIDs[member.MemberID] = struct{}{}
			channels[member.ChannelID] = struct{}{}
			if len(member.CredentialIDs) > routingPolicyMaxCredentialIDsPerMember {
				return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
			}
			member.CredentialIDs = append([]int(nil), member.CredentialIDs...)
			sort.Ints(member.CredentialIDs)
			for credentialIndex := range member.CredentialIDs {
				if member.CredentialIDs[credentialIndex] <= 0 ||
					(credentialIndex > 0 && member.CredentialIDs[credentialIndex] == member.CredentialIDs[credentialIndex-1]) {
					return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
				}
			}
			credentialIDsJSON, err := common.Marshal(member.CredentialIDs)
			if err != nil || len(credentialIDsJSON) > routingPolicyCredentialIDsMaxBytes {
				return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
			}
			totalCredentialRefs += len(member.CredentialIDs)
			if totalCredentialRefs > routingPolicyMaxCredentialRefs {
				return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
			}
			overrides, err := normalizeRoutingPolicyJSONObject(member.Overrides)
			if err != nil {
				return RoutingPolicyDocument{}, "", err
			}
			member.Overrides = overrides
		}
		sort.Slice(pool.Members, func(left, right int) bool {
			return pool.Members[left].MemberID < pool.Members[right].MemberID
		})
		normalized.Pools[poolIndex] = pool
	}
	sort.Slice(normalized.Pools, func(left, right int) bool {
		return normalized.Pools[left].PoolID < normalized.Pools[right].PoolID
	})
	canonical, err := common.Marshal(normalized)
	if err != nil || len(canonical) > routingPolicyMaxCanonicalBytes {
		return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
	}
	return normalized, routingPolicyHash(canonical), nil
}

func validateRoutingPolicyPoolIdentitiesTx(tx *gorm.DB, document RoutingPolicyDocument) error {
	type poolIdentity struct {
		PoolID   int
		GroupKey string
	}
	desiredByPool := make(map[int]string, len(document.Pools))
	desiredByGroup := make(map[string]int, len(document.Pools))
	for index := range document.Pools {
		pool := document.Pools[index]
		groupKey := routingGroupKey(pool.GroupName)
		desiredByPool[pool.PoolID] = groupKey
		desiredByGroup[groupKey] = pool.PoolID
	}
	if len(desiredByPool) == 0 {
		return nil
	}

	var historical []poolIdentity
	if err := tx.Model(&RoutingPolicyPoolRevision{}).
		Select("pool_id", "group_key").
		Group("pool_id, group_key").
		Find(&historical).Error; err != nil {
		return err
	}
	for index := range historical {
		identity := historical[index]
		if groupKey, exists := desiredByPool[identity.PoolID]; exists && groupKey != identity.GroupKey {
			return ErrRoutingPolicyPoolIdentity
		}
		if poolID, exists := desiredByGroup[identity.GroupKey]; exists && poolID != identity.PoolID {
			return ErrRoutingPolicyPoolIdentity
		}
	}
	return nil
}

func validateRoutingPolicyMemberIdentitiesTx(tx *gorm.DB, document RoutingPolicyDocument) error {
	type memberIdentity struct {
		MemberID  int
		PoolID    int
		ChannelID int
	}
	desired := make(map[int]memberIdentity, routingPolicyDocumentMemberCount(document))
	memberIDs := make([]int, 0, len(desired))
	for poolIndex := range document.Pools {
		pool := document.Pools[poolIndex]
		for memberIndex := range pool.Members {
			member := pool.Members[memberIndex]
			desired[member.MemberID] = memberIdentity{
				MemberID: member.MemberID, PoolID: pool.PoolID, ChannelID: member.ChannelID,
			}
			memberIDs = append(memberIDs, member.MemberID)
		}
	}
	if len(memberIDs) == 0 {
		return nil
	}
	sort.Ints(memberIDs)
	for start := 0; start < len(memberIDs); start += routingPolicyMemberInsertBatch {
		end := min(start+routingPolicyMemberInsertBatch, len(memberIDs))
		var historical []memberIdentity
		if err := tx.Model(&RoutingPolicyMemberRevision{}).
			Select("member_id", "pool_id", "channel_id").
			Where("member_id IN ?", memberIDs[start:end]).
			Group("member_id, pool_id, channel_id").
			Find(&historical).Error; err != nil {
			return err
		}
		for index := range historical {
			identity, exists := desired[historical[index].MemberID]
			if !exists || identity.PoolID != historical[index].PoolID || identity.ChannelID != historical[index].ChannelID {
				return ErrRoutingPolicyMemberIdentity
			}
		}
	}
	return nil
}

func normalizeRoutingPolicyJSONObject(value json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(value)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(value) > routingPolicyMaxConfigBytes {
		return nil, ErrRoutingPolicyInvalid
	}
	var object map[string]any
	if err := common.Unmarshal(value, &object); err != nil || object == nil {
		return nil, ErrRoutingPolicyInvalid
	}
	canonical, err := common.Marshal(object)
	if err != nil || len(canonical) > routingPolicyMaxConfigBytes {
		return nil, ErrRoutingPolicyInvalid
	}
	return json.RawMessage(canonical), nil
}

func validateRoutingPolicyCanaryConfiguration(policy json.RawMessage) error {
	_, err := ResolveRoutingCanaryPolicy(policy)
	return err
}

func ValidateRoutingPolicyActivationDocument(document RoutingPolicyDocument, activation RoutingPolicyActivationSpec) error {
	if err := activation.Validate(); err != nil {
		return err
	}
	for index := range document.Pools {
		poolStage := document.Pools[index].DeploymentStage
		valid := false
		switch activation.Stage {
		case RoutingDeploymentStageObserve:
			valid = poolStage == RoutingDeploymentStageObserve
		case RoutingDeploymentStageShadow:
			valid = poolStage == RoutingDeploymentStageObserve || poolStage == RoutingDeploymentStageShadow
		case RoutingDeploymentStageCanary:
			valid = poolStage == RoutingDeploymentStageObserve || poolStage == RoutingDeploymentStageShadow ||
				poolStage == RoutingDeploymentStageCanary
		case RoutingDeploymentStageActive:
			valid = poolStage == RoutingDeploymentStageObserve || poolStage == RoutingDeploymentStageShadow ||
				poolStage == RoutingDeploymentStageActive
		}
		if !valid {
			return ErrRoutingPolicyInvalid
		}
	}
	return nil
}

func routingPolicyRevisionRows(revision int64, document RoutingPolicyDocument) ([]RoutingPolicyPoolRevision, []RoutingPolicyMemberRevision, error) {
	poolRows := make([]RoutingPolicyPoolRevision, 0, len(document.Pools))
	memberRows := make([]RoutingPolicyMemberRevision, 0, routingPolicyDocumentMemberCount(document))
	for poolIndex := range document.Pools {
		pool := document.Pools[poolIndex]
		poolRows = append(poolRows, RoutingPolicyPoolRevision{
			Revision:        revision,
			PoolID:          pool.PoolID,
			GroupKey:        routingGroupKey(pool.GroupName),
			GroupName:       pool.GroupName,
			DisplayName:     pool.DisplayName,
			DeploymentStage: pool.DeploymentStage,
			PolicyProfile:   pool.PolicyProfile,
			PolicyJSON:      string(pool.Policy),
		})
		for memberIndex := range pool.Members {
			member := pool.Members[memberIndex]
			credentialIDs, err := common.Marshal(member.CredentialIDs)
			if err != nil {
				return nil, nil, err
			}
			memberRows = append(memberRows, RoutingPolicyMemberRevision{
				Revision:          revision,
				PoolID:            pool.PoolID,
				MemberID:          member.MemberID,
				ChannelID:         member.ChannelID,
				Enabled:           member.Enabled,
				Priority:          member.Priority,
				Weight:            member.Weight,
				CredentialIDsJSON: string(credentialIDs),
				OverridesJSON:     string(member.Overrides),
			})
		}
	}
	return poolRows, memberRows, nil
}

func createRoutingPolicyRowsInBatches[T any](tx *gorm.DB, rows []T, maxRows int, encodedSize func(T) int) error {
	if maxRows < 1 || encodedSize == nil {
		return ErrRoutingPolicyInvalid
	}
	for start := 0; start < len(rows); {
		end := start
		batchBytes := 0
		for end < len(rows) && end-start < maxRows {
			rowBytes := encodedSize(rows[end])
			if rowBytes < 1 || rowBytes > routingPolicyInsertBatchMaxBytes {
				return ErrRoutingPolicyInvalid
			}
			if end > start && batchBytes > routingPolicyInsertBatchMaxBytes-rowBytes {
				break
			}
			batchBytes += rowBytes
			end++
		}
		batch := rows[start:end]
		if err := tx.Create(&batch).Error; err != nil {
			return err
		}
		start = end
	}
	return nil
}

func routingPolicyPoolRowEncodedSize(row RoutingPolicyPoolRevision) int {
	return 256 + len(row.GroupKey) + len(row.GroupName) + len(row.DisplayName) +
		len(row.DeploymentStage) + len(row.PolicyProfile) + len(row.PolicyJSON)
}

func routingPolicyMemberRowEncodedSize(row RoutingPolicyMemberRevision) int {
	return 256 + len(row.CredentialIDsJSON) + len(row.OverridesJSON)
}

func routingPolicyDocumentMemberCount(document RoutingPolicyDocument) int {
	total := 0
	for index := range document.Pools {
		total += len(document.Pools[index].Members)
	}
	return total
}

func validRoutingPolicyActivationSpec(spec RoutingPolicyActivationSpec) bool {
	if !validRoutingDeploymentStage(spec.Stage) || spec.ActorID < 0 ||
		!validRoutingPolicyText(spec.Reason, routingPolicyReasonMaxRunes) {
		return false
	}
	if spec.Stage == RoutingDeploymentStageCanary {
		return spec.TrafficBasisPoints >= RoutingPolicyCanaryMinBasisPoints &&
			spec.TrafficBasisPoints <= RoutingPolicyCanaryMaxBasisPoints
	}
	return spec.TrafficBasisPoints == 0
}

func validRoutingDeploymentStage(stage string) bool {
	switch stage {
	case RoutingDeploymentStageObserve, RoutingDeploymentStageShadow, RoutingDeploymentStageCanary, RoutingDeploymentStageActive:
		return true
	default:
		return false
	}
}

func validRoutingPolicyProfile(profile string) bool {
	switch profile {
	case RoutingPolicyProfileBalanced, RoutingPolicyProfileReliabilityFirst, RoutingPolicyProfileCostAware,
		RoutingPolicyProfileEnterpriseSLO, RoutingPolicyProfileCustom:
		return true
	default:
		return false
	}
}

func validRoutingPolicyText(value string, maxRunes int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes
}

func routingPolicyHash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func newRoutingPolicyRevisionConflict(expected int64, actual RoutingPolicyHead) error {
	return &RoutingPolicyRevisionConflictError{
		ExpectedRevision: expected,
		ActualRevision:   actual.CurrentRevision,
		ActualHash:       actual.CurrentHash,
	}
}
