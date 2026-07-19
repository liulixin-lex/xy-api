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
  PAYMENT_TYPES,
  DEFAULT_PRESET_MULTIPLIERS,
  DEFAULT_PAYMENT_TYPE,
  DEFAULT_MIN_TOPUP,
  MAX_TOPUP_AMOUNT,
} from '../constants'
import type {
  PaymentMethod,
  PaymentMethodProvider,
  PaymentProvider,
  PresetAmount,
  TopupInfo,
} from '../types'

// ============================================================================
// Payment Processing Functions
// ============================================================================

/**
 * Check if browser is Safari
 */
function isLoopbackHostname(hostname: string): boolean {
  return (
    hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '[::1]'
  )
}

export function getSafePaymentUrl(value: string): URL | null {
  if (typeof window === 'undefined') return null

  try {
    const url = new URL(value, window.location.origin)
    if (url.username || url.password) return null
    if (url.protocol === 'https:') return url

    const sameLoopbackOrigin =
      url.protocol === 'http:' &&
      isLoopbackHostname(url.hostname) &&
      isLoopbackHostname(window.location.hostname)
    return sameLoopbackOrigin ? url : null
  } catch {
    return null
  }
}

export function navigateToPaymentUrl(value: string): boolean {
  const url = getSafePaymentUrl(value)
  if (!url) return false
  window.location.assign(url.href)
  return true
}

export function isSafePaymentQrContent(value: string): boolean {
  const trimmed = value.trim()
  if (!trimmed || trimmed.length > 4096) return false
  if (trimmed.startsWith('weixin://wxpay/')) return true
  try {
    const url = new URL(trimmed)
    return (
      url.protocol === 'https:' &&
      url.hostname.toLowerCase() === 'qr.alipay.com' &&
      !url.username &&
      !url.password &&
      (!url.port || url.port === '443')
    )
  } catch {
    return false
  }
}

/**
 * Submit payment form (for non-Stripe payments)
 */
export function submitPaymentForm(
  url: string,
  params: Record<string, unknown>
): boolean {
  const safeUrl = getSafePaymentUrl(url)
  if (!safeUrl) return false

  const form = document.createElement('form')
  form.action = safeUrl.href
  form.method = 'POST'
  form.target = '_self'
  form.referrerPolicy = 'no-referrer'

  // Add form parameters
  Object.entries(params).forEach(([key, value]) => {
    const input = document.createElement('input')
    input.type = 'hidden'
    input.name = key
    input.value = String(value)
    form.appendChild(input)
  })

  document.body.appendChild(form)
  form.submit()
  document.body.removeChild(form)
  return true
}

export function isPaymentProvider(value: string): value is PaymentProvider {
  return value === 'epay' || value === 'stripe' || value === 'xorpay'
}

export function getPaymentMethodProvider(
  type: string,
  configuredProvider?: unknown
): PaymentMethodProvider {
  if (typeof configuredProvider === 'string') {
    if (
      isPaymentProvider(configuredProvider) ||
      configuredProvider === 'creem' ||
      configuredProvider === 'waffo' ||
      configuredProvider === 'waffo_pancake'
    ) {
      return configuredProvider
    }
  }

  if (type === PAYMENT_TYPES.STRIPE) return 'stripe'
  if (
    type === PAYMENT_TYPES.XORPAY_NATIVE ||
    type === PAYMENT_TYPES.XORPAY_ALIPAY
  ) {
    return 'xorpay'
  }
  if (type === PAYMENT_TYPES.CREEM) return 'creem'
  if (type === PAYMENT_TYPES.WAFFO) return 'waffo'
  if (type === PAYMENT_TYPES.WAFFO_PANCAKE) return 'waffo_pancake'
  return 'epay'
}

/**
 * Check if payment method is Stripe
 */
export function isStripePayment(paymentType: string): boolean {
  return paymentType === PAYMENT_TYPES.STRIPE
}

export function isUnifiedPaymentMethod(
  method: PaymentMethod
): method is PaymentMethod & { provider: PaymentProvider } {
  return isPaymentProvider(method.provider)
}

/**
 * Check if payment method is Waffo Pancake
 *
 * Pancake is a metered-style payment that goes through a dedicated checkout
 * URL flow rather than the generic epay form submission, so it must be
 * special-cased in payment dispatch logic.
 */
export function isWaffoPancakePayment(paymentType: string): boolean {
  return paymentType === PAYMENT_TYPES.WAFFO_PANCAKE
}

/**
 * Get default payment type from topup info
 */
export function getDefaultPaymentType(topupInfo: TopupInfo | null): string {
  if (!topupInfo) {
    return DEFAULT_PAYMENT_TYPE
  }

  // Return first available payment method or default
  if (topupInfo.pay_methods?.length > 0) {
    return topupInfo.pay_methods[0].type
  }

  if (topupInfo.enable_stripe_topup) {
    return PAYMENT_TYPES.STRIPE
  }

  if (topupInfo.enable_waffo_topup) {
    return PAYMENT_TYPES.WAFFO
  }

  if (topupInfo.enable_waffo_pancake_topup) {
    return PAYMENT_TYPES.WAFFO_PANCAKE
  }

  return DEFAULT_PAYMENT_TYPE
}

export function getDefaultPaymentMethod(
  topupInfo: TopupInfo | null
): PaymentMethod | null {
  if (!topupInfo) return null
  return topupInfo.pay_methods?.[0] ?? null
}

/**
 * Get minimum topup amount from topup info
 */
export function getMinTopupAmount(topupInfo: TopupInfo | null): number {
  if (!topupInfo) {
    return DEFAULT_MIN_TOPUP
  }

  const defaultMethodMinimum = Number(topupInfo.pay_methods?.[0]?.min_topup)
  if (Number.isFinite(defaultMethodMinimum) && defaultMethodMinimum > 0) {
    return defaultMethodMinimum
  }

  if (topupInfo.enable_online_topup) {
    return topupInfo.min_topup
  }

  if (topupInfo.enable_stripe_topup) {
    return topupInfo.stripe_min_topup
  }

  if (topupInfo.enable_xorpay_topup) {
    return topupInfo.xorpay_min_topup || DEFAULT_MIN_TOPUP
  }

  if (topupInfo.enable_waffo_topup) {
    return topupInfo.waffo_min_topup || DEFAULT_MIN_TOPUP
  }

  if (topupInfo.enable_waffo_pancake_topup) {
    return topupInfo.waffo_pancake_min_topup || DEFAULT_MIN_TOPUP
  }

  return DEFAULT_MIN_TOPUP
}

/**
 * Generate preset amounts based on minimum topup
 */
export function generatePresetAmounts(minAmount: number): PresetAmount[] {
  return DEFAULT_PRESET_MULTIPLIERS.map((multiplier) => ({
    value: minAmount * multiplier,
  })).filter(
    (preset) =>
      Number.isSafeInteger(preset.value) &&
      preset.value >= minAmount &&
      preset.value <= MAX_TOPUP_AMOUNT
  )
}

/**
 * Merge custom preset amounts with discounts
 */
export function mergePresetAmounts(
  amountOptions: number[],
  discounts: Record<number, number>
): PresetAmount[] {
  if (!amountOptions || amountOptions.length === 0) {
    return []
  }

  return amountOptions
    .filter(
      (amount) =>
        Number.isSafeInteger(amount) &&
        amount >= 1 &&
        amount <= MAX_TOPUP_AMOUNT
    )
    .map((amount) => ({
      value: amount,
      discount: discounts[amount] || 1.0,
    }))
}
