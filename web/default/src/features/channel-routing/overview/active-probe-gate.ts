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
import type {
  ChannelRoutingOverview,
  ChannelRoutingRuntimeSettings,
} from '../types'

export type ActiveProbeGateReason =
  | 'routing_disabled'
  | 'mode_unsupported'
  | 'settings_loading'
  | 'settings_unavailable'
  | 'active_probe_disabled'

export type ActiveProbeGate =
  | { allowed: true; reason: null }
  | { allowed: false; reason: ActiveProbeGateReason }

export function resolveActiveProbeGate(input: {
  overview: Pick<ChannelRoutingOverview, 'enabled' | 'effective_mode'>
  runtimeSettings?: Pick<ChannelRoutingRuntimeSettings, 'settings'>
  runtimeSettingsLoading: boolean
  runtimeSettingsError: boolean
}): ActiveProbeGate {
  if (!input.overview.enabled) {
    return { allowed: false, reason: 'routing_disabled' }
  }
  if (
    input.overview.effective_mode !== 'balanced' &&
    input.overview.effective_mode !== 'enterprise_slo'
  ) {
    return { allowed: false, reason: 'mode_unsupported' }
  }
  if (!input.runtimeSettings) {
    return {
      allowed: false,
      reason:
        input.runtimeSettingsLoading && !input.runtimeSettingsError
          ? 'settings_loading'
          : 'settings_unavailable',
    }
  }
  if (input.runtimeSettingsError) {
    return { allowed: false, reason: 'settings_unavailable' }
  }
  if (!input.runtimeSettings.settings.active_probe_enabled) {
    return { allowed: false, reason: 'active_probe_disabled' }
  }
  return { allowed: true, reason: null }
}
