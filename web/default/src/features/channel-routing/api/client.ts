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
import { isAxiosError } from 'axios'

import { api } from '@/lib/api'

import type {
  ApiEnvelope,
  ChannelRoutingActiveProbeResult,
  ChannelRoutingAuditExportResponse,
  ChannelRoutingBreakerResetRequest,
  ChannelRoutingBreakerResetResponse,
  ChannelRoutingControlAuditPage,
  ChannelRoutingCostDetailResponse,
  ChannelRoutingDecision,
  ChannelRoutingDecisionSummary,
  ChannelRoutingErrorBudgetResponse,
  ChannelRoutingNodePage,
  ChannelRoutingOverview,
  ChannelRoutingReplayProfilesResponse,
  ChannelRoutingRuntimeSettings,
  ChannelSnapshot,
  CostSnapshotSummary,
  CurrentRoutingPolicy,
  CursorResponse,
  DecisionReplayResult,
  DecisionCandidatePage,
  EndpointBreakerPage,
  HistoricalSimulationResponse,
  PagedResponse,
  PolicyActivationSpec,
  PolicyApprovalList,
  PolicyApprovalResponse,
  PolicyDocument,
  PolicyDraftDetail,
  PolicyDraftSummary,
  PolicyPublishResponse,
  PolicyRollbackApprovalList,
  PolicyRollbackApprovalResponse,
  PolicyRollbackResponse,
  PolicySimulationResponse,
  PoolSnapshot,
  PoolSnapshotSummary,
  RoutingChannelConfiguration,
  RoutingChannelConfigurationCostSource,
  RoutingChannelConfigurationPage,
  RoutingChannelConfigurationTrafficClass,
  RoutingChannelConfigurationUpdate,
  RoutingOperation,
  RoutingPolicyRevisionDetail,
  RoutingProbeOutcome,
  RoutingProbeResult,
  SmartRoutingSetting,
} from '../types'

const requestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
  timeout: 15_000,
} as const

function unwrap<T>(response: ApiEnvelope<T>): T {
  if (!response.success) {
    throw new Error(response.message || 'Channel routing request failed')
  }
  return response.data
}

type ChannelRoutingConfigurationErrorPayload = {
  code?: string
  message?: string
  detail?: string
  field?: string
  reason?: string
  conflict?: {
    current?: RoutingChannelConfiguration | null
    current_etag?: string
  }
}

type ChannelRoutingRuntimeSettingsErrorPayload = {
  code?: string
  message?: string
  field?: string
  reason?: string
  field_errors?: Partial<Record<keyof SmartRoutingSetting, string>>
  conflict?: {
    current?: ChannelRoutingRuntimeSettings | null
    current_etag?: string
  }
}

export type ChannelRoutingRuntimeSettingsApiError = {
  status?: number
  code?: string
  message?: string
  field?: string
  reason?: string
  fieldErrors?: Partial<Record<keyof SmartRoutingSetting, string>>
}

export class ChannelRoutingRuntimeSettingsConflictError extends Error {
  current: ChannelRoutingRuntimeSettings | null
  currentETag: string

  constructor(
    current: ChannelRoutingRuntimeSettings | null,
    currentETag: string
  ) {
    super('Channel routing runtime settings changed')
    this.name = 'ChannelRoutingRuntimeSettingsConflictError'
    this.current = current
    this.currentETag = currentETag
  }
}

export function getChannelRoutingRuntimeSettingsApiError(
  error: unknown
): ChannelRoutingRuntimeSettingsApiError {
  if (!isAxiosError<ChannelRoutingRuntimeSettingsErrorPayload>(error)) return {}
  return {
    status: error.response?.status,
    code: error.response?.data?.code,
    message: error.response?.data?.message,
    field: error.response?.data?.field,
    reason: error.response?.data?.reason,
    fieldErrors: error.response?.data?.field_errors,
  }
}

function runtimeSettingsConflictFromError(
  error: unknown
): ChannelRoutingRuntimeSettingsConflictError | null {
  if (!isAxiosError<ChannelRoutingRuntimeSettingsErrorPayload>(error)) {
    return null
  }
  const response = error.response
  if (
    response?.status !== 409 ||
    response.data?.code !== 'runtime_settings_conflict'
  ) {
    return null
  }
  const current = response.data.conflict?.current ?? null
  const currentETag =
    response.data.conflict?.current_etag || current?.etag || ''
  return new ChannelRoutingRuntimeSettingsConflictError(current, currentETag)
}

export type ChannelRoutingConfigurationApiError = {
  status?: number
  code?: string
  message?: string
  detail?: string
  field?: string
  reason?: string
}

export class ChannelRoutingConfigurationConflictError extends Error {
  current: RoutingChannelConfiguration | null
  currentETag: string

  constructor(
    current: RoutingChannelConfiguration | null,
    currentETag: string
  ) {
    super('Channel routing configuration changed')
    this.name = 'ChannelRoutingConfigurationConflictError'
    this.current = current
    this.currentETag = currentETag
  }
}

export function getChannelRoutingConfigurationApiError(
  error: unknown
): ChannelRoutingConfigurationApiError {
  if (!isAxiosError<ChannelRoutingConfigurationErrorPayload>(error)) return {}
  return {
    status: error.response?.status,
    code: error.response?.data?.code,
    message: error.response?.data?.message,
    detail: error.response?.data?.detail,
    field: error.response?.data?.field,
    reason: error.response?.data?.reason,
  }
}

function configurationConflictFromError(
  error: unknown
): ChannelRoutingConfigurationConflictError | null {
  if (!isAxiosError<ChannelRoutingConfigurationErrorPayload>(error)) {
    return null
  }
  const response = error.response
  if (
    response?.status !== 409 ||
    response.data?.code !== 'channel_configuration_conflict'
  ) {
    return null
  }
  const current = response.data.conflict?.current ?? null
  const currentETag =
    response.data.conflict?.current_etag || current?.etag || ''
  return new ChannelRoutingConfigurationConflictError(current, currentETag)
}

export async function getChannelRoutingOverview(): Promise<ChannelRoutingOverview> {
  const response = await api.get<ApiEnvelope<ChannelRoutingOverview>>(
    '/api/channel-routing/overview',
    requestConfig
  )
  return unwrap(response.data)
}

export async function getChannelRoutingRuntimeSettings(): Promise<ChannelRoutingRuntimeSettings> {
  const response = await api.get<ApiEnvelope<ChannelRoutingRuntimeSettings>>(
    '/api/channel-routing/runtime-settings',
    requestConfig
  )
  const settings = unwrap(response.data)
  return { ...settings, etag: response.headers.etag || settings.etag }
}

export async function updateChannelRoutingRuntimeSettings(
  current: Pick<ChannelRoutingRuntimeSettings, 'etag'>,
  settings: SmartRoutingSetting,
  signal?: AbortSignal
): Promise<ChannelRoutingRuntimeSettings> {
  try {
    const response = await api.put<ApiEnvelope<ChannelRoutingRuntimeSettings>>(
      '/api/channel-routing/runtime-settings',
      settings,
      {
        ...requestConfig,
        signal,
        headers: { 'If-Match': current.etag },
      }
    )
    const updated = unwrap(response.data)
    return { ...updated, etag: response.headers.etag || updated.etag }
  } catch (error) {
    const conflict = runtimeSettingsConflictFromError(error)
    if (conflict) throw conflict
    throw error
  }
}

export async function listChannelRoutingControlAudits(params: {
  limit: number
  before_id?: number
  subject_type?: string
  subject_id?: number
  actor_id?: number
}): Promise<ChannelRoutingControlAuditPage> {
  const response = await api.get<ApiEnvelope<ChannelRoutingControlAuditPage>>(
    '/api/channel-routing/control-audits',
    { ...requestConfig, params }
  )
  return unwrap(response.data)
}

export async function listChannelRoutingNodes(params: {
  limit: number
  cursor?: string
}): Promise<ChannelRoutingNodePage> {
  const response = await api.get<ApiEnvelope<ChannelRoutingNodePage>>(
    '/api/channel-routing/nodes',
    { ...requestConfig, params }
  )
  return unwrap(response.data)
}

export async function listChannelRoutingGroups(params: {
  page: number
  page_size: number
  search?: string
}): Promise<PagedResponse<PoolSnapshotSummary>> {
  const response = await api.get<
    ApiEnvelope<PagedResponse<PoolSnapshotSummary>>
  >('/api/channel-routing/groups', { ...requestConfig, params })
  return unwrap(response.data)
}

export async function getChannelRoutingGroup(
  id: number,
  params: {
    page: number
    page_size: number
    model_limit: number
    credential_limit: number
  }
): Promise<{
  group: PoolSnapshot
  summary: PoolSnapshotSummary
  page: number
  page_size: number
  next_page: number
  model_limit: number
  credential_limit: number
  nested_item_budget: number
  snapshot_revision: number
  snapshot_built_at: number
}> {
  const response = await api.get<
    ApiEnvelope<{
      group: PoolSnapshot
      summary: PoolSnapshotSummary
      page: number
      page_size: number
      next_page: number
      model_limit: number
      credential_limit: number
      nested_item_budget: number
      snapshot_revision: number
      snapshot_built_at: number
    }>
  >(`/api/channel-routing/groups/${id}`, { ...requestConfig, params })
  return unwrap(response.data)
}

export async function listChannelRoutingGroupReplayProfiles(
  id: number,
  limit = 20
): Promise<ChannelRoutingReplayProfilesResponse> {
  const response = await api.get<
    ApiEnvelope<ChannelRoutingReplayProfilesResponse>
  >(`/api/channel-routing/groups/${id}/replay-profiles`, {
    ...requestConfig,
    params: { limit },
  })
  return unwrap(response.data)
}

export async function getChannelRoutingGroupErrorBudget(
  id: number
): Promise<ChannelRoutingErrorBudgetResponse> {
  const response = await api.get<
    ApiEnvelope<ChannelRoutingErrorBudgetResponse>
  >(`/api/channel-routing/groups/${id}/error-budget`, requestConfig)
  return unwrap(response.data)
}

export async function listChannelRoutingChannels(params: {
  page: number
  page_size: number
  search?: string
  status?: number
  type?: number
}): Promise<PagedResponse<ChannelSnapshot>> {
  const response = await api.get<ApiEnvelope<PagedResponse<ChannelSnapshot>>>(
    '/api/channel-routing/channels',
    { ...requestConfig, params }
  )
  return unwrap(response.data)
}

export async function listChannelRoutingEndpoints(params: {
  page: number
  page_size: number
  search?: string
  region?: string
}): Promise<EndpointBreakerPage> {
  const response = await api.get<ApiEnvelope<EndpointBreakerPage>>(
    '/api/channel-routing/endpoints',
    { ...requestConfig, params }
  )
  return unwrap(response.data)
}

export async function resetChannelRoutingBreaker(
  payload: ChannelRoutingBreakerResetRequest,
  idempotencyKey: string
): Promise<ChannelRoutingBreakerResetResponse> {
  const response = await api.post<
    ApiEnvelope<ChannelRoutingBreakerResetResponse>
  >('/api/channel-routing/breakers/reset', payload, {
    ...requestConfig,
    headers: { 'Idempotency-Key': idempotencyKey },
  })
  return unwrap(response.data)
}

export async function listChannelRoutingCosts(params: {
  page: number
  page_size: number
  group?: string
  model?: string
  known?: boolean
}): Promise<PagedResponse<CostSnapshotSummary>> {
  const response = await api.get<
    ApiEnvelope<PagedResponse<CostSnapshotSummary>>
  >('/api/channel-routing/costs', { ...requestConfig, params })
  return unwrap(response.data)
}

export async function getChannelRoutingCostDetail(
  poolId: number,
  memberId: number,
  model: string
): Promise<ChannelRoutingCostDetailResponse> {
  const response = await api.get<ApiEnvelope<ChannelRoutingCostDetailResponse>>(
    `/api/channel-routing/costs/${poolId}/${memberId}`,
    { ...requestConfig, params: { model } }
  )
  return unwrap(response.data)
}

export async function listChannelRoutingConfigurations(params: {
  page: number
  page_size: number
  search?: string
  cost_confirmed?: boolean
  cost_source?: RoutingChannelConfigurationCostSource
  traffic_class?: RoutingChannelConfigurationTrafficClass
}): Promise<RoutingChannelConfigurationPage> {
  const response = await api.get<ApiEnvelope<RoutingChannelConfigurationPage>>(
    '/api/channel-routing/channel-configurations',
    { ...requestConfig, params }
  )
  return unwrap(response.data)
}

export async function getChannelRoutingConfiguration(
  channelId: number
): Promise<RoutingChannelConfiguration> {
  const response = await api.get<ApiEnvelope<RoutingChannelConfiguration>>(
    `/api/channel-routing/channel-configurations/${channelId}`,
    requestConfig
  )
  const configuration = unwrap(response.data)
  return {
    ...configuration,
    etag: response.headers.etag || configuration.etag,
  }
}

export async function updateChannelRoutingConfiguration(
  configuration: Pick<RoutingChannelConfiguration, 'channel_id' | 'etag'>,
  request: RoutingChannelConfigurationUpdate,
  signal?: AbortSignal
): Promise<RoutingChannelConfiguration> {
  try {
    const response = await api.put<ApiEnvelope<RoutingChannelConfiguration>>(
      `/api/channel-routing/channel-configurations/${configuration.channel_id}`,
      request,
      {
        ...requestConfig,
        signal,
        headers: { 'If-Match': configuration.etag },
      }
    )
    const updated = unwrap(response.data)
    return { ...updated, etag: response.headers.etag || updated.etag }
  } catch (error) {
    const conflict = configurationConflictFromError(error)
    if (conflict) throw conflict
    throw error
  }
}

export async function listChannelRoutingProbes(params: {
  limit: number
  pool_id?: number
  channel_id?: number
  outcome?: RoutingProbeOutcome
  cursor?: number
}): Promise<CursorResponse<RoutingProbeResult>> {
  const response = await api.get<
    ApiEnvelope<CursorResponse<RoutingProbeResult>>
  >('/api/channel-routing/probes', { ...requestConfig, params })
  return unwrap(response.data)
}

export async function runChannelRoutingActiveProbe(
  idempotencyKey: string
): Promise<RoutingOperation<ChannelRoutingActiveProbeResult>> {
  const response = await api.post<
    ApiEnvelope<RoutingOperation<ChannelRoutingActiveProbeResult>>
  >('/api/channel-routing/probes/run', undefined, {
    ...requestConfig,
    headers: { 'Idempotency-Key': idempotencyKey },
  })
  return unwrap(response.data)
}

export async function createChannelRoutingAuditExport(
  payload: { from_time: number; to_time: number; limit: number },
  idempotencyKey: string
): Promise<ChannelRoutingAuditExportResponse> {
  const response = await api.post<
    ApiEnvelope<ChannelRoutingAuditExportResponse>
  >('/api/channel-routing/audit-exports', payload, {
    ...requestConfig,
    headers: { 'Idempotency-Key': idempotencyKey },
  })
  return unwrap(response.data)
}

export async function downloadChannelRoutingAuditExport(
  exportId: string
): Promise<void> {
  const response = await api.get<Blob>(
    `/api/channel-routing/audit-exports/${encodeURIComponent(exportId)}/download`,
    {
      ...requestConfig,
      responseType: 'blob',
      disableDuplicate: true,
      timeout: 60_000,
    }
  )
  const url = URL.createObjectURL(response.data)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `channel-routing-audit-${exportId}.json`
  anchor.hidden = true
  document.body.append(anchor)
  anchor.click()
  anchor.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 0)
}

export async function listChannelRoutingDecisions(params: {
  limit: number
  cursor?: number
  group?: string
  model?: string
  request_id?: string
  matched?: boolean
  replayable?: boolean
  activation_id?: number
  cohort?: string
  from_time?: number
  to_time?: number
}): Promise<CursorResponse<ChannelRoutingDecisionSummary>> {
  const response = await api.get<
    ApiEnvelope<CursorResponse<ChannelRoutingDecisionSummary>>
  >('/api/channel-routing/decisions', { ...requestConfig, params })
  return unwrap(response.data)
}

export async function getChannelRoutingDecision(
  id: string
): Promise<ChannelRoutingDecision> {
  const response = await api.get<ApiEnvelope<ChannelRoutingDecision>>(
    `/api/channel-routing/decisions/${encodeURIComponent(id)}`,
    requestConfig
  )
  return unwrap(response.data)
}

export async function listChannelRoutingDecisionCandidates(
  id: string,
  params: { cursor: number; limit: number }
): Promise<DecisionCandidatePage> {
  const response = await api.get<ApiEnvelope<DecisionCandidatePage>>(
    `/api/channel-routing/decisions/${encodeURIComponent(id)}/candidates`,
    { ...requestConfig, params }
  )
  return unwrap(response.data)
}

export async function replayChannelRoutingDecision(
  id: string
): Promise<DecisionReplayResult> {
  const response = await api.post<ApiEnvelope<DecisionReplayResult>>(
    `/api/channel-routing/decisions/${encodeURIComponent(id)}/replay`,
    undefined,
    requestConfig
  )
  return unwrap(response.data)
}

export async function simulateChannelRoutingGroup(
  id: number,
  payload: {
    cursor?: number
    limit: number
    selector: Record<string, number | undefined>
  },
  idempotencyKey: string
): Promise<HistoricalSimulationResponse> {
  const response = await api.post<ApiEnvelope<HistoricalSimulationResponse>>(
    `/api/channel-routing/groups/${id}/simulations`,
    payload,
    {
      ...requestConfig,
      headers: { 'Idempotency-Key': idempotencyKey },
    }
  )
  return unwrap(response.data)
}

export async function listChannelRoutingPolicyDrafts(params: {
  limit: number
  cursor?: number
}): Promise<CursorResponse<PolicyDraftSummary>> {
  const response = await api.get<
    ApiEnvelope<CursorResponse<PolicyDraftSummary>>
  >('/api/channel-routing/policy-drafts', { ...requestConfig, params })
  return unwrap(response.data)
}

export async function getChannelRoutingPolicyDraft(
  id: number,
  signal?: AbortSignal
): Promise<PolicyDraftDetail> {
  const response = await api.get<
    ApiEnvelope<Omit<PolicyDraftDetail, 'server_etag'>>
  >(`/api/channel-routing/policy-drafts/${id}`, {
    ...requestConfig,
    signal,
  })
  return {
    ...unwrap(response.data),
    server_etag: response.headers.etag || '',
  }
}

export async function createChannelRoutingPolicyDraft(payload: {
  base_revision: number
  document: PolicyDocument
}): Promise<PolicyDraftSummary> {
  const response = await api.post<ApiEnvelope<PolicyDraftSummary>>(
    '/api/channel-routing/policy-drafts',
    payload,
    requestConfig
  )
  return unwrap(response.data)
}

type PolicyDraftWriteAuthority = PolicyDraftSummary & {
  server_etag?: string
}

function policyDraftIfMatch(draft: PolicyDraftWriteAuthority): string {
  const responseETag = draft.server_etag?.trim()
  if (responseETag) return responseETag
  return `"crd.${draft.id}.${draft.version}.${draft.etag}"`
}

export async function updateChannelRoutingPolicyDraft(
  draft: PolicyDraftWriteAuthority,
  document: PolicyDocument
): Promise<PolicyDraftSummary> {
  const response = await api.put<ApiEnvelope<PolicyDraftSummary>>(
    `/api/channel-routing/policy-drafts/${draft.id}`,
    { document },
    {
      ...requestConfig,
      headers: { 'If-Match': policyDraftIfMatch(draft) },
    }
  )
  return unwrap(response.data)
}

export async function validateChannelRoutingPolicyDraft(
  draft: PolicyDraftSummary
): Promise<PolicyDraftSummary> {
  const response = await api.post<ApiEnvelope<PolicyDraftSummary>>(
    `/api/channel-routing/policy-drafts/${draft.id}/validate`,
    undefined,
    {
      ...requestConfig,
      headers: { 'If-Match': policyDraftIfMatch(draft) },
    }
  )
  return unwrap(response.data)
}

export async function simulateChannelRoutingPolicyDraft(
  draft: PolicyDraftSummary,
  payload: { pool_id: number; cursor?: number; limit: number },
  idempotencyKey: string
): Promise<PolicySimulationResponse> {
  const response = await api.post<ApiEnvelope<PolicySimulationResponse>>(
    `/api/channel-routing/policy-drafts/${draft.id}/simulate`,
    payload,
    {
      ...requestConfig,
      headers: {
        'If-Match': policyDraftIfMatch(draft),
        'Idempotency-Key': idempotencyKey,
      },
    }
  )
  return unwrap(response.data)
}

export async function listChannelRoutingPolicyApprovals(
  draftId: number,
  activation?: PolicyActivationSpec
): Promise<PolicyApprovalList> {
  const params = activation
    ? {
        stage: activation.stage,
        traffic_basis_points: activation.traffic_basis_points,
        reason: activation.reason,
      }
    : undefined
  const response = await api.get<ApiEnvelope<PolicyApprovalList>>(
    `/api/channel-routing/policy-drafts/${draftId}/approvals`,
    { ...requestConfig, params }
  )
  return unwrap(response.data)
}

export async function approveChannelRoutingPolicyDraft(
  draft: PolicyDraftSummary,
  activation: PolicyActivationSpec
): Promise<PolicyApprovalResponse> {
  const response = await api.post<ApiEnvelope<PolicyApprovalResponse>>(
    `/api/channel-routing/policy-drafts/${draft.id}/approvals`,
    activation,
    {
      ...requestConfig,
      headers: { 'If-Match': policyDraftIfMatch(draft) },
    }
  )
  return unwrap(response.data)
}

export async function publishChannelRoutingPolicyDraft(
  draft: PolicyDraftSummary,
  activation: PolicyActivationSpec,
  idempotencyKey: string
): Promise<PolicyPublishResponse> {
  const response = await api.post<ApiEnvelope<PolicyPublishResponse>>(
    `/api/channel-routing/policy-drafts/${draft.id}/publish`,
    activation,
    {
      ...requestConfig,
      headers: {
        'If-Match': policyDraftIfMatch(draft),
        'Idempotency-Key': idempotencyKey,
      },
    }
  )
  return unwrap(response.data)
}

export async function getCurrentChannelRoutingPolicy(): Promise<CurrentRoutingPolicy> {
  const response = await api.get<
    ApiEnvelope<Omit<CurrentRoutingPolicy, 'server_etag'>>
  >('/api/channel-routing/policies/current', requestConfig)
  return {
    ...unwrap(response.data),
    server_etag: response.headers.etag || '',
  }
}

export async function getChannelRoutingPolicyRevision(
  revision: number
): Promise<RoutingPolicyRevisionDetail> {
  const response = await api.get<ApiEnvelope<RoutingPolicyRevisionDetail>>(
    `/api/channel-routing/policies/${revision}`,
    requestConfig
  )
  return unwrap(response.data)
}

function policyHeadIfMatch(current: CurrentRoutingPolicy): string {
  if (current.server_etag) return current.server_etag
  const hash = current.head.current_hash || '0'.repeat(64)
  return `"crh.${current.head.current_revision}.${current.head.current_activation_id}.${hash}"`
}

export async function listChannelRoutingPolicyRollbackApprovals(
  sourceRevision: number,
  activation?: PolicyActivationSpec
): Promise<PolicyRollbackApprovalList> {
  const params = activation
    ? {
        stage: activation.stage,
        traffic_basis_points: activation.traffic_basis_points,
        reason: activation.reason,
      }
    : undefined
  const response = await api.get<ApiEnvelope<PolicyRollbackApprovalList>>(
    `/api/channel-routing/policies/${sourceRevision}/rollback-approvals`,
    { ...requestConfig, params }
  )
  return unwrap(response.data)
}

export async function approveChannelRoutingPolicyRollback(
  sourceRevision: number,
  current: CurrentRoutingPolicy,
  activation: PolicyActivationSpec
): Promise<PolicyRollbackApprovalResponse> {
  const response = await api.post<ApiEnvelope<PolicyRollbackApprovalResponse>>(
    `/api/channel-routing/policies/${sourceRevision}/rollback-approvals`,
    activation,
    {
      ...requestConfig,
      headers: { 'If-Match': policyHeadIfMatch(current) },
    }
  )
  return unwrap(response.data)
}

export async function rollbackChannelRoutingPolicy(
  sourceRevision: number,
  current: CurrentRoutingPolicy,
  activation: PolicyActivationSpec,
  idempotencyKey: string
): Promise<PolicyRollbackResponse> {
  const response = await api.post<ApiEnvelope<PolicyRollbackResponse>>(
    `/api/channel-routing/policies/${sourceRevision}/rollback`,
    activation,
    {
      ...requestConfig,
      headers: {
        'If-Match': policyHeadIfMatch(current),
        'Idempotency-Key': idempotencyKey,
      },
    }
  )
  return unwrap(response.data)
}

export async function listChannelRoutingOperations(params: {
  limit: number
  cursor?: number
  type?: string
  status?: string
}): Promise<CursorResponse<RoutingOperation>> {
  const response = await api.get<ApiEnvelope<CursorResponse<RoutingOperation>>>(
    '/api/channel-routing/operations',
    { ...requestConfig, params }
  )
  return unwrap(response.data)
}

export async function getChannelRoutingOperation<Result = unknown>(
  id: number
): Promise<RoutingOperation<Result>> {
  const response = await api.get<ApiEnvelope<RoutingOperation<Result>>>(
    `/api/channel-routing/operations/${id}`,
    requestConfig
  )
  return unwrap(response.data)
}

let idempotencySequence = 0

export function createChannelRoutingIdempotencyKey(operation: string): string {
  const uuid = globalThis.crypto?.randomUUID?.()
  if (uuid) return `channel-routing-${operation}-${uuid}`
  idempotencySequence += 1
  return `channel-routing-${operation}-${Date.now().toString(36)}-${idempotencySequence.toString(36)}`
}
