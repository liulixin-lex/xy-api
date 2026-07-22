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

import { LEGACY_STRIPE_PRICE_ID_PURPOSE, type PlanRecord } from '../types'
import { hasLegacyStripePriceMapping } from './legacy-stripe'
import { getPlanFormSchema, PLAN_FORM_DEFAULTS } from './plan-form'

const t = ((key: string) => key) as TFunction

function planRecord(
  stripePriceId: string,
  purpose?: typeof LEGACY_STRIPE_PRICE_ID_PURPOSE
): PlanRecord {
  return {
    plan: {
      id: 1,
      title: 'Legacy plan',
      price_amount: 10,
      currency: 'USD',
      duration_unit: 'month',
      duration_value: 1,
      quota_reset_period: 'never',
      enabled: true,
      sort_order: 0,
      allow_balance_pay: true,
      allow_wallet_overflow: true,
      max_purchase_per_user: 0,
      total_amount: 100,
      stripe_price_id: stripePriceId,
    },
    stripe_price_id_purpose: purpose,
  }
}

describe('legacy Stripe plan mapping', () => {
  test('is visible only for records explicitly marked as legacy inventory', () => {
    assert.equal(
      hasLegacyStripePriceMapping(
        planRecord('price_legacy', LEGACY_STRIPE_PRICE_ID_PURPOSE)
      ),
      true
    )
    assert.equal(
      hasLegacyStripePriceMapping(planRecord('price_current')),
      false
    )
    assert.equal(
      hasLegacyStripePriceMapping(
        planRecord('   ', LEGACY_STRIPE_PRICE_ID_PURPOSE)
      ),
      false
    )
  })

  test('trims and validates the Stripe Price ID before submission', () => {
    const schema = getPlanFormSchema(t)
    const valid = schema.safeParse({
      ...PLAN_FORM_DEFAULTS,
      title: 'Legacy plan',
      stripe_price_id: '  price_legacy  ',
    })
    assert.equal(valid.success, true)
    if (valid.success) assert.equal(valid.data.stripe_price_id, 'price_legacy')

    assert.equal(
      schema.safeParse({
        ...PLAN_FORM_DEFAULTS,
        title: 'Legacy plan',
        stripe_price_id: 'prod_not_a_price',
      }).success,
      false
    )
    assert.equal(
      schema.safeParse({
        ...PLAN_FORM_DEFAULTS,
        title: 'Legacy plan',
        stripe_price_id: `price_${'x'.repeat(122)}`,
      }).success,
      true
    )
    assert.equal(
      schema.safeParse({
        ...PLAN_FORM_DEFAULTS,
        title: 'Legacy plan',
        stripe_price_id: `price_${'x'.repeat(123)}`,
      }).success,
      false
    )
  })
})
