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
  initialCostCatalogNavigationState,
  reduceCostCatalogNavigation,
} from './cost-catalog-navigation'

describe('cost catalog hierarchy navigation', () => {
  test('selects pool, channel, and model in order and clears lower levels', () => {
    let state = reduceCostCatalogNavigation(initialCostCatalogNavigationState, {
      type: 'select-pool',
      poolId: 7,
    })
    state = reduceCostCatalogNavigation(state, {
      type: 'select-member',
      memberId: 12,
    })
    state = reduceCostCatalogNavigation(state, {
      type: 'select-model',
      modelName: 'gpt-cost',
    })

    assert.equal(state.selectedPoolId, 7)
    assert.equal(state.selectedMemberId, 12)
    assert.equal(state.selectedModelName, 'gpt-cost')

    state = reduceCostCatalogNavigation(state, {
      type: 'select-pool',
      poolId: 8,
    })
    assert.equal(state.selectedPoolId, 8)
    assert.equal(state.selectedMemberId, undefined)
    assert.equal(state.selectedModelName, undefined)
  })

  test('resets dependent pagination when an upper-level search changes', () => {
    const state = reduceCostCatalogNavigation(
      {
        ...initialCostCatalogNavigationState,
        poolPage: 3,
        memberPage: 4,
        modelPage: 5,
        selectedPoolId: 7,
        selectedMemberId: 12,
        selectedModelName: 'gpt-cost',
      },
      { type: 'search-pools', value: 'premium' }
    )

    assert.equal(state.poolSearch, 'premium')
    assert.equal(state.poolPage, 1)
    assert.equal(state.memberPage, 1)
    assert.equal(state.modelPage, 1)
    assert.equal(state.selectedPoolId, undefined)
    assert.equal(state.selectedMemberId, undefined)
    assert.equal(state.selectedModelName, undefined)
  })
})
