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
  buildEmergencyCredentialReplacement,
  getEmergencyCredentialClearSecrets,
  isEmergencyCredentialRevocationReasonValid,
  normalizeEmergencyCredentialRevocationReason,
  resolveEmergencyCredentialRevocationMode,
} from './payment-credential-revocation'

describe('emergency payment credential replacement', () => {
  test('atomically sends a newly entered Stripe webhook secret', () => {
    assert.deepEqual(
      buildEmergencyCredentialReplacement('stripe', {
        secret: '  whsec_replacement  ',
      }),
      {
        state: 'complete',
        options: { StripeWebhookSecret: 'whsec_replacement' },
      }
    )
  })

  test('disables Stripe webhooks when no replacement secret is entered', () => {
    assert.deepEqual(
      buildEmergencyCredentialReplacement('stripe', { secret: '   ' }),
      { state: 'none', options: {} }
    )
  })

  test('builds complete Epay and XORPay replacement pairs', () => {
    assert.deepEqual(
      buildEmergencyCredentialReplacement('epay', {
        identifier: ' merchant_replacement ',
        savedIdentifier: 'merchant_current',
        secret: ' epay_replacement_secret ',
      }),
      {
        state: 'complete',
        options: {
          EpayId: 'merchant_replacement',
          EpayKey: 'epay_replacement_secret',
        },
      }
    )
    assert.deepEqual(
      buildEmergencyCredentialReplacement('xorpay', {
        identifier: ' aid_replacement ',
        savedIdentifier: 'aid_current',
        secret: ' xorpay_replacement_secret ',
      }),
      {
        state: 'complete',
        options: {
          XorPayAid: 'aid_replacement',
          XorPayAppSecret: 'xorpay_replacement_secret',
        },
      }
    )
  })

  test('distinguishes no replacement from an incomplete credential pair', () => {
    assert.deepEqual(
      buildEmergencyCredentialReplacement('epay', {
        identifier: 'merchant_current',
        savedIdentifier: 'merchant_current',
        secret: '   ',
      }),
      { state: 'none', options: {} }
    )
    assert.deepEqual(
      buildEmergencyCredentialReplacement('epay', {
        identifier: 'merchant_changed',
        savedIdentifier: 'merchant_current',
        secret: '',
      }),
      { state: 'partial', options: {} }
    )
    assert.deepEqual(
      buildEmergencyCredentialReplacement('xorpay', {
        identifier: '',
        savedIdentifier: 'aid_current',
        secret: 'replacement_secret',
      }),
      { state: 'partial', options: {} }
    )
  })

  test('only enables provider actions that can complete safely', () => {
    assert.equal(
      resolveEmergencyCredentialRevocationMode('epay', 'none', false),
      null
    )
    assert.equal(
      resolveEmergencyCredentialRevocationMode('epay', 'none', true),
      'revoke_previous'
    )
    assert.equal(
      resolveEmergencyCredentialRevocationMode('xorpay', 'partial', true),
      null
    )
    assert.equal(
      resolveEmergencyCredentialRevocationMode('xorpay', 'complete', false),
      'replace'
    )
    assert.equal(
      resolveEmergencyCredentialRevocationMode('stripe', 'none', false),
      'stripe_disable'
    )
  })

  test('only full Stripe shutdown clears the API credential', () => {
    assert.deepEqual(getEmergencyCredentialClearSecrets('stripe_disable_all'), [
      'StripeApiSecret',
    ])
    assert.deepEqual(getEmergencyCredentialClearSecrets('stripe_disable'), [])
    assert.deepEqual(getEmergencyCredentialClearSecrets('replace'), [])
    assert.deepEqual(getEmergencyCredentialClearSecrets('revoke_previous'), [])
  })

  test('normalizes and validates the required audit reason boundaries', () => {
    assert.equal(
      normalizeEmergencyCredentialRevocationReason('  incident  '),
      'incident'
    )
    assert.equal(isEmergencyCredentialRevocationReasonValid('1234567'), false)
    assert.equal(isEmergencyCredentialRevocationReasonValid('12345678'), true)
    assert.equal(
      isEmergencyCredentialRevocationReasonValid('x'.repeat(512)),
      true
    )
    assert.equal(
      isEmergencyCredentialRevocationReasonValid('x'.repeat(513)),
      false
    )
  })
})
