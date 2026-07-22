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

import { getSettingsPageLoadState } from './settings-page-state'

describe('settings page load state', () => {
  test('never renders editable defaults when required settings did not load', () => {
    assert.equal(
      getSettingsPageLoadState({
        settingsRequired: true,
        isLoading: false,
        isFetching: false,
        isError: true,
        hasOptions: false,
      }),
      'error'
    )
    assert.equal(
      getSettingsPageLoadState({
        settingsRequired: true,
        isLoading: false,
        isFetching: false,
        isError: false,
        hasOptions: false,
      }),
      'error'
    )
  })

  test('keeps retry loading visible until authoritative options arrive', () => {
    assert.equal(
      getSettingsPageLoadState({
        settingsRequired: true,
        isLoading: false,
        isFetching: true,
        isError: true,
        hasOptions: false,
      }),
      'loading'
    )
    assert.equal(
      getSettingsPageLoadState({
        settingsRequired: true,
        isLoading: false,
        isFetching: false,
        isError: false,
        hasOptions: true,
      }),
      'content'
    )
  })

  test('allows sections that do not depend on system options to render', () => {
    assert.equal(
      getSettingsPageLoadState({
        settingsRequired: false,
        isLoading: false,
        isFetching: false,
        isError: true,
        hasOptions: false,
      }),
      'content'
    )
  })
})
