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
  formatShanghaiTimestamp,
  renderTrustedActivityDescription,
} from './activity-description.ts'

describe('activity description helpers', () => {
  test('renders markdown while preserving trusted raw HTML', () => {
    const html = renderTrustedActivityDescription(
      '## Activity\n\n<section data-test="raw">Trusted HTML</section>\n\n<script>alert(1)</script>'
    )

    assert.match(html, /<h2>Activity<\/h2>/)
    assert.match(html, /<section data-test="raw">Trusted HTML<\/section>/)
    assert.match(html, /<script>alert\(1\)<\/script>/)
  })

  test('formats timestamps in Shanghai time', () => {
    assert.equal(formatShanghaiTimestamp(1_800_000_000), '2027-01-15 16:00')
    assert.equal(formatShanghaiTimestamp(0), '-')
  })
})
