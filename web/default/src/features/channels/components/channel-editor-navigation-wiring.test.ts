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

function readDrawerSource() {
  return readFileSync(
    join(currentDir, 'drawers/channel-mutate-drawer.tsx'),
    'utf8'
  )
}

describe('channel editor navigation wiring', () => {
  test('renders desktop navigation beside anchored editor sections', () => {
    const drawerSource = readDrawerSource()

    assert.match(
      drawerSource,
      /<ChannelEditorNav[\s\S]*items=\{editorNavItems\}/
    )
    assert.match(drawerSource, /ref=\{channelFormRef\}/)
    assert.match(drawerSource, /lg:grid-cols-\[13rem_minmax\(0,1fr\)\]/)

    for (const idKey of ['identity', 'credentials', 'models', 'advanced']) {
      assert.match(
        drawerSource,
        new RegExp(`id=\\{CHANNEL_EDITOR_SECTION_IDS\\.${idKey}\\}`)
      )
    }

    for (const idKey of [
      'routingStrategy',
      'internalNotes',
      'overrideRules',
      'extraSettings',
      'fieldPassthrough',
      'upstreamModelDetection',
    ]) {
      assert.match(
        drawerSource,
        new RegExp(`id=\\{\\s*ADVANCED_SETTINGS_SECTION_IDS\\.${idKey}\\s*\\}`)
      )
    }
  })

  test('keeps navigation synchronized with scroll and invalid submissions', () => {
    const drawerSource = readDrawerSource()

    assert.match(drawerSource, /handleEditorNavNavigate/)
    assert.match(drawerSource, /scrollIntoView\(\{ behavior: 'smooth'/)
    assert.match(drawerSource, /updateActiveEditorSection/)
    assert.match(drawerSource, /formElement\.addEventListener\('scroll'/)
    assert.match(drawerSource, /setActiveEditorSectionId\(/)
    assert.match(drawerSource, /setExpandedEditorNavItemId\(/)
    assert.match(
      drawerSource,
      /if \(hasAdvancedSettingsErrors\(errors\)\)[\s\S]*handleAdvancedSettingsOpenChange\(true\)/
    )
  })
})
