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
import { api, type ApiRequestConfig } from '@/lib/api'

import {
  getPaymentMethodProvider,
  normalizePaymentChannelAlias,
  normalizePaymentRouteId,
  normalizePublicPaymentMethod,
} from './lib/payment'
import type {
  RedemptionRequest,
  PaymentRequest,
  AmountRequest,
  AffiliateTransferRequest,
  ApiResponse,
  TopupInfoResponse,
  RedemptionResponse,
  AmountResponse,
  PaymentResponse,
  StripePaymentResponse,
  AffiliateCodeResponse,
  AffiliateTransferResponse,
  BillingHistoryResponse,
  CreemPaymentRequest,
  CreemPaymentResponse,
  WaffoPaymentRequest,
  WaffoPaymentResponse,
  WaffoPancakePaymentRequest,
  WaffoPancakePaymentResponse,
  PaymentQuoteRequest,
  PaymentQuoteResponse,
  PaymentStartRequest,
  PaymentStartResponse,
  PaymentOrderResponse,
  PaymentOrder,
  PaymentCheckout,
  PaymentJSAPIParameters,
  PublicPaymentStatusCode,
  TopupRecord,
} from './types'

// ============================================================================
// Wallet API Functions
// ============================================================================

const PAYMENT_UI_REQUEST_CONFIG: ApiRequestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
}

function normalizePublicPaymentStatus(value: unknown): PublicPaymentStatusCode {
  if (
    value === 'preparing' ||
    value === 'awaiting_payment' ||
    value === 'confirming' ||
    value === 'succeeded' ||
    value === 'expired' ||
    value === 'temporarily_unavailable'
  ) {
    return value
  }

  if (value === 'success' || value === 'fulfilled') return 'succeeded'
  if (value === 'paid' || value === 'processing') return 'confirming'
  if (value === 'pending') return 'preparing'
  return 'temporarily_unavailable'
}

function normalizePaymentDecimal(value: unknown, fallback = '0'): string {
  const candidate =
    typeof value === 'string' ? value.trim() : String(value ?? '')
  if (/^(?:0|[1-9]\d{0,15})(?:\.\d{1,3})?$/.test(candidate)) {
    return candidate
  }
  return fallback
}

function legacyPaymentMinorToDecimal(
  value: unknown,
  currency: string,
  provider?: string
): string {
  const amountMinor = Number(value)
  if (!Number.isSafeInteger(amountMinor) || amountMinor < 0) return '0'

  let fractionDigits = 2
  try {
    fractionDigits =
      new Intl.NumberFormat('en', {
        style: 'currency',
        currency,
        ...(provider === 'stripe' && (currency === 'ISK' || currency === 'UGX')
          ? { minimumFractionDigits: 2, maximumFractionDigits: 2 }
          : {}),
      }).resolvedOptions().maximumFractionDigits ?? 2
  } catch {
    fractionDigits = 2
  }
  return (amountMinor / 10 ** fractionDigits).toFixed(fractionDigits)
}

export function normalizePublicPaymentOrder(
  data: unknown
): PaymentOrder | null {
  if (!data || typeof data !== 'object') return null
  const raw = data as Record<string, unknown>
  const tradeNo = typeof raw.trade_no === 'string' ? raw.trade_no : ''
  if (!tradeNo) return null

  const legacyPaymentMethod =
    typeof raw.payment_method === 'string' ? raw.payment_method : ''
  const legacyProvider = getPaymentMethodProvider(
    legacyPaymentMethod,
    raw.provider ?? raw.payment_provider
  )
  const isSubscription =
    raw.plan_id !== undefined || raw.order_kind === 'subscription'
  const currency =
    typeof raw.currency === 'string' && /^[A-Za-z]{3}$/.test(raw.currency)
      ? raw.currency.toUpperCase()
      : 'USD'
  const rawCheckout =
    raw.checkout && typeof raw.checkout === 'object'
      ? (raw.checkout as Record<string, unknown>)
      : null
  let checkout: PaymentCheckout | undefined
  if (rawCheckout) {
    const checkoutFlow = rawCheckout.flow
    if (
      checkoutFlow === 'pending' ||
      checkoutFlow === 'qr' ||
      checkoutFlow === 'hosted_redirect' ||
      checkoutFlow === 'form_post' ||
      checkoutFlow === 'wechat_authorize' ||
      checkoutFlow === 'jsapi'
    ) {
      const rawJSAPI =
        rawCheckout.jsapi && typeof rawCheckout.jsapi === 'object'
          ? (rawCheckout.jsapi as Record<string, unknown>)
          : null
      let jsapi: PaymentJSAPIParameters | undefined
      if (
        checkoutFlow === 'jsapi' &&
        rawJSAPI &&
        typeof rawJSAPI.app_id === 'string' &&
        typeof rawJSAPI.timestamp === 'string' &&
        typeof rawJSAPI.nonce_str === 'string' &&
        typeof rawJSAPI.package === 'string' &&
        (rawJSAPI.sign_type === 'MD5' ||
          rawJSAPI.sign_type === 'HMAC-SHA256') &&
        typeof rawJSAPI.pay_sign === 'string'
      ) {
        jsapi = {
          app_id: rawJSAPI.app_id,
          timestamp: rawJSAPI.timestamp,
          nonce_str: rawJSAPI.nonce_str,
          package: rawJSAPI.package,
          sign_type: rawJSAPI.sign_type,
          pay_sign: rawJSAPI.pay_sign,
        }
      }
      checkout = {
        flow: checkoutFlow,
        qr_content:
          checkoutFlow === 'qr' && typeof rawCheckout.qr_content === 'string'
            ? rawCheckout.qr_content
            : undefined,
        continue_url:
          (checkoutFlow === 'hosted_redirect' ||
            checkoutFlow === 'form_post' ||
            checkoutFlow === 'wechat_authorize') &&
          typeof rawCheckout.continue_url === 'string'
            ? rawCheckout.continue_url
            : undefined,
        jsapi,
        expires_at: Number(rawCheckout.expires_at) || 0,
      }
    }
  }

  return {
    trade_no: tradeNo,
    route_id: normalizePaymentRouteId(
      raw.route_id,
      legacyProvider,
      legacyPaymentMethod,
      tradeNo
    ),
    public_method: normalizePublicPaymentMethod(
      raw.public_method,
      legacyPaymentMethod
    ),
    channel_alias: normalizePaymentChannelAlias(raw.channel_alias),
    status_code: normalizePublicPaymentStatus(raw.status_code ?? raw.status),
    payment_amount: normalizePaymentDecimal(
      raw.payment_amount,
      legacyPaymentMinorToDecimal(
        raw.expected_amount_minor,
        currency,
        legacyProvider
      )
    ),
    ...(isSubscription
      ? { plan_id: Number(raw.plan_id ?? raw.requested_amount) || undefined }
      : {
          top_up_amount:
            Number(raw.top_up_amount ?? raw.requested_amount) || undefined,
        }),
    currency,
    expires_at: Number(raw.expires_at) || 0,
    completed_at: Number(raw.completed_at ?? raw.settled_at) || undefined,
    checkout,
  }
}

export function normalizePublicBillingRecord(
  data: unknown
): TopupRecord | null {
  if (!data || typeof data !== 'object') return null
  const raw = data as Record<string, unknown>
  const tradeNo = typeof raw.trade_no === 'string' ? raw.trade_no : ''
  if (!tradeNo) return null

  const currency =
    typeof raw.currency === 'string' && /^[A-Za-z]{3}$/.test(raw.currency)
      ? raw.currency.toUpperCase()
      : 'CNY'
  const legacyPaymentMethod =
    typeof raw.payment_method === 'string' ? raw.payment_method : ''
  const legacyProvider = getPaymentMethodProvider(
    legacyPaymentMethod,
    raw.provider ?? raw.payment_provider
  )
  const legacyMoney = Number(raw.money)
  const legacyPaymentAmount = Number.isFinite(legacyMoney)
    ? normalizePaymentDecimal(legacyMoney.toFixed(2))
    : '0'

  return {
    id: Number(raw.id) || 0,
    amount: Number(raw.amount) || 0,
    payment_amount: normalizePaymentDecimal(
      raw.payment_amount,
      raw.expected_amount_minor !== undefined
        ? legacyPaymentMinorToDecimal(
            raw.expected_amount_minor,
            currency,
            legacyProvider
          )
        : legacyPaymentAmount
    ),
    trade_no: tradeNo,
    route_id: normalizePaymentRouteId(
      raw.route_id,
      legacyProvider,
      legacyPaymentMethod,
      tradeNo
    ),
    public_method: normalizePublicPaymentMethod(
      raw.public_method,
      legacyPaymentMethod
    ),
    channel_alias: normalizePaymentChannelAlias(raw.channel_alias),
    currency,
    created_at: Number(raw.created_at ?? raw.create_time) || 0,
    completed_at: Number(raw.completed_at ?? raw.complete_time) || undefined,
    status_code: normalizePublicPaymentStatus(raw.status_code ?? raw.status),
  }
}

/**
 * Check if API response is successful
 */
export function isApiSuccess(response: ApiResponse): boolean {
  return response.success === true || response.message === 'success'
}

/**
 * Get topup configuration info
 */
export async function getTopupInfo(): Promise<TopupInfoResponse> {
  const res = await api.get('/api/user/topup/info', PAYMENT_UI_REQUEST_CONFIG)
  return res.data
}

/**
 * Create a short-lived, server-authoritative payment quote.
 */
export async function createPaymentQuote(
  request: PaymentQuoteRequest,
  signal?: AbortSignal
): Promise<PaymentQuoteResponse> {
  const res = await api.post('/api/user/payment/quote', request, {
    ...PAYMENT_UI_REQUEST_CONFIG,
    signal,
  })
  const response = res.data as PaymentQuoteResponse & {
    data?: Record<string, unknown>
  }
  if (!response.data) return response

  const raw = response.data
  const legacyRoute =
    request.provider && request.payment_method
      ? {
          provider: request.provider,
          payment_method: request.payment_method,
        }
      : undefined
  const routeId = normalizePaymentRouteId(
    raw.route_id ?? ('route_id' in request ? request.route_id : undefined),
    legacyRoute?.provider || 'epay',
    legacyRoute?.payment_method || '',
    typeof raw.quote_id === 'string' ? raw.quote_id : ''
  )
  response.data = {
    quote_id: typeof raw.quote_id === 'string' ? raw.quote_id : '',
    route_id: routeId,
    public_method: normalizePublicPaymentMethod(
      raw.public_method,
      legacyRoute?.payment_method
    ),
    channel_alias: normalizePaymentChannelAlias(raw.channel_alias),
    ...(request.order_kind === 'subscription'
      ? { plan_id: Number(raw.plan_id ?? request.plan_id) || undefined }
      : {
          top_up_amount:
            Number(raw.top_up_amount ?? request.amount) || undefined,
        }),
    payable_amount: normalizePaymentDecimal(raw.payable_amount),
    currency:
      typeof raw.currency === 'string' && /^[A-Za-z]{3}$/.test(raw.currency)
        ? raw.currency.toUpperCase()
        : 'USD',
    expires_at: Number(raw.expires_at) || 0,
  }
  return response
}

/**
 * Start a payment from a previously issued quote.
 */
export async function startPayment(
  request: PaymentStartRequest
): Promise<PaymentStartResponse> {
  const res = await api.post('/api/user/payment/start', request, {
    ...PAYMENT_UI_REQUEST_CONFIG,
  })
  return res.data
}

/**
 * Read the local order state. Provider return pages and QR polling must use
 * this endpoint instead of trusting query parameters or provider payloads.
 */
export async function getPaymentOrder(
  tradeNo: string,
  signal?: AbortSignal
): Promise<PaymentOrderResponse> {
  const res = await api.get(
    `/api/user/payment/orders/${encodeURIComponent(tradeNo)}`,
    {
      signal,
      ...PAYMENT_UI_REQUEST_CONFIG,
    }
  )
  const response = res.data as PaymentOrderResponse & { data?: unknown }
  if (!response.data) return response
  response.data = normalizePublicPaymentOrder(response.data) || undefined
  return response
}

/**
 * Redeem a topup code
 */
export async function redeemTopupCode(
  request: RedemptionRequest
): Promise<RedemptionResponse> {
  const res = await api.post('/api/user/topup', request)
  return res.data
}

/**
 * Calculate payment amount for regular payment
 */
export async function calculateAmount(
  request: AmountRequest
): Promise<AmountResponse> {
  const res = await api.post('/api/user/amount', request, {
    ...PAYMENT_UI_REQUEST_CONFIG,
  })
  return res.data
}

/**
 * Calculate payment amount for Stripe payment
 */
export async function calculateStripeAmount(
  request: AmountRequest
): Promise<AmountResponse> {
  const res = await api.post('/api/user/stripe/amount', request, {
    ...PAYMENT_UI_REQUEST_CONFIG,
  })
  return res.data
}

/**
 * Request regular payment
 */
export async function requestPayment(
  request: PaymentRequest
): Promise<PaymentResponse> {
  const res = await api.post('/api/user/pay', request, {
    ...PAYMENT_UI_REQUEST_CONFIG,
  })
  return {
    ...res.data,
    url: res.data.url || (res as unknown as { url?: string }).url,
  }
}

/**
 * Request Stripe payment
 */
export async function requestStripePayment(
  request: PaymentRequest
): Promise<StripePaymentResponse> {
  const res = await api.post('/api/user/stripe/pay', request, {
    ...PAYMENT_UI_REQUEST_CONFIG,
  })
  return res.data
}

/**
 * Request Creem payment
 */
export async function requestCreemPayment(
  request: CreemPaymentRequest
): Promise<CreemPaymentResponse> {
  const res = await api.post('/api/user/creem/pay', request, {
    ...PAYMENT_UI_REQUEST_CONFIG,
  })
  return res.data
}

/**
 * Request Waffo payment
 */
export async function requestWaffoPayment(
  request: WaffoPaymentRequest
): Promise<WaffoPaymentResponse> {
  const res = await api.post('/api/user/waffo/pay', request, {
    ...PAYMENT_UI_REQUEST_CONFIG,
  })
  return res.data
}

/**
 * Calculate payment amount for Waffo Pancake payment
 */
export async function calculateWaffoPancakeAmount(
  request: AmountRequest
): Promise<AmountResponse> {
  const res = await api.post('/api/user/waffo-pancake/amount', request, {
    ...PAYMENT_UI_REQUEST_CONFIG,
  })
  return res.data
}

/**
 * Request Waffo Pancake payment
 */
export async function requestWaffoPancakePayment(
  request: WaffoPancakePaymentRequest
): Promise<WaffoPancakePaymentResponse> {
  const res = await api.post('/api/user/waffo-pancake/pay', request, {
    ...PAYMENT_UI_REQUEST_CONFIG,
  })
  return res.data
}

/**
 * Get affiliate code
 */
export async function getAffiliateCode(): Promise<AffiliateCodeResponse> {
  const res = await api.get('/api/user/aff')
  return res.data
}

/**
 * Transfer affiliate quota to balance
 */
export async function transferAffiliateQuota(
  request: AffiliateTransferRequest
): Promise<AffiliateTransferResponse> {
  const res = await api.post('/api/user/aff_transfer', request)
  return res.data
}

/**
 * Get billing history for current user
 */
export async function getUserBillingHistory(
  page: number,
  pageSize: number,
  keyword?: string,
  signal?: AbortSignal
): Promise<ApiResponse<BillingHistoryResponse>> {
  const params = new URLSearchParams({
    p: page.toString(),
    page_size: pageSize.toString(),
  })
  if (keyword) {
    params.append('keyword', keyword)
  }
  const res = await api.get(`/api/user/topup/self?${params.toString()}`, {
    ...PAYMENT_UI_REQUEST_CONFIG,
    signal,
  })
  const response = res.data as ApiResponse<Record<string, unknown>>
  if (!response.data || typeof response.data !== 'object') {
    return {
      success: response.success,
      code: response.code,
      message: response.message,
      params: response.params,
    }
  }

  const rawData = response.data as Record<string, unknown>
  const items = Array.isArray(rawData.items)
    ? rawData.items
        .map(normalizePublicBillingRecord)
        .filter((item): item is TopupRecord => item !== null)
    : []

  return {
    success: response.success,
    code: response.code,
    message: response.message,
    params: response.params,
    data: {
      items,
      total: Number(rawData.total) || 0,
    },
  }
}
