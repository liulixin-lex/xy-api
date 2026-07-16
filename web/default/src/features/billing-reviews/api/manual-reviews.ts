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
  ManualBillingReviewApiError,
  ManualBillingReviewItem,
  ManualBillingReviewPage,
  ManualBillingReviewResolutionRequest,
  ManualBillingReviewResolutionResult,
} from '../manual-review-types'

type ApiEnvelope<T> = {
  success: boolean
  message?: string
  data: T
}

const requestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
  timeout: 15_000,
} as const

type ManualBillingReviewErrorPayload = {
  code?: string
  message?: string
}

function unwrap<T>(response: ApiEnvelope<T>): T {
  if (!response.success) {
    throw new Error(response.message || 'Billing review request failed')
  }
  return response.data
}

export function getManualBillingReviewApiError(
  error: unknown
): ManualBillingReviewApiError {
  if (!isAxiosError<ManualBillingReviewErrorPayload>(error)) return {}
  return {
    status: error.response?.status,
    code: error.response?.data?.code,
    message: error.response?.data?.message,
  }
}

export async function listManualBillingReviews(
  params: { cursor?: number; limit: number },
  signal?: AbortSignal
): Promise<ManualBillingReviewPage> {
  const response = await api.get<ApiEnvelope<ManualBillingReviewPage>>(
    '/api/system-info/async-billing/manual-review',
    { ...requestConfig, disableDuplicate: true, params, signal }
  )
  return unwrap(response.data)
}

export async function resolveManualBillingReview(
  review: Pick<ManualBillingReviewItem, 'reservation_id' | 'etag'>,
  payload: ManualBillingReviewResolutionRequest,
  idempotencyKey: string,
  signal?: AbortSignal
): Promise<ManualBillingReviewResolutionResult> {
  const response = await api.post<
    ApiEnvelope<ManualBillingReviewResolutionResult>
  >(
    `/api/system-info/async-billing/manual-review/${review.reservation_id}/resolve`,
    payload,
    {
      ...requestConfig,
      headers: {
        'If-Match': review.etag,
        'Idempotency-Key': idempotencyKey,
      },
      signal,
    }
  )
  return unwrap(response.data)
}
