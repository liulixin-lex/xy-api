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

import type { TFunction } from 'i18next'

import {
  getCredentialIncidentActions,
  getCredentialIncidentStatusMeta,
  getEventStatusMeta,
  getMappingStatusMeta,
  getPaymentStatusMeta,
  getStripeStatusMeta,
  isPaymentEventActionAvailable,
} from './status'

const t = ((key: string) => key) as TFunction

describe('payment operation status presentation', () => {
  test('keeps all administrator attention states explicit', () => {
    assert.deepEqual(getPaymentStatusMeta('manual_review', t), {
      label: 'Manual Review',
      variant: 'warning',
    })
    assert.deepEqual(getPaymentStatusMeta('refund_pending', t), {
      label: 'Refund Pending',
      variant: 'warning',
    })
    assert.deepEqual(getPaymentStatusMeta('refunded', t), {
      label: 'Refunded',
      variant: 'neutral',
    })
    assert.deepEqual(getPaymentStatusMeta('disputed', t), {
      label: 'Disputed',
      variant: 'danger',
    })
    assert.deepEqual(getPaymentStatusMeta('debt', t), {
      label: 'Payment Debt',
      variant: 'danger',
    })
  })

  test('distinguishes safe mappings from ambiguous Stripe inventory', () => {
    assert.equal(getMappingStatusMeta('mapped', t).variant, 'success')
    assert.equal(getMappingStatusMeta('unmapped_user', t).variant, 'warning')
    assert.equal(getMappingStatusMeta('ambiguous_plan', t).variant, 'danger')
  })

  test('marks collection risk in Stripe subscription states', () => {
    assert.equal(getStripeStatusMeta('active', t).variant, 'success')
    assert.equal(getStripeStatusMeta('past_due', t).variant, 'warning')
    assert.equal(getStripeStatusMeta('unpaid', t).variant, 'danger')
  })

  test('presents terminal unmatched event states explicitly', () => {
    assert.deepEqual(getEventStatusMeta('credential_revoked', t), {
      label: 'Credential Revoked',
      variant: 'danger',
    })
    assert.deepEqual(getEventStatusMeta('dismissed', t), {
      label: 'Dismissed',
      variant: 'neutral',
    })
  })

  test('limits credential incident actions to the current incident state', () => {
    assert.deepEqual(getCredentialIncidentStatusMeta('open', t), {
      label: 'Open',
      variant: 'danger',
    })
    assert.deepEqual(getCredentialIncidentStatusMeta('acknowledged', t), {
      label: 'Acknowledged',
      variant: 'warning',
    })
    assert.deepEqual(getCredentialIncidentStatusMeta('resolved', t), {
      label: 'Resolved',
      variant: 'success',
    })
    assert.deepEqual(getCredentialIncidentActions('open', true), [
      'acknowledge',
      'resolve',
    ])
    assert.deepEqual(getCredentialIncidentActions('acknowledged', true), [
      'resolve',
    ])
    assert.deepEqual(getCredentialIncidentActions('resolved', false), [])
  })

  test('uses server-authorized actions as the only unmatched event gate', () => {
    const event = {
      available_actions: [
        'retry_legacy',
        'resolve_legacy_subscription',
        'resolve_legacy_topup',
      ],
    }
    assert.equal(isPaymentEventActionAvailable(event, 'retry_legacy'), true)
    assert.equal(
      isPaymentEventActionAvailable(event, 'resolve_legacy_topup'),
      true
    )
    assert.equal(
      isPaymentEventActionAvailable(event, 'resolve_legacy_subscription'),
      true
    )
    assert.equal(isPaymentEventActionAvailable(event, 'link'), false)
    assert.equal(
      isPaymentEventActionAvailable({ available_actions: [] }, 'dismiss'),
      false
    )
    assert.equal(
      isPaymentEventActionAvailable({}, 'resolve_legacy_topup'),
      false
    )
  })
})
