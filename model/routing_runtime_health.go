package model

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	routingRuntimeHealthReasonMaxBytes = 256
	routingRuntimeHealthWriteBatch     = 50
)

var (
	ErrRoutingRuntimeHealthInvalid       = errors.New("invalid channel routing runtime health state")
	ErrRoutingRuntimeHealthLimitExceeded = errors.New("channel routing runtime health limit exceeded")
)

// RoutingCredentialHealthState stores stable Credential-scoped serving state.
// The primary key is the immutable routing credential ID, never a mutable key
// array index. Auth and capacity revisions are independent so concurrent nodes
// cannot overwrite one serving dimension with an older value from the other.
type RoutingCredentialHealthState struct {
	CredentialID            int    `json:"credential_id" gorm:"primaryKey"`
	ChannelID               int    `json:"channel_id" gorm:"index;not null"`
	AuthFailure             bool   `json:"auth_failure"`
	AuthFailureReason       string `json:"auth_failure_reason" gorm:"type:varchar(256)"`
	AuthFailureUntilMs      int64  `json:"auth_failure_until_ms" gorm:"bigint;index"`
	AuthVersion             int64  `json:"auth_version" gorm:"bigint"`
	AuthUpdatedTimeMs       int64  `json:"auth_updated_time_ms" gorm:"bigint"`
	CapacityLimited         bool   `json:"capacity_limited"`
	CapacityStatusCode      int    `json:"capacity_status_code"`
	CapacityCooldownUntilMs int64  `json:"capacity_cooldown_until_ms" gorm:"bigint;index"`
	CapacityVersion         int64  `json:"capacity_version" gorm:"bigint"`
	CapacityUpdatedTimeMs   int64  `json:"capacity_updated_time_ms" gorm:"bigint"`
	UpdatedTimeMs           int64  `json:"updated_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingCredentialHealthState) TableName() string {
	return "routing_credential_health_states"
}

func UpsertRoutingCredentialHealthStatesContext(ctx context.Context, states []RoutingCredentialHealthState) error {
	if len(states) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if DB == nil {
		return ErrRoutingRuntimeHealthInvalid
	}
	return upsertRoutingCredentialHealthStatesContext(ctx, DB.WithContext(ctx), states)
}

func upsertRoutingCredentialHealthStatesContext(ctx context.Context, db *gorm.DB, states []RoutingCredentialHealthState) error {
	if len(states) == 0 {
		return nil
	}

	merged := make(map[int]RoutingCredentialHealthState, len(states))
	for index := range states {
		state := states[index]
		if err := normalizeRoutingCredentialHealthState(&state); err != nil {
			return err
		}
		if current, ok := merged[state.CredentialID]; ok {
			if current.ChannelID != state.ChannelID {
				return ErrRoutingRuntimeHealthInvalid
			}
			state = mergeRoutingCredentialHealthState(current, state)
		}
		merged[state.CredentialID] = state
	}

	credentialIDs := make([]int, 0, len(merged))
	for credentialID := range merged {
		credentialIDs = append(credentialIDs, credentialID)
	}
	sort.Ints(credentialIDs)

	activeCredentials := make(map[int]int, len(credentialIDs))
	for start := 0; start < len(credentialIDs); start += routingRuntimeHealthWriteBatch {
		end := min(start+routingRuntimeHealthWriteBatch, len(credentialIDs))
		var refs []RoutingCredentialRef
		if err := db.Select("id", "channel_id").
			Where("id IN ? AND active = ?", credentialIDs[start:end], true).
			Find(&refs).Error; err != nil {
			return err
		}
		for index := range refs {
			activeCredentials[refs[index].ID] = refs[index].ChannelID
		}
	}

	valid := make([]RoutingCredentialHealthState, 0, len(activeCredentials))
	for _, credentialID := range credentialIDs {
		state := merged[credentialID]
		if channelID, ok := activeCredentials[credentialID]; ok && channelID == state.ChannelID {
			valid = append(valid, state)
		}
	}
	if len(valid) == 0 {
		return ctx.Err()
	}
	if err := db.Clauses(routingCredentialHealthOnConflict(db)).
		CreateInBatches(&valid, routingRuntimeHealthWriteBatch).Error; err != nil {
		return err
	}
	return ctx.Err()
}

// PruneRoutingCredentialHealthStatesContext removes credential health whose
// stable serving identity no longer exists. Retired credentials are gone.
func PruneRoutingCredentialHealthStatesContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if DB == nil {
		return ErrRoutingRuntimeHealthInvalid
	}
	return pruneRoutingCredentialHealthStatesContext(ctx, DB.WithContext(ctx))
}

func pruneRoutingCredentialHealthStatesContext(ctx context.Context, db *gorm.DB) error {
	if err := db.Exec(`DELETE FROM routing_credential_health_states
WHERE NOT EXISTS (
	SELECT 1 FROM routing_credential_refs
	WHERE routing_credential_refs.id = routing_credential_health_states.credential_id
		AND routing_credential_refs.channel_id = routing_credential_health_states.channel_id
		AND routing_credential_refs.active = ?
)`, true).Error; err != nil {
		return err
	}
	return ctx.Err()
}

func ListRoutingCredentialHealthStatesContext(ctx context.Context, limit int) ([]RoutingCredentialHealthState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if DB == nil || limit < 1 || limit > routingTopologyMaxCredentials {
		return nil, ErrRoutingRuntimeHealthInvalid
	}
	var states []RoutingCredentialHealthState
	err := DB.WithContext(ctx).
		Where(`EXISTS (
			SELECT 1 FROM routing_credential_refs
			WHERE routing_credential_refs.id = routing_credential_health_states.credential_id
				AND routing_credential_refs.channel_id = routing_credential_health_states.channel_id
				AND routing_credential_refs.active = ?
		)`, true).
		Order("updated_time_ms desc").Order("credential_id asc").
		Limit(limit + 1).Find(&states).Error
	if err != nil {
		return nil, err
	}
	if len(states) > limit {
		return nil, ErrRoutingRuntimeHealthLimitExceeded
	}
	for index := range states {
		if err := normalizeRoutingCredentialHealthState(&states[index]); err != nil {
			return nil, err
		}
	}
	return states, nil
}

func normalizeRoutingCredentialHealthState(state *RoutingCredentialHealthState) error {
	if state == nil || state.CredentialID <= 0 || state.ChannelID <= 0 ||
		state.AuthVersion < 0 || state.AuthUpdatedTimeMs < 0 ||
		state.CapacityVersion < 0 || state.CapacityUpdatedTimeMs < 0 ||
		state.AuthFailureUntilMs < 0 || state.CapacityCooldownUntilMs < 0 ||
		state.CapacityStatusCode < 0 || state.CapacityStatusCode > 599 {
		return ErrRoutingRuntimeHealthInvalid
	}
	if state.AuthVersion == 0 && state.CapacityVersion == 0 {
		if state.UpdatedTimeMs <= 0 {
			return ErrRoutingRuntimeHealthInvalid
		}
		legacyVersion := routingRuntimeHealthLegacyVersion(state.UpdatedTimeMs)
		state.AuthVersion = legacyVersion
		state.AuthUpdatedTimeMs = state.UpdatedTimeMs
		state.CapacityVersion = legacyVersion
		state.CapacityUpdatedTimeMs = state.UpdatedTimeMs
	} else {
		if state.AuthVersion > 0 && state.AuthUpdatedTimeMs == 0 {
			state.AuthUpdatedTimeMs = state.UpdatedTimeMs
		}
		if state.CapacityVersion > 0 && state.CapacityUpdatedTimeMs == 0 {
			state.CapacityUpdatedTimeMs = state.UpdatedTimeMs
		}
		if (state.AuthVersion > 0 && state.AuthUpdatedTimeMs <= 0) ||
			(state.CapacityVersion > 0 && state.CapacityUpdatedTimeMs <= 0) {
			return ErrRoutingRuntimeHealthInvalid
		}
	}
	if (state.AuthFailure && state.AuthVersion == 0) ||
		(state.CapacityLimited && (state.CapacityVersion == 0 || state.CapacityStatusCode == 0 ||
			state.CapacityCooldownUntilMs == 0)) {
		return ErrRoutingRuntimeHealthInvalid
	}

	state.AuthFailureReason = boundedRoutingRuntimeHealthReason(state.AuthFailureReason)
	if !state.AuthFailure {
		state.AuthFailureReason = ""
		state.AuthFailureUntilMs = 0
	}
	if !state.CapacityLimited {
		state.CapacityStatusCode = 0
		state.CapacityCooldownUntilMs = 0
	}
	state.UpdatedTimeMs = max(state.AuthUpdatedTimeMs, state.CapacityUpdatedTimeMs)
	if state.UpdatedTimeMs <= 0 {
		return ErrRoutingRuntimeHealthInvalid
	}
	return nil
}

func mergeRoutingCredentialHealthState(current RoutingCredentialHealthState, incoming RoutingCredentialHealthState) RoutingCredentialHealthState {
	if current.CredentialID == 0 {
		current.CredentialID = incoming.CredentialID
		current.ChannelID = incoming.ChannelID
	}
	if incoming.AuthVersion > current.AuthVersion {
		current.AuthFailure = incoming.AuthFailure
		current.AuthFailureReason = incoming.AuthFailureReason
		current.AuthFailureUntilMs = incoming.AuthFailureUntilMs
		current.AuthVersion = incoming.AuthVersion
		current.AuthUpdatedTimeMs = incoming.AuthUpdatedTimeMs
	}
	if incoming.CapacityVersion > current.CapacityVersion {
		current.CapacityLimited = incoming.CapacityLimited
		current.CapacityStatusCode = incoming.CapacityStatusCode
		current.CapacityCooldownUntilMs = incoming.CapacityCooldownUntilMs
		current.CapacityVersion = incoming.CapacityVersion
		current.CapacityUpdatedTimeMs = incoming.CapacityUpdatedTimeMs
	}
	current.UpdatedTimeMs = max(current.UpdatedTimeMs, current.AuthUpdatedTimeMs, current.CapacityUpdatedTimeMs)
	return current
}

func routingCredentialHealthOnConflict(db *gorm.DB) clause.OnConflict {
	updates := make(clause.Set, 0, 11)
	for _, column := range []string{"auth_failure", "auth_failure_reason", "auth_failure_until_ms", "auth_updated_time_ms"} {
		updates = append(updates, routingRuntimeHealthConditionalAssignment(
			db, "routing_credential_health_states", "auth_version", "capacity_version",
			"updated_time_ms", "auth_updated_time_ms", column,
		))
	}
	for _, column := range []string{"capacity_limited", "capacity_status_code", "capacity_cooldown_until_ms", "capacity_updated_time_ms"} {
		updates = append(updates, routingRuntimeHealthConditionalAssignment(
			db, "routing_credential_health_states", "capacity_version", "auth_version",
			"updated_time_ms", "capacity_updated_time_ms", column,
		))
	}
	updates = append(updates,
		routingCredentialHealthVersionAssignment(db, "auth_version", "capacity_version", "auth_updated_time_ms"),
		routingCredentialHealthVersionAssignment(db, "capacity_version", "auth_version", "capacity_updated_time_ms"),
	)
	updates = append(updates, routingCredentialHealthUpdatedTimeAssignment(db))
	return clause.OnConflict{Columns: []clause.Column{{Name: "credential_id"}}, DoUpdates: updates}
}

func routingCredentialHealthVersionAssignment(
	db *gorm.DB,
	versionColumn string,
	peerVersionColumn string,
	dimensionTimeColumn string,
) clause.Assignment {
	if db == nil || db.Dialector == nil || db.Dialector.Name() != string(common.DatabaseTypeMySQL) {
		return routingRuntimeHealthConditionalAssignment(
			db, "routing_credential_health_states", versionColumn, peerVersionColumn,
			"updated_time_ms", dimensionTimeColumn, versionColumn,
		)
	}
	return clause.Assignment{
		Column: clause.Column{Name: versionColumn},
		Value: clause.Expr{
			SQL: "CASE WHEN ? > 0 THEN CASE WHEN ? < VALUES(?) THEN VALUES(?) ELSE ? END WHEN VALUES(?) > 0 AND ? = VALUES(?) THEN VALUES(?) ELSE ? END",
			Vars: []any{
				clause.Column{Name: versionColumn},
				clause.Column{Name: versionColumn},
				clause.Column{Name: versionColumn},
				clause.Column{Name: versionColumn},
				clause.Column{Name: versionColumn},
				clause.Column{Name: versionColumn},
				clause.Column{Name: dimensionTimeColumn},
				clause.Column{Name: dimensionTimeColumn},
				clause.Column{Name: versionColumn},
				clause.Column{Name: versionColumn},
			},
		},
	}
}

func routingRuntimeHealthConditionalAssignment(
	db *gorm.DB,
	table string,
	versionColumn string,
	peerVersionColumn string,
	legacyTimeColumn string,
	dimensionTimeColumn string,
	targetColumn string,
) clause.Assignment {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == string(common.DatabaseTypeMySQL) {
		if peerVersionColumn != "" {
			return clause.Assignment{
				Column: clause.Column{Name: targetColumn},
				Value: clause.Expr{
					SQL: "CASE WHEN (CASE WHEN ? > 0 THEN ? < VALUES(?) WHEN ? > 0 THEN VALUES(?) > 0 ELSE ? < VALUES(?) END) THEN VALUES(?) ELSE ? END",
					Vars: []any{
						clause.Column{Name: versionColumn},
						clause.Column{Name: versionColumn},
						clause.Column{Name: versionColumn},
						clause.Column{Name: peerVersionColumn},
						clause.Column{Name: versionColumn},
						clause.Column{Name: legacyTimeColumn},
						clause.Column{Name: dimensionTimeColumn},
						clause.Column{Name: targetColumn},
						clause.Column{Name: targetColumn},
					},
				},
			}
		}
		return clause.Assignment{
			Column: clause.Column{Name: targetColumn},
			Value: clause.Expr{
				SQL: "CASE WHEN (CASE WHEN ? > 0 THEN ? < VALUES(?) ELSE ? < VALUES(?) END) THEN VALUES(?) ELSE ? END",
				Vars: []any{
					clause.Column{Name: versionColumn},
					clause.Column{Name: versionColumn},
					clause.Column{Name: versionColumn},
					clause.Column{Name: legacyTimeColumn},
					clause.Column{Name: dimensionTimeColumn},
					clause.Column{Name: targetColumn},
					clause.Column{Name: targetColumn},
				},
			},
		}
	}
	if peerVersionColumn != "" {
		return clause.Assignment{
			Column: clause.Column{Name: targetColumn},
			Value: clause.Expr{
				SQL: "CASE WHEN (CASE WHEN ? > 0 THEN ? < ? WHEN ? > 0 THEN ? > 0 ELSE ? < ? END) THEN ? ELSE ? END",
				Vars: []any{
					clause.Column{Table: table, Name: versionColumn},
					clause.Column{Table: table, Name: versionColumn},
					clause.Column{Table: "excluded", Name: versionColumn},
					clause.Column{Table: table, Name: peerVersionColumn},
					clause.Column{Table: "excluded", Name: versionColumn},
					clause.Column{Table: table, Name: legacyTimeColumn},
					clause.Column{Table: "excluded", Name: dimensionTimeColumn},
					clause.Column{Table: "excluded", Name: targetColumn},
					clause.Column{Table: table, Name: targetColumn},
				},
			},
		}
	}
	return clause.Assignment{
		Column: clause.Column{Name: targetColumn},
		Value: clause.Expr{
			SQL: "CASE WHEN (CASE WHEN ? > 0 THEN ? < ? ELSE ? < ? END) THEN ? ELSE ? END",
			Vars: []any{
				clause.Column{Table: table, Name: versionColumn},
				clause.Column{Table: table, Name: versionColumn},
				clause.Column{Table: "excluded", Name: versionColumn},
				clause.Column{Table: table, Name: legacyTimeColumn},
				clause.Column{Table: "excluded", Name: dimensionTimeColumn},
				clause.Column{Table: "excluded", Name: targetColumn},
				clause.Column{Table: table, Name: targetColumn},
			},
		},
	}
}

func routingCredentialHealthUpdatedTimeAssignment(db *gorm.DB) clause.Assignment {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == string(common.DatabaseTypeMySQL) {
		return clause.Assignment{
			Column: clause.Column{Name: "updated_time_ms"},
			Value: clause.Expr{
				SQL: "GREATEST(?, COALESCE(?, 0), COALESCE(?, 0))",
				Vars: []any{
					clause.Column{Name: "updated_time_ms"},
					clause.Column{Name: "auth_updated_time_ms"},
					clause.Column{Name: "capacity_updated_time_ms"},
				},
			},
		}
	}
	maximumFunction := "MAX"
	if db != nil && db.Dialector != nil && db.Dialector.Name() == string(common.DatabaseTypePostgreSQL) {
		maximumFunction = "GREATEST"
	}
	return clause.Assignment{
		Column: clause.Column{Name: "updated_time_ms"},
		Value: clause.Expr{
			SQL: maximumFunction + "(?, ?, ?)",
			Vars: []any{
				clause.Column{Table: "routing_credential_health_states", Name: "updated_time_ms"},
				routingCredentialHealthUpdatedTimeCandidate("auth_version", "capacity_version", "auth_updated_time_ms"),
				routingCredentialHealthUpdatedTimeCandidate("capacity_version", "auth_version", "capacity_updated_time_ms"),
			},
		},
	}
}

func routingCredentialHealthUpdatedTimeCandidate(
	versionColumn string,
	peerVersionColumn string,
	dimensionTimeColumn string,
) clause.Expr {
	return clause.Expr{
		SQL: "CASE WHEN (CASE WHEN ? > 0 THEN ? < ? WHEN ? > 0 THEN ? > 0 ELSE ? < ? END) THEN ? ELSE ? END",
		Vars: []any{
			clause.Column{Table: "routing_credential_health_states", Name: versionColumn},
			clause.Column{Table: "routing_credential_health_states", Name: versionColumn},
			clause.Column{Table: "excluded", Name: versionColumn},
			clause.Column{Table: "routing_credential_health_states", Name: peerVersionColumn},
			clause.Column{Table: "excluded", Name: versionColumn},
			clause.Column{Table: "routing_credential_health_states", Name: "updated_time_ms"},
			clause.Column{Table: "excluded", Name: dimensionTimeColumn},
			clause.Column{Table: "excluded", Name: dimensionTimeColumn},
			clause.Column{Table: "routing_credential_health_states", Name: "updated_time_ms"},
		},
	}
}

func routingRuntimeHealthLegacyVersion(updatedTimeMs int64) int64 {
	if updatedTimeMs <= 0 {
		return 0
	}
	if updatedTimeMs > math.MaxInt64/int64(time.Millisecond) {
		return math.MaxInt64
	}
	return updatedTimeMs * int64(time.Millisecond)
}

func boundedRoutingRuntimeHealthReason(reason string) string {
	reason = strings.ToValidUTF8(reason, "")
	reason = strings.TrimSpace(common.SanitizeErrorMessage(reason))
	if len(reason) <= routingRuntimeHealthReasonMaxBytes {
		return reason
	}
	end := routingRuntimeHealthReasonMaxBytes
	for end > 0 && !utf8.ValidString(reason[:end]) {
		end--
	}
	return strings.TrimSpace(reason[:end])
}
