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

import {
  LEGACY_TOPUP_CREDIT_QUOTA_MAX,
  PAYMENT_PROVIDER_REFERENCE_MAX_BYTES,
  isExternalRefundAmountValid,
  isPaymentAuditReasonValid,
  isPaymentProviderReferenceValid,
  isPaymentTradeNoValid,
  parseLegacyTopUpCreditQuota,
  parsePositiveSafeInteger,
} from './payment-action-validation'
import type { PaymentOrder } from './types'

const order: PaymentOrder = {
  id: 1,
  trade_no: 'trade-1',
  user_id: 2,
  order_kind: 'topup',
  provider: 'stripe',
  payment_method: 'card',
  request_id: 'request-1',
  expected_amount_minor: 1_000,
  paid_amount_minor: 1_000,
  currency: 'USD',
  requested_amount: 10,
  credit_quota: 5_000_000,
  started_at: 1,
  status: 'fulfilled',
  credential_incident: false,
  expires_at: 2,
  settled_at: 3,
  refunded_amount_minor: 200,
  disputed_amount_minor: 0,
  reversed_amount_minor: 200,
  reversed_quota: 1_000_000,
  created_at: 1,
  updated_at: 2,
  version: 4,
}

describe('payment audit action validation', () => {
  test('matches the backend UTF-8 byte limits for reasons and trade numbers', () => {
    assert.equal(isPaymentAuditReasonValid('12345678'), true)
    assert.equal(isPaymentAuditReasonValid('七个字符呀'), true)
    assert.equal(isPaymentAuditReasonValid('short'), false)
    assert.equal(isPaymentAuditReasonValid('a'.repeat(513)), false)
    assert.equal(isPaymentTradeNoValid('trade-1'), true)
    assert.equal(isPaymentTradeNoValid(''), false)
    assert.equal(isPaymentTradeNoValid('a'.repeat(129)), false)
  })

  test('accepts only positive safe whole-number minor amounts', () => {
    assert.equal(parsePositiveSafeInteger('1000'), 1000)
    assert.equal(parsePositiveSafeInteger('1.5'), null)
    assert.equal(parsePositiveSafeInteger('1e3'), null)
    assert.equal(parsePositiveSafeInteger('9007199254740992'), null)
  })

  test('bounds explicit legacy top-up quota to the int32 accounting range', () => {
    assert.equal(parseLegacyTopUpCreditQuota('1'), 1)
    assert.equal(
      parseLegacyTopUpCreditQuota(String(LEGACY_TOPUP_CREDIT_QUOTA_MAX)),
      LEGACY_TOPUP_CREDIT_QUOTA_MAX
    )
    assert.equal(parseLegacyTopUpCreditQuota('0'), null)
    assert.equal(
      parseLegacyTopUpCreditQuota(String(LEGACY_TOPUP_CREDIT_QUOTA_MAX + 1)),
      null
    )
    assert.equal(parseLegacyTopUpCreditQuota('1.5'), null)
    assert.equal(parseLegacyTopUpCreditQuota('1e3'), null)
  })

  test('requires a bounded UTF-8 provider refund reference', () => {
    assert.equal(isPaymentProviderReferenceValid('refund-1'), true)
    assert.equal(isPaymentProviderReferenceValid('  '), false)
    assert.equal(isPaymentProviderReferenceValid('refund\n1'), false)
    assert.equal(isPaymentProviderReferenceValid('refund\u00001'), false)
    assert.equal(isPaymentProviderReferenceValid('refund\u00851'), false)
    assert.equal(
      isPaymentProviderReferenceValid(
        'a'.repeat(PAYMENT_PROVIDER_REFERENCE_MAX_BYTES)
      ),
      true
    )
    assert.equal(
      isPaymentProviderReferenceValid(
        'a'.repeat(PAYMENT_PROVIDER_REFERENCE_MAX_BYTES + 1)
      ),
      false
    )
    assert.equal(
      isPaymentProviderReferenceValid(`${'退款'.repeat(42)}abc`),
      true
    )
    assert.equal(
      isPaymentProviderReferenceValid(`${'退款'.repeat(42)}abcd`),
      false
    )
  })

  test('requires an advancing bounded refund total and full subscription refunds', () => {
    assert.equal(isExternalRefundAmountValid(order, 201), true)
    assert.equal(isExternalRefundAmountValid(order, 200), false)
    assert.equal(isExternalRefundAmountValid(order, 1001), false)
    assert.equal(
      isExternalRefundAmountValid(
        { ...order, order_kind: 'subscription' },
        900
      ),
      false
    )
    assert.equal(
      isExternalRefundAmountValid(
        { ...order, order_kind: 'subscription' },
        1000
      ),
      true
    )
    assert.equal(
      isExternalRefundAmountValid({ ...order, disputed_amount_minor: 1 }, 1000),
      false
    )
  })
})
