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
import type {
  ChannelRoutingActiveProbeResult,
  ChannelRoutingActiveProbeStats,
  ChannelRoutingBreakerResetResult,
  ChannelRoutingBreakerResetTarget,
  RoutingOperation,
} from '../types'

const activeProbeStatKeys = [
  'cycles',
  'targets_considered',
  'targets_selected',
  'skipped_not_due',
  'skipped_budget',
  'lease_contended',
  'lease_errors',
  'executed',
  'succeeded',
  'failed',
  'timed_out',
  'canceled',
  'local_errors',
  'persistence_errors',
  'completion_errors',
  'effect_errors',
  'reserved_tokens',
  'reserved_cost_nano_usd',
  'inflight',
  'max_inflight',
] as const satisfies readonly (keyof ChannelRoutingActiveProbeStats)[]

export type ChannelRoutingOperationResultRow = {
  label: string
  value: string | number | boolean
  format?:
    | 'basis_points'
    | 'boolean'
    | 'bytes'
    | 'number'
    | 'ratio'
    | 'status'
    | 'timestamp'
    | 'usd'
}

function operationResult(
  operation: RoutingOperation
): Record<string, unknown> | null {
  if (
    operation.result == null ||
    typeof operation.result !== 'object' ||
    Array.isArray(operation.result)
  ) {
    return null
  }
  return operation.result as Record<string, unknown>
}

export function channelRoutingOperationDisplayStatus(
  operation: RoutingOperation
): string {
  if (operation.status === 'partially_succeeded') return 'partially_succeeded'
  const result = operationResult(operation)
  if (
    operation.status === 'succeeded' &&
    result?.execution_state === 'partial'
  ) {
    return 'partial'
  }
  return operation.status
}

export function channelRoutingOperationTypeLabel(type: string): string {
  const labels: Record<string, string> = {
    active_probe: 'Active probe',
    audit_export: 'Audit export',
    breaker_reset: 'Breaker reset',
    canary_auto_rollback: 'Automatic Canary rollback',
    cost_sync: 'Cost sync',
    historical_simulation: 'Historical simulation',
    policy_manual_rollback: 'Manual rollback',
    policy_publish: 'Policy publish',
    policy_simulation: 'Policy simulation',
  }
  return labels[type] ?? type
}

export function channelRoutingOperationSourceLabel(source: string): string {
  const labels: Record<string, string> = {
    admin: 'Administrator',
    migration: 'Migration',
    recovery: 'Recovery',
    scheduler: 'Scheduler',
    system: 'System',
  }
  return labels[source] ?? source
}

export function channelRoutingOperationRetentionLabel(
  retention: string
): string {
  const labels: Record<string, string> = {
    extended: 'Extended retention',
    high_frequency: 'High-frequency retention',
    permanent: 'Permanent retention',
    standard: 'Standard retention',
  }
  return labels[retention] ?? retention
}

export function channelRoutingOperationIsActive(
  operation: RoutingOperation | undefined
): boolean {
  return (
    operation?.status === 'pending' ||
    operation?.status === 'running' ||
    operation?.status === 'retry_wait'
  )
}

export function channelRoutingOperationCanRetry(
  operation: RoutingOperation | undefined
): boolean {
  if (!operation?.retryable) return false
  return (
    operation.status === 'failed' ||
    operation.status === 'partially_succeeded' ||
    operation.status === 'cancelled'
  )
}

export function channelRoutingOperationCanCancel(
  operation: RoutingOperation | undefined
): boolean {
  if (!operation?.cancellable) return false
  return (
    operation.status === 'pending' ||
    operation.status === 'running' ||
    operation.status === 'retry_wait'
  )
}

export function channelRoutingOperationAuditExportId(
  operation: RoutingOperation
): string | null {
  if (operation.type !== 'audit_export') return null
  const exportId = operationResult(operation)?.export_id
  if (typeof exportId !== 'string' || !/^rae_[0-9a-f]{32}$/.test(exportId)) {
    return null
  }
  return exportId
}

export function channelRoutingOperationActiveProbeResult(
  operation: RoutingOperation
): ChannelRoutingActiveProbeResult | null {
  if (operation.type !== 'active_probe') return null
  const result = operationResult(operation)
  if (typeof result?.enabled !== 'boolean') return null
  if (
    result.stats == null ||
    typeof result.stats !== 'object' ||
    Array.isArray(result.stats)
  ) {
    return null
  }

  const rawStats = result.stats as Record<string, unknown>
  const stats: Partial<ChannelRoutingActiveProbeStats> = {}
  for (const key of activeProbeStatKeys) {
    const value = rawStats[key]
    if (!Number.isSafeInteger(value) || (value as number) < 0) return null
    stats[key] = value as number
  }

  return {
    enabled: result.enabled,
    stats: stats as ChannelRoutingActiveProbeStats,
  }
}

export function channelRoutingOperationResultRows(
  operation: RoutingOperation
): ChannelRoutingOperationResultRow[] {
  const result = operationResult(operation)
  if (!result) return []

  if (operation.type === 'active_probe') {
    const probe = channelRoutingOperationActiveProbeResult(operation)
    if (!probe) return []
    return [
      { label: 'Probe enabled', value: probe.enabled, format: 'boolean' },
      { label: 'Executed', value: probe.stats.executed, format: 'number' },
      { label: 'Succeeded', value: probe.stats.succeeded, format: 'number' },
      { label: 'Failed', value: probe.stats.failed, format: 'number' },
      { label: 'Timed out', value: probe.stats.timed_out, format: 'number' },
      {
        label: 'Local errors',
        value: probe.stats.local_errors,
        format: 'number',
      },
    ]
  }

  if (operation.type === 'audit_export') {
    const exportId = channelRoutingOperationAuditExportId(operation)
    if (!exportId) return []
    return compactOperationResultRows([
      { label: 'Export ID', value: exportId },
      operationResultRow(result, 'record_count', 'Records', 'number'),
      operationResultRow(result, 'content_bytes', 'Content size', 'bytes'),
      operationResultRow(result, 'created_time_ms', 'Created', 'timestamp'),
      operationResultRow(result, 'expires_time_ms', 'Expires', 'timestamp'),
    ])
  }

  if (operation.type === 'breaker_reset') {
    const scope = result.scope
    const generation = result.generation
    if (
      (scope !== 'member' && scope !== 'endpoint') ||
      !positiveInteger(generation)
    ) {
      return []
    }
    return [
      {
        label: 'Scope',
        value: scope === 'member' ? 'Member / model' : 'Endpoint / region',
        format: 'status',
      },
      { label: 'Generation', value: generation, format: 'number' },
    ]
  }

  if (
    operation.type === 'historical_simulation' ||
    operation.type === 'policy_simulation'
  ) {
    const risk = operationResultRecord(result.risk)
    const skipReasons = operationResultRecord(result.skip_reasons)
    const skippedSamples = skipReasons
      ? Object.values(skipReasons).reduce<number>((total, value) => {
          if (!Number.isSafeInteger(value) || (value as number) < 0) {
            return total
          }
          return total + (value as number)
        }, 0)
      : undefined
    return compactOperationResultRows([
      operationResultRow(result, 'pool_id', 'Pool', 'number'),
      operationResultRow(
        result,
        'scanned_samples',
        'Scanned samples',
        'number'
      ),
      operationResultRow(
        result,
        'evaluated_samples',
        'Evaluated samples',
        'number'
      ),
      operationResultRow(
        result,
        'actual_match_count',
        'Actual matches',
        'number'
      ),
      operationResultRow(
        result,
        'actual_match_rate',
        'Actual match rate',
        'ratio'
      ),
      operationResultRow(
        result,
        'selection_changed_count',
        'Selection changes',
        'number'
      ),
      operationResultRow(
        result,
        'selection_change_rate',
        'Selection change rate',
        'ratio'
      ),
      operationResultRow(
        result,
        'cost_known_samples',
        'Cost-known samples',
        'number'
      ),
      operationResultRow(
        result,
        'total_expected_cost_delta',
        'Total expected cost delta',
        'usd'
      ),
      operationResultRow(
        result,
        'average_expected_cost_delta',
        'Average expected cost delta',
        'usd'
      ),
      skippedSamples === undefined
        ? null
        : {
            label: 'Skipped samples',
            value: skippedSamples,
            format: 'number',
          },
      operationResultRow(result, 'simulated_algorithm', 'Simulated algorithm'),
      typeof risk?.state === 'string'
        ? { label: 'Risk state', value: risk.state, format: 'status' }
        : null,
      operationResultRow(result, 'target_stage', 'Target stage', 'status'),
      operationResultRow(
        result,
        'target_traffic_basis_points',
        'Canary traffic',
        'basis_points'
      ),
    ])
  }

  if (operation.type === 'policy_publish') {
    return compactOperationResultRows([
      operationResultRow(result, 'draft_id', 'Draft', 'number'),
      operationResultRow(result, 'draft_version', 'Draft version', 'number'),
    ])
  }

  if (operation.type === 'policy_manual_rollback') {
    return compactOperationResultRows([
      operationResultRow(
        result,
        'source_revision',
        'Source revision',
        'number'
      ),
    ])
  }

  if (operation.type === 'cost_sync') {
    return compactOperationResultRows([
      operationResultRow(result, 'task_status', 'Task status', 'status'),
      operationResultRow(
        result,
        'execution_state',
        'Execution state',
        'status'
      ),
    ])
  }

  return []
}

export function channelRoutingOperationBreakerResetResult(
  operation: RoutingOperation
): ChannelRoutingBreakerResetResult | null {
  if (operation.type !== 'breaker_reset') return null
  const result = operationResult(operation)
  if (
    (result?.scope !== 'member' && result?.scope !== 'endpoint') ||
    typeof result.generation !== 'number' ||
    !Number.isSafeInteger(result.generation) ||
    result.generation <= 0 ||
    typeof result.outbox_id !== 'number' ||
    !Number.isSafeInteger(result.outbox_id) ||
    result.outbox_id <= 0
  ) {
    return null
  }
  const target = breakerResetTarget(result.target, result.scope)
  if (!target) return null
  return {
    scope: result.scope,
    generation: result.generation,
    outbox_id: result.outbox_id,
    target,
  }
}

function breakerResetTarget(
  value: unknown,
  expectedScope: 'member' | 'endpoint'
): ChannelRoutingBreakerResetTarget | null {
  if (value == null || typeof value !== 'object' || Array.isArray(value)) {
    return null
  }
  const target = value as Record<string, unknown>
  if (target.scope !== expectedScope) return null
  if (expectedScope === 'member') {
    if (
      !positiveInteger(target.pool_id) ||
      !positiveInteger(target.member_id) ||
      !positiveInteger(target.channel_id) ||
      typeof target.api_key_index !== 'number' ||
      !Number.isSafeInteger(target.api_key_index) ||
      typeof target.model_name !== 'string' ||
      target.model_name.length === 0 ||
      typeof target.group_name !== 'string' ||
      target.group_name.length === 0
    ) {
      return null
    }
    return {
      scope: 'member',
      pool_id: target.pool_id,
      member_id: target.member_id,
      channel_id: target.channel_id,
      api_key_index: target.api_key_index as number,
      model_name: target.model_name,
      group_name: target.group_name,
    }
  }
  if (
    typeof target.endpoint_host !== 'string' ||
    target.endpoint_host.length === 0 ||
    typeof target.endpoint_authority !== 'string' ||
    target.endpoint_authority.length === 0 ||
    typeof target.region !== 'string' ||
    target.region.length === 0
  ) {
    return null
  }
  return {
    scope: 'endpoint',
    endpoint_host: target.endpoint_host,
    endpoint_authority: target.endpoint_authority,
    region: target.region,
  }
}

function positiveInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) > 0
}

function operationResultRecord(value: unknown): Record<string, unknown> | null {
  if (value == null || typeof value !== 'object' || Array.isArray(value)) {
    return null
  }
  return value as Record<string, unknown>
}

function operationResultRow(
  result: Record<string, unknown>,
  key: string,
  label: string,
  format?: ChannelRoutingOperationResultRow['format']
): ChannelRoutingOperationResultRow | null {
  const value = result[key]
  if (typeof value === 'string' && value.trim() !== '') {
    return { label, value, format }
  }
  if (typeof value === 'boolean') return { label, value, format }
  if (typeof value === 'number' && Number.isFinite(value)) {
    return { label, value, format }
  }
  return null
}

function compactOperationResultRows(
  rows: Array<ChannelRoutingOperationResultRow | null>
): ChannelRoutingOperationResultRow[] {
  return rows.filter(
    (row): row is ChannelRoutingOperationResultRow => row != null
  )
}
