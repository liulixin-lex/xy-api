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
import assert from 'node:assert/strict'
import { afterEach, describe, test } from 'node:test'

import {
  AxiosError,
  AxiosHeaders,
  type InternalAxiosRequestConfig,
} from 'axios'

import { api } from '@/lib/api'

import type {
  ManualBillingReviewItem,
  ManualBillingReviewPage,
  ManualBillingReviewResolutionRequest,
  ManualBillingReviewResolutionResult,
} from '../manual-review-types'
import {
  getManualBillingReviewApiError,
  listManualBillingReviews,
  resolveManualBillingReview,
} from './manual-reviews'

type ApiEnvelope<T> = {
  success: boolean
  message?: string
  data: T
}

const originalAdapter = api.defaults.adapter

const review: ManualBillingReviewItem = {
  reservation_id: 42,
  kind: 'task',
  review_kind: 'terminal_overage',
  public_task_id: 'task-public',
  user_id: 7,
  state: 'manual_review',
  current_quota: 100,
  accepted_quota: 100,
  review_version: 3,
  etag: '"async-billing-review-42-v3"',
  manual_review_since_ms: 1_700_000_000_000,
  reason: 'terminal overage',
  can_accept: true,
  can_reject: true,
  blockers: [],
  financial_consequences: {
    current_charge: 100,
    accept_additional_charge: 25,
    accept_final_charge: 125,
    reject_refund: 0,
    reject_final_charge: 100,
    reject_write_off: 25,
  },
  attempts: [],
}

afterEach(() => {
  api.defaults.adapter = originalAdapter
})

describe('manual billing review API', () => {
  test('sends the server cursor and unwraps queue metadata', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    const controller = new AbortController()
    api.defaults.adapter = async (config) => {
      captured.push(config)
      const data: ApiEnvelope<ManualBillingReviewPage> = {
        success: true,
        data: {
          pending_count: 12,
          oldest_age_seconds: 900,
          items: [review],
          next_cursor: 42,
          has_more: true,
          capabilities: { can_resolve: true },
        },
      }
      return {
        data,
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }

    const result = await listManualBillingReviews(
      { cursor: 21, limit: 10 },
      controller.signal
    )

    assert.equal(
      captured[0]?.url,
      '/api/system-info/async-billing/manual-review'
    )
    assert.deepEqual(captured[0]?.params, { cursor: 21, limit: 10 })
    assert.equal(captured[0]?.signal, controller.signal)
    assert.equal(captured[0]?.disableDuplicate, true)
    assert.equal(result.pending_count, 12)
    assert.equal(result.capabilities?.can_resolve, true)
  })

  test('sends compare-and-swap and idempotency headers with the frozen payload', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    const resolutionResult: ManualBillingReviewResolutionResult = {
      reservation_id: 42,
      state: 'terminal',
      review_version: 4,
      etag: '"async-billing-review-42-v4"',
      current_quota: 125,
      resolution: {
        id: 1,
        action: 'confirmed_accepted',
        review_kind: 'terminal_overage',
        before_state: 'manual_review',
        after_state: 'terminal',
        before_quota: 100,
        after_quota: 125,
        quota_delta: 25,
        resolved_time_ms: 1_700_000_100_000,
      },
    }
    api.defaults.adapter = async (config) => {
      captured.push(config)
      return {
        data: {
          success: true,
          data: resolutionResult,
        },
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }
    const payload: ManualBillingReviewResolutionRequest = {
      action: 'confirmed_accepted',
      expected_version: 3,
      upstream_task_id: '',
      provider_status: 'terminal_usage_verified',
      provider_checked_ms: 1_700_000_050_000,
      evidence_reference: 'provider-console/case-1',
      reason: 'verified',
    }

    const result = await resolveManualBillingReview(
      review,
      payload,
      'billing-review-key-1'
    )

    assert.equal(
      captured[0]?.url,
      '/api/system-info/async-billing/manual-review/42/resolve'
    )
    assert.equal(captured[0]?.headers.get('If-Match'), review.etag)
    assert.equal(
      captured[0]?.headers.get('Idempotency-Key'),
      'billing-review-key-1'
    )
    assert.deepEqual(JSON.parse(captured[0]?.data as string), payload)
    assert.deepEqual(result, resolutionResult)
  })

  test('preserves stable error codes for recoverable UI handling', async () => {
    api.defaults.adapter = async (config) => {
      const response = {
        data: {
          success: false,
          code: 'review_precondition_failed',
          message: 'billing review changed',
        },
        status: 412,
        statusText: 'Precondition Failed',
        headers: new AxiosHeaders(),
        config,
      }
      throw new AxiosError(
        'Request failed with status code 412',
        AxiosError.ERR_BAD_REQUEST,
        config,
        undefined,
        response
      )
    }

    await assert.rejects(
      () =>
        resolveManualBillingReview(
          review,
          {
            action: 'confirmed_rejected',
            expected_version: 3,
            upstream_task_id: '',
            provider_status: 'terminal_usage_verified',
            provider_checked_ms: 1_700_000_050_000,
            evidence_reference: 'case-2',
            reason: 'write off approved',
          },
          'billing-review-key-2'
        ),
      (error: unknown) => {
        assert.deepEqual(getManualBillingReviewApiError(error), {
          status: 412,
          code: 'review_precondition_failed',
          message: 'billing review changed',
        })
        return true
      }
    )
  })
})
