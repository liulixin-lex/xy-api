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
import type { StripePaymentGatewayReadiness } from '../api'

export type StripeTestModeNotice = {
  state: 'blocked' | 'enabled'
  isolationRequired: boolean
}

export function resolveStripeTestModeNotice(input: {
  credentialLivemode: string
  initialEnabled: boolean
  initialBlocked: boolean
  initialIsolationRequired: boolean
  readiness?: StripePaymentGatewayReadiness
}): StripeTestModeNotice | null {
  if (input.credentialLivemode.trim().toLowerCase() !== 'test') return null

  const enabled =
    typeof input.readiness?.test_mode_enabled === 'boolean'
      ? input.readiness.test_mode_enabled
      : input.initialEnabled
  const blocked =
    typeof input.readiness?.test_mode_blocked === 'boolean'
      ? input.readiness.test_mode_blocked
      : input.initialBlocked
  const isolationRequired =
    typeof input.readiness?.test_mode_isolation_required === 'boolean'
      ? input.readiness.test_mode_isolation_required
      : input.initialIsolationRequired

  if (blocked) return { state: 'blocked', isolationRequired }
  if (enabled) return { state: 'enabled', isolationRequired }
  return null
}
