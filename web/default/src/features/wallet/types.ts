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
// ============================================================================
// Wallet Type Definitions
// ============================================================================

/**
 * Generic API response
 */
export interface ApiResponse<T = unknown> {
  success?: boolean
  message?: string
  data?: T
}

export type PaymentProvider = 'epay' | 'stripe' | 'xorpay'
export type LegacyPaymentProvider = 'creem' | 'waffo' | 'waffo_pancake'
export type PaymentMethodProvider = PaymentProvider | LegacyPaymentProvider
export type PaymentOrderKind = 'topup' | 'subscription'
export type PaymentOrderStatus =
  | 'pending'
  | 'processing'
  | 'success'
  | 'failed'
  | 'expired'
  | 'manual_review'
  | 'refund_pending'
  | 'refunded'
  | 'disputed'
  | 'debt'

export interface PaymentQuoteRequest {
  order_kind: PaymentOrderKind
  provider: PaymentProvider
  payment_method: string
  amount?: number
  plan_id?: number
}

export interface PaymentQuote {
  quote_id: string
  order_kind: PaymentOrderKind
  provider: PaymentProvider
  payment_method: string
  requested_amount: number
  credit_quota: number
  expected_amount_minor: number
  payable_amount: string
  currency: string
  expires_at: number
}

/** UI quote also represents preserved legacy checkout flows during migration. */
export type ClientPaymentQuote = Omit<PaymentQuote, 'provider'> & {
  provider: PaymentMethodProvider
  legacy?: boolean
}

export interface PaymentStartRequest {
  quote_id: string
  request_id: string
}

interface PaymentStartBase {
  trade_no: string
  expires_at: number
}

export interface PaymentFormPostStart extends PaymentStartBase {
  flow: 'form_post'
  action: string
  fields: Record<string, string>
}

export interface PaymentPendingStart extends PaymentStartBase {
  flow: 'pending'
}

export interface PaymentHostedRedirectStart extends PaymentStartBase {
  flow: 'hosted_redirect'
  url: string
}

export interface PaymentQrStart extends PaymentStartBase {
  flow: 'qr'
  qr_content: string
}

export type PaymentStart =
  | PaymentFormPostStart
  | PaymentHostedRedirectStart
  | PaymentQrStart
  | PaymentPendingStart

export interface PaymentOrder {
  trade_no: string
  order_kind: PaymentOrderKind
  provider: PaymentProvider
  payment_method: string
  status: PaymentOrderStatus
  requested_amount: number
  credit_quota: number
  expected_amount_minor: number
  paid_amount_minor: number
  currency: string
  expires_at: number
  settled_at?: number
  status_reason?: string
}

export type PaymentQuoteResponse = ApiResponse<PaymentQuote>
export type PaymentStartResponse = ApiResponse<PaymentStart>
export type PaymentOrderResponse = ApiResponse<PaymentOrder>

/**
 * Standard API response types
 */
export type TopupInfoResponse = ApiResponse<TopupInfo>
export type RedemptionResponse = ApiResponse<number>
export type AmountResponse = ApiResponse<string>
export type PaymentResponse = ApiResponse<Record<string, unknown>> & {
  url?: string
}
export type StripePaymentResponse = ApiResponse<{ pay_link: string }>
export type AffiliateCodeResponse = ApiResponse<string>
export type AffiliateTransferResponse = ApiResponse
export type CreemPaymentResponse = ApiResponse<{ checkout_url: string }>
export type WaffoPaymentResponse = ApiResponse<
  { payment_url?: string } | string
>
export type WaffoPancakePaymentResponse = ApiResponse<
  | {
      checkout_url?: string
      session_id?: string
      expires_at?: number | string
      order_id?: string
      // Self-service session token + expiry — surfaced by the backend so
      // future flows (refund / cancel from new-api's own UI) can use them
      // without re-issuing checkout. Not consumed by the current handler.
      token?: string
      token_expires_at?: number | string
    }
  | string
>

/**
 * Creem product configuration
 */
export interface CreemProduct {
  /** Product display name */
  name: string
  /** Creem product ID */
  productId: string
  /** Product price */
  price: number
  /** Quota amount to credit */
  quota: number
  /** Currency (USD or EUR) */
  currency: 'USD' | 'EUR'
}

/**
 * Creem payment request
 */
export interface CreemPaymentRequest {
  /** Creem product ID */
  product_id: string
  /** Payment method identifier */
  payment_method: 'creem'
}

/**
 * Payment method configuration
 */
export interface PaymentMethod {
  /** Display name of payment method */
  name: string
  /** Payment method type identifier */
  type: string
  /** Explicit gateway owner. UI dispatch must not infer by exclusion. */
  provider: PaymentMethodProvider
  /** Legacy optional color for UI display */
  color?: string
  /** Minimum topup amount for this payment method */
  min_topup?: number
  /** Optional react-icons component name or safe icon URL */
  icon?: string
  /** ISO 4217 settlement currency advertised by the gateway */
  currency?: string
}

/**
 * Waffo payment method configuration
 */
export interface WaffoPayMethod {
  /** Display name of payment method */
  name: string
  /** Optional icon path */
  icon?: string
  /** Waffo pay method type */
  payMethodType?: string
  /** Waffo pay method name */
  payMethodName?: string
}

/**
 * Topup configuration information
 */
export interface TopupInfo {
  /** Whether online topup is enabled */
  enable_online_topup: boolean
  /** Whether Stripe topup is enabled */
  enable_stripe_topup: boolean
  /** Whether XORPay is available for new payments */
  enable_xorpay_topup?: boolean
  /** Available payment methods */
  pay_methods: PaymentMethod[]
  /** Minimum topup amount for online topup */
  min_topup: number
  /** Minimum topup amount for Stripe */
  stripe_min_topup: number
  /** Minimum topup amount for XORPay */
  xorpay_min_topup?: number
  /** Preset amount options */
  amount_options: number[]
  /** Discount rates by amount */
  discount: Record<number, number>
  /** Current continuous referral reward percentage */
  affiliate_continuous_percent?: number
  /** Current first top-up referral reward percentage */
  affiliate_first_topup_percent?: number
  /** Optional topup link for purchasing codes */
  topup_link?: string
  /** Whether Creem topup is enabled */
  enable_creem_topup?: boolean
  /** Available Creem products */
  creem_products?: CreemProduct[]
  /** Whether Waffo topup is enabled */
  enable_waffo_topup?: boolean
  /** Available Waffo payment methods */
  waffo_pay_methods?: WaffoPayMethod[]
  /** Minimum topup amount for Waffo */
  waffo_min_topup?: number
  /** Whether Waffo Pancake topup is enabled */
  enable_waffo_pancake_topup?: boolean
  /** Minimum topup amount for Waffo Pancake */
  waffo_pancake_min_topup?: number
  /** Whether redemption code usage is enabled */
  enable_redemption?: boolean
  /** Whether compliance confirmation has been completed */
  payment_compliance_confirmed?: boolean
  /** Current compliance terms version */
  payment_compliance_terms_version?: string
}

/**
 * Preset amount option with optional discount
 */
export interface PresetAmount {
  /** Preset amount value */
  value: number
  /** Optional discount rate (0-1) */
  discount?: number
}

/**
 * Redemption code request
 */
export interface RedemptionRequest {
  /** Redemption code key */
  key: string
}

/**
 * Payment request parameters
 */
export interface PaymentRequest {
  /** Topup amount */
  amount: number
  /** Payment method identifier */
  payment_method: string
}

/**
 * Waffo payment request parameters
 */
export interface WaffoPaymentRequest {
  /** Topup amount */
  amount: number
  /** Optional server-side Waffo payment method index */
  pay_method_index?: number
}

/**
 * Waffo Pancake payment request parameters
 */
export interface WaffoPancakePaymentRequest {
  /** Topup amount */
  amount: number
}

/**
 * Amount calculation request
 */
export interface AmountRequest {
  /** Topup amount to calculate */
  amount: number
}

/**
 * Affiliate quota transfer request
 */
export interface AffiliateTransferRequest {
  /** Quota amount to transfer */
  quota: number
}

/**
 * User wallet data
 */
export interface UserWalletData {
  /** User ID */
  id: number
  /** Username */
  username: string
  /** Current quota balance */
  quota: number
  /** Total used quota */
  used_quota: number
  /** Total request count */
  request_count: number
  /** Affiliate quota (pending rewards) */
  aff_quota: number
  /** Total affiliate quota earned (historical) */
  aff_history_quota: number
  /** Number of successful affiliate invites */
  aff_count: number
  /** User group */
  group: string
}

/**
 * Topup record status
 */
export type TopupStatus = PaymentOrderStatus

/**
 * Topup billing record
 */
export interface TopupRecord {
  /** Record ID */
  id: number
  /** User ID */
  user_id: number
  /** Topup amount (quota) */
  amount: number
  /** Payment amount (actual money paid) */
  money: number
  /** Trade/order number */
  trade_no: string
  /** Payment method type */
  payment_method: string
  /** Explicit payment gateway for new records */
  payment_provider?: PaymentMethodProvider
  /** Alias used by the unified payment order projection */
  provider?: PaymentMethodProvider
  /** User-visible order purpose */
  order_kind?: PaymentOrderKind
  /** ISO 4217 currency code for actual payment */
  currency?: string
  /** Canonical quota credited by this order */
  credit_quota?: number
  /** Canonical expected payment amount in the currency's minor unit */
  expected_amount_minor?: number
  /** Actual settled payment amount in the currency's minor unit */
  paid_amount_minor?: number
  /** Cumulative amount reported refunded by the provider */
  refunded_amount_minor?: number
  /** Cumulative amount currently disputed at the provider */
  disputed_amount_minor?: number
  /** Cumulative amount whose entitlement has been reversed */
  reversed_amount_minor?: number
  /** Safe, user-facing status explanation */
  status_reason?: string
  /** Creation timestamp */
  create_time: number
  /** Completion timestamp */
  complete_time?: number
  /** Payment status */
  status: TopupStatus
}

/**
 * Billing history response
 */
export interface BillingHistoryResponse {
  items: TopupRecord[]
  total: number
}

/**
 * Complete order request (admin only)
 */
