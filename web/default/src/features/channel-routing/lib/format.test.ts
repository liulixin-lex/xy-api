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

import { resolveChannelRoutingTimestamp } from './format'

describe('channel routing timestamp resolution', () => {
  test('supports second and millisecond timestamps', () => {
    assert.deepEqual(resolveChannelRoutingTimestamp(1_700_000_000), {
      kind: 'date',
      milliseconds: 1_700_000_000_000,
    })
    assert.deepEqual(resolveChannelRoutingTimestamp(1_700_000_000_000), {
      kind: 'date',
      milliseconds: 1_700_000_000_000,
    })
  })

  test('treats the int64 no-expiry sentinel as never without constructing an invalid Date', () => {
    assert.deepEqual(
      resolveChannelRoutingTimestamp(9_223_372_036_854_776_000),
      { kind: 'never' }
    )
  })

  test('distinguishes corrupt timestamps from an absent timestamp', () => {
    assert.deepEqual(resolveChannelRoutingTimestamp(0), { kind: 'never' })
    assert.deepEqual(resolveChannelRoutingTimestamp(Number.NaN), {
      kind: 'invalid',
    })
    assert.deepEqual(resolveChannelRoutingTimestamp(8_700_000_000_000_000), {
      kind: 'invalid',
    })
  })
})
