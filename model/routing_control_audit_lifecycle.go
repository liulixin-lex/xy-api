package model

import (
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

func insertRoutingChannelLifecycleAuditTx(
	tx *gorm.DB,
	before *RoutingChannelLifecycle,
	after RoutingChannelLifecycle,
	action string,
	reason string,
) error {
	if tx == nil || !tx.Migrator().HasTable(&RoutingControlAudit{}) {
		return nil
	}
	afterHash, err := routingChannelLifecycleControlHash(after)
	if err != nil {
		return err
	}
	beforeHash := ""
	beforeStatus := ""
	if before != nil {
		beforeHash, err = routingChannelLifecycleControlHash(*before)
		if err != nil {
			return err
		}
		beforeStatus = before.Status
	}
	summaryJSON, err := common.Marshal(struct {
		ChannelID         int    `json:"channel_id"`
		RoutingIdentity   string `json:"routing_identity"`
		RoutingGeneration string `json:"routing_generation"`
		Status            string `json:"status"`
		Reason            string `json:"reason"`
	}{
		ChannelID: after.ChannelID, RoutingIdentity: after.RoutingIdentity,
		RoutingGeneration: after.RoutingGeneration, Status: after.Status, Reason: reason,
	})
	if err != nil {
		return err
	}
	snapshotJSON, err := common.Marshal(struct {
		ChannelID         int    `json:"channel_id"`
		RoutingIdentity   string `json:"routing_identity"`
		RoutingGeneration string `json:"routing_generation"`
		Name              string `json:"name"`
		Group             string `json:"group"`
		ChannelType       int    `json:"channel_type"`
		Endpoint          string `json:"endpoint"`
		Status            string `json:"status"`
	}{
		ChannelID: after.ChannelID, RoutingIdentity: after.RoutingIdentity,
		RoutingGeneration: after.RoutingGeneration, Name: after.NameSnapshot,
		Group: after.GroupSnapshot, ChannelType: after.ChannelType,
		Endpoint: after.EndpointSnapshot, Status: after.Status,
	})
	if err != nil {
		return err
	}
	changesJSON, err := common.Marshal(struct {
		Items []RoutingControlPolicyChange `json:"items"`
	}{Items: []RoutingControlPolicyChange{{
		Scope: "lifecycle", Change: action, Field: "status", Before: beforeStatus, After: after.Status,
		RoutingGeneration: after.RoutingGeneration,
	}}})
	if err != nil {
		return err
	}
	impactJSON, err := common.Marshal(struct {
		LifecycleActive       bool `json:"lifecycle_active"`
		MembersRetired        bool `json:"members_retired"`
		CredentialsRetired    bool `json:"credentials_retired"`
		RuntimeStateColdStart bool `json:"runtime_state_cold_start"`
	}{
		LifecycleActive:       after.Status == RoutingChannelLifecycleStatusActive,
		MembersRetired:        after.Status == RoutingChannelLifecycleStatusRetired,
		CredentialsRetired:    after.Status == RoutingChannelLifecycleStatusRetired,
		RuntimeStateColdStart: after.Status == RoutingChannelLifecycleStatusRetired,
	})
	if err != nil {
		return err
	}
	relationsJSON, err := common.Marshal(struct {
		ChannelID       int    `json:"channel_id"`
		RoutingIdentity string `json:"routing_identity"`
	}{ChannelID: after.ChannelID, RoutingIdentity: after.RoutingIdentity})
	if err != nil {
		return err
	}
	source := RoutingControlAuditSourceSystem
	if reason == RoutingChannelLifecycleReasonMigrated {
		source = RoutingControlAuditSourceMigration
	}
	createdTimeMs := time.Now().UnixMilli()
	if after.UpdatedTime > 0 {
		createdTimeMs = after.UpdatedTime * 1_000
	}
	return insertRoutingControlAuditTx(tx, RoutingControlAudit{
		EventType:   "channel_lifecycle." + action,
		SubjectType: RoutingControlSubjectChannelLifecycle, SubjectID: after.ID,
		SubjectIdentity: after.RoutingIdentity, SubjectGeneration: after.RoutingGeneration,
		SubjectName: after.NameSnapshot, Action: action, Source: source, Reason: reason,
		Result: RoutingControlAuditResultSucceeded, BeforeHash: beforeHash, AfterHash: afterHash,
		SummaryJSON: string(summaryJSON), SubjectSnapshotJSON: string(snapshotJSON),
		ChangeSetJSON: string(changesJSON), ImpactJSON: string(impactJSON), RelationJSON: string(relationsJSON),
		CreatedTimeMs: createdTimeMs,
	})
}

func routingChannelLifecycleControlHash(lifecycle RoutingChannelLifecycle) (string, error) {
	payload, err := common.Marshal(struct {
		ID                int64  `json:"id"`
		ChannelID         int    `json:"channel_id"`
		RoutingIdentity   string `json:"routing_identity"`
		RoutingGeneration string `json:"routing_generation"`
		Status            string `json:"status"`
		CreatedReason     string `json:"created_reason"`
		RetiredReason     string `json:"retired_reason"`
		NameSnapshot      string `json:"name_snapshot"`
		GroupSnapshot     string `json:"group_snapshot"`
		ChannelType       int    `json:"channel_type"`
		EndpointSnapshot  string `json:"endpoint_snapshot"`
		CreatedTime       int64  `json:"created_time"`
		RetiredTime       int64  `json:"retired_time"`
		UpdatedTime       int64  `json:"updated_time"`
	}{
		ID: lifecycle.ID, ChannelID: lifecycle.ChannelID, RoutingIdentity: lifecycle.RoutingIdentity,
		RoutingGeneration: lifecycle.RoutingGeneration, Status: lifecycle.Status,
		CreatedReason: lifecycle.CreatedReason, RetiredReason: lifecycle.RetiredReason,
		NameSnapshot: lifecycle.NameSnapshot, GroupSnapshot: lifecycle.GroupSnapshot,
		ChannelType: lifecycle.ChannelType, EndpointSnapshot: lifecycle.EndpointSnapshot,
		CreatedTime: lifecycle.CreatedTime, RetiredTime: lifecycle.RetiredTime, UpdatedTime: lifecycle.UpdatedTime,
	})
	if err != nil {
		return "", err
	}
	return routingPolicyHash(payload), nil
}
