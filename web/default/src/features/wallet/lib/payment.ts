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
  DEFAULT_MIN_TOPUP,
  MAX_TOPUP_AMOUNT,
} from '../constants'
import type {
  PaymentMethod,
  PaymentMethodProvider,
  PaymentJSAPIParameters,
  PaymentProvider,
  PresetAmount,
  PublicPaymentMethod,
  PublicPaymentStatusCode,
  TopupInfo,
} from '../types'

// ============================================================================
// Payment Processing Functions
// ============================================================================

export function getEffectivePaymentStatus(
  status: PublicPaymentStatusCode,
  expiresAt: number,
  nowMs: number = Date.now()
): PublicPaymentStatusCode {
  if (
    expiresAt > 0 &&
    nowMs >= expiresAt * 1000 &&
    (status === 'preparing' || status === 'awaiting_payment')
  ) {
    return 'expired'
  }
  return status
}

export function isPaymentReturnCancelled(search: string): boolean {
  try {
    return new URLSearchParams(search).get('payment_result') === 'cancelled'
  } catch {
    return false
  }
}

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

export function getSafePaymentContinueUrl(
  value: string,
  tradeNo: string
): URL | null {
  if (typeof window === 'undefined' || !tradeNo || tradeNo.length > 128) {
    return null
  }

  try {
    const url = new URL(value, window.location.origin)
    const expectedPath = `/api/user/payment/orders/${encodeURIComponent(tradeNo)}/continue`
    if (
      url.origin !== window.location.origin ||
      url.pathname !== expectedPath ||
      url.username ||
      url.password ||
      url.search ||
      url.hash
    ) {
      return null
    }
    return url
  } catch {
    return null
  }
}

export function getSafeWeChatAuthorizationUrl(
  value: string | undefined,
  tradeNo: string
): URL | null {
  if (
    typeof window === 'undefined' ||
    !value ||
    !tradeNo ||
    tradeNo.length > 128
  ) {
    return null
  }

  try {
    const url = new URL(value, window.location.origin)
    const expectedPath = `/api/user/payment/orders/${encodeURIComponent(tradeNo)}/wechat-authorize`
    if (
      url.origin !== window.location.origin ||
      url.pathname !== expectedPath ||
      url.username ||
      url.password ||
      url.search ||
      url.hash
    ) {
      return null
    }
    return url
  } catch {
    return null
  }
}

export type PaymentBrowserEnvironment = 'desktop' | 'mobile' | 'wechat'

export function detectPaymentBrowserEnvironment(
  userAgent = typeof navigator === 'undefined' ? '' : navigator.userAgent
): PaymentBrowserEnvironment {
  if (/MicroMessenger/i.test(userAgent)) return 'wechat'
  if (/Android|iPhone|iPad|iPod|Mobile/i.test(userAgent)) return 'mobile'
  return 'desktop'
}

export function filterPaymentMethodsForBrowser(
  methods: PaymentMethod[],
  environment: PaymentBrowserEnvironment = detectPaymentBrowserEnvironment()
): PaymentMethod[] {
  const wechatJSAPIGroups = new Set(
    methods
      .filter(
        (method) =>
          method.public_method === 'wechat_pay' &&
          (method.channel_alias === 'wechat_browser' ||
            method.channel_alias === 'jsapi')
      )
      .map((method) => method.public_method)
  )

  const environmentRoutes = methods.filter((method) => {
    if (method.public_method !== 'wechat_pay') return true

    const isJSAPI =
      method.channel_alias === 'wechat_browser' ||
      method.channel_alias === 'jsapi'
    if (environment !== 'wechat') return !isJSAPI

    const isNative =
      method.channel_alias === 'qr' || method.channel_alias === 'native'
    return !(isNative && wechatJSAPIGroups.has(method.public_method))
  })

  const selectedBrands = new Set<string>()
  return environmentRoutes.filter((method) => {
    if (
      method.public_method !== 'alipay' &&
      method.public_method !== 'wechat_pay'
    ) {
      return true
    }
    if (selectedBrands.has(method.public_method)) return false
    selectedBrands.add(method.public_method)
    return true
  })
}

export function isSafePaymentQrContent(value: string): boolean {
  const trimmed = value.trim()
  if (!trimmed || value !== trimmed || trimmed.length > 4096) return false
  if (
    hasForbiddenPaymentControlCharacters(trimmed) ||
    trimmed.includes('\\') ||
    trimmed.includes('#')
  ) {
    return false
  }

  if (trimmed.startsWith('weixin://')) {
    try {
      const url = new URL(trimmed)
      const queryEntries = [...url.searchParams.entries()]
      return (
        trimmed.startsWith('weixin://wxpay/bizpayurl?') &&
        url.protocol === 'weixin:' &&
        url.host === 'wxpay' &&
        !url.username &&
        !url.password &&
        !url.port &&
        !url.hash &&
        url.pathname === '/bizpayurl' &&
        queryEntries.length === 1 &&
        queryEntries[0][0] === 'pr' &&
        /^[A-Za-z0-9_-]{1,512}$/.test(queryEntries[0][1])
      )
    } catch {
      return false
    }
  }

  const hostedMatch = /^(https?):\/\/([^/?#]+)(\/[^?#]*)$/.exec(trimmed)
  if (hostedMatch?.[2].toLowerCase() === 'ipay.yltg.com.cn') {
    const path = hostedMatch[3]
    return (
      path.length >= 2 &&
      path.length <= 2049 &&
      /^\/[A-Za-z0-9._~/-]+$/.test(path) &&
      !path.startsWith('//') &&
      !path.includes('/../') &&
      !path.endsWith('/..')
    )
  }

  const alipayMatch = /^https:\/\/([^/?#]+)(\/[^?#]+)$/.exec(trimmed)
  if (alipayMatch?.[1].toLowerCase() !== 'qr.alipay.com') return false
  const path = alipayMatch[2]
  return (
    path.length >= 2 && path.length <= 2049 && /^\/[A-Za-z0-9._~-]+$/.test(path)
  )
}

function hasForbiddenPaymentControlCharacters(value: string): boolean {
  for (const character of value) {
    const codePoint = character.codePointAt(0) ?? 0
    if (codePoint <= 0x1f || codePoint === 0x7f) return true
  }
  return false
}

export function isSafePaymentJSAPIParameters(
  parameters: PaymentJSAPIParameters | undefined
): parameters is PaymentJSAPIParameters {
  if (!parameters) return false
  return (
    /^wx[A-Za-z0-9]{16}$/.test(parameters.app_id) &&
    /^\d{1,16}$/.test(parameters.timestamp) &&
    parameters.nonce_str.length > 0 &&
    parameters.nonce_str.length <= 128 &&
    !hasForbiddenPaymentControlCharacters(parameters.nonce_str) &&
    parameters.package.startsWith('prepay_id=') &&
    parameters.package.length <= 256 &&
    !hasForbiddenPaymentControlCharacters(parameters.package) &&
    ['MD5', 'HMAC-SHA256'].includes(parameters.sign_type) &&
    parameters.pay_sign.length >= 16 &&
    parameters.pay_sign.length <= 256 &&
    !hasForbiddenPaymentControlCharacters(parameters.pay_sign)
  )
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

type PaymentTranslate = (
  key: string,
  options?: Record<string, unknown>
) => string

const PAYMENT_ERROR_MESSAGES: Record<string, string> = {
  PAYMENT_INVALID_REQUEST:
    'Payment details are invalid. Review them and try again.',
  PAYMENT_REQUEST_INVALID:
    'Payment details are invalid. Review them and try again.',
  PAYMENT_METHOD_UNAVAILABLE: 'This payment method is temporarily unavailable',
  PAYMENT_AMOUNT_INVALID: 'Enter a valid payment amount.',
  PAYMENT_AMOUNT_BELOW_MINIMUM: 'The payment amount is below the minimum.',
  PAYMENT_SINGLE_LIMIT_EXCEEDED:
    'This amount exceeds the single payment limit.',
  PAYMENT_DAILY_LIMIT_EXCEEDED:
    'The daily limit for this payment method has been reached.',
  PAYMENT_QUOTE_LIMIT_REACHED:
    'Too many payment quotes are active. Wait for one to expire and try again.',
  PAYMENT_QUOTE_EXPIRED: 'Payment quote expired. Please request a new quote.',
  PAYMENT_QUOTE_CONSUMED:
    'This payment quote has already been used. Request a new quote.',
  PAYMENT_QUOTE_NOT_FOUND:
    'This payment quote is no longer available. Request a new quote.',
  PAYMENT_ORDER_LIMIT_REACHED:
    'Too many payment orders are active. Complete an existing order or wait for it to expire.',
  PAYMENT_IDEMPOTENCY_CONFLICT:
    'This payment conflicts with an earlier request. Refresh and try again.',
  PAYMENT_REQUEST_CONFLICT:
    'This payment conflicts with an earlier request. Refresh and try again.',
  PAYMENT_CONFIGURATION_CHANGED:
    'Payment settings changed. Request a new quote and try again.',
  PAYMENT_REVIEW_REQUIRED:
    'This payment needs review. Keep the order number and contact support.',
  PAYMENT_REQUIRES_SUPPORT:
    'This payment needs review. Keep the order number and contact support.',
  PAYMENT_ACCOUNT_UNAVAILABLE: 'This payment method is temporarily unavailable',
  PAYMENT_REDIRECT_INVALID: 'Payment is temporarily unavailable. Try again.',
  PAYMENT_PRODUCT_UNAVAILABLE: 'This payment method is temporarily unavailable',
  PAYMENT_COMPLIANCE_REQUIRED:
    'Payment is temporarily unavailable. Try again later or contact support.',
  SUBSCRIPTION_PURCHASE_LIMIT_REACHED:
    'The purchase limit for this access plan has been reached.',
  PAYMENT_CONFIRMATION_PENDING: 'Payment confirmation is pending',
  PAYMENT_ORDER_EXPIRED: 'Payment expired',
  PAYMENT_NOT_READY: 'Preparing your payment',
  PAYMENT_ORDER_NOT_FOUND: 'This payment order could not be found.',
  PAYMENT_TEMPORARILY_UNAVAILABLE:
    'Payment is temporarily unavailable. Try again.',
}

interface PaymentErrorDetails {
  code?: string
  params?: Record<string, unknown>
}

function getPaymentErrorDetails(source: unknown): PaymentErrorDetails {
  if (!source || typeof source !== 'object') return {}
  const candidate = source as {
    code?: unknown
    params?: unknown
    response?: { data?: unknown }
  }
  if (typeof candidate.code === 'string') {
    return {
      code: candidate.code,
      params:
        candidate.params && typeof candidate.params === 'object'
          ? (candidate.params as Record<string, unknown>)
          : undefined,
    }
  }
  return candidate.response?.data
    ? getPaymentErrorDetails(candidate.response.data)
    : {}
}

export function createPaymentError(
  source: unknown
): Error & PaymentErrorDetails {
  const error = new Error('payment_error') as Error & PaymentErrorDetails
  const details = getPaymentErrorDetails(source)
  error.code = details.code
  error.params = details.params
  return error
}

export function getPaymentErrorMessage(
  source: unknown,
  t: PaymentTranslate
): string {
  if (!source || typeof source !== 'object') {
    return t('Payment is temporarily unavailable. Try again.')
  }
  const details = getPaymentErrorDetails(source)
  if (details.code) {
    const normalizedCode = details.code.toUpperCase()
    if (
      normalizedCode === 'PAYMENT_AMOUNT_INVALID' &&
      typeof details.params?.min === 'number' &&
      typeof details.params?.max === 'number'
    ) {
      return t('Enter an amount between {{min}} and {{max}}.', {
        min: details.params.min,
        max: details.params.max,
      })
    }
    const message = PAYMENT_ERROR_MESSAGES[normalizedCode]
    if (message) return t(message)
  }
  return t('Payment is temporarily unavailable. Try again.')
}

const PUBLIC_ROUTE_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/
const PUBLIC_METHOD_PATTERN = /^[a-z][a-z0-9_]{0,63}$/
const PUBLIC_CHANNEL_ALIAS_PATTERN = /^[a-z][a-z0-9_]{0,63}$/
const INTERNAL_PAYMENT_TERMS =
  /(?:^|_)(?:epay|xorpay|stripe|waffo|creem)(?:_|$)/

export function isPublicPaymentRouteId(value: unknown): value is string {
  return typeof value === 'string' && PUBLIC_ROUTE_ID_PATTERN.test(value)
}

export function createLegacyPaymentRouteId(
  provider: PaymentMethodProvider,
  paymentMethod: string,
  displayName = ''
): string {
  const source = `${provider}\u0000${paymentMethod}\u0000${displayName}`
  let hash = 0x811c9dc5
  for (let index = 0; index < source.length; index += 1) {
    hash ^= source.charCodeAt(index)
    hash = Math.imul(hash, 0x01000193)
  }
  return `route_${(hash >>> 0).toString(16).padStart(8, '0')}`
}

export function normalizePaymentRouteId(
  value: unknown,
  provider: PaymentMethodProvider,
  paymentMethod: string,
  displayName = ''
): string {
  if (isPublicPaymentRouteId(value)) {
    return value
  }
  return createLegacyPaymentRouteId(provider, paymentMethod, displayName)
}

export function normalizePublicPaymentMethod(
  value: unknown,
  legacyPaymentMethod = '',
  legacyDisplayName = ''
): PublicPaymentMethod {
  if (typeof value === 'string') {
    const normalized = value.trim().toLowerCase()
    if (
      PUBLIC_METHOD_PATTERN.test(normalized) &&
      !INTERNAL_PAYMENT_TERMS.test(normalized)
    ) {
      return normalized
    }
  }

  const legacyValue =
    `${legacyPaymentMethod} ${legacyDisplayName}`.toLowerCase()
  if (legacyValue.includes('alipay')) return 'alipay'
  if (
    legacyValue.includes('wxpay') ||
    legacyValue.includes('wechat') ||
    legacyPaymentMethod === PAYMENT_TYPES.XORPAY_NATIVE
  ) {
    return 'wechat_pay'
  }
  if (legacyValue.includes('stripe') || legacyValue.includes('card')) {
    return 'card'
  }
  return 'online_payment'
}

export function normalizePaymentChannelAlias(
  value: unknown
): string | undefined {
  if (typeof value !== 'string') return undefined
  const normalized = value.trim().toLowerCase()
  if (
    !PUBLIC_CHANNEL_ALIAS_PATTERN.test(normalized) ||
    INTERNAL_PAYMENT_TERMS.test(normalized)
  ) {
    return undefined
  }
  return normalized
}

export function getPublicPaymentMethodLabel(
  method: Pick<PaymentMethod, 'public_method'>,
  t: PaymentTranslate
): string {
  switch (method.public_method) {
    case 'alipay':
      return t('Alipay')
    case 'wechat_pay':
      return t('WeChat Pay')
    case 'card':
      return t('Card payment')
    default:
      return t('Online payment')
  }
}

export function getPublicPaymentChannelLabel(
  method: PaymentMethod,
  methods: PaymentMethod[],
  t: PaymentTranslate
): string | null {
  const matchingMethods = methods.filter(
    (candidate) => candidate.public_method === method.public_method
  )
  if (matchingMethods.length > 1) {
    const aliasLabel = getPublicPaymentChannelAliasLabel(
      method.channel_alias,
      t
    )
    const sameAliasCount = matchingMethods.filter(
      (candidate) => candidate.channel_alias === method.channel_alias
    ).length
    if (aliasLabel && sameAliasCount === 1) return aliasLabel
    const index = matchingMethods.findIndex(
      (candidate) => candidate.route_id === method.route_id
    )
    if (index <= 0) return t('Recommended channel')
    return t('Backup channel {{number}}', { number: index })
  }

  return getPublicPaymentChannelAliasLabel(method.channel_alias, t)
}

export function getPublicPaymentChannelAliasLabel(
  channelAlias: string | undefined,
  t: PaymentTranslate
): string | null {
  switch (channelAlias) {
    case 'qr':
    case 'native':
      return t('QR payment')
    case 'redirect':
    case 'checkout':
      return t('Secure checkout')
    case 'jsapi':
    case 'wechat_browser':
      return t('WeChat in-app payment')
    case 'payment':
    case undefined:
      return null
    default:
      return t('Online payment')
  }
}

export function getPublicPaymentMethodIconType(
  method: Pick<PaymentMethod, 'public_method'>
): string | undefined {
  if (method.public_method === 'alipay') return PAYMENT_TYPES.ALIPAY
  if (method.public_method === 'wechat_pay') return PAYMENT_TYPES.WECHAT
  return undefined
}

export function getPublicPaymentRecordMethodName(
  publicMethod: unknown,
  t: PaymentTranslate
): string {
  return getPublicPaymentMethodLabel(
    {
      public_method: normalizePublicPaymentMethod(publicMethod),
    },
    t
  )
}

/**
 * Check if payment method is Stripe
 */
export function isStripePayment(paymentType: string): boolean {
  return paymentType === PAYMENT_TYPES.STRIPE
}

export function isUnifiedPaymentMethod(method: PaymentMethod): boolean {
  return method.checkout_mode === 'quote' || method.checkout_mode === 'direct'
}

export function filterEligibleSubscriptionQuoteMethods(
  methods: PaymentMethod[],
  eligibleRouteIDs: readonly string[] | undefined
): PaymentMethod[] {
  const eligibleRoutes = new Set(eligibleRouteIDs || [])
  return methods.filter(
    (method) =>
      method.checkout_mode === 'quote' && eligibleRoutes.has(method.route_id)
  )
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

export function isWaffoPancakeMethod(method: PaymentMethod): boolean {
  return method.checkout_mode === 'direct'
}

export function getDefaultPaymentMethod(
  topupInfo: TopupInfo | null
): PaymentMethod | null {
  if (!topupInfo) return null
  return (
    topupInfo.payment_routes.find(
      (method) =>
        method.checkout_mode === 'quote' || method.checkout_mode === 'direct'
    ) ?? null
  )
}

/**
 * Get minimum topup amount from topup info
 */
export function getMinTopupAmount(topupInfo: TopupInfo | null): number {
  if (!topupInfo) {
    return DEFAULT_MIN_TOPUP
  }

  const routeMinimums = topupInfo.payment_routes
    .filter((route) => route.checkout_mode !== 'product')
    .map((route) => Number(route.min_topup))
    .filter((minimum) => Number.isFinite(minimum) && minimum > 0)
  if (routeMinimums.length > 0) return Math.min(...routeMinimums)

  return topupInfo.min_topup || DEFAULT_MIN_TOPUP
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
