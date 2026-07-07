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

function readSource(fileName: string) {
  return readFileSync(join(currentDir, fileName), 'utf8')
}

describe('dynamic pricing key wiring', () => {
  test('uses duplicate-safe keys for repeated tier and rule summaries', () => {
    const source = readSource('dynamic-pricing-breakdown.tsx')

    assert.match(source, /makeOccurrenceKey/)
    assert.match(source, /tierMobileKeyCounts/)
    assert.match(source, /ruleGroupKeyCounts/)
    assert.doesNotMatch(source, /key=\{`tier-mobile-\$\{i\}`\}/)
    assert.doesNotMatch(source, /key=\{`group-\$\{gi\}`\}/)
  })
})
