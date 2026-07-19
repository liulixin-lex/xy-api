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
import { describe, test } from 'node:test'

import { buildLegacyTopUpResolutionRequest } from './legacy-topup-resolution'
import type { PaymentEvent } from './types'

const event: PaymentEvent = {
  id: 17,
  provider: 'epay',
  event_key: 'legacy-event-17',
  event_type: 'payment.succeeded',
  trade_no: 'legacy-trade-17',
  provider_order_key: 'provider-order-17',
  paid_amount_minor: 1_000,
  refunded_amount_minor: 0,
  disputed_amount_minor: 0,
  currency: 'CNY',
  payment_method: 'alipay',
  paid: true,
  failed: false,
  expired: false,
  refunded: false,
  disputed: false,
  dispute_resolved: false,
  dispute_won: false,
  permanent_failure: false,
  manual_review: true,
  payload_digest: 'digest',
  status: 'manual_review',
  attempts: 3,
  created_at: 1,
  processed_at: 0,
  updated_at: 2,
  available_actions: ['resolve_legacy_topup'],
}

describe('legacy top-up terminal resolution form', () => {
  test('builds an explicit quota fulfillment without consulting QPU', () => {
    assert.deepEqual(
      buildLegacyTopUpResolutionRequest(event, {
        resolution: 'fulfill',
        creditQuota: '2147483647',
        providerRefundReference: 'ignored-for-fulfillment',
        reason: ' Verified against the archived provider receipt. ',
      }),
      {
        event_id: 17,
        expected_event_attempts: 3,
        resolution: 'fulfill',
        credit_quota: 2_147_483_647,
        provider_refund_reference: '',
        reason: 'Verified against the archived provider receipt.',
      }
    )
  })

  test('builds an external refund only with its provider reference', () => {
    assert.deepEqual(
      buildLegacyTopUpResolutionRequest(event, {
        resolution: 'external_refund',
        creditQuota: '999',
        providerRefundReference: ' refund-ledger-42 ',
        reason: 'Provider dashboard confirms the completed full refund.',
      }),
      {
        event_id: 17,
        expected_event_attempts: 3,
        resolution: 'external_refund',
        credit_quota: 0,
        provider_refund_reference: 'refund-ledger-42',
        reason: 'Provider dashboard confirms the completed full refund.',
      }
    )
  })

  test('never enables a resolution that the server did not authorize', () => {
    assert.equal(
      buildLegacyTopUpResolutionRequest(
        { ...event, available_actions: ['retry_legacy'] },
        {
          resolution: 'fulfill',
          creditQuota: '1000',
          providerRefundReference: '',
          reason: 'Verified against archived provider and accounting data.',
        }
      ),
      null
    )
  })

  test('keeps incomplete high-risk forms disabled', () => {
    assert.equal(
      buildLegacyTopUpResolutionRequest(event, {
        resolution: 'fulfill',
        creditQuota: '0',
        providerRefundReference: '',
        reason: 'Verified against archived provider and accounting data.',
      }),
      null
    )
    assert.equal(
      buildLegacyTopUpResolutionRequest(event, {
        resolution: 'external_refund',
        creditQuota: '',
        providerRefundReference: ' ',
        reason: 'Verified against archived provider and accounting data.',
      }),
      null
    )
    assert.equal(
      buildLegacyTopUpResolutionRequest(event, {
        resolution: 'external_refund',
        creditQuota: '',
        providerRefundReference: 'refund-1',
        reason: 'short',
      }),
      null
    )
  })
})
