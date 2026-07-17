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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	routingcapability "github.com/QuantumNous/new-api/pkg/routing_capability"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingPolicyLegacySchemaVersion  = 1
	RoutingPolicySchemaVersion        = 2
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
	routingPolicyJSONMaxDepth              = 64
	routingPolicyJSONMaxNodes              = 8_192
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
	ErrRoutingPolicyLegacyRollback     = errors.New("legacy routing policy rollback requires a v2 conversion draft")
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
	Revision           int64   `json:"revision" gorm:"primaryKey;autoIncrement:false"`
	ParentRevision     int64   `json:"parent_revision" gorm:"bigint;index;not null"`
	RollbackOfRevision int64   `json:"rollback_of_revision" gorm:"bigint;index;not null"`
	SchemaVersion      int     `json:"schema_version" gorm:"not null"`
	ContentHash        string  `json:"content_hash" gorm:"type:char(64);index;not null"`
	ExtensionsJSON     *string `json:"-" gorm:"type:text"`
	PoolCount          int     `json:"pool_count" gorm:"not null"`
	MemberCount        int     `json:"member_count" gorm:"not null"`
	ActorID            int     `json:"actor_id" gorm:"index;not null"`
	Reason             string  `json:"reason" gorm:"type:varchar(512);not null"`
	CreatedTime        int64   `json:"created_time" gorm:"bigint;index;not null"`
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
	ID              int64   `json:"id" gorm:"primaryKey"`
	Revision        int64   `json:"revision" gorm:"bigint;not null;uniqueIndex:idx_routing_policy_pool_revision,priority:1;index"`
	PoolID          int     `json:"pool_id" gorm:"not null;uniqueIndex:idx_routing_policy_pool_revision,priority:2;index"`
	GroupKey        string  `json:"-" gorm:"type:char(64);not null;index"`
	GroupName       string  `json:"group_name" gorm:"type:varchar(64);not null"`
	DisplayName     string  `json:"display_name" gorm:"type:varchar(128);not null"`
	DeploymentStage string  `json:"deployment_stage" gorm:"type:varchar(16);not null;index"`
	PolicyProfile   string  `json:"policy_profile" gorm:"type:varchar(32);not null;index"`
	PolicyJSON      string  `json:"-" gorm:"type:text;not null"`
	DefaultEnabled  *bool   `json:"-"`
	DefaultPriority *int64  `json:"-" gorm:"bigint"`
	DefaultWeight   *int64  `json:"-" gorm:"bigint"`
	ExtensionsJSON  *string `json:"-" gorm:"type:text"`
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
	ID                int64   `json:"id" gorm:"primaryKey"`
	Revision          int64   `json:"revision" gorm:"bigint;not null;uniqueIndex:idx_routing_policy_member_revision,priority:1;uniqueIndex:idx_routing_policy_member_channel,priority:1;index:idx_routing_policy_member_pool,priority:1"`
	PoolID            int     `json:"pool_id" gorm:"not null;uniqueIndex:idx_routing_policy_member_channel,priority:2;index:idx_routing_policy_member_pool,priority:2"`
	MemberID          int     `json:"member_id" gorm:"not null;uniqueIndex:idx_routing_policy_member_revision,priority:2;index"`
	ChannelID         int     `json:"channel_id" gorm:"not null;uniqueIndex:idx_routing_policy_member_channel,priority:3;index"`
	RoutingGeneration string  `json:"routing_generation,omitempty" gorm:"type:varchar(32);index"`
	Enabled           bool    `json:"enabled" gorm:"not null"`
	Priority          int64   `json:"priority" gorm:"bigint;not null"`
	Weight            int64   `json:"weight" gorm:"bigint;not null"`
	EnabledOverride   *bool   `json:"-"`
	PriorityOverride  *int64  `json:"-" gorm:"bigint"`
	WeightOverride    *int64  `json:"-" gorm:"bigint"`
	CredentialIDsJSON string  `json:"-" gorm:"type:text;not null"`
	OverridesJSON     string  `json:"-" gorm:"type:text;not null"`
	ExtensionsJSON    *string `json:"-" gorm:"type:text"`
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
	SchemaVersion   int                        `json:"schema_version"`
	Pools           []RoutingPolicyPoolContent `json:"pools"`
	ExtensionFields map[string]json.RawMessage `json:"-"`
}

type RoutingPolicyPoolContent struct {
	PoolID          int                          `json:"pool_id"`
	GroupName       string                       `json:"group_name"`
	DisplayName     string                       `json:"display_name"`
	DeploymentStage string                       `json:"deployment_stage"`
	PolicyProfile   string                       `json:"policy_profile"`
	Policy          json.RawMessage              `json:"policy"`
	DefaultEnabled  *bool                        `json:"default_enabled,omitempty"`
	DefaultPriority *int64                       `json:"default_priority,omitempty"`
	DefaultWeight   *int64                       `json:"default_weight,omitempty"`
	Members         []RoutingPolicyMemberContent `json:"members"`
	ExtensionFields map[string]json.RawMessage   `json:"-"`
}

type RoutingPolicyMemberContent struct {
	MemberID          int                        `json:"member_id"`
	ChannelID         int                        `json:"channel_id"`
	RoutingGeneration string                     `json:"routing_generation,omitempty"`
	Enabled           bool                       `json:"enabled"`
	Priority          int64                      `json:"priority"`
	Weight            int64                      `json:"weight"`
	EnabledOverride   *bool                      `json:"enabled_override,omitempty"`
	PriorityOverride  *int64                     `json:"priority_override,omitempty"`
	WeightOverride    *int64                     `json:"weight_override,omitempty"`
	CredentialIDs     []int                      `json:"credential_ids"`
	Overrides         json.RawMessage            `json:"overrides"`
	ExtensionFields   map[string]json.RawMessage `json:"-"`
}

func (document RoutingPolicyDocument) MarshalJSON() ([]byte, error) {
	type routingPolicyDocumentJSON RoutingPolicyDocument
	known := routingPolicyDocumentJSON(document)
	known.ExtensionFields = nil
	return marshalRoutingPolicyObject(known, document.ExtensionFields, routingPolicyDocumentKnownField)
}

func (document *RoutingPolicyDocument) UnmarshalJSON(data []byte) error {
	type routingPolicyDocumentJSON RoutingPolicyDocument
	var decoded routingPolicyDocumentJSON
	if err := common.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extensions, err := routingPolicyUnknownFields(data, routingPolicyDocumentKnownField)
	if err != nil {
		return err
	}
	decoded.ExtensionFields = extensions
	*document = RoutingPolicyDocument(decoded)
	return nil
}

func (pool RoutingPolicyPoolContent) MarshalJSON() ([]byte, error) {
	type routingPolicyPoolJSON RoutingPolicyPoolContent
	known := routingPolicyPoolJSON(pool)
	known.ExtensionFields = nil
	return marshalRoutingPolicyObject(known, pool.ExtensionFields, routingPolicyPoolKnownField)
}

func (pool *RoutingPolicyPoolContent) UnmarshalJSON(data []byte) error {
	type routingPolicyPoolJSON RoutingPolicyPoolContent
	var decoded routingPolicyPoolJSON
	if err := common.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extensions, err := routingPolicyUnknownFields(data, routingPolicyPoolKnownField)
	if err != nil {
		return err
	}
	decoded.ExtensionFields = extensions
	*pool = RoutingPolicyPoolContent(decoded)
	return nil
}

func (member RoutingPolicyMemberContent) MarshalJSON() ([]byte, error) {
	type routingPolicyMemberJSON RoutingPolicyMemberContent
	known := routingPolicyMemberJSON(member)
	known.ExtensionFields = nil
	return marshalRoutingPolicyObject(known, member.ExtensionFields, routingPolicyMemberKnownField)
}

func (member *RoutingPolicyMemberContent) UnmarshalJSON(data []byte) error {
	type routingPolicyMemberJSON RoutingPolicyMemberContent
	var decoded routingPolicyMemberJSON
	if err := common.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extensions, err := routingPolicyUnknownFields(data, routingPolicyMemberKnownField)
	if err != nil {
		return err
	}
	decoded.ExtensionFields = extensions
	*member = RoutingPolicyMemberContent(decoded)
	return nil
}

func routingPolicyDocumentKnownField(key string) bool {
	return key == "schema_version" || key == "pools"
}

func routingPolicyPoolKnownField(key string) bool {
	switch key {
	case "pool_id", "group_name", "display_name", "deployment_stage", "policy_profile", "policy",
		"default_enabled", "default_priority", "default_weight", "members":
		return true
	default:
		return false
	}
}

func routingPolicyMemberKnownField(key string) bool {
	switch key {
	case "member_id", "channel_id", "routing_generation", "enabled", "priority", "weight",
		"enabled_override", "priority_override", "weight_override", "credential_ids", "overrides":
		return true
	default:
		return false
	}
}

func routingPolicyUnknownFields(
	data []byte,
	knownField func(string) bool,
) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key := range fields {
		if knownField(key) {
			delete(fields, key)
		}
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return fields, nil
}

func marshalRoutingPolicyObject(
	known any,
	extensions map[string]json.RawMessage,
	knownField func(string) bool,
) ([]byte, error) {
	encodedKnown, err := common.Marshal(known)
	if err != nil || len(encodedKnown) < 2 || encodedKnown[0] != '{' || encodedKnown[len(encodedKnown)-1] != '}' {
		return nil, ErrRoutingPolicyInvalid
	}
	normalizedExtensions, err := normalizeRoutingPolicyExtensionFields(extensions, knownField)
	if err != nil {
		return nil, err
	}
	if len(normalizedExtensions) == 0 {
		return encodedKnown, nil
	}
	encodedExtensions, err := common.Marshal(normalizedExtensions)
	if err != nil || len(encodedExtensions) < 2 || encodedExtensions[0] != '{' ||
		encodedExtensions[len(encodedExtensions)-1] != '}' {
		return nil, ErrRoutingPolicyInvalid
	}
	encoded := make([]byte, 0, len(encodedKnown)+len(encodedExtensions))
	encoded = append(encoded, encodedKnown[:len(encodedKnown)-1]...)
	encoded = append(encoded, ',')
	encoded = append(encoded, encodedExtensions[1:]...)
	return encoded, nil
}

func normalizeRoutingPolicyExtensionFields(
	extensions map[string]json.RawMessage,
	knownField func(string) bool,
) (map[string]json.RawMessage, error) {
	if len(extensions) == 0 {
		return nil, nil
	}
	normalized := make(map[string]json.RawMessage, len(extensions))
	for key, value := range extensions {
		if key == "" || !validRoutingPolicyText(key, 128) || knownField(key) ||
			len(bytes.TrimSpace(value)) == 0 || len(value) > routingPolicyMaxConfigBytes {
			return nil, ErrRoutingPolicyInvalid
		}
		nodes := 0
		canonical, err := normalizeRoutingPolicyJSONValue(value, 0, &nodes)
		if err != nil {
			return nil, ErrRoutingPolicyInvalid
		}
		normalized[key] = canonical
	}
	canonical, err := common.Marshal(normalized)
	if err != nil || len(canonical) > routingPolicyMaxConfigBytes {
		return nil, ErrRoutingPolicyInvalid
	}
	return normalized, nil
}

func routingPolicyExtensionsJSON(
	extensions map[string]json.RawMessage,
	knownField func(string) bool,
) (*string, error) {
	normalized, err := normalizeRoutingPolicyExtensionFields(extensions, knownField)
	if err != nil || len(normalized) == 0 {
		return nil, err
	}
	encoded, err := common.Marshal(normalized)
	if err != nil {
		return nil, ErrRoutingPolicyInvalid
	}
	value := string(encoded)
	return &value, nil
}

func routingPolicyExtensionsFromJSON(
	encoded *string,
	knownField func(string) bool,
) (map[string]json.RawMessage, error) {
	if encoded == nil || strings.TrimSpace(*encoded) == "" {
		return nil, nil
	}
	var extensions map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(*encoded, &extensions); err != nil || extensions == nil {
		return nil, ErrRoutingPolicyContentCorrupt
	}
	normalized, err := normalizeRoutingPolicyExtensionFields(extensions, knownField)
	if err != nil {
		return nil, ErrRoutingPolicyContentCorrupt
	}
	return normalized, nil
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

func RollbackRoutingPolicyRevisionWithOperationContext(
	ctx context.Context,
	expectedRevision int64,
	sourceRevision int64,
	activation RoutingPolicyActivationSpec,
) (RoutingPolicyPublishResult, RoutingOperation, error) {
	return rollbackRoutingPolicyRevisionWithOperationContext(
		ctx, expectedRevision, sourceRevision, activation, RoutingOperationRequestIdentity{},
	)
}

func RollbackRoutingPolicyRevisionWithOperationRequestContext(
	ctx context.Context,
	expectedRevision int64,
	sourceRevision int64,
	activation RoutingPolicyActivationSpec,
	requestIdentity RoutingOperationRequestIdentity,
) (RoutingPolicyPublishResult, RoutingOperation, error) {
	if !validRoutingOperationRequestIdentity(requestIdentity) {
		return RoutingPolicyPublishResult{}, RoutingOperation{}, ErrRoutingOperationInvalid
	}
	return rollbackRoutingPolicyRevisionWithOperationContext(
		ctx, expectedRevision, sourceRevision, activation, requestIdentity,
	)
}

func rollbackRoutingPolicyRevisionWithOperationContext(
	ctx context.Context,
	expectedRevision int64,
	sourceRevision int64,
	activation RoutingPolicyActivationSpec,
	requestIdentity RoutingOperationRequestIdentity,
) (RoutingPolicyPublishResult, RoutingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sourceRevision <= 0 || sourceRevision >= expectedRevision || activation.Validate() != nil {
		return RoutingPolicyPublishResult{}, RoutingOperation{}, ErrRoutingPolicyInvalid
	}
	document, source, err := LoadRoutingPolicyRevisionContext(ctx, sourceRevision)
	if err != nil {
		return RoutingPolicyPublishResult{}, RoutingOperation{}, err
	}
	if source.SchemaVersion != RoutingPolicySchemaVersion {
		return RoutingPolicyPublishResult{}, RoutingOperation{}, ErrRoutingPolicyLegacyRollback
	}
	evaluationHash, err := routingPolicyRollbackOperationHash(expectedRevision, source, activation)
	if err != nil {
		return RoutingPolicyPublishResult{}, RoutingOperation{}, err
	}
	changedPoolIDs := make([]int, len(document.Pools))
	for index := range document.Pools {
		changedPoolIDs[index] = document.Pools[index].PoolID
	}
	var published RoutingPolicyPublishResult
	var operation RoutingOperation
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		head, headErr := ensureRoutingPolicyHeadTx(tx.WithContext(ctx))
		if headErr != nil {
			return headErr
		}
		if err := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", routingPolicyHeadID).First(&head).Error; err != nil {
			return err
		}
		if requestIdentity != (RoutingOperationRequestIdentity{}) {
			existing, found, replayErr := getRoutingOperationByRequestIdentityDB(ctx, tx, requestIdentity)
			if replayErr != nil {
				return replayErr
			}
			if found {
				if existing.OperationType != RoutingOperationTypePolicyRollback ||
					existing.SubjectType != RoutingOperationSubjectPolicyRevision || existing.SubjectID != sourceRevision ||
					existing.ActorID != activation.ActorID || existing.Status != RoutingOperationStatusSucceeded {
					return ErrRoutingOperationIdempotencyConflict
				}
				published, replayErr = routingPolicyPublishResultForOperationTx(ctx, tx, existing)
				if replayErr != nil {
					return replayErr
				}
				operation = existing
				return nil
			}
		}
		if head.CurrentRevision != expectedRevision {
			return newRoutingPolicyRevisionConflict(expectedRevision, head)
		}
		published, err = publishNormalizedRoutingPolicyRevisionTx(
			ctx, tx, expectedRevision, sourceRevision, document, source.ContentHash,
			activation, changedPoolIDs, common.GetTimestamp(),
		)
		if err != nil {
			return err
		}
		operation, _, err = createSucceededRoutingOperationTx(
			ctx,
			tx,
			RoutingOperationSpec{
				Type: RoutingOperationTypePolicyRollback, EvaluationHash: evaluationHash,
				SubjectType: RoutingOperationSubjectPolicyRevision, SubjectID: sourceRevision,
				ExpectedRevision: expectedRevision, ExpectedActivationID: head.CurrentActivationID,
				ActorID: activation.ActorID, Reason: activation.Reason,
				RequestKeyHash: requestIdentity.KeyHash, RequestPayloadHash: requestIdentity.PayloadHash,
			},
			RoutingOperationResult{
				Revision:     published.Revision.Revision,
				ActivationID: published.Activation.ID,
				OutboxID:     published.Outbox.ID,
			},
			struct {
				SourceRevision int64 `json:"source_revision"`
			}{SourceRevision: sourceRevision},
			time.Now().UnixMilli(),
		)
		if err != nil {
			return err
		}
		return insertRoutingOperationTransitionAuditTx(
			tx.WithContext(ctx), routingOperationInitialAuditState(operation), operation, RoutingControlActionRollback,
		)
	})
	if err != nil {
		return RoutingPolicyPublishResult{}, RoutingOperation{}, err
	}
	return published, operation, nil
}

func routingPolicyPublishResultForOperationTx(
	ctx context.Context,
	tx *gorm.DB,
	operation RoutingOperation,
) (RoutingPolicyPublishResult, error) {
	if tx == nil || operation.Status != RoutingOperationStatusSucceeded ||
		operation.ResultRevision <= 0 || operation.ResultActivationID <= 0 || operation.ResultOutboxID <= 0 {
		return RoutingPolicyPublishResult{}, ErrRoutingOperationCorrupt
	}
	var result RoutingPolicyPublishResult
	if err := tx.WithContext(ctx).Where("revision = ?", operation.ResultRevision).First(&result.Revision).Error; err != nil {
		return RoutingPolicyPublishResult{}, err
	}
	if err := tx.WithContext(ctx).Where("id = ?", operation.ResultActivationID).First(&result.Activation).Error; err != nil {
		return RoutingPolicyPublishResult{}, err
	}
	if err := tx.WithContext(ctx).Where("id = ?", operation.ResultOutboxID).First(&result.Outbox).Error; err != nil {
		return RoutingPolicyPublishResult{}, err
	}
	if result.Activation.Revision != result.Revision.Revision || result.Outbox.Revision != result.Revision.Revision {
		return RoutingPolicyPublishResult{}, ErrRoutingOperationCorrupt
	}
	return result, nil
}

func routingPolicyRollbackOperationHash(
	expectedRevision int64,
	source RoutingPolicyRevision,
	activation RoutingPolicyActivationSpec,
) (string, error) {
	payload, err := common.Marshal(struct {
		SchemaVersion      int    `json:"schema_version"`
		ExpectedRevision   int64  `json:"expected_revision"`
		SourceRevision     int64  `json:"source_revision"`
		SourceContentHash  string `json:"source_content_hash"`
		Stage              string `json:"stage"`
		TrafficBasisPoints int    `json:"traffic_basis_points"`
		ActorID            int    `json:"actor_id"`
		Reason             string `json:"reason"`
	}{
		SchemaVersion: 1, ExpectedRevision: expectedRevision, SourceRevision: source.Revision,
		SourceContentHash: source.ContentHash, Stage: activation.Stage,
		TrafficBasisPoints: activation.TrafficBasisPoints, ActorID: activation.ActorID, Reason: activation.Reason,
	})
	if err != nil {
		return "", err
	}
	return routingPolicyHash(payload), nil
}

func RollbackRoutingPolicyRevisionDBContext(
	ctx context.Context,
	db *gorm.DB,
	expectedRevision int64,
	sourceRevision int64,
	activation RoutingPolicyActivationSpec,
) (RoutingPolicyPublishResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil {
		return RoutingPolicyPublishResult{}, errRoutingPolicyDatabaseNil
	}
	if sourceRevision <= 0 || sourceRevision >= expectedRevision || activation.Validate() != nil {
		return RoutingPolicyPublishResult{}, ErrRoutingPolicyInvalid
	}
	var result RoutingPolicyPublishResult
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		head, err := ensureRoutingPolicyHeadTx(tx.WithContext(ctx))
		if err != nil {
			return err
		}
		if err := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", routingPolicyHeadID).First(&head).Error; err != nil {
			return err
		}
		if head.CurrentRevision != expectedRevision {
			return newRoutingPolicyRevisionConflict(expectedRevision, head)
		}
		document, target, err := LoadRoutingPolicyRevisionDBContext(ctx, tx, sourceRevision)
		if err != nil {
			return err
		}
		if target.SchemaVersion != RoutingPolicySchemaVersion {
			return ErrRoutingPolicyLegacyRollback
		}
		changedPoolIDs := make([]int, len(document.Pools))
		for index := range document.Pools {
			changedPoolIDs[index] = document.Pools[index].PoolID
		}
		result, err = publishNormalizedRoutingPolicyRevisionTx(
			ctx, tx, expectedRevision, sourceRevision, document, target.ContentHash,
			activation, changedPoolIDs, common.GetTimestamp(),
		)
		return err
	})
	return result, err
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

	documentExtensions, err := routingPolicyExtensionsFromJSON(
		revision.ExtensionsJSON,
		routingPolicyDocumentKnownField,
	)
	if err != nil {
		return RoutingPolicyDocument{}, RoutingPolicyRevision{}, err
	}
	document := RoutingPolicyDocument{
		SchemaVersion:   revision.SchemaVersion,
		Pools:           make([]RoutingPolicyPoolContent, len(poolRows)),
		ExtensionFields: documentExtensions,
	}
	poolIndexes := make(map[int]int, len(poolRows))
	for index := range poolRows {
		row := poolRows[index]
		if row.GroupKey != routingGroupKey(row.GroupName) {
			return RoutingPolicyDocument{}, RoutingPolicyRevision{}, fmt.Errorf("%w: group hash mismatch", ErrRoutingPolicyContentCorrupt)
		}
		extensions, err := routingPolicyExtensionsFromJSON(row.ExtensionsJSON, routingPolicyPoolKnownField)
		if err != nil {
			return RoutingPolicyDocument{}, RoutingPolicyRevision{}, err
		}
		poolIndexes[row.PoolID] = index
		document.Pools[index] = RoutingPolicyPoolContent{
			PoolID:          row.PoolID,
			GroupName:       row.GroupName,
			DisplayName:     row.DisplayName,
			DeploymentStage: row.DeploymentStage,
			PolicyProfile:   row.PolicyProfile,
			Policy:          json.RawMessage(row.PolicyJSON),
			DefaultEnabled:  row.DefaultEnabled,
			DefaultPriority: row.DefaultPriority,
			DefaultWeight:   row.DefaultWeight,
			Members:         make([]RoutingPolicyMemberContent, 0),
			ExtensionFields: extensions,
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
		extensions, err := routingPolicyExtensionsFromJSON(row.ExtensionsJSON, routingPolicyMemberKnownField)
		if err != nil {
			return RoutingPolicyDocument{}, RoutingPolicyRevision{}, err
		}
		if revision.SchemaVersion == RoutingPolicySchemaVersion {
			pool := document.Pools[poolIndex]
			if pool.DefaultEnabled == nil || pool.DefaultPriority == nil || pool.DefaultWeight == nil {
				return RoutingPolicyDocument{}, RoutingPolicyRevision{}, fmt.Errorf(
					"%w: v2 pool defaults are incomplete", ErrRoutingPolicyContentCorrupt,
				)
			}
			effectiveEnabled := *pool.DefaultEnabled
			effectivePriority := *pool.DefaultPriority
			effectiveWeight := *pool.DefaultWeight
			if row.EnabledOverride != nil {
				effectiveEnabled = *row.EnabledOverride
			}
			if row.PriorityOverride != nil {
				effectivePriority = *row.PriorityOverride
			}
			if row.WeightOverride != nil {
				effectiveWeight = *row.WeightOverride
			}
			if row.Enabled != effectiveEnabled || row.Priority != effectivePriority || row.Weight != effectiveWeight {
				return RoutingPolicyDocument{}, RoutingPolicyRevision{}, fmt.Errorf(
					"%w: v2 member effective values conflict with overrides", ErrRoutingPolicyContentCorrupt,
				)
			}
		}
		document.Pools[poolIndex].Members = append(document.Pools[poolIndex].Members, RoutingPolicyMemberContent{
			MemberID:          row.MemberID,
			ChannelID:         row.ChannelID,
			RoutingGeneration: row.RoutingGeneration,
			Enabled:           row.Enabled,
			Priority:          row.Priority,
			Weight:            row.Weight,
			EnabledOverride:   row.EnabledOverride,
			PriorityOverride:  row.PriorityOverride,
			WeightOverride:    row.WeightOverride,
			CredentialIDs:     credentialIDs,
			Overrides:         json.RawMessage(row.OverridesJSON),
			ExtensionFields:   extensions,
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
	if err := lockForUpdate(tx).Where("id = ?", routingPolicyHeadID).First(&head).Error; err != nil {
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
	if err := validateRoutingPolicyLiveReferencesTx(tx, document); err != nil {
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

	revisionExtensionsJSON, err := routingPolicyExtensionsJSON(
		document.ExtensionFields,
		routingPolicyDocumentKnownField,
	)
	if err != nil {
		return RoutingPolicyPublishResult{}, err
	}
	revision := RoutingPolicyRevision{
		Revision:           nextRevision,
		ParentRevision:     expectedRevision,
		RollbackOfRevision: rollbackOfRevision,
		SchemaVersion:      document.SchemaVersion,
		ContentHash:        contentHash,
		ExtensionsJSON:     revisionExtensionsJSON,
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
	result := RoutingPolicyPublishResult{Revision: revision, Activation: activation, Outbox: outbox}
	if err := insertRoutingPolicyPublicationAuditTx(ctx, tx, head, document, result); err != nil {
		return RoutingPolicyPublishResult{}, err
	}
	return result, nil
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
	if document.SchemaVersion != RoutingPolicyLegacySchemaVersion &&
		document.SchemaVersion != RoutingPolicySchemaVersion || len(document.Pools) > routingTopologyMaxPools {
		return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
	}

	documentExtensions, err := normalizeRoutingPolicyExtensionFields(
		document.ExtensionFields,
		routingPolicyDocumentKnownField,
	)
	if err != nil {
		return RoutingPolicyDocument{}, "", err
	}
	normalized := RoutingPolicyDocument{
		SchemaVersion:   document.SchemaVersion,
		Pools:           make([]RoutingPolicyPoolContent, len(document.Pools)),
		ExtensionFields: documentExtensions,
	}
	poolIDs := make(map[int]struct{}, len(document.Pools))
	groupNames := make(map[string]struct{}, len(document.Pools))
	memberIDs := make(map[int]struct{})
	totalMembers := 0
	totalCredentialRefs := 0
	for poolIndex := range document.Pools {
		pool := document.Pools[poolIndex]
		poolExtensions, err := normalizeRoutingPolicyExtensionFields(
			pool.ExtensionFields,
			routingPolicyPoolKnownField,
		)
		if err != nil {
			return RoutingPolicyDocument{}, "", err
		}
		pool.ExtensionFields = poolExtensions
		if pool.PoolID <= 0 || !validRoutingPolicyText(pool.GroupName, 64) || pool.GroupName == "" ||
			!validRoutingPolicyText(pool.DisplayName, 128) || !validRoutingDeploymentStage(pool.DeploymentStage) ||
			!validRoutingPolicyProfile(pool.PolicyProfile) {
			return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
		}
		if pool.DisplayName == "" {
			pool.DisplayName = pool.GroupName
		}
		if document.SchemaVersion == RoutingPolicySchemaVersion {
			defaultEnabled := true
			defaultPriority := int64(0)
			defaultWeight := int64(100)
			if pool.DefaultEnabled != nil {
				defaultEnabled = *pool.DefaultEnabled
			}
			if pool.DefaultPriority != nil {
				defaultPriority = *pool.DefaultPriority
			}
			if pool.DefaultWeight != nil {
				defaultWeight = *pool.DefaultWeight
			}
			if defaultWeight < 0 {
				return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
			}
			pool.DefaultEnabled = &defaultEnabled
			pool.DefaultPriority = &defaultPriority
			pool.DefaultWeight = &defaultWeight
		} else if pool.DefaultEnabled != nil || pool.DefaultPriority != nil || pool.DefaultWeight != nil {
			return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
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
		if _, err := routingcapability.ParsePoolPolicy(policy); err != nil {
			return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
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
			memberExtensions, err := normalizeRoutingPolicyExtensionFields(
				member.ExtensionFields,
				routingPolicyMemberKnownField,
			)
			if err != nil {
				return RoutingPolicyDocument{}, "", err
			}
			member.ExtensionFields = memberExtensions
			if member.MemberID <= 0 || member.ChannelID <= 0 || member.Weight < 0 {
				return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
			}
			if document.SchemaVersion == RoutingPolicySchemaVersion {
				if member.RoutingGeneration != "" && !validRoutingIdentity(member.RoutingGeneration) {
					return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
				}
				if member.WeightOverride != nil && *member.WeightOverride < 0 {
					return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
				}
				if member.RoutingGeneration == "" &&
					member.EnabledOverride == nil && member.PriorityOverride == nil && member.WeightOverride == nil &&
					(member.Enabled || member.Priority != 0 || member.Weight != 0) {
					enabled := member.Enabled
					priority := member.Priority
					weight := member.Weight
					member.EnabledOverride = &enabled
					member.PriorityOverride = &priority
					member.WeightOverride = &weight
				}
				effectiveEnabled := *pool.DefaultEnabled
				effectivePriority := *pool.DefaultPriority
				effectiveWeight := *pool.DefaultWeight
				if member.EnabledOverride != nil {
					effectiveEnabled = *member.EnabledOverride
				}
				if member.PriorityOverride != nil {
					effectivePriority = *member.PriorityOverride
				}
				if member.WeightOverride != nil {
					effectiveWeight = *member.WeightOverride
				}
				member.Enabled = effectiveEnabled
				member.Priority = effectivePriority
				member.Weight = effectiveWeight
			} else if member.RoutingGeneration != "" || member.EnabledOverride != nil ||
				member.PriorityOverride != nil || member.WeightOverride != nil {
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
			if _, err := routingcapability.ParseMemberOverrides(overrides); err != nil {
				return RoutingPolicyDocument{}, "", ErrRoutingPolicyInvalid
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
		MemberID          int
		PoolID            int
		ChannelID         int
		RoutingGeneration string
	}
	desired := make(map[int]memberIdentity, routingPolicyDocumentMemberCount(document))
	memberIDs := make([]int, 0, len(desired))
	for poolIndex := range document.Pools {
		pool := document.Pools[poolIndex]
		for memberIndex := range pool.Members {
			member := pool.Members[memberIndex]
			desired[member.MemberID] = memberIdentity{
				MemberID: member.MemberID, PoolID: pool.PoolID, ChannelID: member.ChannelID,
				RoutingGeneration: member.RoutingGeneration,
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
			Select("member_id", "pool_id", "channel_id", "routing_generation").
			Where("member_id IN ?", memberIDs[start:end]).
			Group("member_id, pool_id, channel_id, routing_generation").
			Find(&historical).Error; err != nil {
			return err
		}
		for index := range historical {
			identity, exists := desired[historical[index].MemberID]
			if !exists || identity.PoolID != historical[index].PoolID || identity.ChannelID != historical[index].ChannelID ||
				historical[index].RoutingGeneration != "" &&
					identity.RoutingGeneration != historical[index].RoutingGeneration {
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
	if len(value) > routingPolicyMaxConfigBytes || common.GetJsonType(value) != "object" {
		return nil, ErrRoutingPolicyInvalid
	}
	nodes := 0
	canonical, err := normalizeRoutingPolicyJSONValue(value, 0, &nodes)
	if err != nil || len(canonical) > routingPolicyMaxConfigBytes {
		return nil, ErrRoutingPolicyInvalid
	}
	return canonical, nil
}

func normalizeRoutingPolicyJSONValue(
	value json.RawMessage,
	depth int,
	nodes *int,
) (json.RawMessage, error) {
	if nodes == nil || depth > routingPolicyJSONMaxDepth {
		return nil, ErrRoutingPolicyInvalid
	}
	(*nodes)++
	if *nodes > routingPolicyJSONMaxNodes {
		return nil, ErrRoutingPolicyInvalid
	}
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || len(trimmed) > routingPolicyMaxConfigBytes {
		return nil, ErrRoutingPolicyInvalid
	}

	var encoded []byte
	var err error
	switch common.GetJsonType(trimmed) {
	case "object":
		var object map[string]json.RawMessage
		if err := common.Unmarshal(trimmed, &object); err != nil || object == nil {
			return nil, ErrRoutingPolicyInvalid
		}
		normalized := make(map[string]json.RawMessage, len(object))
		for key, child := range object {
			if !utf8.ValidString(key) {
				return nil, ErrRoutingPolicyInvalid
			}
			normalizedChild, err := normalizeRoutingPolicyJSONValue(child, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			normalized[key] = normalizedChild
		}
		encoded, err = common.Marshal(normalized)
	case "array":
		var array []json.RawMessage
		if err := common.Unmarshal(trimmed, &array); err != nil || array == nil {
			return nil, ErrRoutingPolicyInvalid
		}
		normalized := make([]json.RawMessage, len(array))
		for index := range array {
			normalized[index], err = normalizeRoutingPolicyJSONValue(array[index], depth+1, nodes)
			if err != nil {
				return nil, err
			}
		}
		encoded, err = common.Marshal(normalized)
	case "string":
		var decoded string
		if err := common.Unmarshal(trimmed, &decoded); err != nil {
			return nil, ErrRoutingPolicyInvalid
		}
		encoded, err = common.Marshal(decoded)
	case "boolean":
		var decoded bool
		if err := common.Unmarshal(trimmed, &decoded); err != nil {
			return nil, ErrRoutingPolicyInvalid
		}
		encoded, err = common.Marshal(decoded)
	case "number":
		var decoded json.Number
		if err := common.Unmarshal(trimmed, &decoded); err != nil {
			return nil, ErrRoutingPolicyInvalid
		}
		encoded, err = common.Marshal(decoded)
	case "null":
		if !bytes.Equal(trimmed, []byte("null")) {
			return nil, ErrRoutingPolicyInvalid
		}
		encoded = []byte("null")
	default:
		return nil, ErrRoutingPolicyInvalid
	}
	if err != nil || len(encoded) == 0 || len(encoded) > routingPolicyMaxConfigBytes {
		return nil, ErrRoutingPolicyInvalid
	}
	return json.RawMessage(encoded), nil
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
		poolExtensionsJSON, err := routingPolicyExtensionsJSON(pool.ExtensionFields, routingPolicyPoolKnownField)
		if err != nil {
			return nil, nil, err
		}
		poolRows = append(poolRows, RoutingPolicyPoolRevision{
			Revision:        revision,
			PoolID:          pool.PoolID,
			GroupKey:        routingGroupKey(pool.GroupName),
			GroupName:       pool.GroupName,
			DisplayName:     pool.DisplayName,
			DeploymentStage: pool.DeploymentStage,
			PolicyProfile:   pool.PolicyProfile,
			PolicyJSON:      string(pool.Policy),
			DefaultEnabled:  pool.DefaultEnabled,
			DefaultPriority: pool.DefaultPriority,
			DefaultWeight:   pool.DefaultWeight,
			ExtensionsJSON:  poolExtensionsJSON,
		})
		for memberIndex := range pool.Members {
			member := pool.Members[memberIndex]
			credentialIDs, err := common.Marshal(member.CredentialIDs)
			if err != nil {
				return nil, nil, err
			}
			memberExtensionsJSON, err := routingPolicyExtensionsJSON(
				member.ExtensionFields,
				routingPolicyMemberKnownField,
			)
			if err != nil {
				return nil, nil, err
			}
			memberRows = append(memberRows, RoutingPolicyMemberRevision{
				Revision:          revision,
				PoolID:            pool.PoolID,
				MemberID:          member.MemberID,
				ChannelID:         member.ChannelID,
				RoutingGeneration: member.RoutingGeneration,
				Enabled:           member.Enabled,
				Priority:          member.Priority,
				Weight:            member.Weight,
				EnabledOverride:   member.EnabledOverride,
				PriorityOverride:  member.PriorityOverride,
				WeightOverride:    member.WeightOverride,
				CredentialIDsJSON: string(credentialIDs),
				OverridesJSON:     string(member.Overrides),
				ExtensionsJSON:    memberExtensionsJSON,
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
		len(row.DeploymentStage) + len(row.PolicyProfile) + len(row.PolicyJSON) + routingPolicyOptionalTextSize(row.ExtensionsJSON)
}

func routingPolicyMemberRowEncodedSize(row RoutingPolicyMemberRevision) int {
	return 256 + len(row.CredentialIDsJSON) + len(row.OverridesJSON) + routingPolicyOptionalTextSize(row.ExtensionsJSON)
}

func routingPolicyOptionalTextSize(value *string) int {
	if value == nil {
		return 0
	}
	return len(*value)
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
