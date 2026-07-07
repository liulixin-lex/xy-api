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

import { RedemptionsProvider } from './redemptions-provider'
import { RedemptionsPrimaryButtons } from './redemptions-primary-buttons'

describe('RedemptionsPrimaryButtons', () => {
  test('surfaces invalid-code cleanup next to create code', () => {
    const html = renderToStaticMarkup(
      createElement(
        RedemptionsProvider,
        null,
        createElement(RedemptionsPrimaryButtons)
      )
    )

    assert.match(html, /Delete Invalid/)
    assert.match(html, /Create Code/)
  })
})
