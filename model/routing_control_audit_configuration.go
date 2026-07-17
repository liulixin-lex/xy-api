package model

import (
	"fmt"
	"sort"

	"github.com/QuantumNous/new-api/common"
)

type routingControlAuditDocuments struct {
	SubjectSnapshot string
	ChangeSet       string
	Impact          string
	Relations       string
	Technical       string
}

func routingChannelConfigurationAuditDocuments(
	before *RoutingChannelConfiguration,
	after RoutingChannelConfiguration,
) (routingControlAuditDocuments, error) {
	snapshotJSON, err := common.Marshal(struct {
		ChannelID              int     `json:"channel_id"`
		RoutingIdentity        string  `json:"routing_identity"`
		RoutingGeneration      string  `json:"routing_generation"`
		Revision               int64   `json:"revision"`
		UpstreamCostMultiplier float64 `json:"upstream_cost_multiplier"`
		CostSource             string  `json:"cost_source"`
		CostConfirmed          bool    `json:"cost_confirmed"`
		TrafficClass           string  `json:"traffic_class"`
		FailureDomainLabel     string  `json:"failure_domain_label,omitempty"`
		FailureDomainStatus    string  `json:"failure_domain_status"`
	}{
		ChannelID: after.ChannelID, RoutingIdentity: after.RoutingIdentity,
		RoutingGeneration: after.RoutingGeneration, Revision: after.Revision,
		UpstreamCostMultiplier: after.UpstreamCostMultiplier, CostSource: after.CostSource,
		CostConfirmed: after.CostConfirmed, TrafficClass: after.TrafficClass,
		FailureDomainLabel: after.FailureDomainLabel, FailureDomainStatus: after.FailureDomainStatus,
	})
	if err != nil {
		return routingControlAuditDocuments{}, err
	}
	changes := make([]RoutingControlPolicyChange, 0, 10)
	if before == nil {
		changes = append(changes, RoutingControlPolicyChange{
			Scope: "channel_configuration", Change: "created", Field: "revision", After: after.Revision,
			RoutingGeneration: after.RoutingGeneration,
		})
	} else {
		fields := []struct {
			name   string
			before any
			after  any
		}{
			{name: "routing_generation", before: before.RoutingGeneration, after: after.RoutingGeneration},
			{name: "revision", before: before.Revision, after: after.Revision},
			{name: "upstream_cost_multiplier", before: before.UpstreamCostMultiplier, after: after.UpstreamCostMultiplier},
			{name: "cost_source", before: before.CostSource, after: after.CostSource},
			{name: "cost_confirmed", before: before.CostConfirmed, after: after.CostConfirmed},
			{name: "traffic_class", before: before.TrafficClass, after: after.TrafficClass},
			{name: "failure_domain_label", before: before.FailureDomainLabel, after: after.FailureDomainLabel},
			{name: "failure_domain_status", before: before.FailureDomainStatus, after: after.FailureDomainStatus},
		}
		for _, field := range fields {
			if routingControlAuditComparable(field.before) == routingControlAuditComparable(field.after) {
				continue
			}
			changes = append(changes, RoutingControlPolicyChange{
				Scope: "channel_configuration", Change: "updated", Field: field.name,
				Before: field.before, After: field.after, RoutingGeneration: after.RoutingGeneration,
			})
		}
	}
	changesJSON, err := common.Marshal(struct {
		Items []RoutingControlPolicyChange `json:"items"`
	}{Items: changes})
	if err != nil {
		return routingControlAuditDocuments{}, err
	}
	costChanged := before == nil || before.UpstreamCostMultiplier != after.UpstreamCostMultiplier ||
		before.CostConfirmed != after.CostConfirmed || before.CostSource != after.CostSource
	trafficChanged := before == nil || before.TrafficClass != after.TrafficClass
	failureDomainChanged := before == nil || before.FailureDomainLabel != after.FailureDomainLabel ||
		before.FailureDomainStatus != after.FailureDomainStatus || before.FailureDomainHash != after.FailureDomainHash
	generationRotated := before != nil && before.RoutingGeneration != after.RoutingGeneration
	impactJSON, err := common.Marshal(struct {
		CostOrderingChanged    bool `json:"cost_ordering_changed"`
		EligibilityChanged     bool `json:"eligibility_changed"`
		HedgeIsolationChanged  bool `json:"hedge_isolation_changed"`
		GenerationRotated      bool `json:"generation_rotated"`
		RuntimeSnapshotRefresh bool `json:"runtime_snapshot_refresh"`
	}{
		CostOrderingChanged: costChanged, EligibilityChanged: trafficChanged,
		HedgeIsolationChanged: failureDomainChanged, GenerationRotated: generationRotated,
		RuntimeSnapshotRefresh: costChanged || trafficChanged || failureDomainChanged || generationRotated,
	})
	if err != nil {
		return routingControlAuditDocuments{}, err
	}
	relationsJSON, err := common.Marshal(struct {
		ChannelID         int    `json:"channel_id"`
		RoutingIdentity   string `json:"routing_identity"`
		RoutingGeneration string `json:"routing_generation"`
	}{
		ChannelID: after.ChannelID, RoutingIdentity: after.RoutingIdentity,
		RoutingGeneration: after.RoutingGeneration,
	})
	if err != nil {
		return routingControlAuditDocuments{}, err
	}
	technicalJSON, err := common.Marshal(struct {
		Revision          int64  `json:"revision"`
		FailureDomainHash string `json:"failure_domain_hash,omitempty"`
	}{Revision: after.Revision, FailureDomainHash: after.FailureDomainHash})
	if err != nil {
		return routingControlAuditDocuments{}, err
	}
	return routingControlAuditDocuments{
		SubjectSnapshot: string(snapshotJSON), ChangeSet: string(changesJSON), Impact: string(impactJSON),
		Relations: string(relationsJSON), Technical: string(technicalJSON),
	}, nil
}

func routingRuntimeSettingsAuditDocuments(
	beforeDocument string,
	afterDocument string,
	revision int64,
) (routingControlAuditDocuments, []string, error) {
	before := make(map[string]any)
	after := make(map[string]any)
	if beforeDocument != "" && common.UnmarshalJsonStr(beforeDocument, &before) != nil {
		return routingControlAuditDocuments{}, nil, ErrRoutingRuntimeSettingsInvalid
	}
	if afterDocument == "" || common.UnmarshalJsonStr(afterDocument, &after) != nil {
		return routingControlAuditDocuments{}, nil, ErrRoutingRuntimeSettingsInvalid
	}
	keys := make(map[string]struct{}, len(before)+len(after))
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	changedKeys := make([]string, 0, len(ordered))
	changes := make([]RoutingControlPolicyChange, 0, len(ordered))
	for _, key := range ordered {
		beforeValue, beforeExists := before[key]
		afterValue, afterExists := after[key]
		if beforeExists == afterExists && routingControlAuditComparable(beforeValue) == routingControlAuditComparable(afterValue) {
			continue
		}
		change := "updated"
		if !beforeExists {
			change = "added"
		} else if !afterExists {
			change = "removed"
		}
		changedKeys = append(changedKeys, key)
		changes = append(changes, RoutingControlPolicyChange{
			Scope: "runtime_settings", Change: change, Field: key,
			Before: routingControlAuditDisplayValue(beforeValue), After: routingControlAuditDisplayValue(afterValue),
		})
	}
	snapshotJSON, err := common.Marshal(struct {
		Revision int64 `json:"revision"`
	}{Revision: revision})
	if err != nil {
		return routingControlAuditDocuments{}, nil, err
	}
	changesJSON, err := common.Marshal(struct {
		Items []RoutingControlPolicyChange `json:"items"`
	}{Items: changes})
	if err != nil {
		return routingControlAuditDocuments{}, nil, err
	}
	impactJSON, err := common.Marshal(struct {
		RuntimeBehaviorChanged bool     `json:"runtime_behavior_changed"`
		ChangedKeys            []string `json:"changed_keys"`
		ClusterRefreshRequired bool     `json:"cluster_refresh_required"`
	}{
		RuntimeBehaviorChanged: len(changedKeys) > 0, ChangedKeys: changedKeys,
		ClusterRefreshRequired: len(changedKeys) > 0,
	})
	if err != nil {
		return routingControlAuditDocuments{}, nil, err
	}
	technicalJSON, err := common.Marshal(struct {
		Revision int64  `json:"revision"`
		StateKey string `json:"state_key"`
	}{Revision: revision, StateKey: fmt.Sprintf("runtime-settings:%d", revision)})
	if err != nil {
		return routingControlAuditDocuments{}, nil, err
	}
	return routingControlAuditDocuments{
		SubjectSnapshot: string(snapshotJSON), ChangeSet: string(changesJSON),
		Impact: string(impactJSON), Technical: string(technicalJSON),
	}, changedKeys, nil
}
