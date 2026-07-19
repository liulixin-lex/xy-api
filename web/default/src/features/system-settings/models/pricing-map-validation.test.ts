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
  isFiniteNonNegativeNestedNumberMap,
  isFiniteNonNegativeNumberMap,
  parseFiniteNonNegativeNumber,
} from './pricing-map-validation'

describe('pricing map validation', () => {
  test('accepts finite values including zero', () => {
    assert.equal(isFiniteNonNegativeNumberMap({ free: 0, paid: 1.25 }), true)
    assert.equal(
      isFiniteNonNegativeNestedNumberMap({ vip: { free: 0, paid: 0.9 } }),
      true
    )
    assert.equal(parseFiniteNonNegativeNumber('0'), 0)
    assert.equal(parseFiniteNonNegativeNumber('.5'), 0.5)
  })

  test('rejects negative, non-finite, null, and malformed values', () => {
    assert.equal(isFiniteNonNegativeNumberMap({ negative: -0.1 }), false)
    assert.equal(isFiniteNonNegativeNumberMap({ nan: Number.NaN }), false)
    assert.equal(isFiniteNonNegativeNumberMap({ infinite: Infinity }), false)
    assert.equal(isFiniteNonNegativeNumberMap({ null: null }), false)
    assert.equal(
      isFiniteNonNegativeNestedNumberMap({ vip: { negative: -0.1 } }),
      false
    )
    assert.equal(isFiniteNonNegativeNestedNumberMap({ vip: null }), false)
    assert.equal(parseFiniteNonNegativeNumber('-0.1'), null)
    assert.equal(parseFiniteNonNegativeNumber('Infinity'), null)
    assert.equal(parseFiniteNonNegativeNumber('1abc'), null)
    assert.equal(parseFiniteNonNegativeNumber(''), null)
  })
})
