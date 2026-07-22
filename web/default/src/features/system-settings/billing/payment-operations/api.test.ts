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

import {
  cancelAdminStripeSubscriptionAtPeriodEnd,
  getPaymentOperationsOverview,
  resolveLegacySubscription,
  resolveLegacyEpayTopUp,
} from './api'
import type {
  CancelStripeLegacySubscriptionRequest,
  CancelStripeLegacySubscriptionResult,
  PaymentEvent,
  ResolveLegacySubscriptionRequest,
  ResolveLegacyEpayTopUpRequest,
  UnmatchedPaymentEventActionResult,
} from './types'

test('posts the versioned Stripe cancellation contract with protected request handling', async () => {
  const request: CancelStripeLegacySubscriptionRequest = {
    inventory_id: 42,
    expected_updated_at: 1721200000,
    reason: 'Stop renewal after confirming the legacy account owner.',
  }
  const expectedResult: CancelStripeLegacySubscriptionResult = {
    subscription: {
      id: 42,
      stripe_subscription_id: 'sub_legacy_42',
      stripe_customer_id: 'cus_legacy_42',
      mapping_status: 'mapped',
      price_ids: [],
      quantity: 1,
      status: 'active',
      cancel_at_period_end: true,
      current_period_start: 0,
      current_period_end: 1721800000,
      cancel_at: 0,
      canceled_at: 0,
      ended_at: 0,
      trial_start: 0,
      trial_end: 0,
      latest_invoice_paid: true,
      latest_invoice_amount_due: 0,
      latest_invoice_amount_paid: 0,
      livemode: true,
      stripe_created_at: 0,
      state_observed_at: 1721200010,
      last_synced_at: 1721200010,
      sync_source: 'api_sync',
      expected_updated_at: 1721200011,
    },
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
    return { data: { success: true, data: expectedResult } }
  }) as typeof api.post

  try {
    const result = await cancelAdminStripeSubscriptionAtPeriodEnd(request)
    assert.equal(
      capturedUrl,
      '/api/subscription/admin/stripe/inventory/42/cancel-at-period-end'
    )
    assert.deepEqual(capturedData, request)
    assert.deepEqual(capturedConfig, {
      skipBusinessError: true,
      skipErrorHandler: true,
      skipGlobalError: true,
    })
    assert.equal(result, expectedResult)
  } finally {
    api.post = originalPost
  }
})

test('keeps an explicitly unready payment runtime as a valid overview', async () => {
  const overview = {
    operations: {
      preparing_orders: 0,
      awaiting_payment_orders: 0,
      confirming_orders: 0,
      manual_review_orders: 0,
      create_task_backlog: 0,
      reconcile_task_backlog: 0,
      running_tasks: 0,
      retry_waiting_tasks: 0,
      expired_task_leases: 0,
      oldest_create_task_age_seconds: 0,
      unmatched_payment_events: 0,
      unprocessed_payment_events: 0,
      oldest_unprocessed_event_age_seconds: 0,
      active_limit_reservations: 0,
      expired_active_limit_reservations: 0,
      payment_configuration_version: 1,
    },
    runtime: {
      schema_version: 3,
      configuration_version: 1,
      configuration_fingerprint: '',
      payment_secret_key_id: '',
      session_secret_fingerprint: '',
      database_type: 'sqlite',
      redis_enabled: false,
      ready: false,
      readiness_code: 'payment_secret_key_missing',
    },
    cluster: {
      ready: false,
      code: 'shared_database_required',
    },
  }
  const originalGet = api.get
  api.get = (async () => ({
    data: { success: true, data: overview },
  })) as typeof api.get

  try {
    assert.deepEqual(await getPaymentOperationsOverview(), overview)
  } finally {
    api.get = originalGet
  }
})

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
      skipGlobalError: true,
    })
    assert.equal(result, expectedResult)
  } finally {
    api.post = originalPost
  }
})

test('posts the complete legacy subscription refund contract', async () => {
  const request: ResolveLegacySubscriptionRequest = {
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
    const result = await resolveLegacySubscription(request)
    assert.equal(
      capturedUrl,
      '/api/option/payment/audit/unmatched/23/resolve-legacy-subscription'
    )
    assert.deepEqual(capturedData, request)
    assert.deepEqual(capturedConfig, {
      skipBusinessError: true,
      skipErrorHandler: true,
      skipGlobalError: true,
    })
    assert.equal(result, expectedResult)
  } finally {
    api.post = originalPost
  }
})
