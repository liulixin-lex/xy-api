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

import { channelRoutingAlgorithmKind } from './algorithm'

describe('channelRoutingAlgorithmKind', () => {
  test('distinguishes current unversioned identifiers from historical records', () => {
    assert.equal(
      channelRoutingAlgorithmKind('channel-routing-balanced'),
      'current'
    )
    assert.equal(
      channelRoutingAlgorithmKind('channel-routing-canary'),
      'current'
    )
    assert.equal(
      channelRoutingAlgorithmKind('channel-routing-shadow-v1'),
      'historical'
    )
    assert.equal(
      channelRoutingAlgorithmKind('channel-routing-balanced-v2'),
      'historical'
    )
  })

  test('fails visibly for identifiers outside the routing audit contract', () => {
    assert.equal(channelRoutingAlgorithmKind(''), 'unknown')
    assert.equal(channelRoutingAlgorithmKind('custom-selector'), 'unknown')
  })
})
