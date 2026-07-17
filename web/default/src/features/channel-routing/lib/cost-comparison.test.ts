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
  buildCostComparisonRequest,
  costComparisonDefaultValues,
  costComparisonFormSchema,
  getCurrentRoutingCostComparison,
} from './cost-comparison'

describe('request cost comparison contract', () => {
  test('preserves explicit zero quantities while omitting blank quantities', () => {
    const values = costComparisonFormSchema.parse({
      ...costComparisonDefaultValues,
      pool_id: '7',
      model_name: 'gpt-cost',
      input_tokens: '0',
      image_input_tokens: '0',
      video_seconds: '8',
    })
    const request = buildCostComparisonRequest(values, [12, 11])

    assert.deepEqual(request.member_ids, [11, 12])
    assert.equal(request.profile?.input_tokens, 0)
    assert.equal(request.profile?.output_tokens, undefined)
    assert.equal(request.profile?.image_input_tokens, 0)
    assert.equal(request.profile?.audio_input_tokens, undefined)
    assert.equal(request.profile?.video_seconds, 8)
  })

  test('recent decision mode never sends a competing manual profile', () => {
    const values = costComparisonFormSchema.parse({
      ...costComparisonDefaultValues,
      source: 'recent_decision',
      pool_id: '7',
      decision_id: 'decision-1',
    })
    const request = buildCostComparisonRequest(values, [])

    assert.equal(request.decision_id, 'decision-1')
    assert.equal(request.profile, undefined)
  })

  test('hides comparison results after the cost catalog snapshot changes', () => {
    const result = { pricing_epoch: 7, pricing_hash: 'a'.repeat(64) }
    const catalog = { pricing_epoch: 7, pricing_hash: 'a'.repeat(64) }
    const current = getCurrentRoutingCostComparison({
      result,
      comparisonCatalogUpdatedAt: 100,
      catalog,
      catalogUpdatedAt: 100,
      catalogFetching: false,
      catalogError: false,
    })

    assert.equal(current, result)
    assert.equal(
      getCurrentRoutingCostComparison({
        result,
        comparisonCatalogUpdatedAt: 100,
        catalog,
        catalogUpdatedAt: 101,
        catalogFetching: false,
        catalogError: false,
      }),
      undefined
    )
    assert.equal(
      getCurrentRoutingCostComparison({
        result,
        comparisonCatalogUpdatedAt: 100,
        catalog: { ...catalog, pricing_epoch: 8 },
        catalogUpdatedAt: 100,
        catalogFetching: false,
        catalogError: false,
      }),
      undefined
    )
    assert.equal(
      getCurrentRoutingCostComparison({
        result,
        comparisonCatalogUpdatedAt: 100,
        catalog,
        catalogUpdatedAt: 100,
        catalogFetching: true,
        catalogError: false,
      }),
      undefined
    )
  })
})
