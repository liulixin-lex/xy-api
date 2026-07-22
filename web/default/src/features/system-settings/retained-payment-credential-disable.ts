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
import {
  isEmergencyCredentialRevocationReasonValid,
  normalizeEmergencyCredentialRevocationReason,
} from './payment-credential-revocation'

export const RETAINED_PAYMENT_PROVIDERS = [
  'creem',
  'waffo',
  'waffo_pancake',
] as const

export type RetainedPaymentProvider =
  (typeof RETAINED_PAYMENT_PROVIDERS)[number]

export type RetainedCredentialDisableImpact = {
  provider: RetainedPaymentProvider
  mode: 'all_active'
  canonical_affected_orders: number
  canonical_unfinished_orders: number
  legacy_pending_topups: number
  legacy_pending_subscriptions: number
  unmatched_economic_events: number
  total_affected_orders: number
  total_unfinished_orders: number
}

export type RetainedCredentialDisablePreview = {
  configuration_version: number
  generated_at: number
  impact: RetainedCredentialDisableImpact
}

export type RetainedCredentialDisableResponse<T> = {
  success: boolean
  code?: string
  message?: string
  data?: T
}

export function buildRetainedCredentialDisablePreviewParams(
  provider: RetainedPaymentProvider
) {
  return { provider, mode: 'all_active' as const }
}

export function buildRetainedCredentialDisablePayload(
  provider: RetainedPaymentProvider,
  reason: string,
  expectedVersion: number
) {
  const normalizedReason = normalizeEmergencyCredentialRevocationReason(reason)
  if (!isEmergencyCredentialRevocationReasonValid(normalizedReason)) {
    throw new RangeError('emergency credential disable reason is invalid')
  }
  if (!Number.isSafeInteger(expectedVersion) || expectedVersion <= 0) {
    throw new RangeError('payment configuration version is invalid')
  }
  return {
    options: {},
    disable_current_credentials: [provider],
    reason: normalizedReason,
    expected_version: expectedVersion,
  }
}
