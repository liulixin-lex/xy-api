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
const source = readFileSync(
  join(__dirname, 'group-ratio-visual-editor.tsx'),
  'utf8'
)

describe('group ratio visual editor draft values', () => {
  test('keeps group ratio input as a string draft while editing decimals', () => {
    assert.match(source, /ratio:\s+string/)
    assert.match(source, /ratio:\s+String\(normalizeRatio\(ratioMap\[name\]\)\)/)
    assert.match(source, /ratio:\s+'1'/)
    assert.match(source, /value=\{row\.ratio\}/)
    assert.match(source, /updateRow\(row\._id,\s*'ratio',\s*event\.target\.value\)/)
  })
})
