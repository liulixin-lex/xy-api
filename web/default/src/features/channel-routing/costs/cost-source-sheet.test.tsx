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

import '@/i18n/config'

import { CostSourceCredentialSummary } from './cost-source-sheet'

describe('cost source credential summary', () => {
  test('renders only server-provided masks in a semantic details list', () => {
    const html = renderToStaticMarkup(
      createElement(CostSourceCredentialSummary, {
        masks: {
          new_api_access_token: '****1234',
          gateway_api_key: '',
          sub2api_email: 'a***z@example.com',
          custom_ca_configured: true,
        },
      })
    )

    assert.match(html, /<dl/)
    assert.match(html, /<dt/)
    assert.match(html, /<dd/)
    assert.match(html, /\*\*\*\*1234/)
    assert.match(html, /a\*\*\*z@example\.com/)
    assert.match(html, /Configured/)
    assert.doesNotMatch(html, /gateway_api_key/)
  })
})
