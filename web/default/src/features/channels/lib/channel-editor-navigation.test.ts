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
  ADVANCED_SETTINGS_SECTION_IDS,
  CHANNEL_EDITOR_SECTION_IDS,
  buildChannelEditorNavigationState,
} from './channel-editor-navigation'
import { CHANNEL_FORM_DEFAULT_VALUES } from './channel-form'

describe('channel editor navigation state', () => {
  test('marks required editor sections complete when visible required fields are filled', () => {
    const state = buildChannelEditorNavigationState({
      values: {
        ...CHANNEL_FORM_DEFAULT_VALUES,
        name: 'OpenAI primary',
        type: 1,
        status: 1,
        key: 'sk-test',
        models: 'gpt-4.1,gpt-4.1-mini',
        group: ['default'],
      },
      errors: {},
      isEditing: false,
    })

    assert.equal(state.progressLabel, '3/3')
    assert.equal(
      state.items.find(
        (item) => item.id === CHANNEL_EDITOR_SECTION_IDS.identity
      )?.status,
      'complete'
    )
    assert.equal(
      state.items.find(
        (item) => item.id === CHANNEL_EDITOR_SECTION_IDS.credentials
      )?.status,
      'complete'
    )
    assert.equal(
      state.items.find((item) => item.id === CHANNEL_EDITOR_SECTION_IDS.models)
        ?.status,
      'complete'
    )
  })

  test('maps field errors and configured advanced settings to navigation state', () => {
    const state = buildChannelEditorNavigationState({
      values: {
        ...CHANNEL_FORM_DEFAULT_VALUES,
        name: 'Claude backup',
        type: 14,
        status: 1,
        key: 'anthropic-key',
        models: 'claude-sonnet-4',
        group: ['default'],
        param_override: '{"temperature":0}',
        allow_speed: true,
        upstream_model_update_check_enabled: true,
      },
      errors: {
        model_mapping: { message: 'Invalid mapping' },
        proxy: { message: 'Invalid proxy' },
      },
      isEditing: false,
    })

    assert.equal(
      state.items.find((item) => item.id === CHANNEL_EDITOR_SECTION_IDS.models)
        ?.status,
      'error'
    )

    const advancedItem = state.items.find(
      (item) => item.id === CHANNEL_EDITOR_SECTION_IDS.advanced
    )
    assert.equal(advancedItem?.status, 'error')
    assert.equal(advancedItem?.configured, true)
    assert.deepEqual(
      advancedItem?.children
        ?.filter((child) => child.configured)
        .map((child) => child.id),
      [
        ADVANCED_SETTINGS_SECTION_IDS.overrideRules,
        ADVANCED_SETTINGS_SECTION_IDS.fieldPassthrough,
        ADVANCED_SETTINGS_SECTION_IDS.upstreamModelDetection,
      ]
    )
  })
})
