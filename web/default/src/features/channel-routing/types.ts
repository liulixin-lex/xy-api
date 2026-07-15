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
export type ChannelRoutingSection =
  | 'overview'
  | 'groups'
  | 'channels'
  | 'decisions'
  | 'costs'
  | 'policies'

export type ApiEnvelope<T> = {
  success: boolean
  message?: string
  data: T
}

export type ChannelRoutingTelemetry = {
  status: string
  reason?: string
  metric_rollup_rows: number
  metric_rollup_row_limit: number
  metric_rollup_scanned_rows: number
  metric_rollup_scan_limit: number
  metric_sketch_bytes: number
  metric_sketch_byte_limit: number
  observed_requests: number
  observed_successes: number
  logical_success_rate?: number
  p95_ttft_ms?: number
  p95_ttft_status: string
  max_member_p95_ttft_ms: number
  output_tokens_per_second?: number
  unknown_classification_rate?: number
  coverage: number
}

export type ChannelRoutingTopology = {
  pools: number
  members: number
  channels: number
  credentials: number
  credential_coverage: number
  invalid_numeric_values: number
}

export type RuntimeWorkerStats = {
  runs: number
  failures: number
  consecutive_failures: number
  last_success_at: number
  last_failure_at: number
  last_duration_ms: number
  last_error?: string
}

export type ChannelRoutingOverview = {
  api_version: string
  enabled: boolean
  legacy_mode: string
  deployment_stage: string
  control_plane_available: boolean
  control_plane_revision: number
  revision_lag: number
  revision_ahead: number
  propagation_status: string
  snapshot_available: boolean
  snapshot_revision: number
  runtime_generation: number
  policy_hash: string
  node_epoch_id: string
  snapshot_built_at: number
  snapshot_age_sec: number
  snapshot_stale: boolean
  telemetry: ChannelRoutingTelemetry
  topology: ChannelRoutingTopology
  runtime: Record<string, RuntimeWorkerStats | number | string | object>
  events?: Record<string, number | string | boolean>
  adaptive_concurrency: Record<string, number | string | boolean | object>
  strict_capacity: Record<string, number | string | boolean | object>
  attempt_metrics_available: boolean
  attempt_metrics_degraded: boolean
  attempt_metrics_coverage: number
  attempt_metrics_pipeline: RoutingAttemptMetricsPipeline
  attempt_metrics: RoutingAttemptWindowMetrics
  risk_groups_available: boolean
  risk_groups_truncated: boolean
  risk_groups: PoolSnapshotSummary[]
  recent_events_available: boolean
  recent_events: ChannelRoutingRecentEvent[]
}

export type RoutingPreCommitFailoverMetric = {
  known: boolean
  rate?: number
  numerator: number
  denominator: number
  covered: number
  coverage: number
}

export type RoutingUnitPlatformCostMetric = {
  known: boolean
  value?: number
  total_platform_cost?: number
  request_count: number
  sent_attempts: number
  known_attempts: number
  unknown_attempts: number
  coverage: number
  currency?: string
  unit?: string
  dimension_consistent: boolean
}

export type RoutingAttemptWindowMetrics = {
  from_time_ms: number
  to_time_ms: number
  pre_commit_failover_success_rate: RoutingPreCommitFailoverMetric
  unit_request_platform_cost: RoutingUnitPlatformCostMetric
}

export type RoutingAttemptMetricsPipeline = {
  entries: number
  capacity: number
  buffered_bytes: number
  byte_capacity: number
  in_progress: number
  completed: number
  reserved: number
  rejected: number
  persisted: number
  persist_failures: number
  consecutive_persist_failures: number
  last_rejected_ms: number
  oldest_started_ms: number
}

export type ChannelRoutingRecentEvent = {
  id: string
  sequence: number
  node_epoch_id: string
  type: string
  revision?: number
  created_time_ms: number
  payload: Record<string, unknown>
}

export type PoolSnapshotSummary = {
  id: number
  group_name: string
  display_name: string
  source: string
  deployment_stage: string
  policy_profile: string
  member_count: number
  enabled_channels: number
  telemetry_coverage: number
  model_count: number
  open_models: number
  degraded_models: number
  known_cost_models: number
  unknown_cost_models: number
}

export type ModelSnapshot = {
  model_name: string
  metric_known: boolean
  metric_source?: string
  request_count: number
  success_count: number
  failure_count: number
  unknown_classification_count: number
  reliability_request_count: number
  reliability_failure_count: number
  average_latency_ms: number
  average_ttft_ms: number
  p50_latency_ms: number
  p95_latency_ms: number
  p99_latency_ms: number
  p50_ttft_ms: number
  p95_ttft_ms: number
  p99_ttft_ms: number
  p95_latency_known: boolean
  p95_ttft_known: boolean
  output_tokens: number
  generation_ms: number
  output_tokens_per_second: number
  err_4xx: number
  err_5xx: number
  err_429: number
  err_529: number
  inflight: number
  breaker_known: boolean
  breaker_state: string
  breaker_reason: string
  breaker_cooldown_until: number
  capacity_limited: boolean
  capacity_status_code: number
  capacity_cooldown_until_ms: number
  cost_known: boolean
  cost: number
  cost_confidence: string
  cost_updated_at: number
  cost_billing_mode: string
}

export type PoolMemberSnapshot = {
  id: number
  pool_id: number
  channel_id: number
  channel_name: string
  channel_type: number
  physical_status: number
  legacy_priority: number
  legacy_weight: number
  multi_key: boolean
  credential_count: number
  credentials_truncated: boolean
  credential_ids: number[]
  model_count: number
  models_truncated: boolean
  models: ModelSnapshot[]
  telemetry_known: boolean
}

export type PoolSnapshot = {
  id: number
  group_name: string
  display_name: string
  source: string
  deployment_stage: string
  policy_profile: string
  selector_policy: Record<string, unknown>
  balanced_policy: Record<string, unknown>
  canary_policy: Record<string, unknown>
  member_count: number
  members_truncated: boolean
  members: PoolMemberSnapshot[]
}

export type ErrorBudgetWindow = {
  window_seconds: number
  request_count: number
  failure_count: number
  unisolated_request_count: number
  unisolated_failure_count: number
  revision_isolated: boolean
  error_rate: number
  burn_rate: number
  minimum_volume: number
  sufficient: boolean
}

export type ErrorBudgetBurn = {
  pool_id: number
  policy_revision: number
  availability_target: number
  error_budget: number
  status: 'healthy' | 'warning' | 'critical' | 'insufficient_data'
  reason:
    | 'within_multi_window_budget'
    | 'fast_multi_window_burn'
    | 'slow_multi_window_burn'
    | 'insufficient_reliability_volume'
    | 'revision_isolation_unavailable'
  fast_short: ErrorBudgetWindow
  fast_long: ErrorBudgetWindow
  slow_short: ErrorBudgetWindow
  slow_long: ErrorBudgetWindow
  evaluated_at_ms: number
}

export type ErrorBudgetState = {
  evaluation: ErrorBudgetBurn
  policy_revision: number
  first_observed_at_ms: number
  last_changed_at_ms: number
  persisted: boolean
}

export type ChannelRoutingErrorBudgetResponse = {
  current: ErrorBudgetBurn
  persisted?: ErrorBudgetState
  snapshot_revision: number
  snapshot_built_at: number
}

export type ChannelSnapshot = {
  id: number
  name: string
  type: number
  status: number
  endpoint?: string
  endpoint_authority: string
  region: string
  endpoint_state: EndpointBreakerSource
  multi_key: boolean
  credential_count: number
  credentials_truncated: boolean
  credential_ids: number[]
  model_count: number
  models_truncated: boolean
  models: string[]
  auth_failure: boolean
  auth_failure_updated_at: number
  balance_known: boolean
  balance: number
  balance_updated_at: number
  cost_connector_enabled: boolean
  cost_sync_failures: number
  cost_sync_backoff_until: number
  cost_sync_error?: string
}

export type CostSnapshotSummary = {
  pool_id: number
  group_name: string
  member_id: number
  channel_id: number
  channel_name: string
  model_name: string
  known: boolean
  cost?: number
  display_rate?: number
  display_rate_basis?:
    | 'per_request'
    | 'model_price'
    | 'input_per_million'
    | string
  expression_pricing: boolean
  billing_mode?: string
  currency?: string
  unit?: string
  version?: string
  pricing_version?: string
  upstream_group?: string
  upstream_model?: string
  observed_time?: number
  effective_time?: number
  expires_time?: number
  confidence: string
  confidence_score: number
  freshness: string
  freshness_score: number
  source_sync_status: string
  source_sync_error?: string
  snapshot_time: number
  account?: RoutingCostAccount
}

export type CostSnapshot = Omit<
  CostSnapshotSummary,
  'display_rate' | 'display_rate_basis' | 'expression_pricing' | 'billing_mode'
> & {
  pricing?: RoutingNormalizedPricing
}

export type ChannelRoutingCostDetailResponse = {
  item: CostSnapshot
  snapshot_revision: number
  snapshot_built_at: number
}

export type RoutingCostBindingUpstreamType = 'newapi' | 'sub2api'

export type RoutingCostBindingCredentialMasks = {
  new_api_access_token?: string
  gateway_api_key?: string
  sub2api_email?: string
  sub2api_password?: string
  sub2api_token?: string
  custom_ca_configured?: boolean
}

export type RoutingCostBindingCredentials = {
  new_api_access_token?: string
  gateway_api_key?: string
  sub2api_email?: string
  sub2api_password?: string
  sub2api_token?: string
  custom_ca_pem?: string
}

export type RoutingCostBinding = {
  id: number
  channel_id: number
  channel_name?: string
  etag: string
  upstream_type: RoutingCostBindingUpstreamType
  base_url: string
  upstream_group: string
  serves_claude_code: boolean
  egress_allowed_private_cidrs?: string[] | null
  new_api_user_id?: number
  enabled: boolean
  sync_failure_count: number
  sync_backoff_until: number
  last_sync_error?: string
  credential_masks: RoutingCostBindingCredentialMasks
  credential_error?: string
  egress_policy_error?: string
  created_time: number
  updated_time: number
}

export type RoutingCostBindingRequest = {
  channel_id: number
  upstream_type: RoutingCostBindingUpstreamType
  base_url: string
  upstream_group: string
  serves_claude_code: boolean
  egress_allowed_private_cidrs: string[]
  new_api_user_id?: number
  enabled: boolean
  credentials: RoutingCostBindingCredentials
}

export type RoutingCostBindingPage = {
  items: RoutingCostBinding[]
  total: number
  page: number
  page_size: number
}

export type RoutingCostBindingGroupMetadata = {
  id: string
  name: string
  platform?: string
  subscription_type?: string
  claude_code_only: boolean
}

export type RoutingCostBindingActionResult = {
  channel_id: number
  upstream_type: RoutingCostBindingUpstreamType
  upstream_group?: string
  credential_ready?: boolean
  credential_test?: boolean
  groups: string[]
  group_meta?: Record<string, RoutingCostBindingGroupMetadata>
  groups_truncated?: boolean
  groups_total?: number
  model_count: number
  pricing_version?: string
  requires_sync?: boolean
  sync_task_type?: string
  serves_claude?: boolean
}

export type RoutingNormalizedPricing = {
  quota_type: number
  billing_mode: string
  currency: string
  unit: string
  group_ratio: number | null
  base_ratio: number | null
  completion_ratio: number | null
  model_price: number | null
  input_cost_per_million: number | null
  output_cost_per_million: number | null
  cache_read_cost_per_million: number | null
  cache_write_cost_per_million: number | null
  cache_write_1h_cost_per_million: number | null
  image_input_cost_per_million: number | null
  image_output_cost_per_million: number | null
  image_cost: number | null
  per_image_cost: number | null
  audio_input_cost_per_million: number | null
  audio_output_cost_per_million: number | null
  per_request_cost: number | null
  billing_expression: string
  tiers: unknown
  extras: unknown
}

export type RoutingCostAccount = {
  id: number
  source_type: string
  masked_identity: string
  status: string
  balance_known: boolean
  balance?: number
  balance_updated_at?: number
  last_sync_status: string
  last_sync_error?: string
}

export type RoutingCostBreakdown = {
  input: number
  output: number
  cache_read: number
  cache_write: number
  cache_write_1h: number
  image_input: number
  image_output: number
  image_units: number
  audio_input: number
  audio_output: number
  per_request: number
  expression: number
  total: number
}

export type RoutingCostEstimate = {
  known: boolean
  cost: number
  worst_case_known?: boolean
  worst_case_cost?: number
  effective_known?: boolean
  effective_cost?: number
  currency?: string
  unit?: string
  pricing_basis?: string
  pricing_hash?: string
  pricing_version?: string
  observed_time?: number
  effective_time?: number
  expires_time?: number
  version_confidence?: string
  freshness?: string
  source_sync_status?: string
  account_source_type?: string
  account_key_hash?: string
  confidence_score?: number
  freshness_score?: number
  expected_breakdown?: RoutingCostBreakdown
  worst_single_breakdown?: RoutingCostBreakdown
  updated_unix: number
}

export type DecisionCandidate = {
  pool_member_id: number
  channel_id: number
  eligible: boolean
  exclusion_reason?: string
  score: number
  availability: number
  latency: number
  throughput: number
  cost_score: number
  cost_known: boolean
  degraded: boolean
  open: boolean
  inflight: number
}

export type DecisionCandidateDetail = DecisionCandidate & {
  rank: number
  credential_id?: number
  business_tier?: number
  target_weight?: number
  confidence?: number
  freshness?: number
  slow_start_factor?: number
  capacity_utilization?: number
  queue_delay_ms?: number
  metric_updated_unix?: number
  request_count?: number
  success_count?: number
  reliability_request_count?: number
  reliability_failure_count?: number
  p95_latency_ms?: number
  p95_ttft_ms?: number
  output_tokens_per_second?: number
  exploration_eligible?: boolean
}

export type DecisionCandidatePage = {
  decision_id: string
  snapshot_revision: number
  items: DecisionCandidateDetail[]
  total: number
  available: number
  cursor: number
  next_cursor: number
  complete: boolean
  source: string
  request_count_known: boolean
  request_count_coverage: number
  total_request_count: number
  truncation_reason?: string
}

export type DecisionExclusionSummary = {
  reasons: Array<{ reason: string; count: number }>
}

export type ChannelRoutingDecisionSummary = {
  id: number
  decision_id: string
  request_id: string
  pool_id: number
  group_name: string
  model_name: string
  snapshot_revision: number
  runtime_generation: number
  activation_id: number
  activation_stage: string
  traffic_basis_points: number
  cohort?: string
  algorithm_version: string
  retry_index: number
  is_stream: boolean
  actual_channel_id: number
  observed_channel_id: number
  selected_member_id: number
  selected_credential_id: number
  candidate_count: number
  eligible_count: number
  filtered_open: number
  filtered_capacity: number
  breaker_bypassed: boolean
  observed_matches_actual: boolean
  difference_type?: string
  actual_cost_known: boolean
  actual_expected_cost: number
  observed_cost_known: boolean
  observed_expected_cost: number
  expected_cost_delta: number
  replayable: boolean
  created_time: number
}

export type ChannelRoutingReplayProfilesResponse = {
  items: ChannelRoutingDecisionSummary[]
  limit: number
}

export type ChannelRoutingDecision = {
  id: number
  decision_id: string
  request_id: string
  pool_id: number
  group_name: string
  model_name: string
  snapshot_revision: number
  runtime_generation: number
  policy_hash?: string
  snapshot_hash?: string
  profile_hash?: string
  algorithm_version: string
  seed: number
  retry_index: number
  is_stream: boolean
  actual_channel_id: number
  observed_channel_id: number
  candidate_count: number
  eligible_count: number
  filtered_open: number
  filtered_capacity: number
  breaker_bypassed: boolean
  observed_matches_actual: boolean
  difference_type?: string
  actual_cost_known: boolean
  actual_expected_cost: number
  observed_cost_known: boolean
  observed_expected_cost: number
  expected_cost_delta: number
  actual_cost_estimate?: RoutingCostEstimate
  observed_cost_estimate?: RoutingCostEstimate
  replayable: boolean
  gate?: {
    activation_id: number
    activation_stage: string
    policy_revision: number
    traffic_basis_points: number
    bucket: number
    in_canary: boolean
    rollout_key: string
  }
  cohort?: string
  selected_identity?: {
    snapshot_revision: number
    pool_id: number
    member_id: number
    credential_id: number
    channel_id: number
  }
  capacity_admission?: Record<string, unknown>
  exclusion_summary: DecisionExclusionSummary
  candidate_set: {
    truncated: boolean
    candidates: DecisionCandidate[]
  }
  attempt_timeline?: RoutingAttemptTimeline
  hedge?: RoutingAttemptTimeline
  created_time: number
}

export type RoutingAttempt = {
  node_epoch_id: string
  stable_node_id?: string
  stable_node_known: boolean
  policy_revision: number
  algorithm_version: string
  execution_mode: 'serial' | 'hedge' | string
  attempt_index: number
  role: 'serial' | 'primary' | 'secondary' | string
  state: string
  result: string
  winner: boolean
  member_id: number
  channel_id: number
  region: string
  endpoint_authority: string
  failure_domain_hash: string
  cost_known: boolean
  expected_cost?: number
  worst_case_cost?: number
  effective_cost?: number
  cost_currency?: string
  cost_unit?: string
  actual_cost_known: boolean
  actual_cost?: number
  actual_prompt_tokens?: number
  actual_completion_tokens?: number
  actual_total_tokens?: number
  actual_cache_read_tokens?: number
  actual_cache_write_tokens?: number
  actual_cache_write_1h_tokens?: number
  http_status?: number
  error_classification?: string
  error_responsibility?: string
  error_retryability?: string
  error_code?: string
  upstream_sent: boolean
  client_committed: boolean
  will_retry: boolean
  final_attempt: boolean
  first_byte_time_ms?: number
  started_time_ms: number
  completed_time_ms?: number
  duration_ms?: number
}

export type RoutingAttemptTimeline = {
  attempt_count: number
  attempts_truncated: boolean
  all_attempts_completed: boolean
  winner_role?: string
  final_member_id?: number
  final_channel_id?: number
  final_region?: string
  final_node_epoch_id?: string
  final_stable_node_id?: string
  final_stable_node_known: boolean
  final_result?: string
  final_http_status?: number
  final_error_classification?: string
  final_error_responsibility?: string
  estimated_total_cost_known: boolean
  estimated_total_cost?: number
  worst_case_total_cost_known: boolean
  worst_case_total_cost?: number
  duplicate_expected_cost_known: boolean
  duplicate_expected_cost?: number
  duplicate_worst_case_cost_known: boolean
  duplicate_worst_case_cost?: number
  actual_total_cost_known: boolean
  actual_total_cost?: number
  duplicate_actual_cost_known: boolean
  duplicate_actual_cost?: number
  cost_currency?: string
  cost_unit?: string
  attempts: RoutingAttempt[]
}

export type RoutingHedgeAttempt = RoutingAttempt
export type RoutingHedgeDecisionAudit = RoutingAttemptTimeline

export type RoutingProbeResult = {
  id: number
  probe_id: string
  probe_type: string
  snapshot_revision: number
  pool_id: number
  member_id: number
  channel_id: number
  credential_id: number
  group_name: string
  model_name: string
  endpoint_host: string
  endpoint_authority?: string
  region?: string
  breaker_scope?: string
  evidence_count?: number
  node_count?: number
  breaker_state: string
  breaker_reason?: string
  breaker_cooldown_until?: number
  breaker_updated_at?: number
  outcome: string
  responsibility: string
  status_code: number
  error_code: string
  error_message: string
  latency_ms: number
  started_time_ms: number
  finished_time_ms: number
  node_epoch_id: string
  created_time: number
}

export type EndpointBreakerSource = {
  known: boolean
  state: string
  reason: string
  cooldown_until: number
  updated_at: number
  expires_at?: number
  evidence_count?: number
  network_failure_count?: number
  node_count?: number
  failure_node_count?: number
}

export type EndpointBreaker = {
  endpoint_authority: string
  region: string
  local: EndpointBreakerSource
  shared: EndpointBreakerSource
  effective: EndpointBreakerSource
}

export type EndpointBreakerPage = {
  items: EndpointBreaker[]
  total: number
  page: number
  page_size: number
  region: string
  stable_node_id: string
  endpoint_quorum_eligible: boolean
}

export type PolicyMemberDocument = {
  member_id: number
  channel_id: number
  enabled: boolean
  priority: number
  weight: number
  credential_ids: number[]
  overrides: Record<string, unknown>
  [key: string]: unknown
}

export type PolicyPoolDocument = {
  pool_id: number
  group_name: string
  display_name: string
  deployment_stage: 'observe' | 'shadow' | 'canary' | 'active'
  policy_profile:
    | 'balanced'
    | 'reliability_first'
    | 'cost_aware'
    | 'enterprise_slo'
    | 'custom'
  policy: Record<string, unknown>
  members: PolicyMemberDocument[]
  [key: string]: unknown
}

export type PolicyDocument = {
  schema_version: number
  pools: PolicyPoolDocument[]
  [key: string]: unknown
}

export type PolicyDraftSummary = {
  id: number
  base_revision: number
  base_hash: string
  version: number
  etag: string
  document_hash: string
  status: string
  created_by: number
  updated_by: number
  validated_head_revision: number
  validated_head_hash: string
  published_revision: number
  created_time_ms: number
  updated_time_ms: number
  validated_time_ms: number
  published_time_ms: number
}

export type PolicyDraftDetail = PolicyDraftSummary & {
  document: PolicyDocument
  server_etag: string
}

export type PolicyActivationSpec = {
  stage: 'observe' | 'shadow' | 'canary' | 'active'
  traffic_basis_points: number
  reason: string
}

export type RoutingPolicyHead = {
  id: number
  current_revision: number
  current_activation_id: number
  current_hash: string
  current_stage: string
  created_time: number
  updated_time: number
}

export type RoutingPolicyRevision = {
  revision: number
  parent_revision: number
  rollback_of_revision: number
  schema_version: number
  content_hash: string
  pool_count: number
  member_count: number
  actor_id: number
  reason: string
  created_time: number
}

export type RoutingPolicyActivation = {
  id: number
  revision: number
  previous_revision: number
  rollback_of_revision: number
  stage: string
  traffic_basis_points: number
  actor_id: number
  reason: string
  created_time: number
}

export type RoutingConfigOutbox = {
  id: number
  event_id: string
  revision: number
  event_type: string
  payload_hash: string
  created_time: number
  published_time: number
  attempts: number
  next_attempt_time: number
  last_error: string
}

export type RoutingPolicyPublishResult = {
  revision: RoutingPolicyRevision
  activation: RoutingPolicyActivation
  outbox: RoutingConfigOutbox
}

export type CurrentRoutingPolicy = {
  head: RoutingPolicyHead
  revision?: RoutingPolicyRevision
  document: PolicyDocument
  server_etag: string
}

export type RoutingPolicyRevisionDetail = {
  revision: RoutingPolicyRevision
  document: PolicyDocument
}

export type RoutingOperation<Result = unknown> = {
  id: number
  type: string
  idempotency_hash: string
  system_task_id?: string
  evaluation_hash: string
  subject_type: string
  subject_id: number
  pool_id: number
  expected_revision: number
  expected_activation_id: number
  actor_id: number
  reason: string
  status: string
  claim_until_ms: number
  attempts: number
  next_retry_ms: number
  last_error: string
  result_revision: number
  result_activation_id: number
  result_outbox_id: number
  result_payload_hash: string
  created_time_ms: number
  updated_time_ms: number
  completed_time_ms: number
  result?: Result
}

export type ChannelRoutingCostSyncSummary = {
  bindings: number
  accounts: number
  snapshots: number
  versions_created: number
  metrics: number
  breakers: number
  loaded_breakers: number
  errors: number
  partial_accounts: number
  skipped_backoff: number
  stale_bindings: number
}

export type ChannelRoutingCostSyncResult = {
  system_task_id: string
  system_task_type: string
  task_status: string
  created?: boolean
  execution_state: 'completed' | 'partial' | string
  summary?: Partial<ChannelRoutingCostSyncSummary>
}

export type ChannelRoutingBreakerResetRequest =
  | {
      scope: 'member'
      pool_id: number
      member_id: number
      model_name: string
      reason?: string
    }
  | {
      scope: 'endpoint'
      endpoint_authority: string
      region: string
      reason?: string
    }

export type ChannelRoutingBreakerResetTarget =
  | {
      scope: 'member'
      pool_id: number
      member_id: number
      channel_id: number
      api_key_index: number
      model_name: string
      group_name: string
    }
  | {
      scope: 'endpoint'
      endpoint_host: string
      endpoint_authority: string
      region: string
    }

export type ChannelRoutingBreakerResetResult = {
  scope: 'member' | 'endpoint'
  generation: number
  outbox_id: number
  target: ChannelRoutingBreakerResetTarget
}

export type ChannelRoutingBreakerResetResponse = {
  operation: RoutingOperation<ChannelRoutingBreakerResetResult>
  target: ChannelRoutingBreakerResetTarget
}

export type ChannelRoutingActiveProbeStats = {
  cycles: number
  targets_considered: number
  targets_selected: number
  skipped_not_due: number
  skipped_budget: number
  lease_contended: number
  lease_errors: number
  executed: number
  succeeded: number
  failed: number
  timed_out: number
  canceled: number
  local_errors: number
  persistence_errors: number
  completion_errors: number
  effect_errors: number
  reserved_tokens: number
  reserved_cost_nano_usd: number
  inflight: number
  max_inflight: number
}

export type ChannelRoutingActiveProbeResult = {
  enabled: boolean
  stats: ChannelRoutingActiveProbeStats
}

export type ChannelRoutingAuditExport = {
  export_id: string
  operation_id: number
  actor_id: number
  from_time: number
  to_time: number
  record_count: number
  content_bytes: number
  content_hash: string
  chunk_count: number
  created_time_ms: number
  expires_time_ms: number
}

export type ChannelRoutingAuditExportResult = {
  export_id: string
  record_count: number
  content_bytes: number
  content_hash: string
  created_time_ms: number
  expires_time_ms: number
}

export type ChannelRoutingAuditExportResponse = {
  export: ChannelRoutingAuditExport
  operation: RoutingOperation<ChannelRoutingAuditExportResult>
}

export type PolicySimulationResponse = {
  draft: PolicyDraftSummary
  operation: RoutingOperation
  result: HistoricalSimulationResult
}

export type HistoricalSimulationResponse = {
  operation: RoutingOperation<HistoricalSimulationResult>
  result: HistoricalSimulationResult
}

export type PolicyApproval = {
  id: number
  draft_id: number
  draft_version: number
  draft_etag: string
  document_hash: string
  head_revision: number
  head_hash: string
  activation_stage: string
  activation_traffic_basis_points: number
  activation_reason_hash: string
  activation_hash: string
  actor_id: number
  created_time_ms: number
}

export type PolicyApprovalGroup = {
  activation_hash: string
  activation_stage: string
  activation_traffic_basis_points: number
  activation_reason_hash: string
  count: number
  quorum: boolean
}

export type PolicyApprovalList = {
  items: PolicyApproval[]
  groups: PolicyApprovalGroup[]
  requires_approval: boolean
  required: number
  count: number
  quorum: boolean
  target_activation_hash?: string
}

export type PolicyApprovalResponse = {
  approval: PolicyApproval
  created: boolean
}

export type PolicyPublishResponse = {
  draft: PolicyDraftSummary
  published: RoutingPolicyPublishResult
  operation: RoutingOperation
}

export type PolicyRollbackApproval = {
  id: number
  expected_revision: number
  expected_activation_id: number
  expected_head_hash: string
  target_revision: number
  target_content_hash: string
  activation_stage: string
  activation_traffic_basis_points: number
  activation_reason_hash: string
  activation_hash: string
  actor_id: number
  created_time_ms: number
}

export type PolicyRollbackApprovalList = {
  items: PolicyRollbackApproval[]
  groups: PolicyApprovalGroup[]
  requires_approval: boolean
  required: number
  count: number
  quorum: boolean
  expected_revision: number
  target_revision: number
  target_activation_hash?: string
}

export type PolicyRollbackApprovalResponse = {
  approval: PolicyRollbackApproval
  created: boolean
}

export type PolicyRollbackResponse = {
  published: RoutingPolicyPublishResult
  operation: RoutingOperation
}

export type HistoricalSimulationResult = {
  pool_id: number
  cursor: number
  next_cursor: number
  limit: number
  scanned_samples: number
  evaluated_samples: number
  actual_match_count: number
  actual_match_rate?: number
  selection_changed_count: number
  selection_change_rate?: number
  cost_known_samples: number
  total_expected_cost_delta: number
  average_expected_cost_delta?: number
  skip_reasons: Record<string, number>
  samples: Array<{
    decision_id: string
    created_time: number
    algorithm_version: string
    actual_channel_id: number
    baseline_channel_id: number
    simulated_channel_id: number
    matches_actual: boolean
    selection_changed: boolean
    baseline_cost_known: boolean
    baseline_expected_cost: number
    simulated_cost_known: boolean
    simulated_expected_cost: number
    expected_cost_delta: number
    counterfactual_hash: string
  }>
  skipped: Array<{ decision_id: string; reason: string }>
  risk?: PolicySimulationRiskAssessment
}

export type PolicySimulationImpactScope = {
  affected_pool_count: number
  affected_pool_ids: number[]
  unsimulated_pool_count: number
  unsimulated_pool_ids: number[]
  affected_channel_count: number
  affected_channel_ids: number[]
  affected_model_count: number
  affected_models: string[]
  model_evidence_state: string
  truncated: boolean
}

export type PolicySimulationStructuralChanges = {
  added_pools: number
  removed_pools: number
  policy_changes: number
  display_name_changes: number
  group_changes: number
  deployment_stage_changes: number
  policy_profile_changes: number
  policy_config_changes: number
  added_members: number
  removed_members: number
  changed_members: number
  member_channel_changes: number
  member_enablement_changes: number
  member_priority_changes: number
  member_weight_changes: number
  member_credential_changes: number
  member_override_changes: number
  traffic_affecting: boolean
}

export type PolicySimulationSLOImpact = {
  state: 'pass' | 'fail' | 'unknown' | string
  known_samples: number
  total_samples: number
  average_success_rate_delta?: number
  average_latency_delta_ms?: number
  latency_metric?: string
  assessment: string
}

export type PolicySimulationCapacityAssessment = {
  state: 'pass' | 'fail' | 'unknown' | string
  known_samples: number
  total_samples: number
  exceeded_samples: number
  max_observed_utilization?: number
  utilization_limit?: number
}

export type PolicySimulationTrafficRateAssessment = {
  state: 'pass' | 'fail' | 'unknown' | string
  estimated_selection_change_rate?: number
  configured_rate_limit?: number
  reason?: string
}

export type PolicySimulationRiskAssessment = {
  state: 'pass' | 'fail' | 'unknown' | string
  reasons: string[]
  scope: PolicySimulationImpactScope
  changes: PolicySimulationStructuralChanges
  slo: PolicySimulationSLOImpact
  capacity: PolicySimulationCapacityAssessment
  traffic_change_rate: PolicySimulationTrafficRateAssessment
}

export type DecisionReplayResult = {
  decision_id: string
  pool_id: number
  snapshot_revision: number
  runtime_generation: number
  algorithm_version: string
  actual_channel_id: number
  stored_channel_id: number
  replayed_channel_id: number
  difference_type: string
  audit_verified: boolean
  gate_verified: boolean
  result: unknown
}

export type PagedResponse<T> = {
  items: T[]
  total: number
  page: number
  page_size: number
  snapshot_revision: number
  snapshot_built_at: number
}

export type CursorResponse<T> = {
  items: T[]
  next_cursor: number | string
  limit?: number
  has_more?: boolean
}
