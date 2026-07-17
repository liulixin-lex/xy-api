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
import { getRoleLabelKey } from '@/lib/roles'

export type RoutingControlAuditChange = {
  scope: string
  change: string
  pool_id?: number
  group_name?: string
  member_id?: number
  routing_generation?: string
  field?: string
  before?: unknown
  after?: unknown
}

const subjectLabels: Record<string, string> = {
  channel_configuration: 'Channel configuration',
  channel_lifecycle: 'Channel lifecycle',
  cost_binding: 'Retired cost binding',
  operation: 'Operation',
  policy_activation: 'Policy activation',
  policy_draft: 'Policy draft',
  policy_revision: 'Policy revision',
  policy_risk_acceptance: 'Policy risk acceptance',
  pricing: 'Routing pricing',
  runtime_settings: 'Runtime settings',
}

const actionLabels: Record<string, string> = {
  bootstrap: 'Bootstrap',
  cancel: 'Cancel',
  create: 'Create',
  delete: 'Delete',
  publish: 'Publish',
  reconcile: 'Reconcile',
  retire: 'Retire',
  retry: 'Retry',
  risk_accept: 'Accept risk',
  rollback: 'Rollback',
  rotate: 'Rotate',
  update: 'Update',
  validate: 'Validate',
}

const sourceLabels: Record<string, string> = {
  admin: 'Administrator',
  migration: 'Migration',
  reconciler: 'Reconciler',
  system: 'System',
}

const fieldLabels: Record<string, string> = {
  accepted: 'Accepted',
  actions: 'Actions',
  activation_stage: 'Activation stage',
  activation_id: 'Activation',
  attempts: 'Attempts',
  base_revision: 'Base revision',
  channel_id: 'Channel ID',
  channel_type: 'Channel type',
  changed_pool_ids: 'Pools',
  correlation_id: 'Correlation ID',
  deterministic_validation_passed: 'Validated',
  draft_id: 'Draft ID',
  draft_version: 'Draft version',
  endpoint: 'Endpoint',
  group: 'Group',
  member_count: 'Member count',
  member_change_count: 'Changed members',
  member_id: 'Member ID',
  name: 'Name',
  needs_attention: 'Needs attention',
  operation_id: 'Operation ID',
  operation_type: 'Operation type',
  parent_operation_id: 'Parent operation',
  parent_revision: 'Parent revision',
  policy_revision: 'Policy revision',
  pool_count: 'Pool count',
  pool_change_count: 'Policy changes',
  pool_id: 'Pool ID',
  previous_revision: 'Previous revision',
  published_revision: 'Published revision',
  reason: 'Reason',
  retention_category: 'Retention category',
  retry_of_operation_id: 'Retry of operation',
  retry_sequence: 'Retry sequence',
  revision: 'Revision',
  risk_accepted: 'Risk accepted',
  risk_state: 'Risk state',
  rollback: 'Rollback',
  rollback_of_revision: 'Rollback of revision',
  routing_generation: 'Routing generation',
  routing_identity: 'Routing identity',
  runtime_snapshot_rebuild: 'Runtime snapshot rebuild',
  schema_version: 'Schema version',
  source: 'Source',
  stage: 'Stage',
  status: 'Status',
  target_stage: 'Target stage',
  target_traffic_basis_points: 'Target traffic',
  traffic_basis_points: 'Traffic',
  truncated: 'Truncated',
  version: 'Version',
}

export function routingControlAuditSubjectLabel(value: string): string {
  return subjectLabels[value] ?? routingControlAuditHumanizeKey(value)
}

export function routingControlAuditActionLabel(value: string): string {
  return actionLabels[value] ?? routingControlAuditHumanizeKey(value)
}

export function routingControlAuditSourceLabel(value: string): string {
  return sourceLabels[value] ?? routingControlAuditHumanizeKey(value)
}

export function routingControlAuditActorRoleLabel(
  actorId: number,
  actorRole: number,
  source: string
): string {
  if (
    actorId <= 0 ||
    source === 'system' ||
    source === 'migration' ||
    source === 'reconciler'
  ) {
    return 'System'
  }
  return getRoleLabelKey(actorRole)
}

export function routingControlAuditFieldLabel(value: string): string | null {
  return fieldLabels[value] ?? null
}

export function routingControlAuditRecord(
  value: unknown
): Record<string, unknown> | null {
  if (value == null || typeof value !== 'object' || Array.isArray(value)) {
    return null
  }
  return value as Record<string, unknown>
}

export function routingControlAuditChanges(
  value: unknown
): RoutingControlAuditChange[] {
  const record = routingControlAuditRecord(value)
  if (!record || !Array.isArray(record.items)) return []
  return record.items.flatMap((item) => {
    const change = routingControlAuditRecord(item)
    if (
      !change ||
      typeof change.scope !== 'string' ||
      typeof change.change !== 'string'
    ) {
      return []
    }
    return [
      {
        scope: change.scope,
        change: change.change,
        pool_id:
          typeof change.pool_id === 'number' ? change.pool_id : undefined,
        group_name:
          typeof change.group_name === 'string' ? change.group_name : undefined,
        member_id:
          typeof change.member_id === 'number' ? change.member_id : undefined,
        routing_generation:
          typeof change.routing_generation === 'string'
            ? change.routing_generation
            : undefined,
        field: typeof change.field === 'string' ? change.field : undefined,
        before: change.before,
        after: change.after,
      },
    ]
  })
}

export function routingControlAuditEntries(
  value: unknown
): Array<[string, unknown]> {
  const record = routingControlAuditRecord(value)
  if (!record) return []
  return Object.entries(record).filter(([, entry]) => entry !== undefined)
}

export function routingControlAuditHumanizeKey(value: string): string {
  return value
    .split(/[._]/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(' ')
}
