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
  getVolcEngineBaseUrlSelectValue,
  VOLCENGINE_BASE_URL_OPTIONS,
  VOLCENGINE_DEFAULT_BASE_URL,
} from './volcengine-base-url'

describe('VolcEngine base URL options', () => {
  test('includes Doubao coding plan as a selectable saved value', () => {
    assert.deepEqual(
      VOLCENGINE_BASE_URL_OPTIONS.map((option) => option.value),
      [
        'https://ark.cn-beijing.volces.com',
        'https://ark.ap-southeast.bytepluses.com',
        'doubao-coding-plan',
      ]
    )
  })

  test('preserves Doubao coding plan as the select value', () => {
    assert.equal(
      getVolcEngineBaseUrlSelectValue('doubao-coding-plan'),
      'doubao-coding-plan'
    )
  })

  test('falls back to the default Beijing endpoint for an empty value', () => {
    assert.equal(
      getVolcEngineBaseUrlSelectValue(''),
      VOLCENGINE_DEFAULT_BASE_URL
    )
    assert.equal(
      getVolcEngineBaseUrlSelectValue(undefined),
      VOLCENGINE_DEFAULT_BASE_URL
    )
  })
})
