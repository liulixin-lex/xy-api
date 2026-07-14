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

import type { ApiEnvelope } from '@/features/channel-routing/types'
import { api } from '@/lib/api'

import type {
  BillingLogSinkConflict,
  BillingProjectionOperationApiError,
  BillingProjectionOperationResult,
  BillingProjectionPage,
  FailedBillingProjection,
  FailedBillingProjectionDataset,
} from '../projection-types'

const requestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
  timeout: 15_000,
} as const

type ProjectionOperationErrorPayload = {
  code?: string
  message?: string
}

function unwrap<T>(response: ApiEnvelope<T>): T {
  if (!response.success) {
    throw new Error(response.message || 'Billing projection request failed')
  }
  return response.data
}

export function getBillingProjectionOperationApiError(
  error: unknown
): BillingProjectionOperationApiError {
  if (!isAxiosError<ProjectionOperationErrorPayload>(error)) return {}
  return {
    status: error.response?.status,
    code: error.response?.data?.code,
    message: error.response?.data?.message,
  }
}

export async function listFailedBillingProjections(
  dataset: FailedBillingProjectionDataset,
  params: { cursor?: number; limit: number },
  signal?: AbortSignal
): Promise<BillingProjectionPage<FailedBillingProjection>> {
  const response = await api.get<
    ApiEnvelope<BillingProjectionPage<FailedBillingProjection>>
  >(`/api/system-info/billing-projections/${dataset}/failed`, {
    ...requestConfig,
    disableDuplicate: true,
    params,
    signal,
  })
  return unwrap(response.data)
}

export async function listOpenBillingLogSinkConflicts(
  params: { cursor?: number; limit: number },
  signal?: AbortSignal
): Promise<BillingProjectionPage<BillingLogSinkConflict>> {
  const response = await api.get<
    ApiEnvelope<BillingProjectionPage<BillingLogSinkConflict>>
  >('/api/system-info/billing-projections/log-sink-conflicts/open', {
    ...requestConfig,
    disableDuplicate: true,
    params,
    signal,
  })
  return unwrap(response.data)
}

export async function requeueFailedBillingProjection(
  dataset: FailedBillingProjectionDataset,
  projection: Pick<FailedBillingProjection, 'id' | 'etag' | 'failure_code'>,
  idempotencyKey: string,
  signal?: AbortSignal
): Promise<BillingProjectionOperationResult> {
  const response = await api.post<
    ApiEnvelope<BillingProjectionOperationResult>
  >(
    `/api/system-info/billing-projections/${dataset}/failed/${projection.id}/requeue`,
    { expected_failure_code: projection.failure_code },
    {
      ...requestConfig,
      headers: {
        'If-Match': projection.etag,
        'Idempotency-Key': idempotencyKey,
      },
      signal,
    }
  )
  return unwrap(response.data)
}

export async function resolveBillingLogSinkConflict(
  conflict: Pick<BillingLogSinkConflict, 'id' | 'etag' | 'version'>,
  reason: string,
  idempotencyKey: string,
  signal?: AbortSignal
): Promise<BillingProjectionOperationResult> {
  const response = await api.post<
    ApiEnvelope<BillingProjectionOperationResult>
  >(
    `/api/system-info/billing-projections/log-sink-conflicts/${conflict.id}/resolve-requeue`,
    { expected_version: conflict.version, reason },
    {
      ...requestConfig,
      headers: {
        'If-Match': conflict.etag,
        'Idempotency-Key': idempotencyKey,
      },
      signal,
    }
  )
  return unwrap(response.data)
}
