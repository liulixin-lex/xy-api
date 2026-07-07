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

function readDialogSource() {
  return readFileSync(join(currentDir, 'details-dialog.tsx'), 'utf8')
}

describe('usage log details dialog key wiring', () => {
  test('uses duplicate-safe keys for repeated detail rows that may share labels', () => {
    const source = readDialogSource()

    assert.match(source, /makeOccurrenceKey/)
    assert.match(source, /detailRowKey/)
    assert.match(source, /topupAuditFieldKeyCounts/)
    assert.match(source, /loginAuditFieldKeyCounts/)
    assert.match(source, /paramOverrideKeyCounts/)
    assert.doesNotMatch(source, /key=\{idx\}/)
  })
})
