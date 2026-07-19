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
  billingReservationResolutionError,
  isBillingReservationReviewable,
  parseBillingActualQuota,
} from './billing-reservation'
import type { BillingReservation } from './types'

describe('billing reservation administrator safeguards', () => {
  const t = ((key: string) => key) as TFunction

  test('accepts only explicit int32 settlement quota', () => {
    assert.equal(parseBillingActualQuota('0'), 0)
    assert.equal(parseBillingActualQuota('2147483647'), 2_147_483_647)
    assert.equal(parseBillingActualQuota(''), null)
    assert.equal(parseBillingActualQuota('1.5'), null)
    assert.equal(parseBillingActualQuota('-1'), null)
    assert.equal(parseBillingActualQuota('2147483648'), null)
  })

  test('permits actions only after the reconciler marked review', () => {
    const reservation = {
      status: 'reserved',
      last_reconciled_at: 0,
      reconcile_note: '',
    } as BillingReservation
    assert.equal(isBillingReservationReviewable(reservation), false)
    assert.equal(
      isBillingReservationReviewable({
        ...reservation,
        reconcile_note: 'manual review',
      }),
      false
    )
    assert.equal(
      isBillingReservationReviewable({
        ...reservation,
        last_reconciled_at: 1,
        reconcile_note: 'manual review',
      }),
      true
    )
    assert.equal(
      isBillingReservationReviewable({
        ...reservation,
        status: 'settled',
        last_reconciled_at: 1,
      }),
      false
    )
  })

  test('localizes known failures without exposing unknown backend errors', () => {
    assert.equal(
      billingReservationResolutionError(
        new Error('billing reservation version has changed'),
        t
      ),
      'Billing reservation changed. Refresh the detail before trying again.'
    )
    assert.equal(
      billingReservationResolutionError(
        Object.assign(new Error('Request failed with status code 409'), {
          response: {
            data: { message: 'billing reservation version has changed' },
          },
        }),
        t
      ),
      'Billing reservation changed. Refresh the detail before trying again.'
    )
    assert.equal(
      billingReservationResolutionError(
        new Error('database connection string'),
        t
      ),
      'Failed to resolve billing reservation'
    )
  })
})
