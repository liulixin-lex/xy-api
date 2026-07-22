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
import i18next from 'i18next'

import { api, get2FAStatus } from '@/lib/api'
import {
  buildAssertionResult,
  prepareCredentialRequestOptions,
  isPasskeySupported as detectPasskeySupport,
} from '@/lib/passkey'

import {
  beginPasskeyVerification,
  finishPasskeyVerification,
  getPasskeyStatus,
} from '../passkey'
import type { VerificationMethod, VerificationMethods } from './types'

type SecureVerificationResponse = {
  success?: boolean
  code?: string
}

class SecureVerificationError extends Error {
  code?: string

  constructor(message: string, code?: string, cause?: unknown) {
    super(message, cause === undefined ? undefined : { cause })
    this.name = 'SecureVerificationError'
    this.code = code
  }
}

const secureVerificationErrorKeys: Record<string, string> = {
  secure_verification_auth_required:
    'Sign in again before completing security verification.',
  secure_verification_request_invalid:
    'The security verification request is invalid. Try again.',
  secure_verification_user_unavailable:
    'Security verification is temporarily unavailable. Try again.',
  secure_verification_account_disabled:
    'This account cannot complete security verification.',
  secure_verification_method_unavailable:
    'This verification method is unavailable. Choose another method or try again.',
  secure_verification_not_configured:
    'Enable Two-factor Authentication or Passkey before continuing.',
  secure_verification_code_required:
    'Please enter the verification code or backup code.',
  secure_verification_passkey_state_invalid:
    'Passkey verification expired or is invalid. Start again.',
  secure_verification_passkey_required:
    'Complete Passkey verification before continuing.',
  secure_verification_session_unavailable:
    'Security verification could not be saved. Try again.',
  secure_verification_method_invalid:
    'This security verification method is not supported.',
  secure_verification_failed:
    'The verification code or backup code is incorrect.',
}

function secureVerificationError(
  response: SecureVerificationResponse | undefined,
  fallbackKey: string,
  cause?: unknown
): SecureVerificationError {
  const code =
    typeof response?.code === 'string' && response.code.trim()
      ? response.code.trim()
      : undefined
  const messageKey = code ? secureVerificationErrorKeys[code] : undefined
  return new SecureVerificationError(
    i18next.t(messageKey ?? fallbackKey),
    code,
    cause
  )
}

function secureVerificationResponseFromError(
  error: unknown
): SecureVerificationResponse | undefined {
  if (!error || typeof error !== 'object' || !('response' in error)) {
    return undefined
  }
  const response = error.response
  if (!response || typeof response !== 'object' || !('data' in response)) {
    return undefined
  }
  const data = response.data
  return data && typeof data === 'object'
    ? (data as SecureVerificationResponse)
    : undefined
}

/**
 * Fetch available verification methods for the current user.
 */
export async function checkVerificationMethods(): Promise<VerificationMethods> {
  try {
    const [twoFAResponse, passkeyResponse, passkeySupported] =
      await Promise.all([
        get2FAStatus(),
        getPasskeyStatus(),
        detectPasskeySupport(),
      ])

    const has2FA =
      Boolean(twoFAResponse?.success) && Boolean(twoFAResponse?.data?.enabled)
    const hasPasskey =
      Boolean(passkeyResponse?.success) &&
      Boolean(passkeyResponse?.data?.enabled)

    return {
      has2FA,
      hasPasskey,
      passkeySupported,
    }
  } catch (error) {
    // eslint-disable-next-line no-console
    console.error('[Secure Verification] Failed to check methods', error)
    return {
      has2FA: false,
      hasPasskey: false,
      passkeySupported: false,
    }
  }
}

/**
 * Execute a verification flow based on the method type.
 */
export async function verify(
  method: VerificationMethod,
  code?: string
): Promise<void> {
  switch (method) {
    case '2fa':
      return verifyTwoFA(code)
    case 'passkey':
      return verifyPasskey()
    default:
      throw secureVerificationError(
        undefined,
        'This security verification method is not supported.'
      )
  }
}

/**
 * Perform 2FA verification flow.
 */
async function verifyTwoFA(code?: string | null): Promise<void> {
  const trimmed = code?.trim()
  if (!trimmed) {
    throw secureVerificationError(
      undefined,
      'Please enter the verification code or backup code.'
    )
  }

  try {
    const res = await api.post<SecureVerificationResponse>(
      '/api/verify',
      {
        method: '2fa',
        code: trimmed,
      },
      {
        skipBusinessError: true,
        skipErrorHandler: true,
        skipGlobalError: true,
      }
    )

    if (!res.data?.success) {
      throw secureVerificationError(res.data, 'Verification failed')
    }
  } catch (error) {
    if (error instanceof SecureVerificationError) throw error
    throw secureVerificationError(
      secureVerificationResponseFromError(error),
      'Verification failed',
      error
    )
  }
}

/**
 * Perform Passkey verification flow.
 */
async function verifyPasskey(): Promise<void> {
  if (typeof navigator === 'undefined' || !navigator.credentials) {
    throw secureVerificationError(
      undefined,
      'Passkey verification is not supported in this environment.'
    )
  }

  try {
    const beginResponse = await beginPasskeyVerification()
    if (!beginResponse.success) {
      throw secureVerificationError(
        beginResponse,
        'Passkey verification could not be started. Try again.'
      )
    }

    const publicKey = prepareCredentialRequestOptions(
      beginResponse.data?.options ?? beginResponse.data
    )

    const credential = (await navigator.credentials.get({
      publicKey,
    })) as PublicKeyCredential | null

    if (!credential) {
      throw secureVerificationError(
        undefined,
        'Passkey verification was cancelled.'
      )
    }

    const assertion = buildAssertionResult(credential)
    if (!assertion) {
      throw secureVerificationError(
        undefined,
        'Passkey verification could not be completed. Try again.'
      )
    }

    const finishResponse = await finishPasskeyVerification(assertion)
    if (!finishResponse.success) {
      throw secureVerificationError(
        finishResponse,
        'Passkey verification failed. Try again.'
      )
    }

    const verifyResponse = await api.post<SecureVerificationResponse>(
      '/api/verify',
      {
        method: 'passkey',
      },
      {
        skipBusinessError: true,
        skipErrorHandler: true,
        skipGlobalError: true,
      }
    )

    if (!verifyResponse.data?.success) {
      throw secureVerificationError(
        verifyResponse.data,
        'Security verification could not be completed. Try again.'
      )
    }
  } catch (error: unknown) {
    if (error instanceof SecureVerificationError) throw error
    if (error instanceof DOMException && error.name === 'NotAllowedError') {
      throw secureVerificationError(
        undefined,
        'Passkey verification was cancelled or timed out.',
        error
      )
    }
    if (error instanceof DOMException && error.name === 'InvalidStateError') {
      throw secureVerificationError(
        undefined,
        'Passkey verification is not available in the current state.',
        error
      )
    }
    const response = secureVerificationResponseFromError(error)
    if (response) {
      throw secureVerificationError(
        response,
        'Passkey verification failed. Try again.',
        error
      )
    }
    throw secureVerificationError(
      undefined,
      'Passkey verification failed. Try again.',
      error
    )
  }
}
