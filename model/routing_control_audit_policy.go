package model

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

const routingControlPolicyChangeLimit = 256

type RoutingControlPolicyChange struct {
	Scope             string `json:"scope"`
	Change            string `json:"change"`
	PoolID            int    `json:"pool_id,omitempty"`
	GroupName         string `json:"group_name,omitempty"`
	MemberID          int    `json:"member_id,omitempty"`
	RoutingGeneration string `json:"routing_generation,omitempty"`
	Field             string `json:"field,omitempty"`
	Before            any    `json:"before,omitempty"`
	After             any    `json:"after,omitempty"`
}

type RoutingControlPolicyChangeSet struct {
	Items     []RoutingControlPolicyChange `json:"items"`
	Truncated bool                         `json:"truncated"`
}

type routingControlPolicyImpact struct {
	ChangedPoolIDs          []int  `json:"changed_pool_ids"`
	PoolChangeCount         int    `json:"pool_change_count"`
	MemberChangeCount       int    `json:"member_change_count"`
	ActivationStage         string `json:"activation_stage"`
	TrafficBasisPoints      int    `json:"traffic_basis_points"`
	Rollback                bool   `json:"rollback"`
	RollbackOfRevision      int64  `json:"rollback_of_revision,omitempty"`
	RuntimeSnapshotRebuild  bool   `json:"runtime_snapshot_rebuild"`
	DeterministicValidation bool   `json:"deterministic_validation_passed"`
}

func insertRoutingPolicyPublicationAuditTx(
	ctx context.Context,
	tx *gorm.DB,
	previousHead RoutingPolicyHead,
	document RoutingPolicyDocument,
	result RoutingPolicyPublishResult,
) error {
	if tx == nil || !tx.Migrator().HasTable(&RoutingControlAudit{}) {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	beforeDocument := RoutingPolicyDocument{SchemaVersion: RoutingPolicySchemaVersion, Pools: []RoutingPolicyPoolContent{}}
	beforeTrafficBasisPoints := 0
	if previousHead.CurrentRevision > 0 {
		loaded, _, err := LoadRoutingPolicyRevisionDBContext(ctx, tx, previousHead.CurrentRevision)
		if err != nil {
			return err
		}
		beforeDocument = loaded
	}
	if previousHead.CurrentActivationID > 0 {
		var activation RoutingPolicyActivation
		if err := tx.WithContext(ctx).Where("id = ?", previousHead.CurrentActivationID).First(&activation).Error; err != nil {
			return err
		}
		beforeTrafficBasisPoints = activation.TrafficBasisPoints
	}
	changeSet, err := routingControlPolicyDiff(
		beforeDocument,
		document,
		previousHead.CurrentStage,
		beforeTrafficBasisPoints,
		result.Activation.Stage,
		result.Activation.TrafficBasisPoints,
	)
	if err != nil {
		return err
	}
	changedPoolIDs := make([]int, 0)
	changedPoolSet := make(map[int]struct{})
	poolChanges := 0
	memberChanges := 0
	for _, change := range changeSet.Items {
		if change.PoolID > 0 {
			changedPoolSet[change.PoolID] = struct{}{}
		}
		switch change.Scope {
		case "pool", "activation":
			poolChanges++
		case "member":
			memberChanges++
		}
	}
	for poolID := range changedPoolSet {
		changedPoolIDs = append(changedPoolIDs, poolID)
	}
	sort.Ints(changedPoolIDs)

	summaryJSON, err := common.Marshal(struct {
		Revision           int64  `json:"revision"`
		PreviousRevision   int64  `json:"previous_revision"`
		RollbackOfRevision int64  `json:"rollback_of_revision,omitempty"`
		Stage              string `json:"stage"`
		TrafficBasisPoints int    `json:"traffic_basis_points"`
		PoolCount          int    `json:"pool_count"`
		MemberCount        int    `json:"member_count"`
	}{
		Revision: result.Revision.Revision, PreviousRevision: result.Revision.ParentRevision,
		RollbackOfRevision: result.Revision.RollbackOfRevision, Stage: result.Activation.Stage,
		TrafficBasisPoints: result.Activation.TrafficBasisPoints,
		PoolCount:          result.Revision.PoolCount, MemberCount: result.Revision.MemberCount,
	})
	if err != nil {
		return err
	}
	snapshotJSON, err := common.Marshal(struct {
		Revision           int64  `json:"revision"`
		SchemaVersion      int    `json:"schema_version"`
		ParentRevision     int64  `json:"parent_revision"`
		RollbackOfRevision int64  `json:"rollback_of_revision,omitempty"`
		Stage              string `json:"stage"`
		TrafficBasisPoints int    `json:"traffic_basis_points"`
	}{
		Revision: result.Revision.Revision, SchemaVersion: result.Revision.SchemaVersion,
		ParentRevision: result.Revision.ParentRevision, RollbackOfRevision: result.Revision.RollbackOfRevision,
		Stage: result.Activation.Stage, TrafficBasisPoints: result.Activation.TrafficBasisPoints,
	})
	if err != nil {
		return err
	}
	changesJSON, err := common.Marshal(changeSet)
	if err != nil {
		return err
	}
	impactJSON, err := common.Marshal(routingControlPolicyImpact{
		ChangedPoolIDs: changedPoolIDs, PoolChangeCount: poolChanges, MemberChangeCount: memberChanges,
		ActivationStage: result.Activation.Stage, TrafficBasisPoints: result.Activation.TrafficBasisPoints,
		Rollback: result.Revision.RollbackOfRevision > 0, RollbackOfRevision: result.Revision.RollbackOfRevision,
		RuntimeSnapshotRebuild: true, DeterministicValidation: true,
	})
	if err != nil {
		return err
	}
	relationsJSON, err := common.Marshal(struct {
		Revision           int64 `json:"revision"`
		PreviousRevision   int64 `json:"previous_revision"`
		ActivationID       int64 `json:"activation_id"`
		OutboxID           int64 `json:"outbox_id"`
		RollbackOfRevision int64 `json:"rollback_of_revision,omitempty"`
	}{
		Revision: result.Revision.Revision, PreviousRevision: result.Revision.ParentRevision,
		ActivationID: result.Activation.ID, OutboxID: result.Outbox.ID,
		RollbackOfRevision: result.Revision.RollbackOfRevision,
	})
	if err != nil {
		return err
	}
	technicalJSON, err := common.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		EventID       string `json:"event_id"`
		PayloadHash   string `json:"payload_hash"`
	}{
		SchemaVersion: result.Revision.SchemaVersion, EventID: result.Outbox.EventID, PayloadHash: result.Outbox.PayloadHash,
	})
	if err != nil {
		return err
	}
	action := RoutingControlActionPublish
	if result.Revision.RollbackOfRevision > 0 {
		action = RoutingControlActionRollback
	}
	return insertRoutingControlAuditTx(tx.WithContext(ctx), RoutingControlAudit{
		SubjectType: RoutingControlSubjectPolicyRevision, SubjectID: result.Revision.Revision,
		SubjectIdentity: fmt.Sprintf("policy-revision:%d", result.Revision.Revision),
		SubjectName:     fmt.Sprintf("Routing policy revision %d", result.Revision.Revision),
		Action:          action, Source: routingControlAuditActorSource(result.Revision.ActorID),
		Reason: result.Revision.Reason, Result: RoutingControlAuditResultSucceeded,
		ActorID: result.Revision.ActorID, BeforeHash: previousHead.CurrentHash,
		AfterHash: result.Revision.ContentHash, SummaryJSON: string(summaryJSON),
		SubjectSnapshotJSON: string(snapshotJSON), ChangeSetJSON: string(changesJSON),
		ImpactJSON: string(impactJSON), RelationJSON: string(relationsJSON), TechnicalJSON: string(technicalJSON),
		PolicyRevision: result.Revision.Revision, ActivationID: result.Activation.ID,
		RollbackOfRevision: result.Revision.RollbackOfRevision, CreatedTimeMs: time.Now().UnixMilli(),
	})
}

func insertRoutingPolicyDraftPublicationAuditTx(
	tx *gorm.DB,
	before RoutingPolicyDraft,
	after RoutingPolicyDraft,
	published RoutingPolicyPublishResult,
	operation RoutingOperation,
	riskAccepted bool,
	reason string,
) error {
	if tx == nil || !tx.Migrator().HasTable(&RoutingControlAudit{}) {
		return nil
	}
	summaryJSON, err := common.Marshal(struct {
		DraftID           int64  `json:"draft_id"`
		DraftVersion      int64  `json:"draft_version"`
		PublishedRevision int64  `json:"published_revision"`
		Stage             string `json:"stage"`
		RiskAccepted      bool   `json:"risk_accepted"`
	}{
		DraftID: after.ID, DraftVersion: after.Version, PublishedRevision: published.Revision.Revision,
		Stage: published.Activation.Stage, RiskAccepted: riskAccepted,
	})
	if err != nil {
		return err
	}
	snapshotJSON, err := common.Marshal(struct {
		DraftID           int64  `json:"draft_id"`
		Version           int64  `json:"version"`
		Status            string `json:"status"`
		BaseRevision      int64  `json:"base_revision"`
		PublishedRevision int64  `json:"published_revision"`
	}{
		DraftID: after.ID, Version: after.Version, Status: after.Status,
		BaseRevision: after.BaseRevision, PublishedRevision: after.PublishedRevision,
	})
	if err != nil {
		return err
	}
	changesJSON, err := common.Marshal(struct {
		Items []RoutingControlPolicyChange `json:"items"`
	}{Items: []RoutingControlPolicyChange{
		{Scope: "draft", Change: "updated", Field: "status", Before: before.Status, After: after.Status},
		{Scope: "draft", Change: "updated", Field: "version", Before: before.Version, After: after.Version},
		{Scope: "draft", Change: "updated", Field: "published_revision", Before: before.PublishedRevision, After: after.PublishedRevision},
	}})
	if err != nil {
		return err
	}
	impactJSON, err := common.Marshal(struct {
		LeavesWorkspace     bool `json:"leaves_workspace"`
		FormalPolicyCreated bool `json:"formal_policy_created"`
		RiskAccepted        bool `json:"risk_accepted"`
	}{LeavesWorkspace: true, FormalPolicyCreated: true, RiskAccepted: riskAccepted})
	if err != nil {
		return err
	}
	relationsJSON, err := common.Marshal(struct {
		OperationID  int64 `json:"operation_id,omitempty"`
		Revision     int64 `json:"revision"`
		ActivationID int64 `json:"activation_id"`
		OutboxID     int64 `json:"outbox_id"`
	}{
		OperationID: operation.ID, Revision: published.Revision.Revision,
		ActivationID: published.Activation.ID, OutboxID: published.Outbox.ID,
	})
	if err != nil {
		return err
	}
	technicalJSON, err := common.Marshal(struct {
		DocumentHash string `json:"document_hash"`
		DraftETag    string `json:"draft_etag"`
	}{DocumentHash: after.DocumentHash, DraftETag: after.ETag})
	if err != nil {
		return err
	}
	return insertRoutingControlAuditTx(tx, RoutingControlAudit{
		EventType:   "policy_draft.published",
		SubjectType: RoutingControlSubjectPolicyDraft, SubjectID: after.ID,
		SubjectIdentity: fmt.Sprintf("policy-draft:%d", after.ID), SubjectName: fmt.Sprintf("Routing policy draft %d", after.ID),
		Action: RoutingControlActionPublish, Source: RoutingControlAuditSourceAdmin,
		Reason: reason, Result: RoutingControlAuditResultSucceeded, ActorID: published.Revision.ActorID,
		BeforeHash: before.ETag, AfterHash: after.ETag, SummaryJSON: string(summaryJSON),
		SubjectSnapshotJSON: string(snapshotJSON), ChangeSetJSON: string(changesJSON),
		ImpactJSON: string(impactJSON), RelationJSON: string(relationsJSON), TechnicalJSON: string(technicalJSON),
		CorrelationID: operation.CorrelationID, OperationID: operation.ID, DraftID: after.ID,
		PolicyRevision: published.Revision.Revision, ActivationID: published.Activation.ID,
		CreatedTimeMs: after.PublishedTimeMs,
	})
}

func insertRoutingPolicyRiskAcceptanceAuditTx(
	tx *gorm.DB,
	acceptance RoutingPolicyRiskAcceptance,
) error {
	if tx == nil || acceptance.ID <= 0 || !tx.Migrator().HasTable(&RoutingControlAudit{}) {
		return nil
	}
	summaryJSON, err := common.Marshal(struct {
		Accepted                 bool   `json:"accepted"`
		RiskState                string `json:"risk_state"`
		TargetStage              string `json:"target_stage"`
		TargetTrafficBasisPoints int    `json:"target_traffic_basis_points"`
		PublishedRevision        int64  `json:"published_revision"`
	}{
		Accepted: true, RiskState: RoutingPolicySimulationRiskFail,
		TargetStage: acceptance.TargetStage, TargetTrafficBasisPoints: acceptance.TargetTrafficBasisPoints,
		PublishedRevision: acceptance.PublishedRevision,
	})
	if err != nil {
		return err
	}
	snapshotJSON, err := common.Marshal(struct {
		AcceptanceID      int64  `json:"acceptance_id"`
		DraftID           int64  `json:"draft_id"`
		DraftVersion      int64  `json:"draft_version"`
		DocumentHash      string `json:"document_hash"`
		PublishedRevision int64  `json:"published_revision"`
	}{
		AcceptanceID: acceptance.ID, DraftID: acceptance.DraftID, DraftVersion: acceptance.DraftVersion,
		DocumentHash: acceptance.DocumentHash, PublishedRevision: acceptance.PublishedRevision,
	})
	if err != nil {
		return err
	}
	impactJSON, err := common.Marshal(struct {
		KnownSimulationFailureAccepted  bool `json:"known_simulation_failure_accepted"`
		DeterministicValidationBypassed bool `json:"deterministic_validation_bypassed"`
		NeedsReview                     bool `json:"needs_review"`
	}{
		KnownSimulationFailureAccepted: true, DeterministicValidationBypassed: false, NeedsReview: true,
	})
	if err != nil {
		return err
	}
	recommendationJSON, err := common.Marshal(struct {
		Actions []string `json:"actions"`
	}{Actions: []string{"Review the published revision after rollout and record the observed outcome."}})
	if err != nil {
		return err
	}
	relationsJSON, err := common.Marshal(struct {
		EvidenceID            int64 `json:"evidence_id"`
		SimulationOperationID int64 `json:"simulation_operation_id"`
		DraftID               int64 `json:"draft_id"`
		PublishedRevision     int64 `json:"published_revision"`
	}{
		EvidenceID: acceptance.EvidenceID, SimulationOperationID: acceptance.OperationID,
		DraftID: acceptance.DraftID, PublishedRevision: acceptance.PublishedRevision,
	})
	if err != nil {
		return err
	}
	afterHash := routingPolicyHash(summaryJSON)
	return insertRoutingControlAuditTx(tx, RoutingControlAudit{
		EventType:   "policy_risk_acceptance.accepted",
		SubjectType: RoutingControlSubjectPolicyRiskAcceptance, SubjectID: acceptance.ID,
		SubjectIdentity: fmt.Sprintf("policy-risk-acceptance:%d", acceptance.ID),
		SubjectName:     fmt.Sprintf("Routing policy risk acceptance %d", acceptance.ID),
		Action:          RoutingControlActionRiskAccept, Source: RoutingControlAuditSourceAdmin,
		Reason: acceptance.Reason, Result: RoutingControlAuditResultSucceeded, ActorID: acceptance.ActorID,
		BeforeHash: acceptance.RiskHash, AfterHash: afterHash, SummaryJSON: string(summaryJSON),
		SubjectSnapshotJSON: string(snapshotJSON), ImpactJSON: string(impactJSON),
		RecommendationJSON: string(recommendationJSON), RelationJSON: string(relationsJSON),
		NeedsAttention: true, DraftID: acceptance.DraftID, SimulationOperationID: acceptance.OperationID,
		PolicyRevision: acceptance.PublishedRevision, CreatedTimeMs: acceptance.CreatedTimeMs,
	})
}

func routingControlPolicyDiff(
	before RoutingPolicyDocument,
	after RoutingPolicyDocument,
	beforeStage string,
	beforeTrafficBasisPoints int,
	afterStage string,
	afterTrafficBasisPoints int,
) (RoutingControlPolicyChangeSet, error) {
	result := RoutingControlPolicyChangeSet{Items: make([]RoutingControlPolicyChange, 0, 32)}
	appendChange := func(change RoutingControlPolicyChange) {
		if len(result.Items) >= routingControlPolicyChangeLimit {
			result.Truncated = true
			return
		}
		result.Items = append(result.Items, change)
	}
	if beforeStage != afterStage {
		appendChange(RoutingControlPolicyChange{Scope: "activation", Change: "updated", Field: "stage", Before: beforeStage, After: afterStage})
	}
	if beforeTrafficBasisPoints != afterTrafficBasisPoints {
		appendChange(RoutingControlPolicyChange{
			Scope: "activation", Change: "updated", Field: "traffic_basis_points",
			Before: beforeTrafficBasisPoints, After: afterTrafficBasisPoints,
		})
	}

	beforePools := make(map[int]RoutingPolicyPoolContent, len(before.Pools))
	afterPools := make(map[int]RoutingPolicyPoolContent, len(after.Pools))
	poolIDs := make(map[int]struct{}, len(before.Pools)+len(after.Pools))
	for _, pool := range before.Pools {
		beforePools[pool.PoolID] = pool
		poolIDs[pool.PoolID] = struct{}{}
	}
	for _, pool := range after.Pools {
		afterPools[pool.PoolID] = pool
		poolIDs[pool.PoolID] = struct{}{}
	}
	orderedPoolIDs := make([]int, 0, len(poolIDs))
	for poolID := range poolIDs {
		orderedPoolIDs = append(orderedPoolIDs, poolID)
	}
	sort.Ints(orderedPoolIDs)
	for _, poolID := range orderedPoolIDs {
		beforePool, beforeExists := beforePools[poolID]
		afterPool, afterExists := afterPools[poolID]
		if !beforeExists {
			appendChange(RoutingControlPolicyChange{Scope: "pool", Change: "added", PoolID: poolID, GroupName: afterPool.GroupName})
			for _, member := range afterPool.Members {
				appendChange(RoutingControlPolicyChange{
					Scope: "member", Change: "added", PoolID: poolID, GroupName: afterPool.GroupName,
					MemberID: member.MemberID, RoutingGeneration: member.RoutingGeneration,
				})
			}
			continue
		}
		if !afterExists {
			appendChange(RoutingControlPolicyChange{Scope: "pool", Change: "removed", PoolID: poolID, GroupName: beforePool.GroupName})
			continue
		}
		compareRoutingControlPolicyPool(beforePool, afterPool, appendChange)
		if err := compareRoutingControlPolicyJSON(beforePool, afterPool, appendChange); err != nil {
			return RoutingControlPolicyChangeSet{}, err
		}
		compareRoutingControlPolicyMembers(beforePool, afterPool, appendChange)
	}
	return result, nil
}

func compareRoutingControlPolicyPool(
	before RoutingPolicyPoolContent,
	after RoutingPolicyPoolContent,
	appendChange func(RoutingControlPolicyChange),
) {
	fields := []struct {
		name   string
		before any
		after  any
	}{
		{name: "group_name", before: before.GroupName, after: after.GroupName},
		{name: "display_name", before: before.DisplayName, after: after.DisplayName},
		{name: "deployment_stage", before: before.DeploymentStage, after: after.DeploymentStage},
		{name: "policy_profile", before: before.PolicyProfile, after: after.PolicyProfile},
		{name: "default_enabled", before: routingControlBoolValue(before.DefaultEnabled), after: routingControlBoolValue(after.DefaultEnabled)},
		{name: "default_priority", before: routingControlInt64Value(before.DefaultPriority), after: routingControlInt64Value(after.DefaultPriority)},
		{name: "default_weight", before: routingControlInt64Value(before.DefaultWeight), after: routingControlInt64Value(after.DefaultWeight)},
	}
	for _, field := range fields {
		if routingControlAuditComparable(field.before) == routingControlAuditComparable(field.after) {
			continue
		}
		appendChange(RoutingControlPolicyChange{
			Scope: "pool", Change: "updated", PoolID: after.PoolID, GroupName: after.GroupName,
			Field: field.name, Before: field.before, After: field.after,
		})
	}
}

func compareRoutingControlPolicyJSON(
	before RoutingPolicyPoolContent,
	after RoutingPolicyPoolContent,
	appendChange func(RoutingControlPolicyChange),
) error {
	var beforePolicy map[string]any
	var afterPolicy map[string]any
	if len(before.Policy) > 0 && common.Unmarshal(before.Policy, &beforePolicy) != nil {
		return ErrRoutingPolicyInvalid
	}
	if len(after.Policy) > 0 && common.Unmarshal(after.Policy, &afterPolicy) != nil {
		return ErrRoutingPolicyInvalid
	}
	keys := make(map[string]struct{}, len(beforePolicy)+len(afterPolicy))
	for key := range beforePolicy {
		keys[key] = struct{}{}
	}
	for key := range afterPolicy {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		beforeValue, beforeExists := beforePolicy[key]
		afterValue, afterExists := afterPolicy[key]
		beforeComparable := routingControlAuditComparable(beforeValue)
		afterComparable := routingControlAuditComparable(afterValue)
		if beforeExists == afterExists && beforeComparable == afterComparable {
			continue
		}
		change := "updated"
		if !beforeExists {
			change = "added"
		} else if !afterExists {
			change = "removed"
		}
		appendChange(RoutingControlPolicyChange{
			Scope: "pool", Change: change, PoolID: after.PoolID, GroupName: after.GroupName,
			Field: "policy." + key, Before: routingControlAuditDisplayValue(beforeValue), After: routingControlAuditDisplayValue(afterValue),
		})
	}
	return nil
}

func compareRoutingControlPolicyMembers(
	beforePool RoutingPolicyPoolContent,
	afterPool RoutingPolicyPoolContent,
	appendChange func(RoutingControlPolicyChange),
) {
	beforeMembers := make(map[int]RoutingPolicyMemberContent, len(beforePool.Members))
	afterMembers := make(map[int]RoutingPolicyMemberContent, len(afterPool.Members))
	memberIDs := make(map[int]struct{}, len(beforePool.Members)+len(afterPool.Members))
	for _, member := range beforePool.Members {
		beforeMembers[member.MemberID] = member
		memberIDs[member.MemberID] = struct{}{}
	}
	for _, member := range afterPool.Members {
		afterMembers[member.MemberID] = member
		memberIDs[member.MemberID] = struct{}{}
	}
	ordered := make([]int, 0, len(memberIDs))
	for memberID := range memberIDs {
		ordered = append(ordered, memberID)
	}
	sort.Ints(ordered)
	for _, memberID := range ordered {
		before, beforeExists := beforeMembers[memberID]
		after, afterExists := afterMembers[memberID]
		if !beforeExists || !afterExists {
			member := after
			change := "added"
			if !afterExists {
				member = before
				change = "removed"
			}
			appendChange(RoutingControlPolicyChange{
				Scope: "member", Change: change, PoolID: afterPool.PoolID, GroupName: afterPool.GroupName,
				MemberID: member.MemberID, RoutingGeneration: member.RoutingGeneration,
			})
			continue
		}
		fields := []struct {
			name   string
			before any
			after  any
		}{
			{name: "routing_generation", before: before.RoutingGeneration, after: after.RoutingGeneration},
			{name: "enabled", before: before.Enabled, after: after.Enabled},
			{name: "priority", before: before.Priority, after: after.Priority},
			{name: "weight", before: before.Weight, after: after.Weight},
			{name: "enabled_override", before: routingControlBoolValue(before.EnabledOverride), after: routingControlBoolValue(after.EnabledOverride)},
			{name: "priority_override", before: routingControlInt64Value(before.PriorityOverride), after: routingControlInt64Value(after.PriorityOverride)},
			{name: "weight_override", before: routingControlInt64Value(before.WeightOverride), after: routingControlInt64Value(after.WeightOverride)},
			{name: "credential_count", before: len(before.CredentialIDs), after: len(after.CredentialIDs)},
			{name: "overrides_hash", before: routingPolicyHash(before.Overrides), after: routingPolicyHash(after.Overrides)},
		}
		for _, field := range fields {
			if routingControlAuditComparable(field.before) == routingControlAuditComparable(field.after) {
				continue
			}
			appendChange(RoutingControlPolicyChange{
				Scope: "member", Change: "updated", PoolID: afterPool.PoolID, GroupName: afterPool.GroupName,
				MemberID: after.MemberID, RoutingGeneration: after.RoutingGeneration,
				Field: field.name, Before: field.before, After: field.after,
			})
		}
	}
}

func routingControlAuditComparable(value any) string {
	encoded, err := common.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%T:%v", value, value)
	}
	return string(encoded)
}

func routingControlAuditDisplayValue(value any) any {
	switch value.(type) {
	case nil, bool, float64, string:
		return value
	default:
		encoded, err := common.Marshal(value)
		if err != nil {
			return "unavailable"
		}
		return "hash:" + routingPolicyHash(encoded)
	}
}

func routingControlBoolValue(value *bool) any {
	if value == nil {
		return "inherit"
	}
	return *value
}

func routingControlInt64Value(value *int64) any {
	if value == nil {
		return "inherit"
	}
	return *value
}

func routingControlAuditActorSource(actorID int) string {
	if actorID > 0 {
		return RoutingControlAuditSourceAdmin
	}
	return RoutingControlAuditSourceSystem
}

func routingControlAuditOperationIdentity(operationID int64) string {
	return "operation:" + strconv.FormatInt(operationID, 10)
}

func routingControlAuditJoin(values ...string) string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			filtered = append(filtered, value)
		}
	}
	return strings.Join(filtered, ":")
}
