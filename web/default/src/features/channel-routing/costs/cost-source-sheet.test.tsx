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
import { revealSideDrawerAlert } from '@/components/drawer-layout'

import { COST_BINDING_GROUP_DOM_LIMIT } from '../lib/cost-binding'
import { CostSourceCredentialSummary } from './cost-source-credentials'
import { CostSourceGroupDatalist } from './cost-source-groups'

describe('cost source conflict recovery', () => {
  test('reveals and focuses a stale-write alert immediately', () => {
    const events: Array<[string, unknown]> = []
    const alert = {
      scrollIntoView: (options?: ScrollIntoViewOptions) => {
        events.push(['scroll', options])
      },
      focus: (options?: FocusOptions) => {
        events.push(['focus', options])
      },
    }

    revealSideDrawerAlert(alert)

    assert.deepEqual(events, [
      ['scroll', { block: 'nearest' }],
      ['focus', { preventScroll: true }],
    ])
  })
})

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

  test('replaces credential decryption details with an actionable recovery message', () => {
    const html = renderToStaticMarkup(
      createElement(CostSourceCredentialSummary, {
        masks: {},
        error: 'cipher: message authentication failed for credential 77',
      })
    )

    assert.match(html, /Credentials need to be re-entered/)
    assert.match(html, /administrator with credential access/i)
    assert.doesNotMatch(html, /No credentials are saved/i)
    assert.doesNotMatch(html, /cipher|authentication failed|credential 77/i)
  })

  test('keeps group suggestion DOM bounded for oversized upstream responses', () => {
    const html = renderToStaticMarkup(
      createElement(CostSourceGroupDatalist, {
        id: 'groups',
        groups: Array.from(
          { length: COST_BINDING_GROUP_DOM_LIMIT + 500 },
          (_, index) => `group-${index}`
        ),
      })
    )

    assert.equal((html.match(/<option/g) ?? []).length, 100)
    assert.doesNotMatch(html, /group-100"/)
  })
})
