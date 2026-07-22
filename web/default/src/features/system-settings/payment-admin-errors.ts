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
import type { TFunction } from 'i18next'

import { getApiErrorMessage } from '@/lib/api-error'

type PaymentAdminErrorSource = {
  code?: unknown
  data?: { code?: unknown; data?: { code?: unknown } }
  response?: { data?: { code?: unknown; data?: { code?: unknown } } }
  cause?: unknown
}

export function getPaymentAdminErrorCode(value: unknown): string | null {
  if (!value || typeof value !== 'object') return null
  const source = value as PaymentAdminErrorSource
  const responseCode = source.response?.data?.code
  const responseDataCode = source.response?.data?.data?.code
  const businessCode = source.data?.code
  const nestedBusinessCode = source.data?.data?.code

  // Axios exposes transport identifiers such as ERR_BAD_REQUEST on `code`.
  // Prefer the server's business code so verification, permission, and
  // configuration errors are not collapsed into a generic transport error.
  const candidates = [
    responseCode,
    responseDataCode,
    businessCode,
    nestedBusinessCode,
    source.code,
  ]
  const code = candidates.find(
    (candidate) =>
      typeof candidate === 'string' &&
      candidate.trim() &&
      !candidate.startsWith('ERR_')
  )
  if (typeof code === 'string') return code.trim()

  if (source.cause && source.cause !== value) {
    return getPaymentAdminErrorCode(source.cause)
  }
  return null
}

export function createPaymentAdminError(
  response: { code?: string; message?: string },
  fallback: string
): Error & { code?: string; skipGlobalError: true } {
  const error = new Error(response.message || fallback) as Error & {
    code?: string
    skipGlobalError: true
  }
  error.skipGlobalError = true
  if (response.code) error.code = response.code
  return error
}

export function getPaymentAdminErrorMessage(
  error: unknown,
  t: TFunction,
  fallback: string
): string {
  const code = getPaymentAdminErrorCode(error)
  if (!code) return getApiErrorMessage(error, fallback)
  let message = t(
    'The payment operation failed. Review the error code and try again.'
  )
  if (code === 'payment_limit_list_unavailable') {
    message = t('Payment limit policies could not be loaded. Try again.')
  } else if (code === 'payment_limit_usage_unavailable') {
    message = t('Current payment limit usage could not be loaded. Try again.')
  } else if (code === 'payment_overview_unavailable') {
    message = t('Payment overview could not be loaded. Try again.')
  } else if (code === 'payment_overview_schema_invalid') {
    message = t(
      'Payment overview data is from an incompatible or incomplete server version. Refresh after the server is updated.'
    )
  } else if (code === 'payment_operations_schema_not_ready') {
    message = t(
      'Payment operations need a database migration before operational data can be loaded.'
    )
  } else if (code === 'payment_limit_invalid') {
    message = t(
      'The payment limit settings are invalid. Review the method, currency, amounts, and time zone.'
    )
  } else if (code === 'payment_limit_timezone_locked') {
    message = t(
      'This payment policy time zone is locked because usage already exists.'
    )
  } else if (code === 'payment_limit_reload_failed') {
    message = t(
      'Payment limit was saved, but the latest policy could not be reloaded.'
    )
  } else if (code === 'payment_limit_usage_refresh_failed') {
    message = t(
      'Payment limit was saved, but current usage could not be refreshed.'
    )
  } else if (code === 'payment_settings_auth_required') {
    message = t('You are not authorized to change payment settings.')
  } else if (code === 'VERIFICATION_REQUIRED') {
    message = t('Additional security verification is required to continue.')
  } else if (code === 'VERIFICATION_EXPIRED') {
    message = t('Your security verification expired. Verify again and retry.')
  } else if (code === 'VERIFICATION_INVALID') {
    message = t('Security verification failed. Verify your identity and retry.')
  } else if (code === 'payment_settings_permission_denied') {
    message = t('You do not have permission to change payment settings.')
  } else if (code === 'payment_operations_permission_denied') {
    message = t(
      'You do not have permission to perform this payment administration action.'
    )
  } else if (code === 'payment_operations_auth_required') {
    message = t(
      'Use an authenticated administrator browser session for this payment operation.'
    )
  } else if (code === 'stripe_inventory_cancel_invalid') {
    message = t(
      'The Stripe cancellation request is invalid. Review the reason and refresh the inventory.'
    )
  } else if (code === 'stripe_inventory_subscription_not_found') {
    message = t(
      'This Stripe subscription is no longer in the inventory. Refresh before continuing.'
    )
  } else if (code === 'stripe_inventory_cancel_conflict') {
    message = t(
      'This Stripe subscription changed after it was loaded. Refresh the inventory before retrying.'
    )
  } else if (code === 'stripe_inventory_cancel_not_configured') {
    message = t(
      'The current Stripe credentials cannot manage legacy subscription cancellations.'
    )
  } else if (code === 'stripe_inventory_cancel_account_mismatch') {
    message = t(
      'This legacy subscription belongs to a different Stripe account than the configured credentials.'
    )
  } else if (code === 'stripe_inventory_cancel_mode_mismatch') {
    message = t(
      'This legacy subscription uses a different Stripe test or live mode than the configured credentials.'
    )
  } else if (code === 'stripe_inventory_cancel_unavailable') {
    message = t(
      'Stripe could not schedule this cancellation. The subscription was not marked as canceled locally.'
    )
  } else if (code === 'payment_cluster_unready') {
    message = t(
      'Payment services are not ready across the application nodes. Try again later.'
    )
  } else if (code === 'payment_settings_field_invalid') {
    message = t(
      'One or more payment settings are invalid. Review the highlighted fields.'
    )
  } else if (code === 'payment_settings_sync_failed') {
    message = t(
      'Payment settings could not be synchronized across the application.'
    )
  } else if (code === 'payment_settings_invalid') {
    message = t(
      'The payment settings are invalid. Review this section and try again.'
    )
  } else if (code === 'payment_settings_scope_conflict') {
    message = t(
      'This save conflicts with another payment settings section. Reload and try again.'
    )
  } else if (code === 'payment_settings_secret_storage_unavailable') {
    message = t(
      'Payment secrets could not be stored securely. No credentials were changed.'
    )
  } else if (code === 'payment_settings_stripe_verification_failed') {
    message = t(
      'Stripe could not verify these credentials. Review the Stripe account settings.'
    )
  } else if (code === 'payment_settings_stripe_checkout_hosts_invalid') {
    message = t(
      'Enter only exact custom Stripe Checkout hostnames. Wildcards, URLs, ports, IP addresses, localhost, and credentials are not allowed.'
    )
  } else if (code === 'payment_settings_stripe_test_mode_disabled') {
    message = t('Stripe test mode is disabled for this installation.')
  } else if (code === 'payment_settings_rotation_blocked') {
    message = t(
      'Credential rotation is blocked while earlier orders still require the previous credential.'
    )
  } else if (code === 'payment_settings_change_blocked') {
    message = t('Payment settings changes are temporarily blocked for safety.')
  } else if (code === 'payment_settings_version_conflict') {
    message = t(
      'Payment settings changed in another session. Reload before saving again.'
    )
  } else if (code === 'payment_settings_save_failed') {
    message = t('Payment settings could not be saved. No changes were applied.')
  } else if (code === 'waffo_pancake_configuration_incomplete') {
    message = t(
      'Waffo Pancake configuration is incomplete. Provide credentials, return settings, and a Store and Product binding.'
    )
  } else if (code === 'waffo_pancake_credentials_invalid') {
    message = t('Waffo Pancake could not verify these merchant credentials.')
  } else if (code === 'waffo_pancake_catalog_unavailable') {
    message = t('Waffo Pancake catalog could not be loaded. Try again.')
  } else if (code === 'waffo_pancake_pair_create_failed') {
    message = t('Waffo Pancake could not create the Store and Product.')
  } else if (code === 'waffo_pancake_pair_partial_failure') {
    message = t(
      'The Waffo Pancake Store was created, but the Product could not be created. Review the orphan Store before retrying.'
    )
  } else if (code === 'waffo_pancake_product_create_failed') {
    message = t('Waffo Pancake could not create the Product.')
  }
  return `${message} (${code})`
}

export function getRetainedCredentialDisableErrorMessage(
  error: unknown,
  t: TFunction,
  fallback: string
): string {
  const code = getPaymentAdminErrorCode(error)
  if (
    code === 'payment_credential_revocation_unavailable' ||
    code === 'payment_settings_rotation_blocked'
  ) {
    return `${t(
      'No active current credential is available to disable for this gateway.'
    )} (${code})`
  }
  if (code === 'payment_credential_revocation_preview_invalid') {
    return `${t(
      'The emergency impact preview request is invalid. Reload the payment settings and try again.'
    )} (${code})`
  }
  if (code === 'payment_credential_revocation_preview_unavailable') {
    return `${t(
      'The emergency impact preview could not be loaded. Try again before continuing.'
    )} (${code})`
  }
  return getPaymentAdminErrorMessage(error, t, fallback)
}

export function getSubscriptionPlanAdminErrorMessage(
  error: unknown,
  t: TFunction
): string {
  const code = getPaymentAdminErrorCode(error)
  if (code === 'subscription_plan_invalid') {
    return t(
      'The subscription plan settings are invalid. Review the plan details and try again.'
    )
  }
  if (code === 'subscription_plan_save_failed') {
    return t(
      'The subscription plan could not be saved. No changes were applied.'
    )
  }
  if (code === 'payment_temporarily_unavailable') {
    return t(
      'Subscription plan changes are temporarily unavailable. Try again later.'
    )
  }
  return t('The subscription plan could not be saved. Try again.')
}
