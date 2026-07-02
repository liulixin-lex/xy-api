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
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const currentDir = dirname(fileURLToPath(import.meta.url))
const homeSource = readFileSync(join(currentDir, 'index.tsx'), 'utf8')

describe('custom home page iframe sandbox', () => {
  test('allows same-origin access without broadening iframe navigation permissions', () => {
    const sandboxMatch = homeSource.match(/sandbox='([^']+)'/)

    assert.ok(sandboxMatch)
    assert.equal(
      sandboxMatch[1],
      'allow-scripts allow-same-origin allow-forms allow-popups allow-presentation'
    )
    assert.equal(sandboxMatch[1].includes('allow-top-navigation'), false)
    assert.equal(
      sandboxMatch[1].includes('allow-top-navigation-by-user-activation'),
      false
    )
    assert.equal(
      sandboxMatch[1].includes('allow-popups-to-escape-sandbox'),
      false
    )
    assert.equal(sandboxMatch[1].includes('allow-modals'), false)
    assert.equal(sandboxMatch[1].includes('allow-downloads'), false)
    assert.equal(sandboxMatch[1].includes('allow-pointer-lock'), false)
  })
})
