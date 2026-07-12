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
  parseSidebarModulesAdmin,
  serializeSidebarModulesAdmin,
} from './config'

describe('channel routing sidebar compatibility', () => {
  test('maps the legacy smart routing key to channel routing', () => {
    const parsed = parseSidebarModulesAdmin(
      JSON.stringify({
        admin: { enabled: true, smart_routing: false },
      })
    )

    assert.equal(parsed.admin.channel_routing, false)
    assert.equal('smart_routing' in parsed.admin, false)
  })

  test('keeps the new key authoritative when both keys are present', () => {
    const parsed = parseSidebarModulesAdmin(
      JSON.stringify({
        admin: {
          enabled: true,
          smart_routing: false,
          channel_routing: true,
        },
      })
    )

    assert.equal(parsed.admin.channel_routing, true)
    assert.equal('smart_routing' in parsed.admin, false)
    assert.equal(
      serializeSidebarModulesAdmin(parsed).includes('smart_routing'),
      false
    )
  })
})
