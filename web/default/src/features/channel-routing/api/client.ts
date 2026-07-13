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
  ChannelRoutingCostSyncResult,
  ChannelRoutingCostDetailResponse,
  ChannelRoutingDecision,
  ChannelRoutingDecisionSummary,
  ChannelRoutingErrorBudgetResponse,
  ChannelRoutingOverview,
  ChannelRoutingReplayProfilesResponse,
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
  RoutingOperation,
  RoutingCostBinding,
  RoutingCostBindingActionResult,
  RoutingCostBindingPage,
  RoutingCostBindingRequest,
  RoutingPolicyRevisionDetail,
  RoutingProbeResult,
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

type ChannelRoutingCostBindingErrorPayload = {
  code?: string
  message?: string
  detail?: string
  field?: string
  reason?: string
  conflict?: {
    current?: RoutingCostBinding | null
    current_etag?: string
  }
}

export type ChannelRoutingCostBindingApiError = {
  status?: number
  code?: string
  message?: string
  detail?: string
  field?: string
  reason?: string
}

export class ChannelRoutingCostBindingConflictError extends Error {
  current: RoutingCostBinding | null
  currentETag: string

  constructor(current: RoutingCostBinding | null, currentETag: string) {
    super('Channel routing cost binding changed')
    this.name = 'ChannelRoutingCostBindingConflictError'
    this.current = current
    this.currentETag = currentETag
  }
}

export function getChannelRoutingCostBindingApiError(
  error: unknown
): ChannelRoutingCostBindingApiError {
  if (!isAxiosError<ChannelRoutingCostBindingErrorPayload>(error)) return {}
  return {
    status: error.response?.status,
    code: error.response?.data?.code,
    message: error.response?.data?.message,
    detail: error.response?.data?.detail,
    field: error.response?.data?.field,
    reason: error.response?.data?.reason,
  }
}

function costBindingConflictFromError(
  error: unknown
): ChannelRoutingCostBindingConflictError | null {
  if (!isAxiosError<ChannelRoutingCostBindingErrorPayload>(error)) return null
  const response = error.response
  if (
    response?.status !== 409 ||
    response.data?.code !== 'cost_binding_conflict'
  ) {
    return null
  }
  const current = response.data.conflict?.current ?? null
  const currentETag =
    response.data.conflict?.current_etag || current?.etag || ''
  return new ChannelRoutingCostBindingConflictError(current, currentETag)
}

export async function getChannelRoutingOverview(): Promise<ChannelRoutingOverview> {
  const response = await api.get<ApiEnvelope<ChannelRoutingOverview>>(
    '/api/channel-routing/v2/overview',
    requestConfig
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
  >('/api/channel-routing/v2/groups', { ...requestConfig, params })
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
  >(`/api/channel-routing/v2/groups/${id}`, { ...requestConfig, params })
  return unwrap(response.data)
}

export async function listChannelRoutingGroupReplayProfiles(
  id: number,
  limit = 20
): Promise<ChannelRoutingReplayProfilesResponse> {
  const response = await api.get<
    ApiEnvelope<ChannelRoutingReplayProfilesResponse>
  >(`/api/channel-routing/v2/groups/${id}/replay-profiles`, {
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
  >(`/api/channel-routing/v2/groups/${id}/error-budget`, requestConfig)
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
    '/api/channel-routing/v2/channels',
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
    '/api/channel-routing/v2/endpoints',
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
  >('/api/channel-routing/v2/breakers/reset', payload, {
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
  >('/api/channel-routing/v2/costs', { ...requestConfig, params })
  return unwrap(response.data)
}

export async function getChannelRoutingCostDetail(
  poolId: number,
  memberId: number,
  model: string
): Promise<ChannelRoutingCostDetailResponse> {
  const response = await api.get<ApiEnvelope<ChannelRoutingCostDetailResponse>>(
    `/api/channel-routing/v2/costs/${poolId}/${memberId}`,
    { ...requestConfig, params: { model } }
  )
  return unwrap(response.data)
}

export async function syncChannelRoutingCosts(
  idempotencyKey: string
): Promise<RoutingOperation<ChannelRoutingCostSyncResult>> {
  const response = await api.post<
    ApiEnvelope<RoutingOperation<ChannelRoutingCostSyncResult>>
  >('/api/channel-routing/v2/costs/sync', undefined, {
    ...requestConfig,
    headers: { 'Idempotency-Key': idempotencyKey },
  })
  return unwrap(response.data)
}

export async function listChannelRoutingCostBindings(params: {
  page: number
  page_size: number
  search?: string
  upstream_type?: string
  enabled?: boolean
  channel_id?: number
}): Promise<RoutingCostBindingPage> {
  const response = await api.get<ApiEnvelope<RoutingCostBindingPage>>(
    '/api/channel-routing/v2/cost-bindings',
    { ...requestConfig, params }
  )
  return unwrap(response.data)
}

export async function getChannelRoutingCostBinding(
  channelId: number
): Promise<RoutingCostBinding> {
  const response = await api.get<ApiEnvelope<RoutingCostBinding>>(
    `/api/channel-routing/v2/cost-bindings/${channelId}`,
    requestConfig
  )
  const binding = unwrap(response.data)
  return { ...binding, etag: response.headers.etag || binding.etag }
}

export async function createChannelRoutingCostBinding(
  request: RoutingCostBindingRequest,
  signal?: AbortSignal
): Promise<RoutingCostBinding> {
  const response = await api.post<ApiEnvelope<RoutingCostBinding>>(
    '/api/channel-routing/v2/cost-bindings',
    request,
    { ...requestConfig, signal }
  )
  const binding = unwrap(response.data)
  return { ...binding, etag: response.headers.etag || binding.etag }
}

export async function updateChannelRoutingCostBinding(
  binding: Pick<RoutingCostBinding, 'channel_id' | 'etag'>,
  request: RoutingCostBindingRequest,
  signal?: AbortSignal
): Promise<RoutingCostBinding> {
  try {
    const response = await api.put<ApiEnvelope<RoutingCostBinding>>(
      `/api/channel-routing/v2/cost-bindings/${binding.channel_id}`,
      request,
      {
        ...requestConfig,
        signal,
        headers: { 'If-Match': binding.etag },
      }
    )
    const updated = unwrap(response.data)
    return { ...updated, etag: response.headers.etag || updated.etag }
  } catch (error) {
    const conflict = costBindingConflictFromError(error)
    if (conflict) throw conflict
    throw error
  }
}

export async function deleteChannelRoutingCostBinding(
  binding: Pick<RoutingCostBinding, 'channel_id' | 'etag'>
): Promise<{ channel_id: number }> {
  try {
    const response = await api.delete<ApiEnvelope<{ channel_id: number }>>(
      `/api/channel-routing/v2/cost-bindings/${binding.channel_id}`,
      {
        ...requestConfig,
        headers: { 'If-Match': binding.etag },
      }
    )
    return unwrap(response.data)
  } catch (error) {
    const conflict = costBindingConflictFromError(error)
    if (conflict) throw conflict
    throw error
  }
}

export async function testChannelRoutingCostBinding(
  channelId: number | 'new',
  request?: RoutingCostBindingRequest,
  signal?: AbortSignal
): Promise<RoutingCostBindingActionResult> {
  const response = await api.post<ApiEnvelope<RoutingCostBindingActionResult>>(
    `/api/channel-routing/v2/cost-bindings/${channelId}/test`,
    request,
    {
      ...requestConfig,
      signal,
      timeout: 30_000,
    }
  )
  return unwrap(response.data)
}

export async function loadChannelRoutingCostBindingGroups(
  channelId: number | 'new',
  request?: RoutingCostBindingRequest,
  signal?: AbortSignal
): Promise<RoutingCostBindingActionResult> {
  const response = await api.post<ApiEnvelope<RoutingCostBindingActionResult>>(
    `/api/channel-routing/v2/cost-bindings/${channelId}/groups`,
    request,
    {
      ...requestConfig,
      signal,
      timeout: 30_000,
    }
  )
  return unwrap(response.data)
}

export async function listChannelRoutingProbes(params: {
  limit: number
  pool_id?: number
  channel_id?: number
  outcome?: string
  cursor?: number
}): Promise<CursorResponse<RoutingProbeResult>> {
  const response = await api.get<
    ApiEnvelope<CursorResponse<RoutingProbeResult>>
  >('/api/channel-routing/v2/probes', { ...requestConfig, params })
  return unwrap(response.data)
}

export async function runChannelRoutingActiveProbe(
  idempotencyKey: string
): Promise<RoutingOperation<ChannelRoutingActiveProbeResult>> {
  const response = await api.post<
    ApiEnvelope<RoutingOperation<ChannelRoutingActiveProbeResult>>
  >('/api/channel-routing/v2/probes/run', undefined, {
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
  >('/api/channel-routing/v2/audit-exports', payload, {
    ...requestConfig,
    headers: { 'Idempotency-Key': idempotencyKey },
  })
  return unwrap(response.data)
}

export async function downloadChannelRoutingAuditExport(
  exportId: string
): Promise<void> {
  const response = await api.get<Blob>(
    `/api/channel-routing/v2/audit-exports/${encodeURIComponent(exportId)}/download`,
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
  >('/api/channel-routing/v2/decisions', { ...requestConfig, params })
  return unwrap(response.data)
}

export async function getChannelRoutingDecision(
  id: string
): Promise<ChannelRoutingDecision> {
  const response = await api.get<ApiEnvelope<ChannelRoutingDecision>>(
    `/api/channel-routing/v2/decisions/${encodeURIComponent(id)}`,
    requestConfig
  )
  return unwrap(response.data)
}

export async function listChannelRoutingDecisionCandidates(
  id: string,
  params: { cursor: number; limit: number }
): Promise<DecisionCandidatePage> {
  const response = await api.get<ApiEnvelope<DecisionCandidatePage>>(
    `/api/channel-routing/v2/decisions/${encodeURIComponent(id)}/candidates`,
    { ...requestConfig, params }
  )
  return unwrap(response.data)
}

export async function replayChannelRoutingDecision(
  id: string
): Promise<DecisionReplayResult> {
  const response = await api.post<ApiEnvelope<DecisionReplayResult>>(
    `/api/channel-routing/v2/decisions/${encodeURIComponent(id)}/replay`,
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
    `/api/channel-routing/v2/groups/${id}/simulations`,
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
  >('/api/channel-routing/v2/policy-drafts', { ...requestConfig, params })
  return unwrap(response.data)
}

export async function getChannelRoutingPolicyDraft(
  id: number
): Promise<PolicyDraftDetail> {
  const response = await api.get<
    ApiEnvelope<Omit<PolicyDraftDetail, 'server_etag'>>
  >(`/api/channel-routing/v2/policy-drafts/${id}`, requestConfig)
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
    '/api/channel-routing/v2/policy-drafts',
    payload,
    requestConfig
  )
  return unwrap(response.data)
}

function policyDraftIfMatch(draft: PolicyDraftSummary): string {
  return `"crd.${draft.id}.${draft.version}.${draft.etag}"`
}

export async function updateChannelRoutingPolicyDraft(
  draft: PolicyDraftSummary,
  document: PolicyDocument
): Promise<PolicyDraftSummary> {
  const response = await api.put<ApiEnvelope<PolicyDraftSummary>>(
    `/api/channel-routing/v2/policy-drafts/${draft.id}`,
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
    `/api/channel-routing/v2/policy-drafts/${draft.id}/validate`,
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
    `/api/channel-routing/v2/policy-drafts/${draft.id}/simulate`,
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
    `/api/channel-routing/v2/policy-drafts/${draftId}/approvals`,
    { ...requestConfig, params }
  )
  return unwrap(response.data)
}

export async function approveChannelRoutingPolicyDraft(
  draft: PolicyDraftSummary,
  activation: PolicyActivationSpec
): Promise<PolicyApprovalResponse> {
  const response = await api.post<ApiEnvelope<PolicyApprovalResponse>>(
    `/api/channel-routing/v2/policy-drafts/${draft.id}/approvals`,
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
    `/api/channel-routing/v2/policy-drafts/${draft.id}/publish`,
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
  >('/api/channel-routing/v2/policies/current', requestConfig)
  return {
    ...unwrap(response.data),
    server_etag: response.headers.etag || '',
  }
}

export async function getChannelRoutingPolicyRevision(
  revision: number
): Promise<RoutingPolicyRevisionDetail> {
  const response = await api.get<ApiEnvelope<RoutingPolicyRevisionDetail>>(
    `/api/channel-routing/v2/policies/${revision}`,
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
    `/api/channel-routing/v2/policies/${sourceRevision}/rollback-approvals`,
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
    `/api/channel-routing/v2/policies/${sourceRevision}/rollback-approvals`,
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
    `/api/channel-routing/v2/policies/${sourceRevision}/rollback`,
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
    '/api/channel-routing/v2/operations',
    { ...requestConfig, params }
  )
  return unwrap(response.data)
}

export async function getChannelRoutingOperation<Result = unknown>(
  id: number
): Promise<RoutingOperation<Result>> {
  const response = await api.get<ApiEnvelope<RoutingOperation<Result>>>(
    `/api/channel-routing/v2/operations/${id}`,
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
