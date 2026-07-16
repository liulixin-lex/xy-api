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
import z from 'zod'

import type {
  SmartRoutingSetting,
  SmartRoutingSettingField,
} from '../../../types'

const finiteNumber = z.number().finite('Enter a finite number')
const safeInteger = finiteNumber
  .int('Enter a whole number')
  .min(0, 'Enter zero or a positive number')
  .max(Number.MAX_SAFE_INTEGER, 'Enter a smaller number')
const positiveInteger = safeInteger.min(1, 'Enter a value of at least 1')
const allowedMaximumMessage =
  'Enter a value no greater than the allowed maximum'
const allowedMinimumMessage = 'Enter a value at or above the allowed minimum'
const greaterThanZeroMessage = 'Enter a value greater than zero'
const zeroToOneMessage = 'Enter a value from 0 to 1'

export const runtimeSettingsSchema = z
  .object({
    enabled: z.boolean(),
    request_profile_enabled: z.boolean(),
    mode: z.enum(['observe', 'shadow', 'balanced', 'enterprise_slo']),
    weight_availability: finiteNumber
      .min(0, zeroToOneMessage)
      .max(1, zeroToOneMessage),
    weight_latency: finiteNumber
      .min(0, zeroToOneMessage)
      .max(1, zeroToOneMessage),
    weight_throughput: finiteNumber
      .min(0, zeroToOneMessage)
      .max(1, zeroToOneMessage),
    weight_cost: finiteNumber.min(0, zeroToOneMessage).max(1, zeroToOneMessage),
    availability_floor: finiteNumber
      .min(0, zeroToOneMessage)
      .max(1, zeroToOneMessage),
    min_volume: safeInteger,
    top_k: positiveInteger,
    consecutive_5xx: positiveInteger,
    failure_rate_pct: positiveInteger.max(100, allowedMaximumMessage),
    base_cooldown_sec: positiveInteger,
    max_cooldown_sec: positiveInteger,
    max_ejected_pct: safeInteger.max(100, allowedMaximumMessage),
    half_open_probes: positiveInteger,
    max_switches: safeInteger,
    retry_token_capacity: positiveInteger.max(1_000_000, allowedMaximumMessage),
    retry_token_refill_per_sec: finiteNumber
      .gt(0, greaterThanZeroMessage)
      .max(1_000_000, allowedMaximumMessage),
    failover_deadline_ms: positiveInteger.max(600_000, allowedMaximumMessage),
    retry_extra_cost_multiplier: finiteNumber
      .gt(0, greaterThanZeroMessage)
      .max(16, allowedMaximumMessage),
    backoff_base_ms_5xx: positiveInteger.max(600_000, allowedMaximumMessage),
    backoff_base_ms_429: positiveInteger.max(600_000, allowedMaximumMessage),
    backoff_cap_ms: positiveInteger.max(600_000, allowedMaximumMessage),
    first_byte_failover_enabled: z.boolean(),
    first_byte_min_ms: positiveInteger,
    first_byte_cap_ms: positiveInteger,
    first_byte_p95_multiplier: finiteNumber.gt(0, greaterThanZeroMessage),
    hedge_enabled: z.boolean(),
    hedge_max_concurrent: positiveInteger.max(128, allowedMaximumMessage),
    hedge_max_response_bytes: positiveInteger
      .min(64 << 10, allowedMinimumMessage)
      .max(64 << 20, allowedMaximumMessage),
    hedge_max_buffered_bytes: positiveInteger.max(
      1 << 30,
      allowedMaximumMessage
    ),
    hedge_ratio_window_sec: positiveInteger.max(3_600, allowedMaximumMessage),
    hedge_max_extra_basis_points: positiveInteger.max(
      10_000,
      allowedMaximumMessage
    ),
    hedge_audit_retention_days: positiveInteger.max(365, allowedMaximumMessage),
    snapshot_live_sec: positiveInteger,
    snapshot_stale_sec: positiveInteger,
    balance_margin_usd: finiteNumber.min(0, 'Enter zero or a positive number'),
    sync_interval_min: positiveInteger,
    hotcache_refresh_sec: positiveInteger,
    metric_bucket_sec: positiveInteger,
    flush_interval_min: positiveInteger,
    retention_days: positiveInteger,
    active_probe_enabled: z.boolean(),
    active_probe_healthy_sec: positiveInteger.max(
      86_400,
      allowedMaximumMessage
    ),
    active_probe_degraded_sec: positiveInteger.max(
      86_400,
      allowedMaximumMessage
    ),
    active_probe_open_sec: positiveInteger.max(86_400, allowedMaximumMessage),
    active_probe_timeout_ms: positiveInteger.max(
      120_000,
      allowedMaximumMessage
    ),
    active_probe_max_targets: positiveInteger.max(4_096, allowedMaximumMessage),
    active_probe_concurrency: positiveInteger.max(64, allowedMaximumMessage),
    active_probe_per_host: positiveInteger.max(64, allowedMaximumMessage),
    active_probe_token_budget: positiveInteger.max(
      1_000_000_000,
      allowedMaximumMessage
    ),
    active_probe_cost_budget_usd: finiteNumber
      .gt(0, greaterThanZeroMessage)
      .max(1_000, allowedMaximumMessage),
    agent_enabled: z.boolean(),
    agent_auto_apply: z.boolean(),
    agent_model: z.string().max(256, 'Enter 256 characters or fewer'),
  })
  .superRefine((settings, context) => {
    const addIssue = (field: SmartRoutingSettingField, message: string) => {
      context.addIssue({ code: 'custom', path: [field], message })
    }
    const totalWeight =
      settings.weight_availability +
      settings.weight_latency +
      settings.weight_throughput +
      settings.weight_cost
    if (totalWeight <= 0) {
      addIssue(
        'weight_availability',
        'At least one routing weight must be greater than zero'
      )
    }
    if (settings.max_cooldown_sec < settings.base_cooldown_sec) {
      addIssue(
        'max_cooldown_sec',
        'Maximum cooldown must not be lower than base cooldown'
      )
    }
    if (settings.backoff_base_ms_5xx > settings.backoff_cap_ms) {
      addIssue(
        'backoff_base_ms_5xx',
        'Backoff base must not exceed the backoff cap'
      )
    }
    if (settings.backoff_base_ms_429 > settings.backoff_cap_ms) {
      addIssue(
        'backoff_base_ms_429',
        'Backoff base must not exceed the backoff cap'
      )
    }
    if (settings.first_byte_cap_ms < settings.first_byte_min_ms) {
      addIssue(
        'first_byte_cap_ms',
        'First-byte cap must not be lower than the minimum'
      )
    }
    if (
      settings.hedge_max_buffered_bytes <
      settings.hedge_max_response_bytes * 2
    ) {
      addIssue(
        'hedge_max_buffered_bytes',
        'Hedge buffer must hold at least two maximum responses'
      )
    }
    if (
      settings.active_probe_degraded_sec > settings.active_probe_healthy_sec
    ) {
      addIssue(
        'active_probe_degraded_sec',
        'Degraded probe interval must not exceed the healthy interval'
      )
    }
    if (settings.active_probe_open_sec > settings.active_probe_degraded_sec) {
      addIssue(
        'active_probe_open_sec',
        'Open-state probe interval must not exceed the degraded interval'
      )
    }
    if (settings.active_probe_per_host > settings.active_probe_concurrency) {
      addIssue(
        'active_probe_per_host',
        'Per-host probe concurrency must not exceed total concurrency'
      )
    }
    if (settings.agent_enabled && settings.agent_model.trim() === '') {
      addIssue('agent_model', 'Choose an agent model before enabling the agent')
    }
  })

export type RuntimeSettingsFormValues = z.infer<typeof runtimeSettingsSchema>

export function displayRuntimeSettingValue(
  field: SmartRoutingSettingField,
  value: boolean | number | string,
  translate: (key: string) => string
): string {
  if (typeof value === 'boolean') {
    return translate(value ? 'Enabled' : 'Disabled')
  }
  if (typeof value === 'number') return value.toLocaleString()
  if (field === 'mode') {
    const labels: Record<string, string> = {
      observe: 'Observe only',
      shadow: 'Shadow routing',
      balanced: 'Balanced routing',
      enterprise_slo: 'Enterprise SLO',
    }
    return translate(labels[value] ?? value)
  }
  if (field === 'agent_model') return value || translate('Empty')
  return value ? translate(value) : translate('Empty')
}

export const runtimeSettingFields = [
  'enabled',
  'request_profile_enabled',
  'mode',
  'weight_availability',
  'weight_latency',
  'weight_throughput',
  'weight_cost',
  'availability_floor',
  'min_volume',
  'top_k',
  'consecutive_5xx',
  'failure_rate_pct',
  'base_cooldown_sec',
  'max_cooldown_sec',
  'max_ejected_pct',
  'half_open_probes',
  'max_switches',
  'retry_token_capacity',
  'retry_token_refill_per_sec',
  'failover_deadline_ms',
  'retry_extra_cost_multiplier',
  'backoff_base_ms_5xx',
  'backoff_base_ms_429',
  'backoff_cap_ms',
  'first_byte_failover_enabled',
  'first_byte_min_ms',
  'first_byte_cap_ms',
  'first_byte_p95_multiplier',
  'hedge_enabled',
  'hedge_max_concurrent',
  'hedge_max_response_bytes',
  'hedge_max_buffered_bytes',
  'hedge_ratio_window_sec',
  'hedge_max_extra_basis_points',
  'hedge_audit_retention_days',
  'snapshot_live_sec',
  'snapshot_stale_sec',
  'balance_margin_usd',
  'sync_interval_min',
  'hotcache_refresh_sec',
  'metric_bucket_sec',
  'flush_interval_min',
  'retention_days',
  'active_probe_enabled',
  'active_probe_healthy_sec',
  'active_probe_degraded_sec',
  'active_probe_open_sec',
  'active_probe_timeout_ms',
  'active_probe_max_targets',
  'active_probe_concurrency',
  'active_probe_per_host',
  'active_probe_token_budget',
  'active_probe_cost_budget_usd',
  'agent_enabled',
  'agent_auto_apply',
  'agent_model',
] as const satisfies readonly SmartRoutingSettingField[]

export const runtimeSettingLabels: Record<SmartRoutingSettingField, string> = {
  enabled: 'Channel routing enabled',
  request_profile_enabled: 'Request profiling',
  mode: 'Routing mode',
  weight_availability: 'Availability weight',
  weight_latency: 'Latency weight',
  weight_throughput: 'Throughput weight',
  weight_cost: 'Cost weight',
  availability_floor: 'Availability floor',
  min_volume: 'Minimum sample volume',
  top_k: 'Candidate count',
  consecutive_5xx: 'Consecutive 5xx threshold',
  failure_rate_pct: 'Failure rate threshold',
  base_cooldown_sec: 'Base cooldown',
  max_cooldown_sec: 'Maximum cooldown',
  max_ejected_pct: 'Maximum ejected capacity',
  half_open_probes: 'Half-open probes',
  max_switches: 'Maximum route switches',
  retry_token_capacity: 'Retry token capacity',
  retry_token_refill_per_sec: 'Retry token refill rate',
  failover_deadline_ms: 'Failover deadline',
  retry_extra_cost_multiplier: 'Retry cost multiplier',
  backoff_base_ms_5xx: '5xx backoff base',
  backoff_base_ms_429: '429 backoff base',
  backoff_cap_ms: 'Backoff cap',
  first_byte_failover_enabled: 'First-byte failover',
  first_byte_min_ms: 'First-byte minimum',
  first_byte_cap_ms: 'First-byte cap',
  first_byte_p95_multiplier: 'P95 latency multiplier',
  hedge_enabled: 'Hedge requests',
  hedge_max_concurrent: 'Maximum concurrent hedges',
  hedge_max_response_bytes: 'Maximum hedged response size',
  hedge_max_buffered_bytes: 'Maximum hedge buffer',
  hedge_ratio_window_sec: 'Hedge ratio window',
  hedge_max_extra_basis_points: 'Maximum extra hedge ratio',
  hedge_audit_retention_days: 'Hedge audit retention',
  snapshot_live_sec: 'Live snapshot window',
  snapshot_stale_sec: 'Stale snapshot window',
  balance_margin_usd: 'Balance safety margin',
  sync_interval_min: 'Configuration sync interval',
  hotcache_refresh_sec: 'Hot-cache refresh interval',
  metric_bucket_sec: 'Metric bucket size',
  flush_interval_min: 'Metric flush interval',
  retention_days: 'Metric retention',
  active_probe_enabled: 'Active probes',
  active_probe_healthy_sec: 'Healthy probe interval',
  active_probe_degraded_sec: 'Degraded probe interval',
  active_probe_open_sec: 'Open-state probe interval',
  active_probe_timeout_ms: 'Probe timeout',
  active_probe_max_targets: 'Maximum probe targets',
  active_probe_concurrency: 'Probe concurrency',
  active_probe_per_host: 'Per-host probe concurrency',
  active_probe_token_budget: 'Probe token budget',
  active_probe_cost_budget_usd: 'Probe cost budget',
  agent_enabled: 'Routing agent',
  agent_auto_apply: 'Agent auto apply',
  agent_model: 'Agent model',
}

export const highRiskRuntimeSettingFields = new Set<SmartRoutingSettingField>([
  'hedge_enabled',
  'hedge_max_concurrent',
  'hedge_max_response_bytes',
  'hedge_max_buffered_bytes',
  'hedge_ratio_window_sec',
  'hedge_max_extra_basis_points',
  'agent_auto_apply',
  'active_probe_enabled',
  'active_probe_token_budget',
  'active_probe_cost_budget_usd',
  'retry_token_capacity',
  'retry_token_refill_per_sec',
  'retry_extra_cost_multiplier',
  'balance_margin_usd',
])

export function changedRuntimeSettingFields(
  base: SmartRoutingSetting,
  next: SmartRoutingSetting
): SmartRoutingSettingField[] {
  return runtimeSettingFields.filter(
    (field) => !Object.is(base[field], next[field])
  )
}

export function mergeRuntimeSettingsConflict(
  base: SmartRoutingSetting,
  draft: SmartRoutingSetting,
  current: SmartRoutingSetting
): {
  merged: SmartRoutingSetting
  conflicts: SmartRoutingSettingField[]
  draftChanges: SmartRoutingSettingField[]
} {
  const merged = { ...current }
  const conflicts: SmartRoutingSettingField[] = []
  const draftChanges: SmartRoutingSettingField[] = []
  for (const field of runtimeSettingFields) {
    const draftChanged = !Object.is(draft[field], base[field])
    if (!draftChanged) continue
    draftChanges.push(field)
    const serverChanged = !Object.is(current[field], base[field])
    if (serverChanged && !Object.is(current[field], draft[field])) {
      conflicts.push(field)
    }
    ;(merged[field] as SmartRoutingSetting[typeof field]) = draft[field]
  }
  return { merged, conflicts, draftChanges }
}
