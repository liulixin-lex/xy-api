/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

export const channelRoutingQueryKeys = {
  all: ['channel-routing'] as const,
  overview: () => [...channelRoutingQueryKeys.all, 'overview'] as const,
  nodesRoot: () => [...channelRoutingQueryKeys.all, 'nodes'] as const,
  nodes: (params: object) =>
    [...channelRoutingQueryKeys.nodesRoot(), params] as const,
  groupsRoot: () => [...channelRoutingQueryKeys.all, 'groups'] as const,
  groups: (params: object) =>
    [...channelRoutingQueryKeys.groupsRoot(), params] as const,
  group: (id: number, params: object) =>
    [...channelRoutingQueryKeys.groupsRoot(), id, params] as const,
  groupReplayProfiles: (id: number, limit: number) =>
    [
      ...channelRoutingQueryKeys.groupsRoot(),
      id,
      'replay-profiles',
      limit,
    ] as const,
  errorBudget: (id: number) =>
    [...channelRoutingQueryKeys.groupsRoot(), id, 'error-budget'] as const,
  channelsRoot: () => [...channelRoutingQueryKeys.all, 'channels'] as const,
  channels: (params: object) =>
    [...channelRoutingQueryKeys.channelsRoot(), params] as const,
  endpointsRoot: () => [...channelRoutingQueryKeys.all, 'endpoints'] as const,
  endpoints: (params: object) =>
    [...channelRoutingQueryKeys.endpointsRoot(), params] as const,
  costsRoot: () => [...channelRoutingQueryKeys.all, 'costs'] as const,
  costs: (params: object) =>
    [...channelRoutingQueryKeys.costsRoot(), params] as const,
  costDetail: (poolId: number, memberId: number, model: string) =>
    [
      ...channelRoutingQueryKeys.costsRoot(),
      'detail',
      poolId,
      memberId,
      model,
    ] as const,
  probesRoot: () => [...channelRoutingQueryKeys.all, 'probes'] as const,
  probes: (params: object) =>
    [...channelRoutingQueryKeys.probesRoot(), params] as const,
  decisionsRoot: () => [...channelRoutingQueryKeys.all, 'decisions'] as const,
  decisions: (params: object) =>
    [...channelRoutingQueryKeys.decisionsRoot(), params] as const,
  decision: (id: string) =>
    [...channelRoutingQueryKeys.decisionsRoot(), id] as const,
  decisionCandidates: (id: string, cursor: number, limit: number) =>
    [
      ...channelRoutingQueryKeys.decision(id),
      'candidates',
      cursor,
      limit,
    ] as const,
  policyDraftsRoot: () =>
    [...channelRoutingQueryKeys.all, 'policy-drafts'] as const,
  policyDrafts: (params: object) =>
    [...channelRoutingQueryKeys.policyDraftsRoot(), params] as const,
  policyDraft: (id: number) =>
    [...channelRoutingQueryKeys.policyDraftsRoot(), id] as const,
  policySimulationRisk: (id: number) =>
    [...channelRoutingQueryKeys.policyDraft(id), 'simulation-risk'] as const,
  policyApprovals: (id: number, params: object) =>
    [...channelRoutingQueryKeys.policyDraft(id), 'approvals', params] as const,
  policiesRoot: () => [...channelRoutingQueryKeys.all, 'policies'] as const,
  currentPolicy: () =>
    [...channelRoutingQueryKeys.policiesRoot(), 'current'] as const,
  policyRevision: (revision: number) =>
    [...channelRoutingQueryKeys.policiesRoot(), revision] as const,
  policyRollbackApprovalsRoot: (revision: number) =>
    [
      ...channelRoutingQueryKeys.policyRevision(revision),
      'rollback-approvals',
    ] as const,
  policyRollbackApprovals: (revision: number, params: object) =>
    [
      ...channelRoutingQueryKeys.policyRollbackApprovalsRoot(revision),
      params,
    ] as const,
  operationsRoot: () => [...channelRoutingQueryKeys.all, 'operations'] as const,
  operations: (params: object) =>
    [...channelRoutingQueryKeys.operationsRoot(), params] as const,
  operation: (id: number) =>
    [...channelRoutingQueryKeys.operationsRoot(), id] as const,
  auditExportsRoot: () =>
    [...channelRoutingQueryKeys.all, 'audit-exports'] as const,
}
