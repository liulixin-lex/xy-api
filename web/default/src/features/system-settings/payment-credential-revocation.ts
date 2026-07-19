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
export type RevocablePaymentProvider = 'epay' | 'stripe' | 'xorpay'
export type EmergencyCredentialReplacementState =
  | 'none'
  | 'partial'
  | 'complete'
export type EmergencyCredentialRevocationMode =
  | 'replace'
  | 'revoke_previous'
  | 'stripe_disable'

export type EmergencyCredentialReplacement = {
  state: EmergencyCredentialReplacementState
  options: Record<string, string>
}

export type EmergencyCredentialReplacementInput = {
  identifier?: string
  savedIdentifier?: string
  secret?: string
}

export const EMERGENCY_CREDENTIAL_REVOCATION_REASON_MIN_LENGTH = 8
export const EMERGENCY_CREDENTIAL_REVOCATION_REASON_MAX_LENGTH = 512

export function normalizeEmergencyCredentialRevocationReason(
  reason: string
): string {
  return reason.trim()
}

export function isEmergencyCredentialRevocationReasonValid(
  reason: string
): boolean {
  const normalized = normalizeEmergencyCredentialRevocationReason(reason)
  return (
    normalized.length >= EMERGENCY_CREDENTIAL_REVOCATION_REASON_MIN_LENGTH &&
    normalized.length <= EMERGENCY_CREDENTIAL_REVOCATION_REASON_MAX_LENGTH
  )
}

export function buildEmergencyCredentialReplacement(
  provider: RevocablePaymentProvider,
  input: EmergencyCredentialReplacementInput
): EmergencyCredentialReplacement {
  const secret = (input.secret ?? '').trim()
  if (provider === 'stripe') {
    return secret
      ? {
          state: 'complete',
          options: { StripeWebhookSecret: secret },
        }
      : { state: 'none', options: {} }
  }

  const identifier = (input.identifier ?? '').trim()
  const savedIdentifier = (input.savedIdentifier ?? '').trim()
  if (!secret) {
    return {
      state: identifier && identifier !== savedIdentifier ? 'partial' : 'none',
      options: {},
    }
  }
  if (!identifier) return { state: 'partial', options: {} }

  if (provider === 'epay') {
    return {
      state: 'complete',
      options: { EpayId: identifier, EpayKey: secret },
    }
  }
  return {
    state: 'complete',
    options: { XorPayAid: identifier, XorPayAppSecret: secret },
  }
}

export function resolveEmergencyCredentialRevocationMode(
  provider: RevocablePaymentProvider,
  replacementState: EmergencyCredentialReplacementState,
  previousCredentialActive: boolean
): EmergencyCredentialRevocationMode | null {
  if (replacementState === 'partial') return null
  if (replacementState === 'complete') return 'replace'
  if (provider === 'stripe') return 'stripe_disable'
  return previousCredentialActive ? 'revoke_previous' : null
}
