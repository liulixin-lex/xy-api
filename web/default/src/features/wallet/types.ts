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
  code?: string
  message?: string
  params?: Record<string, unknown>
  data?: T
}

export type PaymentProvider = 'epay' | 'stripe' | 'xorpay'
export type LegacyPaymentProvider = 'creem' | 'waffo' | 'waffo_pancake'
export type PaymentMethodProvider = PaymentProvider | LegacyPaymentProvider
/** Stable public capability code. Unknown safe values render generically. */
export type PublicPaymentMethod = string
export type PaymentOrderKind = 'topup' | 'subscription'
export type PublicPaymentStatusCode =
  | 'preparing'
  | 'awaiting_payment'
  | 'confirming'
  | 'succeeded'
  | 'expired'
  | 'temporarily_unavailable'

interface PaymentQuoteRequestBase {
  order_kind: PaymentOrderKind
  amount?: number
  plan_id?: number
  product_id?: string
  option_id?: string
}

export type PaymentQuoteRequest = PaymentQuoteRequestBase &
  (
    | {
        route_id: string
        provider?: never
        payment_method?: never
      }
    | {
        route_id?: never
        provider: PaymentProvider
        payment_method: string
      }
  )

export interface PaymentQuote {
  quote_id: string
  route_id: string
  public_method: PublicPaymentMethod
  channel_alias?: string
  top_up_amount?: number
  plan_id?: number
  payable_amount: string
  currency: string
  expires_at: number
}

/** UI quote also represents preserved legacy checkout flows during migration. */
export type ClientPaymentQuote = PaymentQuote & {
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

export type PaymentCheckoutFlow =
  | 'pending'
  | 'qr'
  | 'hosted_redirect'
  | 'form_post'
  | 'wechat_authorize'
  | 'jsapi'

export interface PaymentJSAPIParameters {
  app_id: string
  timestamp: string
  nonce_str: string
  package: string
  sign_type: 'MD5' | 'HMAC-SHA256'
  pay_sign: string
}

export interface PaymentCheckout {
  flow: PaymentCheckoutFlow
  qr_content?: string
  continue_url?: string
  jsapi?: PaymentJSAPIParameters
  expires_at: number
}

export interface PaymentOrder {
  trade_no: string
  route_id: string
  public_method: PublicPaymentMethod
  channel_alias?: string
  status_code: PublicPaymentStatusCode
  payment_amount: string
  top_up_amount?: number
  plan_id?: number
  currency: string
  expires_at: number
  completed_at?: number
  checkout?: PaymentCheckout
}

export type PaymentQuoteResponse = ApiResponse<ClientPaymentQuote>
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
      // new-api intentionally does not expose the provider buyer session token.
    }
  | string
>

/**
 * Public fixed-price payment product.
 */
export interface PaymentProduct {
  /** Opaque selection token resolved only against the current server catalog. */
  product_id: string
  /** Public route that owns this product. */
  route_id: string
  /** Product display name */
  name: string
  /** Server-authoritative fixed-point amount. */
  payment_amount: string
  /** Amount credited to the wallet. */
  top_up_amount: number
  /** ISO 4217 currency. */
  currency: string
}

/**
 * Creem payment request
 */
export interface CreemPaymentRequest {
  /** Opaque product selection token. */
  product_id: string
}

export type PaymentCheckoutMode = 'quote' | 'product' | 'option' | 'direct'

/**
 * Payment method configuration
 */
export interface PaymentMethod {
  /** Stable public route identifier used for UI selection and quote creation. */
  route_id: string
  /** Public payment brand; never exposes the internal gateway. */
  public_method: PublicPaymentMethod
  /** Stable public capability code, translated by the frontend. */
  channel_alias?: string
  /** Public checkout capability; no gateway identity is exposed. */
  checkout_mode: PaymentCheckoutMode
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
 * Public selectable option within a retained checkout route.
 */
export interface PaymentRouteOption {
  /** Opaque selection token resolved only against the current server catalog. */
  option_id: string
  /** Public route that owns this option. */
  route_id: string
  /** Server-vetted user-facing label. */
  public_label: 'Card' | 'Apple Pay' | 'Google Pay' | 'Online payment'
}

/**
 * Topup configuration information
 */
export interface TopupInfo {
  /** Whether at least one public online payment route is available */
  online_payment_available?: boolean
  /** Available public payment routes. */
  payment_routes: PaymentMethod[]
  /** Routes whose shared gateway capability can price subscription plans. */
  subscription_payment_routes?: PaymentMethod[]
  /** Fixed-price products owned by product checkout routes. */
  payment_products: PaymentProduct[]
  /** Selectable options owned by option checkout routes. */
  payment_route_options: PaymentRouteOption[]
  /** Minimum topup amount for online topup */
  min_topup: number
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
  /** Opaque option selection token. */
  option_id?: string
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
export type BillingRecordStatus = PublicPaymentStatusCode

/**
 * Topup billing record
 */
export interface TopupRecord {
  /** Record ID */
  id: number
  /** Topup amount (quota) */
  amount: number
  /** Server-authoritative payment amount as a fixed-point decimal string */
  payment_amount: string
  /** Trade/order number */
  trade_no: string
  /** Stable public route for user-facing records. */
  route_id?: string
  /** Public payment brand for user-facing records. */
  public_method?: PublicPaymentMethod
  /** Stable public capability code; never render an unknown value directly. */
  channel_alias?: string
  /** ISO 4217 currency code for actual payment */
  currency?: string
  /** Creation timestamp */
  created_at: number
  /** Completion timestamp */
  completed_at?: number
  status_code?: PublicPaymentStatusCode
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
