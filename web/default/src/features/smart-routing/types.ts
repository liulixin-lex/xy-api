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
import type { SystemTask } from '@/features/system-settings/types'

export type ApiResponse<T> = {
  success: boolean
  message?: string
  data: T
}

export type SmartRoutingMode =
  | 'observe'
  | 'shadow'
  | 'balanced'
  | 'enterprise_slo'

export type SmartRoutingSettings = {
  enabled: boolean
  mode: SmartRoutingMode
  weight_availability: number
  weight_latency: number
  weight_throughput: number
  weight_cost: number
  availability_floor: number
  min_volume: number
  top_k: number
  consecutive_5xx: number
  failure_rate_pct: number
  base_cooldown_sec: number
  max_cooldown_sec: number
  max_ejected_pct: number
  half_open_probes: number
  max_switches: number
  backoff_base_ms_5xx: number
  backoff_base_ms_429: number
  backoff_cap_ms: number
  first_byte_failover_enabled: boolean
  first_byte_min_ms: number
  first_byte_cap_ms: number
  first_byte_p95_multiplier: number
  snapshot_live_sec: number
  snapshot_stale_sec: number
  balance_margin_usd: number
  sync_interval_min: number
  hotcache_refresh_sec: number
  metric_bucket_sec: number
  flush_interval_min: number
  retention_days: number
  agent_enabled: boolean
  agent_auto_apply: boolean
  agent_model: string
}

export type RoutingCredentials = {
  new_api_access_token?: string
  gateway_api_key?: string
  sub2api_email?: string
  sub2api_password?: string
  sub2api_token?: string
}

export type RoutingCredentialMasks = {
  new_api_access_token?: string
  gateway_api_key?: string
  sub2api_email?: string
  sub2api_password?: string
  sub2api_token?: string
}

export type RoutingBinding = {
  id: number
  channel_id: number
  upstream_type: 'newapi' | 'sub2api'
  base_url: string
  upstream_group: string
  serves_claude_code: boolean
  new_api_user_id?: number
  enabled: boolean
  sync_backoff_until: number
  last_sync_error?: string
  credential_masks: RoutingCredentialMasks
  credential_error?: string
  created_time: number
  updated_time: number
}

export type RoutingBindingRequest = {
  channel_id: number
  upstream_type: 'newapi' | 'sub2api'
  base_url: string
  upstream_group: string
  serves_claude_code: boolean
  new_api_user_id?: number
  enabled: boolean
  credentials: RoutingCredentials
}

export type RoutingBindingActionResult = {
  channel_id: number
  upstream_type: string
  upstream_group?: string
  credential_ready?: boolean
  credential_test?: boolean
  groups: string[]
  model_count: number
  pricing_version?: string
  requires_sync?: boolean
  sync_task_type?: string
  serves_claude?: boolean
}

export type RoutingMetric = {
  id: number
  channel_id: number
  api_key_index: number
  model_name: string
  group: string
  bucket_ts: number
  request_count: number
  success_count: number
  total_latency_ms: number
  latency_p95_ms: number
  ttft_sum_ms: number
  ttft_count: number
  ttft_p95_ms: number
  output_tokens: number
  generation_ms: number
  err_4xx: number
  err_5xx: number
  err_429: number
  retry_after_max_ms: number
}

export type RoutingCostSnapshot = {
  id: number
  channel_id: number
  model_name: string
  group_ratio: number
  base_ratio: number
  completion_ratio: number
  model_price: number
  billing_mode: string
  tiers_json?: string
  extras_json?: string
  confidence: 'full' | 'group_only' | 'unknown' | string
  snapshot_ts: number
  pricing_version: string
}

export type RoutingBreakerState = 'healthy' | 'degraded' | 'open' | 'half_open'

export type RoutingBreaker = {
  id: number
  channel_id: number
  api_key_index: number
  model_name: string
  group: string
  state: RoutingBreakerState | string
  reason: string
  consecutive_failures: number
  ejection_count: number
  opened_at: number
  cooldown_until: number
  half_open_inflight: number
  updated_time: number
}

export type RoutingAgentRecommendation = {
  id: number
  type: string
  target_json: string
  proposed_json: string
  rationale: string
  severity: string
  status: string
  applied_by?: number
  created_time: number
  updated_time: number
}

export type RoutingSyncResponse = {
  task: SystemTask
  created: boolean
}
