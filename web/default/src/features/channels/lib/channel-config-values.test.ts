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

import { hasConfiguredOverrideValue } from './channel-config-values'

describe('channel config value helpers', () => {
  test('treats empty JSON override values as not configured', () => {
    for (const value of [undefined, '', '   ', 'null', '{}', '[]']) {
      assert.equal(hasConfiguredOverrideValue(value), false)
    }

    for (const value of [
      '{"temperature":0}',
      '[{"path":"temperature","value":0}]',
      'not-json-yet',
      '0',
      'false',
    ]) {
      assert.equal(hasConfiguredOverrideValue(value), true)
    }
  })
})
