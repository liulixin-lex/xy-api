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

import { publicPlanRecordSchema, selfSubscriptionDataSchema } from './types'

describe('default public subscription boundary', () => {
  test('keeps only public plan fields and converts legacy group data to a boolean benefit', () => {
    const record = publicPlanRecordSchema.parse({
      plan: {
        id: 42,
        title: 'Thirty days',
        subtitle: 'Fixed-term access',
        price_amount: 12,
        currency: 'USD',
        duration_unit: 'day',
        duration_value: 30,
        custom_seconds: 0,
        quota_reset_period: 'never',
        quota_reset_custom_seconds: 0,
        allow_balance_pay: true,
        allow_wallet_overflow: true,
        max_purchase_per_user: 1,
        total_amount: 10000,
        external_payment_route_ids: ['pay_public'],
        upgrade_group: 'internal-premium',
        downgrade_group: 'internal-default',
        stripe_price_id: 'price_private',
        sort_order: 99,
        enabled: true,
      },
    })

    assert.equal(record.plan.includes_expanded_access, true)
    const serialized = JSON.stringify(record)
    for (const forbidden of [
      'upgrade_group',
      'downgrade_group',
      'internal-premium',
      'internal-default',
      'stripe_price_id',
      'price_private',
      'sort_order',
      'enabled',
      'allow_wallet_overflow',
    ]) {
      assert.doesNotMatch(serialized, new RegExp(forbidden))
    }
  })

  test('strips ownership, payment, accounting, and internal group fields from self data', () => {
    const data = selfSubscriptionDataSchema.parse({
      billing_preference: 'subscription_first',
      subscriptions: [
        {
          subscription: {
            id: 61,
            plan_id: 42,
            plan_title: 'Thirty days',
            status: 'active',
            start_time: 100,
            end_time: 200,
            amount_total: 10000,
            amount_used: 2500,
            next_reset_time: 150,
            user_id: 90210,
            payment_order_id: 99123,
            source: 'order',
            upgrade_group: 'internal-premium',
            downgrade_group: 'internal-default',
            prev_user_group: 'internal-previous',
            amount_used_total: 9000,
            usage_accounting_version: 1,
          },
        },
      ],
      all_subscriptions: [],
    })

    assert.equal(data.subscriptions[0]?.subscription.plan_title, 'Thirty days')
    const serialized = JSON.stringify(data)
    for (const forbidden of [
      'user_id',
      'payment_order_id',
      'source',
      'upgrade_group',
      'downgrade_group',
      'prev_user_group',
      'amount_used_total',
      'usage_accounting_version',
      'internal-premium',
      'internal-default',
      'internal-previous',
    ]) {
      assert.doesNotMatch(serialized, new RegExp(forbidden))
    }
  })
})
