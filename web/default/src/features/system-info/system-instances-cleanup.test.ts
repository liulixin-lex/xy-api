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

function readFeatureSource(fileName: string) {
  return readFileSync(join(currentDir, fileName), 'utf8')
}

describe('system instance stale cleanup wiring', () => {
  test('exposes API helpers for deleting stale instance records', () => {
    const apiSource = readFeatureSource('api.ts')
    const typesSource = readFeatureSource('types.ts')

    assert.match(typesSource, /type SystemInstanceDeleteResponse/)
    assert.match(typesSource, /deleted_count:\s*number/)
    assert.match(apiSource, /deleteStaleSystemInstances/)
    assert.match(apiSource, /\/api\/system-info\/stale-instances/)
    assert.match(apiSource, /deleteStaleSystemInstance\(nodeName: string\)/)
    assert.match(apiSource, /encodeURIComponent\(nodeName\)/)
  })

  test('renders stale-only cleanup controls with destructive confirmation', () => {
    const panelSource = readFileSync(
      join(currentDir, 'components/system-instances-panel.tsx'),
      'utf8'
    )

    assert.match(panelSource, /useMutation/)
    assert.match(panelSource, /useQueryClient/)
    assert.match(panelSource, /ConfirmDialog/)
    assert.match(panelSource, /Trash2/)
    assert.match(panelSource, /toast\.success\(t\('Deleted stale instance'\)\)/)
    assert.match(panelSource, /toast\.success\(\s*t\('Deleted \{\{count\}\} stale instances'/)
    assert.match(panelSource, /instance\.status === 'stale'/)
    assert.match(panelSource, /staleInstances\.length === 0/)
    assert.match(panelSource, /t\('Delete all stale'\)/)
    assert.match(panelSource, /t\('Delete stale instances'\)/)
    assert.match(panelSource, /t\('Delete stale instance'\)/)
    assert.match(
      panelSource,
      /Delete \{\{count\}\} stale instance records\? Online instances will not be deleted\./
    )
  })
})
