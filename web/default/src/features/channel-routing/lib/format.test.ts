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

import { channelRoutingIntlLocale, formatChannelRoutingCost } from './format'

describe('channel routing cost formatting', () => {
  test('preserves meaningful digits for small non-zero costs', () => {
    assert.equal(formatChannelRoutingCost(0.0000045, 'en-US'), '0.0000045')
    assert.equal(formatChannelRoutingCost(0.0000061, 'en-US'), '0.0000061')
    assert.notEqual(formatChannelRoutingCost(0.0000045, 'en-US'), '0')
  })

  test('keeps an explicit known zero distinct from an unknown value', () => {
    assert.equal(formatChannelRoutingCost(0, 'en-US'), '0')
    assert.equal(formatChannelRoutingCost(Number.NaN, 'en-US'), '')
  })

  test('maps internal Chinese locale codes to valid Intl locales', () => {
    assert.equal(channelRoutingIntlLocale('zhCN'), 'zh-CN')
    assert.equal(channelRoutingIntlLocale('zhTW'), 'zh-TW')
    assert.doesNotThrow(() => formatChannelRoutingCost(12.5, 'zhCN'))
    assert.doesNotThrow(() => formatChannelRoutingCost(12.5, 'zhTW'))
  })
})
