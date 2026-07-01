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
  removeModelPricingEntry,
  upsertModelPricingEntry,
} from './model-pricing-snapshots'

function parseRecord<T>(value: string): Record<string, T> {
  return JSON.parse(value) as Record<string, T>
}

describe('model pricing snapshot helpers', () => {
  test('removes a model from every pricing option map', () => {
    const next = removeModelPricingEntry(
      {
        modelPrice: JSON.stringify({ deleted: 0.42, kept: 0.2 }),
        modelRatio: JSON.stringify({ deleted: 12, kept: 3 }),
        cacheRatio: JSON.stringify({ deleted: 0.1, kept: 0.5 }),
        createCacheRatio: JSON.stringify({ deleted: 1.25, kept: 1.5 }),
        completionRatio: JSON.stringify({ deleted: 2, kept: 4 }),
        imageRatio: JSON.stringify({ deleted: 1.8, kept: 2.4 }),
        audioRatio: JSON.stringify({ deleted: 2.2, kept: 3.1 }),
        audioCompletionRatio: JSON.stringify({ deleted: 2.6, kept: 3.4 }),
        billingMode: JSON.stringify({
          deleted: 'tiered_expr',
          kept: 'tiered_expr',
        }),
        billingExpr: JSON.stringify({
          deleted: 'tier("base", p * 1 + c * 2)',
          kept: 'tier("base", p * 3 + c * 4)',
        }),
      },
      'deleted'
    )

    for (const value of Object.values(next)) {
      assert.equal(Object.hasOwn(parseRecord<unknown>(value), 'deleted'), false)
      assert.equal(Object.hasOwn(parseRecord<unknown>(value), 'kept'), true)
    }
  })

  test('stores tiered expression models without legacy price fallbacks', () => {
    const next = upsertModelPricingEntry(
      {
        modelPrice: JSON.stringify({ dynamic: 0.42, kept: 0.2 }),
        modelRatio: JSON.stringify({ dynamic: 12, kept: 3 }),
        cacheRatio: JSON.stringify({ dynamic: 0.1, kept: 0.5 }),
        createCacheRatio: JSON.stringify({ dynamic: 1.25, kept: 1.5 }),
        completionRatio: JSON.stringify({ dynamic: 2, kept: 4 }),
        imageRatio: JSON.stringify({ dynamic: 1.8, kept: 2.4 }),
        audioRatio: JSON.stringify({ dynamic: 2.2, kept: 3.1 }),
        audioCompletionRatio: JSON.stringify({ dynamic: 2.6, kept: 3.4 }),
        billingMode: '{}',
        billingExpr: '{}',
      },
      {
        name: 'dynamic',
        price: '0.42',
        ratio: '12',
        cacheRatio: '0.1',
        createCacheRatio: '1.25',
        completionRatio: '2',
        imageRatio: '1.8',
        audioRatio: '2.2',
        audioCompletionRatio: '2.6',
        billingMode: 'tiered_expr',
        billingExpr: 'tier("base", p * 1 + c * 2)',
      }
    )

    assert.equal(
      Object.hasOwn(parseRecord<number>(next.modelPrice), 'dynamic'),
      false
    )
    assert.equal(
      Object.hasOwn(parseRecord<number>(next.modelRatio), 'dynamic'),
      false
    )
    assert.equal(
      Object.hasOwn(parseRecord<number>(next.cacheRatio), 'dynamic'),
      false
    )
    assert.equal(parseRecord<string>(next.billingMode).dynamic, 'tiered_expr')
    assert.equal(
      parseRecord<string>(next.billingExpr).dynamic,
      'tier("base", p * 1 + c * 2)'
    )
    assert.equal(parseRecord<number>(next.modelPrice).kept, 0.2)
  })
})
