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

import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { channelRoutingIdentityTextIsTruncated } from '../lib/identity-text'
import { ChannelRoutingIdentityText } from './identity-text'

describe('channel routing identity text', () => {
  test('keeps ordinary text out of the keyboard tab order before overflow', () => {
    const html = renderToStaticMarkup(
      createElement(ChannelRoutingIdentityText, { text: 'short identity' })
    )

    assert.match(html, /^<span/)
    assert.doesNotMatch(html, /<button/)
  })

  test('detects horizontal and multi-line vertical truncation exactly', () => {
    assert.equal(
      channelRoutingIdentityTextIsTruncated({
        clientWidth: 100,
        scrollWidth: 101,
        clientHeight: 40,
        scrollHeight: 40,
      }),
      true
    )
    assert.equal(
      channelRoutingIdentityTextIsTruncated({
        clientWidth: 100,
        scrollWidth: 100,
        clientHeight: 40,
        scrollHeight: 41,
      }),
      true
    )
    assert.equal(
      channelRoutingIdentityTextIsTruncated({
        clientWidth: 100,
        scrollWidth: 100,
        clientHeight: 40,
        scrollHeight: 40,
      }),
      false
    )
  })
})
