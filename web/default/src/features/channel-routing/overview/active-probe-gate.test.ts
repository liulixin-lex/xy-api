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

import type {
  ChannelRoutingOverview,
  ChannelRoutingRuntimeSettings,
  SmartRoutingSetting,
} from '../types'
import { resolveActiveProbeGate } from './active-probe-gate'

const settings = {
  active_probe_enabled: true,
} as SmartRoutingSetting
const runtimeSettings = {
  settings,
} as ChannelRoutingRuntimeSettings

function overview(
  enabled: boolean,
  effectiveMode: ChannelRoutingOverview['effective_mode']
): Pick<ChannelRoutingOverview, 'enabled' | 'effective_mode'> {
  return { enabled, effective_mode: effectiveMode }
}

describe('active probe capability gate', () => {
  test('fails closed for every server-side prerequisite', () => {
    assert.deepEqual(
      resolveActiveProbeGate({
        overview: overview(false, 'legacy'),
        runtimeSettings,
        runtimeSettingsLoading: false,
        runtimeSettingsError: false,
      }),
      { allowed: false, reason: 'routing_disabled' }
    )

    for (const mode of ['legacy', 'observe', 'shadow'] as const) {
      assert.deepEqual(
        resolveActiveProbeGate({
          overview: overview(true, mode),
          runtimeSettings,
          runtimeSettingsLoading: false,
          runtimeSettingsError: false,
        }),
        { allowed: false, reason: 'mode_unsupported' }
      )
    }

    assert.deepEqual(
      resolveActiveProbeGate({
        overview: overview(true, 'balanced'),
        runtimeSettings: undefined,
        runtimeSettingsLoading: true,
        runtimeSettingsError: false,
      }),
      { allowed: false, reason: 'settings_loading' }
    )

    assert.deepEqual(
      resolveActiveProbeGate({
        overview: overview(true, 'balanced'),
        runtimeSettings: undefined,
        runtimeSettingsLoading: false,
        runtimeSettingsError: true,
      }),
      { allowed: false, reason: 'settings_unavailable' }
    )

    assert.deepEqual(
      resolveActiveProbeGate({
        overview: overview(true, 'balanced'),
        runtimeSettings,
        runtimeSettingsLoading: false,
        runtimeSettingsError: true,
      }),
      { allowed: false, reason: 'settings_unavailable' }
    )

    assert.deepEqual(
      resolveActiveProbeGate({
        overview: overview(true, 'enterprise_slo'),
        runtimeSettings: {
          settings: { ...settings, active_probe_enabled: false },
        },
        runtimeSettingsLoading: false,
        runtimeSettingsError: false,
      }),
      { allowed: false, reason: 'active_probe_disabled' }
    )
  })

  test('allows only balanced-capable modes with the active probe switch on', () => {
    for (const mode of ['balanced', 'enterprise_slo'] as const) {
      assert.deepEqual(
        resolveActiveProbeGate({
          overview: overview(true, mode),
          runtimeSettings,
          runtimeSettingsLoading: false,
          runtimeSettingsError: false,
        }),
        { allowed: true, reason: null }
      )
    }
  })
})
