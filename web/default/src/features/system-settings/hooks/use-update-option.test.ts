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

import { isStatusRelatedOptionKey } from '../utils/status-related-options.ts'

describe('system option status refresh keys', () => {
  test('refreshes status after API info action visibility changes', () => {
    assert.equal(
      isStatusRelatedOptionKey('console_setting.api_info_test_latency_enabled'),
      true
    )
    assert.equal(
      isStatusRelatedOptionKey(
        'console_setting.api_info_external_speed_test_enabled'
      ),
      true
    )
    assert.equal(
      isStatusRelatedOptionKey('console_setting.api_info_open_new_tab_enabled'),
      true
    )
  })
})
