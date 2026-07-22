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
import { z } from 'zod'

// ============================================================================
// Subscription Plan Schema & Types
// ============================================================================

export const subscriptionPlanSchema = z.object({
  id: z.number(),
  title: z.string(),
  subtitle: z.string().optional(),
  price_amount: z.number(),
  currency: z.string().default('USD'),
  duration_unit: z.enum(['year', 'month', 'day', 'hour', 'custom']),
  duration_value: z.number(),
  custom_seconds: z.number().optional(),
  quota_reset_period: z.enum(['never', 'daily', 'weekly', 'monthly', 'custom']),
  quota_reset_custom_seconds: z.number().optional(),
  enabled: z.boolean(),
  sort_order: z.number(),
  allow_balance_pay: z.boolean().optional().default(true),
  allow_wallet_overflow: z.boolean().optional().default(true),
  max_purchase_per_user: z.number(),
  total_amount: z.number(),
  upgrade_group: z.string().optional(),
  downgrade_group: z.string().optional(),
  stripe_price_id: z.string().optional(),
  creem_product_id: z.string().optional(),
  waffo_pancake_product_id: z.string().optional(),
  external_payment_route_ids: z.array(z.string()).optional(),
})

export type SubscriptionPlan = z.infer<typeof subscriptionPlanSchema>

export const LEGACY_STRIPE_PRICE_ID_PURPOSE =
  'legacy_recurring_mapping_only' as const

export interface PlanRecord {
  plan: SubscriptionPlan
  stripe_price_id_purpose?: typeof LEGACY_STRIPE_PRICE_ID_PURPOSE
}

const publicSubscriptionPlanInputSchema = z.object({
  id: z.number(),
  title: z.string(),
  subtitle: z.string().optional(),
  price_amount: z.number(),
  currency: z.string().default('USD'),
  duration_unit: z.enum(['year', 'month', 'day', 'hour', 'custom']),
  duration_value: z.number(),
  custom_seconds: z.number().optional(),
  quota_reset_period: z.enum(['never', 'daily', 'weekly', 'monthly', 'custom']),
  quota_reset_custom_seconds: z.number().optional(),
  allow_balance_pay: z.boolean().optional().default(true),
  max_purchase_per_user: z.number(),
  total_amount: z.number(),
  includes_expanded_access: z.boolean().optional(),
  external_payment_route_ids: z.array(z.string()).optional(),
  // Read only during a rolling update from an older backend. The transformed
  // public object never retains or exposes the internal group identifier.
  upgrade_group: z.string().optional(),
})

export const publicSubscriptionPlanSchema =
  publicSubscriptionPlanInputSchema.transform((value) => {
    const { upgrade_group: legacyUpgradeGroup, ...plan } = value
    return {
      ...plan,
      includes_expanded_access:
        plan.includes_expanded_access ?? Boolean(legacyUpgradeGroup?.trim()),
    }
  })

export type PublicSubscriptionPlan = z.infer<
  typeof publicSubscriptionPlanSchema
>

export const publicPlanRecordSchema = z.object({
  plan: publicSubscriptionPlanSchema,
})

export type PublicPlanRecord = z.infer<typeof publicPlanRecordSchema>

// ============================================================================
// User Subscription Schema & Types
// ============================================================================

export const userSubscriptionSchema = z.object({
  id: z.number(),
  user_id: z.number(),
  plan_id: z.number(),
  status: z.string(),
  source: z.string().optional(),
  start_time: z.number(),
  end_time: z.number(),
  amount_total: z.number(),
  amount_used: z.number(),
  next_reset_time: z.number().optional(),
})

export type UserSubscription = z.infer<typeof userSubscriptionSchema>

export interface UserSubscriptionRecord {
  subscription: UserSubscription
}

export const publicUserSubscriptionSchema = z.object({
  id: z.number(),
  plan_id: z.number(),
  plan_title: z.string().optional().default(''),
  status: z.string(),
  start_time: z.number(),
  end_time: z.number(),
  amount_total: z.number(),
  amount_used: z.number(),
  next_reset_time: z.number().optional().default(0),
})

export type PublicUserSubscription = z.infer<
  typeof publicUserSubscriptionSchema
>

export const publicUserSubscriptionRecordSchema = z.object({
  subscription: publicUserSubscriptionSchema,
})

export type PublicUserSubscriptionRecord = z.infer<
  typeof publicUserSubscriptionRecordSchema
>

// ============================================================================
// API Request/Response Types
// ============================================================================

export interface ApiResponse<T = unknown> {
  success: boolean
  code?: string
  message?: string
  data?: T
}

export interface PlanPayload {
  plan: Partial<SubscriptionPlan>
}

export interface SubscriptionPayRequest {
  plan_id: number
  payment_method?: string
  request_id?: string
}

export interface SubscriptionPayResponse {
  success: boolean
  message?: string
  data?: {
    // Stripe-style hosted checkout link.
    pay_link?: string
    // Waffo Pancake / Creem hosted checkout URL.
    checkout_url?: string
    // Pancake checkout metadata; new-api does not expose buyer session tokens.
    session_id?: string
    expires_at?: number | string
    order_id?: string
  }
  url?: string
}

export interface CreateUserSubscriptionRequest {
  plan_id: number
}

export interface ResetUserSubscriptionsRequest {
  plan_id: number
  advance_reset_time: boolean
}

export interface ResetPlanSubscriptionsRequest {
  advance_reset_time: boolean
}

export interface SubscriptionResetResult {
  plan_id: number
  matched_count: number
  reset_count: number
  user_count: number
  advance_reset_time: boolean
}

// ============================================================================
// Self Subscription Data (user-facing)
// ============================================================================

export const selfSubscriptionDataSchema = z.object({
  billing_preference: z.string(),
  subscriptions: z.array(publicUserSubscriptionRecordSchema),
  all_subscriptions: z.array(publicUserSubscriptionRecordSchema),
})

export type SelfSubscriptionData = z.infer<typeof selfSubscriptionDataSchema>

// ============================================================================
// Read-only Stripe Legacy Subscription Inventory
// ============================================================================

export interface StripeLegacySubscription {
  id: number
  stripe_subscription_id: string
  stripe_customer_id: string
  checkout_session_id?: string
  trade_no?: string
  user_id?: number
  subscription_plan_id?: number
  mapping_status: string
  mapping_reason?: string
  mapping_source?: string
  review_reason?: string
  price_ids: string[]
  product_id?: string
  quantity: number
  currency?: string
  status: string
  collection_method?: string
  cancel_at_period_end: boolean
  current_period_start: number
  current_period_end: number
  cancel_at: number
  canceled_at: number
  ended_at: number
  trial_start: number
  trial_end: number
  latest_invoice_id?: string
  latest_invoice_status?: string
  latest_invoice_paid: boolean
  latest_invoice_amount_due: number
  latest_invoice_amount_paid: number
  latest_invoice_currency?: string
  livemode: boolean
  stripe_created_at: number
  state_observed_at: number
  last_synced_at: number
  sync_source: string
}

export interface StripeInventoryPage {
  page: number
  page_size: number
  total: number
  items: StripeLegacySubscription[]
}

// ============================================================================
// Dialog Types
// ============================================================================

export type SubscriptionsDialogType =
  | 'create'
  | 'update'
  | 'toggle-status'
  | 'reset-subscriptions'
