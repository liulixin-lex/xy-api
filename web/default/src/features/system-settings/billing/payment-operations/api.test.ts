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
import { test } from 'node:test'

import { api } from '@/lib/api'

import { resolveLegacyEpaySubscription, resolveLegacyEpayTopUp } from './api'
import type {
  PaymentEvent,
  ResolveLegacyEpaySubscriptionRequest,
  ResolveLegacyEpayTopUpRequest,
  UnmatchedPaymentEventActionResult,
} from './types'

test('posts the complete legacy top-up resolution contract', async () => {
  const request: ResolveLegacyEpayTopUpRequest = {
    event_id: 17,
    expected_event_attempts: 3,
    resolution: 'external_refund',
    credit_quota: 0,
    provider_refund_reference: 'refund-ledger-42',
    reason: 'Provider dashboard confirms the completed full refund.',
  }
  const expectedResult: UnmatchedPaymentEventActionResult = {
    event: { id: 17 } as PaymentEvent,
    duplicate: false,
  }
  let capturedUrl = ''
  let capturedData: unknown
  let capturedConfig: unknown
  const originalPost = api.post
  api.post = (async (url: string, data?: unknown, config?: unknown) => {
    capturedUrl = url
    capturedData = data
    capturedConfig = config
    return {
      data: { success: true, data: expectedResult },
    }
  }) as typeof api.post

  try {
    const result = await resolveLegacyEpayTopUp(request)
    assert.equal(
      capturedUrl,
      '/api/option/payment/audit/unmatched/17/resolve-legacy-topup'
    )
    assert.deepEqual(capturedData, request)
    assert.deepEqual(capturedConfig, {
      skipBusinessError: true,
      skipErrorHandler: true,
    })
    assert.equal(result, expectedResult)
  } finally {
    api.post = originalPost
  }
})

test('posts the complete legacy subscription refund contract', async () => {
  const request: ResolveLegacyEpaySubscriptionRequest = {
    event_id: 23,
    expected_event_attempts: 4,
    resolution: 'external_refund',
    provider_refund_reference: 'refund-subscription-23',
    reason: 'Provider confirms that the full payment was refunded.',
  }
  const expectedResult: UnmatchedPaymentEventActionResult = {
    event: { id: 23 } as PaymentEvent,
    duplicate: false,
  }
  let capturedUrl = ''
  let capturedData: unknown
  let capturedConfig: unknown
  const originalPost = api.post
  api.post = (async (url: string, data?: unknown, config?: unknown) => {
    capturedUrl = url
    capturedData = data
    capturedConfig = config
    return {
      data: { success: true, data: expectedResult },
    }
  }) as typeof api.post

  try {
    const result = await resolveLegacyEpaySubscription(request)
    assert.equal(
      capturedUrl,
      '/api/option/payment/audit/unmatched/23/resolve-legacy-subscription'
    )
    assert.deepEqual(capturedData, request)
    assert.deepEqual(capturedConfig, {
      skipBusinessError: true,
      skipErrorHandler: true,
    })
    assert.equal(result, expectedResult)
  } finally {
    api.post = originalPost
  }
})
