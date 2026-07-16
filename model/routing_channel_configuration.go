package model

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingChannelCostSourceManual         = "manual"
	RoutingChannelCostSourceLegacyMigrated = "legacy_migrated"
	RoutingChannelCostSourceDefaulted      = "defaulted"

	RoutingChannelTrafficClassAll            = "all"
	RoutingChannelTrafficClassClaudeCodeOnly = "claude_code_only"

	RoutingFailureDomainStatusConfigured         = "configured"
	RoutingFailureDomainStatusHistoricalMigrated = "historical_migrated"
	RoutingFailureDomainStatusUnconfigured       = "unconfigured"

	RoutingChannelConfigurationEventType = "routing.channel_configuration.changed"

	RoutingChannelUpstreamCostMultiplierMaximum = 1_000
	RoutingChannelFailureDomainLabelMaxRunes    = 128

	routingChannelConfigurationMigrationBatch        = 200
	routingChannelConfigurationMigrationVersionLimit = 50_001
	routingChannelConfigurationOutboxPayloadMaxBytes = 16 << 10
	routingChannelConfigurationOutboxErrorMaxRunes   = 1_024
	routingConfigurationEpochSingletonID             = 1
	retiredRoutingUpstreamAccountHealthTable         = "routing_upstream_account_health_states"
)

var (
	ErrRoutingChannelConfigurationInvalid         = errors.New("invalid routing channel configuration")
	ErrRoutingChannelConfigurationChanged         = errors.New("routing channel configuration changed")
	ErrRoutingChannelConfigurationOutboxClaimLost = errors.New("routing channel configuration outbox claim lost")
)

// RoutingChannelConfiguration is the sole mutable, channel-scoped routing
// configuration. Its multiplier is routing-only and must never be threaded
// into user quota, settlement, or billing logs.
type RoutingChannelConfiguration struct {
	ChannelID              int     `json:"channel_id" gorm:"primaryKey;autoIncrement:false"`
	UpstreamCostMultiplier float64 `json:"upstream_cost_multiplier" gorm:"not null"`
	CostSource             string  `json:"cost_source" gorm:"type:varchar(32);index;not null"`
	CostConfirmed          bool    `json:"cost_confirmed" gorm:"index;not null"`
	TrafficClass           string  `json:"traffic_class" gorm:"type:varchar(32);index;not null"`
	FailureDomainLabel     string  `json:"failure_domain_label" gorm:"type:varchar(512);not null"`
	FailureDomainHash      string  `json:"-" gorm:"type:char(64);index;not null"`
	FailureDomainStatus    string  `json:"failure_domain_status" gorm:"type:varchar(32);index;not null"`
	Revision               int64   `json:"revision" gorm:"bigint;not null"`
	UpdatedBy              int     `json:"updated_by" gorm:"index;not null"`
	CreatedTime            int64   `json:"created_time" gorm:"bigint;not null"`
	UpdatedTime            int64   `json:"updated_time" gorm:"bigint;index;not null"`
}

func (RoutingChannelConfiguration) TableName() string {
	return "routing_channel_configurations"
}

func (configuration *RoutingChannelConfiguration) AfterFind(_ *gorm.DB) error {
	// PostgreSQL pads an empty CHAR(64) value with spaces when scanning it.
	// The failure-domain hash is optional, so normalize that storage detail
	// before validation, hashing, API serialization, or retirement checks.
	configuration.FailureDomainHash = strings.TrimSpace(configuration.FailureDomainHash)
	return nil
}

type RoutingChannelConfigurationFilter struct {
	Search        string
	CostConfirmed *bool
	CostSource    string
	TrafficClass  string
}

type RoutingUpstreamConnectorRetirementResult struct {
	BindingsScrubbed          int64 `json:"bindings_scrubbed"`
	AccountsScrubbed          int64 `json:"accounts_scrubbed"`
	AccountHealthRowsCleared  int64 `json:"account_health_rows_cleared"`
	ChannelBalanceRowsCleared int64 `json:"channel_balance_rows_cleared"`
}

type retiredRoutingColumnScrub struct {
	column    string
	predicate string
	args      []any
	value     any
}

func retiredRoutingChannelBindingScrubs() []retiredRoutingColumnScrub {
	return []retiredRoutingColumnScrub{
		{column: "enabled", predicate: "enabled IS NULL OR enabled <> ?", args: []any{false}, value: false},
		{column: "egress_policy_json", predicate: "egress_policy_json IS NOT NULL", value: nil},
		{column: "enc_credentials", predicate: "enc_credentials IS NOT NULL", value: nil},
		{column: "key_version", predicate: "key_version IS NULL OR key_version <> ?", args: []any{0}, value: 0},
		{column: "new_api_user_id", predicate: "new_api_user_id IS NOT NULL", value: nil},
		{column: "account_key_hash", predicate: "account_key_hash IS NULL OR account_key_hash <> ?", args: []any{""}, value: ""},
		{column: "sync_failure_count", predicate: "sync_failure_count IS NULL OR sync_failure_count <> ?", args: []any{0}, value: 0},
		{column: "sync_backoff_until", predicate: "sync_backoff_until IS NULL OR sync_backoff_until <> ?", args: []any{0}, value: 0},
		{column: "last_sync_error", predicate: "last_sync_error IS NOT NULL", value: nil},
	}
}

func retiredRoutingUpstreamAccountScrubs() []retiredRoutingColumnScrub {
	return []retiredRoutingColumnScrub{
		{column: "masked_identity", predicate: "masked_identity IS NULL OR masked_identity <> ?", args: []any{"retired"}, value: "retired"},
		{column: "status", predicate: "status IS NULL OR status <> ?", args: []any{RoutingUpstreamAccountStatusDisabled}, value: RoutingUpstreamAccountStatusDisabled},
		{column: "balance_known", predicate: "balance_known IS NULL OR balance_known <> ?", args: []any{false}, value: false},
		{column: "balance", predicate: "balance IS NULL OR balance <> ?", args: []any{0}, value: 0},
		{column: "balance_updated_at", predicate: "balance_updated_at IS NULL OR balance_updated_at <> ?", args: []any{0}, value: 0},
		{column: "last_sync_status", predicate: "last_sync_status IS NULL OR last_sync_status <> ?", args: []any{RoutingUpstreamSyncStatusUnknown}, value: RoutingUpstreamSyncStatusUnknown},
		{column: "last_sync_error", predicate: "last_sync_error IS NULL OR last_sync_error <> ?", args: []any{""}, value: ""},
	}
}

func retiredRoutingChannelBalanceScrubs() []retiredRoutingColumnScrub {
	return []retiredRoutingColumnScrub{
		{column: "balance_known", predicate: "balance_known IS NULL OR balance_known <> ?", args: []any{false}, value: false},
		{column: "balance", predicate: "balance IS NULL OR balance <> ?", args: []any{0}, value: 0},
		{column: "balance_updated_time", predicate: "balance_updated_time IS NULL OR balance_updated_time <> ?", args: []any{0}, value: 0},
	}
}

func retiredRoutingCostSnapshotAccountScrubs() []retiredRoutingColumnScrub {
	return []retiredRoutingColumnScrub{
		{column: "account_key_hash", predicate: "account_key_hash IS NULL OR account_key_hash <> ?", args: []any{""}, value: ""},
		{column: "account_masked_id", predicate: "account_masked_id IS NULL OR account_masked_id <> ?", args: []any{""}, value: ""},
		{column: "account_status", predicate: "account_status IS NULL OR account_status <> ?", args: []any{RoutingUpstreamAccountStatusDisabled}, value: RoutingUpstreamAccountStatusDisabled},
		{column: "account_balance_known", predicate: "account_balance_known IS NULL OR account_balance_known <> ?", args: []any{false}, value: false},
		{column: "account_balance", predicate: "account_balance IS NULL OR account_balance <> ?", args: []any{0}, value: 0},
		{column: "account_balance_at", predicate: "account_balance_at IS NULL OR account_balance_at <> ?", args: []any{0}, value: 0},
		{column: "account_sync_status", predicate: "account_sync_status IS NULL OR account_sync_status <> ?", args: []any{RoutingUpstreamSyncStatusUnknown}, value: RoutingUpstreamSyncStatusUnknown},
		{column: "account_sync_error", predicate: "account_sync_error IS NULL OR account_sync_error <> ?", args: []any{""}, value: ""},
	}
}

type RoutingChannelConfigurationEvent struct {
	EventID           string `json:"event_id"`
	EventType         string `json:"event_type"`
	Action            string `json:"action"`
	ChannelID         int    `json:"channel_id"`
	Revision          int64  `json:"revision"`
	PreviousRevision  int64  `json:"previous_revision"`
	ConfigEpoch       int64  `json:"config_epoch"`
	ConfigurationHash string `json:"configuration_hash"`
	TrafficClass      string `json:"traffic_class"`
	StateHash         string `json:"state_hash"`
	UpdatedTime       int64  `json:"updated_time"`
}

// RoutingConfigurationEpoch is the single durable monotonic clock for
// mutable routing configuration. Channel-local revisions remain the CAS
// token; this epoch orders invalidation across different channels.
type RoutingConfigurationEpoch struct {
	ID          int    `json:"id" gorm:"primaryKey;autoIncrement:false"`
	Epoch       int64  `json:"epoch" gorm:"bigint;not null"`
	StateHash   string `json:"state_hash" gorm:"type:char(64);not null"`
	UpdatedTime int64  `json:"updated_time" gorm:"bigint;not null"`
}

func (RoutingConfigurationEpoch) TableName() string {
	return "routing_configuration_epochs"
}

type RoutingChannelConfigurationOutbox struct {
	ID              int64  `json:"id" gorm:"primaryKey"`
	EventID         string `json:"event_id" gorm:"type:varchar(96);uniqueIndex;not null"`
	ChannelID       int    `json:"channel_id" gorm:"index;not null"`
	Revision        int64  `json:"revision" gorm:"bigint;index;not null"`
	ConfigEpoch     int64  `json:"config_epoch" gorm:"bigint;uniqueIndex;not null"`
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

func (RoutingChannelConfigurationOutbox) TableName() string {
	return "routing_channel_configuration_outbox"
}

type RoutingChannelConfigurationMutation struct {
	Configuration RoutingChannelConfiguration       `json:"configuration"`
	Outbox        RoutingChannelConfigurationOutbox `json:"outbox"`
}

func NewDefaultRoutingChannelConfiguration(channelID int, createdTime int64) (RoutingChannelConfiguration, error) {
	if createdTime <= 0 {
		createdTime = common.GetTimestamp()
	}
	configuration := RoutingChannelConfiguration{
		ChannelID:              channelID,
		UpstreamCostMultiplier: 1,
		CostSource:             RoutingChannelCostSourceDefaulted,
		CostConfirmed:          false,
		TrafficClass:           RoutingChannelTrafficClassAll,
		FailureDomainStatus:    RoutingFailureDomainStatusUnconfigured,
		Revision:               1,
		CreatedTime:            createdTime,
		UpdatedTime:            createdTime,
	}
	if !ValidRoutingChannelConfiguration(configuration) {
		return RoutingChannelConfiguration{}, ErrRoutingChannelConfigurationInvalid
	}
	return configuration, nil
}

func NormalizeRoutingFailureDomainLabel(label string) (string, string, error) {
	if !utf8.ValidString(label) {
		return "", "", ErrRoutingChannelConfigurationInvalid
	}
	label = strings.Join(strings.Fields(norm.NFKC.String(label)), " ")
	if utf8.RuneCountInString(label) > RoutingChannelFailureDomainLabelMaxRunes || len(label) > 512 {
		return "", "", ErrRoutingChannelConfigurationInvalid
	}
	if label == "" {
		return "", "", nil
	}
	normalized := strings.ToLower(label)
	digest := sha256.Sum256([]byte("routing-failure-domain:configured:v1\x00" + normalized))
	return label, hex.EncodeToString(digest[:]), nil
}

func ValidRoutingChannelConfiguration(configuration RoutingChannelConfiguration) bool {
	if configuration.ChannelID <= 0 || configuration.Revision <= 0 || configuration.UpdatedBy < 0 ||
		configuration.CreatedTime <= 0 || configuration.UpdatedTime < configuration.CreatedTime ||
		!validRoutingChannelMultiplier(configuration.UpstreamCostMultiplier) ||
		!validRoutingChannelCostSource(configuration.CostSource) ||
		!validRoutingChannelTrafficClass(configuration.TrafficClass) ||
		!validRoutingFailureDomainStatus(configuration.FailureDomainStatus) {
		return false
	}
	label, hash, err := NormalizeRoutingFailureDomainLabel(configuration.FailureDomainLabel)
	if err != nil || label != configuration.FailureDomainLabel {
		return false
	}
	switch configuration.FailureDomainStatus {
	case RoutingFailureDomainStatusUnconfigured:
		return label == "" && configuration.FailureDomainHash == ""
	case RoutingFailureDomainStatusConfigured:
		return label != "" && hash == configuration.FailureDomainHash
	case RoutingFailureDomainStatusHistoricalMigrated:
		return label == "" && validRoutingChannelHash(configuration.FailureDomainHash)
	default:
		return false
	}
}

func RoutingChannelConfigurationStateHash(configuration RoutingChannelConfiguration) (string, error) {
	if !ValidRoutingChannelConfiguration(configuration) {
		return "", ErrRoutingChannelConfigurationInvalid
	}
	payload, err := common.Marshal(struct {
		ChannelID              int     `json:"channel_id"`
		UpstreamCostMultiplier float64 `json:"upstream_cost_multiplier"`
		CostSource             string  `json:"cost_source"`
		CostConfirmed          bool    `json:"cost_confirmed"`
		TrafficClass           string  `json:"traffic_class"`
		FailureDomainLabel     string  `json:"failure_domain_label"`
		FailureDomainHash      string  `json:"failure_domain_hash"`
		FailureDomainStatus    string  `json:"failure_domain_status"`
		Revision               int64   `json:"revision"`
		UpdatedBy              int     `json:"updated_by"`
		CreatedTime            int64   `json:"created_time"`
		UpdatedTime            int64   `json:"updated_time"`
	}{
		ChannelID: configuration.ChannelID, UpstreamCostMultiplier: configuration.UpstreamCostMultiplier,
		CostSource: configuration.CostSource, CostConfirmed: configuration.CostConfirmed,
		TrafficClass: configuration.TrafficClass, FailureDomainLabel: configuration.FailureDomainLabel,
		FailureDomainHash: configuration.FailureDomainHash, FailureDomainStatus: configuration.FailureDomainStatus,
		Revision: configuration.Revision, UpdatedBy: configuration.UpdatedBy,
		CreatedTime: configuration.CreatedTime, UpdatedTime: configuration.UpdatedTime,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func EnsureRoutingConfigurationEpoch(db *gorm.DB) error {
	if db == nil || db.Dialector == nil || !db.Migrator().HasTable(&RoutingConfigurationEpoch{}) {
		return ErrRoutingChannelConfigurationInvalid
	}
	if err := ensureRoutingConfigurationEpochTx(db); err != nil {
		return err
	}
	state, err := GetRoutingConfigurationEpochDBContext(context.Background(), db)
	if err != nil {
		return err
	}
	if state.Epoch != 0 && !validRoutingChannelHash(state.StateHash) {
		return ErrRoutingChannelConfigurationInvalid
	}
	return nil
}

func GetRoutingConfigurationEpochContext(ctx context.Context) (RoutingConfigurationEpoch, error) {
	return GetRoutingConfigurationEpochDBContext(ctx, DB)
}

func GetRoutingConfigurationEpochDBContext(ctx context.Context, db *gorm.DB) (RoutingConfigurationEpoch, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil || db.Dialector == nil {
		return RoutingConfigurationEpoch{}, ErrRoutingChannelConfigurationInvalid
	}
	var state RoutingConfigurationEpoch
	err := db.WithContext(ctx).Where("id = ?", routingConfigurationEpochSingletonID).First(&state).Error
	if err != nil {
		return RoutingConfigurationEpoch{}, err
	}
	if state.ID != routingConfigurationEpochSingletonID || state.Epoch < 0 ||
		!validRoutingChannelHash(state.StateHash) || state.UpdatedTime <= 0 {
		return RoutingConfigurationEpoch{}, ErrRoutingChannelConfigurationInvalid
	}
	return state, nil
}

func GetRoutingChannelConfigurationContext(ctx context.Context, channelID int) (RoutingChannelConfiguration, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if channelID <= 0 {
		return RoutingChannelConfiguration{}, gorm.ErrRecordNotFound
	}
	var configuration RoutingChannelConfiguration
	err := DB.WithContext(ctx).Where("channel_id = ?", channelID).First(&configuration).Error
	return configuration, err
}

func ListRoutingChannelConfigurationsContext(
	ctx context.Context,
	filter RoutingChannelConfigurationFilter,
	offset int,
	limit int,
) ([]RoutingChannelConfiguration, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if offset < 0 || limit < 1 || limit > 100 || !validRoutingChannelConfigurationFilter(filter) {
		return nil, 0, ErrRoutingChannelConfigurationInvalid
	}
	query := DB.WithContext(ctx).Model(&RoutingChannelConfiguration{}).
		Joins("JOIN channels ON channels.id = routing_channel_configurations.channel_id")
	if filter.CostConfirmed != nil {
		query = query.Where("routing_channel_configurations.cost_confirmed = ?", *filter.CostConfirmed)
	}
	if filter.CostSource != "" {
		query = query.Where("routing_channel_configurations.cost_source = ?", filter.CostSource)
	}
	if filter.TrafficClass != "" {
		query = query.Where("routing_channel_configurations.traffic_class = ?", filter.TrafficClass)
	}
	search := strings.TrimSpace(filter.Search)
	if search != "" {
		replacer := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
		pattern := "%" + replacer.Replace(search) + "%"
		if channelID, err := parsePositiveRoutingChannelID(search); err == nil {
			query = query.Where(
				"(routing_channel_configurations.channel_id = ? OR channels.name LIKE ? ESCAPE '!')",
				channelID, pattern,
			)
		} else {
			query = query.Where("channels.name LIKE ? ESCAPE '!'", pattern)
		}
	}
	var total int64
	if err := query.Distinct("routing_channel_configurations.channel_id").Count(&total).Error; err != nil {
		return nil, 0, err
	}
	configurations := make([]RoutingChannelConfiguration, 0, min(limit, int(total)))
	err := query.Select("routing_channel_configurations.*").
		Order("routing_channel_configurations.channel_id asc").Offset(offset).Limit(limit).Find(&configurations).Error
	return configurations, total, err
}

func UpdateRoutingChannelConfigurationContext(
	ctx context.Context,
	expected RoutingChannelConfiguration,
	multiplier float64,
	trafficClass string,
	failureDomainLabel string,
	clearFailureDomain bool,
	actorID int,
) (RoutingChannelConfigurationMutation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if actorID <= 0 || !ValidRoutingChannelConfiguration(expected) || !validRoutingChannelMultiplier(multiplier) ||
		!validRoutingChannelTrafficClass(trafficClass) {
		return RoutingChannelConfigurationMutation{}, ErrRoutingChannelConfigurationInvalid
	}
	label, failureHash, err := NormalizeRoutingFailureDomainLabel(failureDomainLabel)
	if err != nil {
		return RoutingChannelConfigurationMutation{}, err
	}
	if clearFailureDomain && label != "" {
		return RoutingChannelConfigurationMutation{}, ErrRoutingChannelConfigurationInvalid
	}
	var mutation RoutingChannelConfigurationMutation
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := loadRoutingChannelConfigurationForUpdate(tx, expected.ChannelID)
		if err != nil {
			return err
		}
		if !sameRoutingChannelConfiguration(current, expected) {
			return ErrRoutingChannelConfigurationChanged
		}
		if current.Revision == math.MaxInt64 {
			return ErrRoutingChannelConfigurationInvalid
		}
		now := common.GetTimestamp()
		if now <= current.UpdatedTime {
			now = current.UpdatedTime + 1
		}
		updated := current
		updated.UpstreamCostMultiplier = multiplier
		updated.CostSource = RoutingChannelCostSourceManual
		updated.CostConfirmed = true
		updated.TrafficClass = trafficClass
		switch {
		case clearFailureDomain:
			updated.FailureDomainLabel = ""
			updated.FailureDomainHash = ""
			updated.FailureDomainStatus = RoutingFailureDomainStatusUnconfigured
		case label != "":
			updated.FailureDomainLabel = label
			updated.FailureDomainHash = failureHash
			updated.FailureDomainStatus = RoutingFailureDomainStatusConfigured
		default:
			// Empty labels are common when an administrator only adjusts the
			// multiplier. Preserve configured and historical domains unless the
			// full document explicitly asks to clear them.
		}
		updated.Revision++
		updated.UpdatedBy = actorID
		updated.UpdatedTime = now
		if !ValidRoutingChannelConfiguration(updated) {
			return ErrRoutingChannelConfigurationInvalid
		}
		result := tx.Model(&RoutingChannelConfiguration{}).
			Where("channel_id = ? AND revision = ?", current.ChannelID, current.Revision).
			Updates(map[string]any{
				"upstream_cost_multiplier": updated.UpstreamCostMultiplier,
				"cost_source":              updated.CostSource,
				"cost_confirmed":           updated.CostConfirmed,
				"traffic_class":            updated.TrafficClass,
				"failure_domain_label":     updated.FailureDomainLabel,
				"failure_domain_hash":      updated.FailureDomainHash,
				"failure_domain_status":    updated.FailureDomainStatus,
				"revision":                 updated.Revision,
				"updated_by":               updated.UpdatedBy,
				"updated_time":             updated.UpdatedTime,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRoutingChannelConfigurationChanged
		}
		beforeHash, err := RoutingChannelConfigurationStateHash(current)
		if err != nil {
			return err
		}
		afterHash, err := RoutingChannelConfigurationStateHash(updated)
		if err != nil {
			return err
		}
		configEpoch, err := advanceRoutingConfigurationEpochTx(tx, now, afterHash)
		if err != nil {
			return err
		}
		summary, err := common.Marshal(struct {
			ChannelID              int     `json:"channel_id"`
			UpstreamCostMultiplier float64 `json:"upstream_cost_multiplier"`
			CostConfirmed          bool    `json:"cost_confirmed"`
			TrafficClass           string  `json:"traffic_class"`
			FailureDomainStatus    string  `json:"failure_domain_status"`
		}{
			ChannelID: updated.ChannelID, UpstreamCostMultiplier: updated.UpstreamCostMultiplier,
			CostConfirmed: updated.CostConfirmed, TrafficClass: updated.TrafficClass,
			FailureDomainStatus: updated.FailureDomainStatus,
		})
		if err != nil {
			return err
		}
		if err := insertRoutingControlAuditTx(tx, RoutingControlAudit{
			SubjectType: RoutingControlSubjectChannelConfiguration, SubjectID: int64(updated.ChannelID),
			Action: RoutingControlActionUpdate, ActorID: actorID,
			BeforeHash: beforeHash, AfterHash: afterHash, SummaryJSON: string(summary),
			CreatedTimeMs: time.Now().UnixMilli(),
		}); err != nil {
			return err
		}
		outbox, err := newRoutingChannelConfigurationOutbox(
			updated, current.Revision, configEpoch, afterHash, RoutingControlActionUpdate,
		)
		if err != nil {
			return err
		}
		if err := tx.Create(&outbox).Error; err != nil {
			return err
		}
		mutation = RoutingChannelConfigurationMutation{Configuration: updated, Outbox: outbox}
		return nil
	})
	return mutation, err
}

func MigrateRoutingChannelConfigurations(db *gorm.DB) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingChannelConfigurationInvalid
	}
	if !db.Migrator().HasTable(&RoutingChannelConfiguration{}) || !db.Migrator().HasTable(&Channel{}) {
		return ErrRoutingChannelConfigurationInvalid
	}
	lastChannelID := 0
	for {
		var channels []Channel
		query := db.Select("id", "created_time").Order("id asc").Limit(routingChannelConfigurationMigrationBatch)
		if lastChannelID > 0 {
			query = query.Where("id > ?", lastChannelID)
		}
		if err := query.Find(&channels).Error; err != nil {
			return err
		}
		if len(channels) == 0 {
			return nil
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			channelIDs := make([]int, 0, len(channels))
			for index := range channels {
				channelIDs = append(channelIDs, channels[index].Id)
			}
			bindingsByChannel := make(map[int]RoutingChannelBinding, len(channels))
			if tx.Migrator().HasTable(&RoutingChannelBinding{}) &&
				tx.Migrator().HasColumn(&RoutingChannelBinding{}, "channel_id") {
				columns := []string{"channel_id"}
				for _, column := range []string{"upstream_type", "serves_claude_code", "account_key_hash"} {
					if tx.Migrator().HasColumn(&RoutingChannelBinding{}, column) {
						columns = append(columns, column)
					}
				}
				var bindings []RoutingChannelBinding
				if err := tx.Select(columns).
					Where("channel_id IN ?", channelIDs).Find(&bindings).Error; err != nil {
					return err
				}
				for index := range bindings {
					bindingsByChannel[bindings[index].ChannelID] = bindings[index]
				}
			}
			for index := range channels {
				if err := migrateRoutingChannelConfigurationTx(tx, channels[index], bindingsByChannel[channels[index].Id]); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		lastChannelID = channels[len(channels)-1].Id
		if len(channels) < routingChannelConfigurationMigrationBatch {
			return nil
		}
	}
}

// RetireRoutingUpstreamAccountConnectorsContext irreversibly scrubs the
// management side of the legacy cost connectors after the caller has stopped
// connector workers and migrated every channel configuration. It deliberately
// preserves Channel.Key/Balance, routing credential references, immutable cost
// history, serving metrics, and breaker state.
func RetireRoutingUpstreamAccountConnectorsContext(
	ctx context.Context,
) (RoutingUpstreamConnectorRetirementResult, error) {
	return retireRoutingUpstreamAccountConnectorsDB(ctx, DB)
}

func retireRoutingUpstreamAccountConnectorsDB(
	ctx context.Context,
	db *gorm.DB,
) (RoutingUpstreamConnectorRetirementResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil || db.Dialector == nil {
		return RoutingUpstreamConnectorRetirementResult{}, ErrRoutingChannelConfigurationInvalid
	}
	if err := clearRetiredRoutingSub2APIJWTCachesContext(ctx, db); err != nil {
		// No runtime path can consume these keys after connector retirement.
		// Keep startup available, while surfacing the best-effort cleanup failure
		// for operators to remove the inert keys manually.
		common.SysError("failed to clear retired routing connector caches: " + err.Error())
	}
	result := RoutingUpstreamConnectorRetirementResult{}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var missingConfigurations int64
		if err := tx.Table("channels").
			Joins("LEFT JOIN routing_channel_configurations ON routing_channel_configurations.channel_id = channels.id").
			Where("routing_channel_configurations.channel_id IS NULL").Count(&missingConfigurations).Error; err != nil {
			return err
		}
		if missingConfigurations != 0 {
			return ErrRoutingChannelConfigurationInvalid
		}
		lastChannelID := 0
		for {
			var configurations []RoutingChannelConfiguration
			if err := tx.Where("channel_id > ?", lastChannelID).Order("channel_id asc").
				Limit(routingChannelConfigurationMigrationBatch).Find(&configurations).Error; err != nil {
				return err
			}
			if len(configurations) == 0 {
				break
			}
			for _, configuration := range configurations {
				if !ValidRoutingChannelConfiguration(configuration) {
					return ErrRoutingChannelConfigurationInvalid
				}
			}
			lastChannelID = configurations[len(configurations)-1].ChannelID
		}

		now := common.GetTimestamp()
		if tx.Migrator().HasTable(&RoutingChannelBinding{}) {
			rows, err := scrubRetiredRoutingTableColumns(
				tx, &RoutingChannelBinding{}, RoutingChannelBinding{}.TableName(), now,
				retiredRoutingChannelBindingScrubs(),
			)
			if err != nil {
				return err
			}
			result.BindingsScrubbed = rows
		}
		if tx.Migrator().HasTable(&RoutingUpstreamAccount{}) {
			rows, err := scrubRetiredRoutingTableColumns(
				tx, &RoutingUpstreamAccount{}, RoutingUpstreamAccount{}.TableName(), now,
				retiredRoutingUpstreamAccountScrubs(),
			)
			if err != nil {
				return err
			}
			result.AccountsScrubbed = rows
		}
		if tx.Migrator().HasTable(retiredRoutingUpstreamAccountHealthTable) {
			deleted := tx.Exec("DELETE FROM " + retiredRoutingUpstreamAccountHealthTable)
			if deleted.Error != nil {
				return deleted.Error
			}
			result.AccountHealthRowsCleared = deleted.RowsAffected
		}
		if tx.Migrator().HasTable(&RoutingChannelHealthState{}) {
			rows, err := scrubRetiredRoutingTableColumns(
				tx, &RoutingChannelHealthState{}, RoutingChannelHealthState{}.TableName(), now,
				retiredRoutingChannelBalanceScrubs(),
			)
			if err != nil {
				return err
			}
			result.ChannelBalanceRowsCleared = rows
		}
		if tx.Migrator().HasTable(&RoutingCostSnapshot{}) {
			if _, err := scrubRetiredRoutingTableColumns(
				tx, &RoutingCostSnapshot{}, RoutingCostSnapshot{}.TableName(), 0,
				retiredRoutingCostSnapshotAccountScrubs(),
			); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

func clearRetiredRoutingSub2APIJWTCachesContext(ctx context.Context, db *gorm.DB) error {
	if !common.RedisEnabled {
		return nil
	}
	if common.RDB == nil {
		// The connector runtime has already been removed, so stale cache entries
		// have no reader. Clear them when a Redis client is available without
		// making database initialization depend on optional cache wiring.
		return nil
	}
	if !db.Migrator().HasTable(&RoutingChannelBinding{}) ||
		!db.Migrator().HasColumn(&RoutingChannelBinding{}, "channel_id") ||
		!db.Migrator().HasColumn(&RoutingChannelBinding{}, "upstream_type") {
		return nil
	}
	var channelIDs []int
	if err := db.Model(&RoutingChannelBinding{}).
		Where("upstream_type = ?", RoutingUpstreamTypeSub2API).
		Distinct("channel_id").Order("channel_id asc").Pluck("channel_id", &channelIDs).Error; err != nil {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, channelID := range channelIDs {
		keys := make([]string, 0, 16)
		for _, namespace := range []string{"jwt", "retired", "lock"} {
			prefix := fmt.Sprintf("routing:sub2api:%s:%d", namespace, channelID)
			keys = append(keys, prefix)
			var cursor uint64
			for {
				matched, nextCursor, err := common.RDB.Scan(cleanupCtx, cursor, prefix+":*", 200).Result()
				if err != nil {
					return fmt.Errorf("scan retired routing %s cache for channel %d: %w", namespace, channelID, err)
				}
				keys = append(keys, matched...)
				cursor = nextCursor
				if cursor == 0 {
					break
				}
			}
		}
		if len(keys) > 0 {
			if err := common.RDB.Del(cleanupCtx, keys...).Err(); err != nil {
				return fmt.Errorf("delete retired routing JWT cache for channel %d: %w", channelID, err)
			}
		}
	}
	return nil
}

func ClaimRoutingChannelConfigurationOutboxContext(
	ctx context.Context,
	outboxID int64,
	now int64,
	leaseSeconds int64,
) (*RoutingChannelConfigurationOutbox, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if outboxID < 0 || now <= 0 || leaseSeconds <= 0 || leaseSeconds > 300 || now > math.MaxInt64-leaseSeconds {
		return nil, ErrRoutingChannelConfigurationInvalid
	}
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, err
	}
	claimToken := hex.EncodeToString(tokenBytes[:])
	var claimed RoutingChannelConfigurationOutbox
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := lockForUpdate(tx).Where(
			"published_time = ? AND next_attempt_time <= ? AND claimed_until <= ?", 0, now, now,
		)
		if outboxID > 0 {
			query = query.Where("id = ?", outboxID)
		}
		if err := query.Order("id asc").First(&claimed).Error; err != nil {
			return err
		}
		if claimed.Attempts == math.MaxInt {
			return ErrRoutingChannelConfigurationInvalid
		}
		result := tx.Model(&RoutingChannelConfigurationOutbox{}).
			Where("id = ? AND published_time = ? AND claimed_until <= ?", claimed.ID, 0, now).
			Updates(map[string]any{
				"claim_token": claimToken, "claimed_until": now + leaseSeconds, "attempts": claimed.Attempts + 1,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRoutingChannelConfigurationOutboxClaimLost
		}
		return tx.Where("id = ? AND claim_token = ?", claimed.ID, claimToken).First(&claimed).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := claimed.DecodePayload(); err != nil {
		return nil, err
	}
	return &claimed, nil
}

func (outbox RoutingChannelConfigurationOutbox) DecodePayload() (RoutingChannelConfigurationEvent, error) {
	if !validRoutingChannelHash(outbox.PayloadHash) || routingChannelConfigurationHash([]byte(outbox.PayloadJSON)) != outbox.PayloadHash {
		return RoutingChannelConfigurationEvent{}, ErrRoutingChannelConfigurationInvalid
	}
	var event RoutingChannelConfigurationEvent
	if err := common.UnmarshalJsonStr(outbox.PayloadJSON, &event); err != nil || !validRoutingChannelConfigurationEvent(event) ||
		event.EventID != outbox.EventID || event.EventType != outbox.EventType || event.ChannelID != outbox.ChannelID ||
		event.Revision != outbox.Revision || event.ConfigEpoch != outbox.ConfigEpoch {
		return RoutingChannelConfigurationEvent{}, ErrRoutingChannelConfigurationInvalid
	}
	return event, nil
}

func MarkRoutingChannelConfigurationOutboxPublishedContext(
	ctx context.Context,
	id int64,
	claimToken string,
	publishedTime int64,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 || len(claimToken) != 32 || publishedTime <= 0 {
		return ErrRoutingChannelConfigurationInvalid
	}
	result := DB.WithContext(ctx).Model(&RoutingChannelConfigurationOutbox{}).
		Where("id = ? AND claim_token = ? AND published_time = ?", id, claimToken, 0).
		Updates(map[string]any{
			"published_time": publishedTime, "claim_token": "", "claimed_until": 0,
			"next_attempt_time": 0, "last_error": "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRoutingChannelConfigurationOutboxClaimLost
	}
	return nil
}

func ReleaseRoutingChannelConfigurationOutboxClaimContext(
	ctx context.Context,
	id int64,
	claimToken string,
	nextAttemptTime int64,
	publishErr error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 || len(claimToken) != 32 || nextAttemptTime <= 0 || publishErr == nil {
		return ErrRoutingChannelConfigurationInvalid
	}
	message := common.SanitizeErrorMessage(publishErr.Error())
	messageRunes := []rune(message)
	if len(messageRunes) > routingChannelConfigurationOutboxErrorMaxRunes {
		message = string(messageRunes[:routingChannelConfigurationOutboxErrorMaxRunes])
	}
	result := DB.WithContext(ctx).Model(&RoutingChannelConfigurationOutbox{}).
		Where("id = ? AND claim_token = ? AND published_time = ?", id, claimToken, 0).
		Updates(map[string]any{
			"claim_token": "", "claimed_until": 0, "next_attempt_time": nextAttemptTime, "last_error": message,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRoutingChannelConfigurationOutboxClaimLost
	}
	return nil
}

func createDefaultRoutingChannelConfigurationsTx(tx *gorm.DB, channels []Channel) error {
	if tx == nil || len(channels) == 0 || !tx.Migrator().HasTable(&RoutingChannelConfiguration{}) {
		return nil
	}
	for index := range channels {
		configuration, err := NewDefaultRoutingChannelConfiguration(channels[index].Id, channels[index].CreatedTime)
		if err != nil {
			return err
		}
		created := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "channel_id"}}, DoNothing: true,
		}).Create(&configuration)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 1 && tx.Migrator().HasTable(&RoutingControlAudit{}) {
			afterHash, err := RoutingChannelConfigurationStateHash(configuration)
			if err != nil {
				return err
			}
			if err := insertRoutingControlAuditTx(tx, RoutingControlAudit{
				SubjectType: RoutingControlSubjectChannelConfiguration, SubjectID: int64(configuration.ChannelID),
				Action: RoutingControlActionBootstrap, AfterHash: afterHash,
				SummaryJSON: `{"source":"channel_creation"}`, CreatedTimeMs: time.Now().UnixMilli(),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func createDefaultRoutingChannelConfigurationTx(tx *gorm.DB, channelID int, createdTime int64) error {
	return createDefaultRoutingChannelConfigurationsTx(tx, []Channel{{Id: channelID, CreatedTime: createdTime}})
}

func migrateRoutingChannelConfigurationTx(tx *gorm.DB, channel Channel, binding RoutingChannelBinding) error {
	current, err := loadRoutingChannelConfigurationForUpdate(tx, channel.Id)
	if err == nil {
		if !ValidRoutingChannelConfiguration(current) {
			return ErrRoutingChannelConfigurationInvalid
		}
		// Any existing valid document is already a completed migration or an
		// explicitly initialized new-channel default. Recomputing it after the
		// legacy connector has been scrubbed would erase historical failure-domain
		// independence, so restarts must leave it byte-for-byte unchanged.
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	now := common.GetTimestamp()
	createdTime := channel.CreatedTime
	if createdTime <= 0 {
		createdTime = now
	}
	candidate, err := NewDefaultRoutingChannelConfiguration(channel.Id, createdTime)
	if err != nil {
		return err
	}
	if multiplier, migrated := migratedRoutingChannelCostMultiplier(tx, channel.Id); migrated {
		candidate.UpstreamCostMultiplier = multiplier
		candidate.CostSource = RoutingChannelCostSourceLegacyMigrated
	}
	if binding.ChannelID == channel.Id && binding.ServesClaudeCode {
		candidate.TrafficClass = RoutingChannelTrafficClassClaudeCodeOnly
	}
	if failureHash := migratedRoutingFailureDomainHash(tx, binding); failureHash != "" {
		candidate.FailureDomainHash = failureHash
		candidate.FailureDomainStatus = RoutingFailureDomainStatusHistoricalMigrated
	}
	if !ValidRoutingChannelConfiguration(candidate) {
		return ErrRoutingChannelConfigurationInvalid
	}
	if err := tx.Create(&candidate).Error; err != nil {
		return err
	}
	afterHash, err := RoutingChannelConfigurationStateHash(candidate)
	if err != nil {
		return err
	}
	return insertRoutingControlAuditTx(tx, RoutingControlAudit{
		SubjectType: RoutingControlSubjectChannelConfiguration, SubjectID: int64(candidate.ChannelID),
		Action: RoutingControlActionBootstrap, AfterHash: afterHash,
		SummaryJSON: `{"source":"legacy_migration"}`, CreatedTimeMs: time.Now().UnixMilli(),
	})
}

func migratedRoutingChannelCostMultiplier(tx *gorm.DB, channelID int) (float64, bool) {
	if tx == nil || channelID <= 0 || !tx.Migrator().HasTable(&RoutingCostSnapshotVersion{}) ||
		!tx.Migrator().HasTable(&RoutingUpstreamAccount{}) {
		return 0, false
	}
	var versions []RoutingCostSnapshotVersion
	if err := tx.Where("channel_id = ?", channelID).Order("observed_time desc").Order("id desc").
		Limit(routingChannelConfigurationMigrationVersionLimit).Find(&versions).Error; err != nil || len(versions) == 0 {
		return 0, false
	}
	truncated := len(versions) == routingChannelConfigurationMigrationVersionLimit
	if truncated {
		versions = versions[:len(versions)-1]
	}
	accounts := make(map[int]RoutingUpstreamAccount)
	for start := 0; start < len(versions); {
		end := start + 1
		for end < len(versions) && versions[end].ObservedTime == versions[start].ObservedTime {
			end++
		}
		if truncated && end == len(versions) {
			break
		}
		if multiplier, ok := validMigratedRoutingCostVersionGroup(tx, versions[start:end], accounts); ok {
			return multiplier, true
		}
		start = end
	}
	return 0, false
}

func validMigratedRoutingCostVersionGroup(
	tx *gorm.DB,
	versions []RoutingCostSnapshotVersion,
	accounts map[int]RoutingUpstreamAccount,
) (float64, bool) {
	if len(versions) == 0 {
		return 0, false
	}
	first := versions[0]
	models := make(map[string]struct{}, len(versions))
	var multiplier float64
	for index := range versions {
		version := versions[index]
		if version.SchemaVersion != routingCostSnapshotVersionSchema || version.ChannelID != first.ChannelID ||
			version.ObservedTime != first.ObservedTime || version.AccountID != first.AccountID ||
			version.AccountKey != first.AccountKey || version.SourceType != first.SourceType ||
			version.UpstreamGroup != first.UpstreamGroup || version.PricingVersion != first.PricingVersion ||
			version.SourceSyncStatus != RoutingUpstreamSyncStatusSuccess || strings.TrimSpace(version.SourceSyncError) != "" ||
			version.Confidence == RoutingCostConfidenceUnknown || version.Freshness == RoutingCostFreshnessUnknown ||
			!validRoutingChannelHash(version.PricingHash) || version.ChannelID <= 0 || version.AccountID <= 0 ||
			version.ObservedTime <= 0 || version.EffectiveTime <= 0 || version.ExpiresTime <= version.EffectiveTime ||
			version.UpstreamGroupKey != routingCostHash([]byte(version.UpstreamGroup)) ||
			version.UpstreamModelKey != RoutingCostModelKey(version.UpstreamModel) ||
			version.LocalModelKey != RoutingCostModelKey(version.LocalModel) {
			return 0, false
		}
		if _, duplicate := models[version.LocalModelKey]; duplicate {
			return 0, false
		}
		models[version.LocalModelKey] = struct{}{}
		account, exists := accounts[version.AccountID]
		if !exists {
			if err := tx.Where("id = ?", version.AccountID).First(&account).Error; err != nil {
				return 0, false
			}
			accounts[version.AccountID] = account
		}
		if account.AccountKey != version.AccountKey || account.SourceType != version.SourceType {
			return 0, false
		}
		var pricing RoutingNormalizedPricing
		if err := common.UnmarshalJsonStr(version.PricingJSON, &pricing); err != nil {
			return 0, false
		}
		normalized, pricingJSON, err := normalizeRoutingNormalizedPricing(pricing)
		if err != nil || normalized.GroupRatio == nil || !validRoutingChannelMultiplier(*normalized.GroupRatio) {
			return 0, false
		}
		manifest := routingCostSnapshotManifest{
			AccountID: version.AccountID, ChannelID: version.ChannelID,
			UpstreamGroup: version.UpstreamGroup, UpstreamModel: version.UpstreamModel, LocalModel: version.LocalModel,
			ObservedTime: version.ObservedTime, EffectiveTime: version.EffectiveTime, ExpiresTime: version.ExpiresTime,
			PricingVersion: version.PricingVersion, Pricing: normalized,
			Confidence: version.Confidence, ConfidenceScore: version.ConfidenceScore,
			Freshness: version.Freshness, FreshnessScore: version.FreshnessScore,
			SourceSyncStatus: version.SourceSyncStatus, SourceSyncError: version.SourceSyncError,
		}
		pricingHash, err := routingCostPricingHash(account, manifest, pricingJSON)
		if err != nil || pricingHash != version.PricingHash {
			return 0, false
		}
		contentHash, err := routingCostContentHash(account, manifest, pricingJSON)
		if err != nil || version.ContentHash != "" && contentHash != version.ContentHash {
			return 0, false
		}
		ratio := *normalized.GroupRatio
		if ratio == 0 {
			ratio = 0
		}
		if index == 0 {
			multiplier = ratio
		} else if math.Float64bits(multiplier) != math.Float64bits(ratio) {
			return 0, false
		}
	}
	return multiplier, true
}

func migratedRoutingFailureDomainHash(tx *gorm.DB, binding RoutingChannelBinding) string {
	if tx == nil || binding.ChannelID <= 0 || !validRoutingChannelHash(binding.AccountKeyHash) ||
		!tx.Migrator().HasTable(&RoutingUpstreamAccount{}) ||
		!tx.Migrator().HasColumn(&RoutingUpstreamAccount{}, "account_key") ||
		!tx.Migrator().HasColumn(&RoutingUpstreamAccount{}, "source_type") {
		return ""
	}
	var account RoutingUpstreamAccount
	if err := tx.Select("account_key", "source_type").Where("account_key = ?", binding.AccountKeyHash).First(&account).Error; err != nil ||
		account.SourceType != binding.UpstreamType {
		return ""
	}
	digest := sha256.Sum256([]byte("routing-failure-domain:legacy-account:v1\x00" + account.AccountKey))
	return hex.EncodeToString(digest[:])
}

func scrubRetiredRoutingTableColumns(
	tx *gorm.DB,
	tableModel any,
	tableName string,
	updatedTime int64,
	columns []retiredRoutingColumnScrub,
) (int64, error) {
	if tx == nil || tx.Dialector == nil || tableModel == nil || tableName == "" {
		return 0, ErrRoutingChannelConfigurationInvalid
	}
	predicates := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	updates := make(map[string]any, len(columns)+1)
	for _, column := range columns {
		if column.column == "" || column.predicate == "" || !tx.Migrator().HasColumn(tableModel, column.column) {
			continue
		}
		predicates = append(predicates, "("+column.predicate+")")
		args = append(args, column.args...)
		updates[column.column] = column.value
	}
	if len(updates) == 0 {
		return 0, nil
	}
	if updatedTime > 0 && tx.Migrator().HasColumn(tableModel, "updated_time") {
		updates["updated_time"] = updatedTime
	}
	update := tx.Table(tableName).Where(strings.Join(predicates, " OR "), args...).Updates(updates)
	return update.RowsAffected, update.Error
}

func newRoutingChannelConfigurationOutbox(
	configuration RoutingChannelConfiguration,
	previousRevision int64,
	configEpoch RoutingConfigurationEpoch,
	stateHash string,
	action string,
) (RoutingChannelConfigurationOutbox, error) {
	if !ValidRoutingChannelConfiguration(configuration) || previousRevision <= 0 || previousRevision >= configuration.Revision ||
		configEpoch.Epoch <= 0 || !validRoutingChannelHash(configEpoch.StateHash) ||
		!validRoutingChannelHash(stateHash) || action != RoutingControlActionUpdate {
		return RoutingChannelConfigurationOutbox{}, ErrRoutingChannelConfigurationInvalid
	}
	eventID := fmt.Sprintf("routing-channel-config:%d:%020d", configuration.ChannelID, configuration.Revision)
	event := RoutingChannelConfigurationEvent{
		EventID: eventID, EventType: RoutingChannelConfigurationEventType, Action: action,
		ChannelID: configuration.ChannelID, Revision: configuration.Revision, PreviousRevision: previousRevision,
		ConfigEpoch: configEpoch.Epoch, ConfigurationHash: configEpoch.StateHash,
		TrafficClass: configuration.TrafficClass,
		StateHash:    stateHash, UpdatedTime: configuration.UpdatedTime,
	}
	payload, err := common.Marshal(event)
	if err != nil || len(payload) == 0 || len(payload) > routingChannelConfigurationOutboxPayloadMaxBytes {
		return RoutingChannelConfigurationOutbox{}, ErrRoutingChannelConfigurationInvalid
	}
	return RoutingChannelConfigurationOutbox{
		EventID: eventID, ChannelID: configuration.ChannelID, Revision: configuration.Revision,
		ConfigEpoch: configEpoch.Epoch,
		EventType:   RoutingChannelConfigurationEventType, PayloadJSON: string(payload),
		PayloadHash: routingChannelConfigurationHash(payload), CreatedTime: common.GetTimestamp(),
	}, nil
}

func validRoutingChannelConfigurationEvent(event RoutingChannelConfigurationEvent) bool {
	return event.EventID != "" && len(event.EventID) <= 96 && event.EventType == RoutingChannelConfigurationEventType &&
		event.Action == RoutingControlActionUpdate && event.ChannelID > 0 && event.Revision > 1 &&
		event.PreviousRevision > 0 && event.PreviousRevision < event.Revision &&
		event.ConfigEpoch > 0 && validRoutingChannelHash(event.ConfigurationHash) &&
		validRoutingChannelTrafficClass(event.TrafficClass) &&
		validRoutingChannelHash(event.StateHash) && event.UpdatedTime > 0
}

func DecodeRoutingChannelConfigurationEvent(data []byte) (RoutingChannelConfigurationEvent, error) {
	if len(data) == 0 || len(data) > routingChannelConfigurationOutboxPayloadMaxBytes {
		return RoutingChannelConfigurationEvent{}, ErrRoutingChannelConfigurationInvalid
	}
	var event RoutingChannelConfigurationEvent
	if err := common.Unmarshal(data, &event); err != nil || !validRoutingChannelConfigurationEvent(event) {
		return RoutingChannelConfigurationEvent{}, ErrRoutingChannelConfigurationInvalid
	}
	return event, nil
}

func ensureRoutingConfigurationEpochTx(tx *gorm.DB) error {
	if tx == nil || tx.Dialector == nil {
		return ErrRoutingChannelConfigurationInvalid
	}
	now := common.GetTimestamp()
	if now <= 0 {
		return ErrRoutingChannelConfigurationInvalid
	}
	initialHash := routingChannelConfigurationHash([]byte("routing-configuration-epoch:v1"))
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoNothing: true,
	}).Create(&RoutingConfigurationEpoch{
		ID: routingConfigurationEpochSingletonID, Epoch: 0, StateHash: initialHash, UpdatedTime: now,
	}).Error; err != nil {
		return err
	}
	return tx.Model(&RoutingConfigurationEpoch{}).
		Where("id = ? AND epoch = ? AND (state_hash IS NULL OR state_hash = ?)", routingConfigurationEpochSingletonID, 0, "").
		Update("state_hash", initialHash).Error
}

func advanceRoutingConfigurationEpochTx(
	tx *gorm.DB,
	now int64,
	channelStateHash string,
) (RoutingConfigurationEpoch, error) {
	if tx == nil || tx.Dialector == nil || now <= 0 || !validRoutingChannelHash(channelStateHash) {
		return RoutingConfigurationEpoch{}, ErrRoutingChannelConfigurationInvalid
	}
	if err := ensureRoutingConfigurationEpochTx(tx); err != nil {
		return RoutingConfigurationEpoch{}, err
	}
	query := tx
	if tx.Dialector.Name() != string(common.DatabaseTypeSQLite) {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var current RoutingConfigurationEpoch
	if err := query.Where("id = ?", routingConfigurationEpochSingletonID).First(&current).Error; err != nil {
		return RoutingConfigurationEpoch{}, err
	}
	if current.ID != routingConfigurationEpochSingletonID || current.Epoch < 0 || current.Epoch == math.MaxInt64 ||
		!validRoutingChannelHash(current.StateHash) || current.UpdatedTime <= 0 || current.UpdatedTime == math.MaxInt64 {
		return RoutingConfigurationEpoch{}, ErrRoutingChannelConfigurationInvalid
	}
	if now <= current.UpdatedTime {
		now = current.UpdatedTime + 1
	}
	next := current.Epoch + 1
	nextHash := routingChannelConfigurationHash([]byte(fmt.Sprintf(
		"routing-configuration-epoch:v1\x00%d\x00%s\x00%s", next, current.StateHash, channelStateHash,
	)))
	result := tx.Model(&RoutingConfigurationEpoch{}).
		Where("id = ? AND epoch = ? AND state_hash = ?", current.ID, current.Epoch, current.StateHash).
		Updates(map[string]any{"epoch": next, "state_hash": nextHash, "updated_time": now})
	if result.Error != nil {
		return RoutingConfigurationEpoch{}, result.Error
	}
	if result.RowsAffected != 1 {
		return RoutingConfigurationEpoch{}, ErrRoutingChannelConfigurationChanged
	}
	return RoutingConfigurationEpoch{
		ID: current.ID, Epoch: next, StateHash: nextHash, UpdatedTime: now,
	}, nil
}

func loadRoutingChannelConfigurationForUpdate(tx *gorm.DB, channelID int) (RoutingChannelConfiguration, error) {
	query := tx
	if tx.Dialector.Name() != string(common.DatabaseTypeSQLite) {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var configuration RoutingChannelConfiguration
	err := query.Where("channel_id = ?", channelID).First(&configuration).Error
	return configuration, err
}

func sameRoutingChannelConfiguration(left RoutingChannelConfiguration, right RoutingChannelConfiguration) bool {
	return left.ChannelID == right.ChannelID &&
		math.Float64bits(left.UpstreamCostMultiplier) == math.Float64bits(right.UpstreamCostMultiplier) &&
		left.CostSource == right.CostSource && left.CostConfirmed == right.CostConfirmed &&
		left.TrafficClass == right.TrafficClass && left.FailureDomainLabel == right.FailureDomainLabel &&
		left.FailureDomainHash == right.FailureDomainHash && left.FailureDomainStatus == right.FailureDomainStatus &&
		left.Revision == right.Revision && left.UpdatedBy == right.UpdatedBy &&
		left.CreatedTime == right.CreatedTime && left.UpdatedTime == right.UpdatedTime
}

func validRoutingChannelConfigurationFilter(filter RoutingChannelConfigurationFilter) bool {
	search := strings.TrimSpace(filter.Search)
	return utf8.ValidString(search) && utf8.RuneCountInString(search) <= 256 &&
		(filter.CostSource == "" || validRoutingChannelCostSource(filter.CostSource)) &&
		(filter.TrafficClass == "" || validRoutingChannelTrafficClass(filter.TrafficClass))
}

func validRoutingChannelMultiplier(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= RoutingChannelUpstreamCostMultiplierMaximum
}

func validRoutingChannelCostSource(value string) bool {
	switch value {
	case RoutingChannelCostSourceManual, RoutingChannelCostSourceLegacyMigrated, RoutingChannelCostSourceDefaulted:
		return true
	default:
		return false
	}
}

func validRoutingChannelTrafficClass(value string) bool {
	return value == RoutingChannelTrafficClassAll || value == RoutingChannelTrafficClassClaudeCodeOnly
}

func validRoutingFailureDomainStatus(value string) bool {
	switch value {
	case RoutingFailureDomainStatusConfigured, RoutingFailureDomainStatusHistoricalMigrated, RoutingFailureDomainStatusUnconfigured:
		return true
	default:
		return false
	}
}

func validRoutingChannelHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for index := range value {
		char := value[index]
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func routingChannelConfigurationHash(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func parsePositiveRoutingChannelID(value string) (int, error) {
	var channelID int
	if _, err := fmt.Sscanf(value, "%d", &channelID); err != nil || channelID <= 0 || fmt.Sprintf("%d", channelID) != value {
		return 0, ErrRoutingChannelConfigurationInvalid
	}
	return channelID, nil
}

// Stable order is useful to callers that batch-load configuration maps.
func SortRoutingChannelConfigurations(configurations []RoutingChannelConfiguration) {
	sort.Slice(configurations, func(i, j int) bool {
		return configurations[i].ChannelID < configurations[j].ChannelID
	})
}
