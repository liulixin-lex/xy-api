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
import { api } from '@/lib/api'

import type { RevocablePaymentProvider } from './payment-credential-revocation'
import {
  buildRetainedCredentialDisablePayload,
  buildRetainedCredentialDisablePreviewParams,
  type RetainedCredentialDisablePreview,
  type RetainedCredentialDisableResponse,
  type RetainedPaymentProvider,
} from './retained-payment-credential-disable'
import type {
  AffiliateRewardSummaryResponse,
  ConfirmPaymentComplianceResponse,
  FetchUpstreamRatiosRequest,
  InviteLinkBatch,
  InviteLinkBatchListResponse,
  InviteLinkBatchRandomResponse,
  InviteLinkBatchResponse,
  LogCleanupTask,
  SystemOptionsResponse,
  SystemTaskListResponse,
  SystemTaskResponse,
  UpdateOptionRequest,
  UpdateOptionResponse,
  UpstreamChannelsResponse,
  UpstreamRatiosResponse,
} from './types'

export type PaymentProviderReadiness = boolean | Record<string, unknown>

export interface StripePaymentGatewayReadiness extends Record<string, unknown> {
  credential_account_id?: string
  credential_livemode?: 'live' | 'test' | string
  previous_credential_active?: boolean
  test_mode_enabled?: boolean
  test_mode_blocked?: boolean
  test_mode_isolation_required?: boolean
}

export interface PaymentGatewayReadiness {
  [provider: string]: PaymentProviderReadiness | undefined
  stripe?: StripePaymentGatewayReadiness
}

/**
 * Stable metadata returned by the atomic payment-settings endpoint when a
 * single option is rejected.  The backend deliberately keeps the diagnostic
 * out of the response; the field key is enough for the admin form to focus
 * the right control without exposing provider internals.
 */
export type PaymentSettingsErrorParams = {
  field?: unknown
  [key: string]: unknown
}

export type PaymentSettingsResponse<
  T = {
    readiness?: PaymentGatewayReadiness
    version?: number
  },
> = {
  success: boolean
  code?: string
  message?: string
  params?: PaymentSettingsErrorParams
  data?: T
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

/**
 * Read the structured payment error payload from either a direct business
 * response or an Axios error.  Keeping this at the API boundary prevents
 * individual settings controls from making subtly different assumptions
 * about how the response was wrapped by the transport layer.
 */
export function getPaymentSettingsErrorParams(
  value: unknown
): PaymentSettingsErrorParams | undefined {
  const queue: unknown[] = [value]
  const visited = new Set<object>()

  while (queue.length > 0) {
    const current = queue.shift()
    if (!isRecord(current) || visited.has(current)) continue
    visited.add(current)

    const params = current.params
    if (isRecord(params)) {
      return params as PaymentSettingsErrorParams
    }

    const response = current.response
    if (isRecord(response)) queue.push(response.data)
    if ('data' in current) queue.push(current.data)
    if ('cause' in current) queue.push(current.cause)
  }

  return undefined
}

export function getPaymentSettingsErrorField(value: unknown): string | null {
  const field = getPaymentSettingsErrorParams(value)?.field
  return typeof field === 'string' && field.trim() ? field.trim() : null
}

export async function getSystemOptions() {
  const res = await api.get<SystemOptionsResponse>('/api/option/', {
    // Settings pages render their own retryable failure state. Suppress the
    // global transport toast so one failed load does not produce duplicate or
    // generic ERR_BAD_REQUEST-style notifications.
    skipErrorHandler: true,
  })
  return res.data
}

export async function updateSystemOption(request: UpdateOptionRequest) {
  const res = await api.put<UpdateOptionResponse>('/api/option/', request)
  return res.data
}

export async function confirmPaymentCompliance() {
  const res = await api.post<ConfirmPaymentComplianceResponse>(
    '/api/option/payment_compliance',
    { confirmed: true },
    {
      skipBusinessError: true,
      skipErrorHandler: true,
      skipGlobalError: true,
    }
  )
  return res.data
}

export async function updatePaymentSettings(request: {
  options: Record<string, string | number | boolean>
  clearSecrets?: string[]
  revokePreviousCredentials?: RevocablePaymentProvider[]
  reason?: string
  expectedVersion: number
}): Promise<PaymentSettingsResponse> {
  const res = await api.put<PaymentSettingsResponse>(
    '/api/option/payment',
    {
      options: request.options,
      clear_secrets: request.clearSecrets,
      revoke_previous_credentials: request.revokePreviousCredentials,
      reason: request.reason,
      expected_version: request.expectedVersion,
    },
    {
      skipBusinessError: true,
      skipErrorHandler: true,
      skipGlobalError: true,
    }
  )
  return res.data
}

export async function getRetainedCredentialDisablePreview(
  provider: RetainedPaymentProvider
): Promise<
  RetainedCredentialDisableResponse<RetainedCredentialDisablePreview>
> {
  const res = await api.get(
    '/api/option/payment/credential-revocation-preview',
    {
      params: buildRetainedCredentialDisablePreviewParams(provider),
      skipBusinessError: true,
      skipErrorHandler: true,
    }
  )
  return res.data
}

export async function disableRetainedPaymentCredential(request: {
  provider: RetainedPaymentProvider
  reason: string
  expectedVersion: number
}): Promise<
  RetainedCredentialDisableResponse<{
    readiness?: PaymentGatewayReadiness
    version?: number
  }>
> {
  const res = await api.put(
    '/api/option/payment',
    buildRetainedCredentialDisablePayload(
      request.provider,
      request.reason,
      request.expectedVersion
    ),
    {
      skipBusinessError: true,
      skipErrorHandler: true,
    }
  )
  return res.data
}

export async function getAffiliateRewardSummary(params?: {
  search_field?: string
  search?: string
  invite_type?: string
  registered_start?: number
  registered_end?: number
  reward_percent?: number
}) {
  const res = await api.get<AffiliateRewardSummaryResponse>(
    '/api/option/affiliate_rewards',
    { params }
  )
  return res.data
}

export async function getInviteLinkBatches() {
  const res = await api.get<InviteLinkBatchListResponse>(
    '/api/option/invite_link_batches'
  )
  return res.data
}

export async function createInviteLinkBatch(request: Partial<InviteLinkBatch>) {
  const res = await api.post<InviteLinkBatchResponse>(
    '/api/option/invite_link_batches',
    request
  )
  return res.data
}

export async function updateInviteLinkBatch(
  id: number,
  request: Partial<InviteLinkBatch>
) {
  const res = await api.put<InviteLinkBatchResponse>(
    `/api/option/invite_link_batches/${id}`,
    request
  )
  return res.data
}

export async function activateInviteLinkBatch(id: number) {
  const res = await api.post<UpdateOptionResponse>(
    `/api/option/invite_link_batches/${id}/active`
  )
  return res.data
}

export async function generateInviteLinkBatchRandomLink() {
  const res = await api.get<InviteLinkBatchRandomResponse>(
    '/api/option/invite_link_batches/random'
  )
  return res.data
}

export async function startLogCleanupTask(targetTimestamp: number) {
  const res = await api.post<SystemTaskResponse<LogCleanupTask>>(
    '/api/system-task/log-cleanup',
    null,
    {
      params: { target_timestamp: targetTimestamp },
    }
  )
  return res.data
}

export async function getCurrentLogCleanupTask() {
  const res = await api.get<SystemTaskResponse<LogCleanupTask | null>>(
    '/api/system-task/current',
    {
      params: { type: 'log_cleanup' },
    }
  )
  return res.data
}

export async function getSystemTask(taskId: string) {
  const res = await api.get<SystemTaskResponse<LogCleanupTask>>(
    `/api/system-task/${taskId}`
  )
  return res.data
}

export async function listSystemTasks(limit = 20) {
  const res = await api.get<SystemTaskListResponse>('/api/system-task/list', {
    params: { limit },
  })
  return res.data
}

export async function resetModelRatios() {
  const res = await api.post<UpdateOptionResponse>(
    '/api/option/rest_model_ratio'
  )
  return res.data
}

export async function getUpstreamChannels() {
  const res = await api.get<UpstreamChannelsResponse>(
    '/api/ratio_sync/channels'
  )
  return res.data
}

export async function fetchUpstreamRatios(request: FetchUpstreamRatiosRequest) {
  const res = await api.post<UpstreamRatiosResponse>(
    '/api/ratio_sync/fetch',
    request
  )
  return res.data
}
