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

const __dirname = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(join(__dirname, 'tool-price-settings.tsx'), 'utf8')

describe('tool price visual editor draft values', () => {
  test('does not turn an emptied price input into a free price', () => {
    assert.match(
      source,
      /parseFiniteNonNegativeNumber\(\s*event\.target\.value\s*\)/
    )
    assert.match(source, /if \(nextPrice !== null\)/)
    assert.doesNotMatch(source, /Number\(event\.target\.value\)/)
  })
})
