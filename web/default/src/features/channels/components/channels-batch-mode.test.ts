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

function readSibling(fileName: string) {
  return readFileSync(join(currentDir, fileName), 'utf8')
}

describe('channel batch mode wiring', () => {
  test('keeps row selection and bulk actions behind an explicit batch mode toggle', () => {
    const providerSource = readSibling('channels-provider.tsx')
    const tableSource = readSibling('channels-table.tsx')
    const columnsSource = readSibling('channels-columns.tsx')
    const buttonsSource = readSibling('channels-primary-buttons.tsx')

    assert.match(providerSource, /batchMode: boolean/)
    assert.match(providerSource, /setBatchMode: \(enabled: boolean\) => void/)
    assert.match(
      providerSource,
      /const \[batchMode, setBatchMode\] = useState\(false\)/
    )

    assert.match(columnsSource, /enableSelection\?: boolean/)
    assert.match(columnsSource, /\.\.\.\(enableSelection\s*\?/)

    assert.match(
      tableSource,
      /const columns = useChannelsColumns\(\{ enableSelection: batchMode \}\)/
    )
    assert.match(tableSource, /enableRowSelection: batchMode/)
    assert.match(
      tableSource,
      /bulkActions=\{batchMode \? <DataTableBulkActions table=\{table\} \/> : null\}/
    )

    assert.match(buttonsSource, /checked=\{batchMode\}/)
    assert.match(buttonsSource, /setBatchMode\(checked\)/)
    assert.match(buttonsSource, /t\('Batch Operations'\)/)
  })

  test('requires confirmation before repairing channel consistency', () => {
    const buttonsSource = readSibling('channels-primary-buttons.tsx')
    const actionsSource = readFileSync(
      join(currentDir, '../lib/channel-actions.ts'),
      'utf8'
    )

    assert.match(buttonsSource, /showConsistencyDialog/)
    assert.match(buttonsSource, /setShowConsistencyDialog\(true\)/)
    assert.match(buttonsSource, /t\('Repair Channel Consistency'\)/)
    assert.match(buttonsSource, /t\('Repair channel consistency\?'\)/)
    assert.match(buttonsSource, /confirmText=\{t\('Repair'\)\}/)
    assert.match(buttonsSource, /handleFixAbilities\(queryClient/)

    assert.match(
      actionsSource,
      /i18next\.t\(\s*'Channel consistency repaired: \{\{success\}\} succeeded, \{\{fails\}\} failed'/
    )
    assert.match(
      actionsSource,
      /i18next\.t\('Failed to repair channel consistency'\)/
    )
  })

  test('passes card layout context to balance display for compact card values', () => {
    const cardSource = readSibling('channel-card.tsx')
    const columnsSource = readSibling('channels-columns.tsx')

    assert.match(
      cardSource,
      /<ChannelRowActionsLayoutContext\.Provider value='card'>[\s\S]*<div[\s\S]*data-state=\{isSelected \? 'selected' : undefined\}/
    )
    assert.doesNotMatch(
      cardSource,
      /<ChannelRowActionsLayoutContext\.Provider value='card'>\s*\{actionsCell\}\s*<\/ChannelRowActionsLayoutContext\.Provider>/
    )
    assert.match(
      columnsSource,
      /const layout = useContext\(ChannelRowActionsLayoutContext\)/
    )
    assert.match(columnsSource, /const showBalanceSymbol = layout !== 'card'/)
    assert.match(columnsSource, /showSymbol: showBalanceSymbol/)
  })
})
