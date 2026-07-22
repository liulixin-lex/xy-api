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

import { buildWaffoPancakeSavePayload } from './waffo-pancake-api'
import { getWaffoPancakePricingError } from './waffo-pancake-pricing'

describe('Waffo Pancake settings request', () => {
  test('uses the dedicated save contract for pricing and environment', () => {
    assert.deepEqual(
      buildWaffoPancakeSavePayload({
        merchantID: 'MER_example',
        privateKey: '',
        returnURL: 'https://merchant.example/payments/return',
        storeID: 'STO_example',
        productID: 'PROD_example',
        unitPrice: 1.07,
        minTopUp: 5,
        testMode: true,
        expectedVersion: 12,
      }),
      {
        merchant_id: 'MER_example',
        private_key: '',
        return_url: 'https://merchant.example/payments/return',
        store_id: 'STO_example',
        product_id: 'PROD_example',
        unit_price: 1.07,
        min_top_up: 5,
        test_mode: true,
        expected_version: 12,
      }
    )
  })

  test('accepts only bounded positive pricing values', () => {
    assert.equal(getWaffoPancakePricingError(1.07, 5), null)
    assert.equal(getWaffoPancakePricingError(0, 5), 'unit_price')
    assert.equal(getWaffoPancakePricingError(Number.NaN, 5), 'unit_price')
    assert.equal(getWaffoPancakePricingError(1, 0), 'min_top_up')
    assert.equal(getWaffoPancakePricingError(1, 1.5), 'min_top_up')
  })

  test('sends the production default explicitly', () => {
    assert.equal(
      buildWaffoPancakeSavePayload({
        merchantID: '',
        privateKey: '',
        returnURL: '',
        storeID: '',
        productID: '',
        unitPrice: 1,
        minTopUp: 1,
        testMode: false,
        expectedVersion: 1,
      }).test_mode,
      false
    )
  })
})
