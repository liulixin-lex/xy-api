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
import type { StripeLegacySubscription } from '@/features/subscriptions/types'

export interface PaymentOrder {
  id: number
  trade_no: string
  user_id: number
  order_kind: string
  provider: string
  payment_method: string
  request_id: string
  provider_order_key?: string
  provider_payment_key?: string
  expected_amount_minor: number
  paid_amount_minor: number
  currency: string
  requested_amount: number
  credit_quota: number
  started_at: number
  legacy_record_type?: string
  legacy_record_id?: number
  status: string
  status_reason?: string
  credential_incident: boolean
  credential_incident_state?: 'open' | 'acknowledged' | 'resolved' | string
  credential_incident_generation?: number
  credential_incident_reason?: string
  credential_incident_at?: number
  credential_incident_reviewed_at?: number
  credential_incident_reviewed_by?: number
  credential_incident_review_note?: string
  expires_at: number
  settled_at: number
  refunded_amount_minor: number
  disputed_amount_minor: number
  reversed_amount_minor: number
  reversed_quota: number
  created_at: number
  updated_at: number
  version: number
}

export interface PaymentEvent {
  id: number
  provider: string
  event_key: string
  event_type: string
  trade_no: string
  payment_order_id?: number
  provider_order_key?: string
  provider_payment_key?: string
  provider_resource_key?: string
  customer_id?: string
  provider_created_at?: number
  provider_state?: string
  provider_livemode?: boolean
  paid_amount_minor: number
  refunded_amount_minor: number
  disputed_amount_minor: number
  currency?: string
  payment_method?: string
  paid: boolean
  failed: boolean
  expired: boolean
  refunded: boolean
  disputed: boolean
  dispute_resolved: boolean
  dispute_won: boolean
  permanent_failure: boolean
  manual_review: boolean
  payload_digest: string
  normalized_payload?: string
  status: string
  attempts: number
  last_error?: string
  created_at: number
  processed_at: number
  updated_at: number
  legacy_kind?: 'topup' | 'subscription' | string
  review_code?: string
  available_actions?: Array<
    | 'dismiss'
    | 'link'
    | 'retry_legacy'
    | 'resolve_legacy_subscription'
    | 'resolve_legacy_topup'
    | string
  >
}

export interface PaymentDebt {
  id: number
  payment_order_id: number
  user_id: number
  debt_kind: string
  currency: string
  original_amount_minor: number
  outstanding_amount_minor: number
  original_quota: number
  outstanding_quota: number
  recovered_quota: number
  previous_user_status: number
  freeze_applied: boolean
  status: string
  created_at: number
  updated_at: number
  resolved_at: number
  resolution?: string
  resolution_note?: string
  resolved_by?: number
}

export interface PaymentLedgerEntry {
  id: number
  payment_order_id: number
  payment_event_id: number
  user_id: number
  entry_type: string
  amount_minor: number
  quota_delta: number
  currency: string
  description?: string
  created_at: number
}

export interface PaymentCustomerBinding {
  id: number
  provider: string
  customer_key: string
  user_id: number
  created_at: number
  updated_at: number
  version: number
}

export interface PaymentCustomerBindingRetirement {
  id: number
  original_binding_id: number
  provider: string
  customer_key: string
  user_id: number
  binding_created_at: number
  binding_updated_at: number
  binding_version: number
  user_customer_before?: string
  retired_by: number
  actor_ip: string
  reason: string
  retired_at: number
}

export interface PaymentOperationsAudit {
  id: number
  action: string
  admin_id: number
  actor_ip: string
  payment_order_id?: number
  user_id?: number
  subject_id?: number
  provider?: string
  expected_version: number
  reason: string
  metadata?: string
  created_at: number
}

export interface PaymentAuditListData {
  orders: PaymentOrder[]
  total: number
  unmatched_events: PaymentEvent[]
  unmatched_total: number
  unmatched_page: number
  unmatched_page_size: number
}

export interface PaymentAuditDetail {
  order?: PaymentOrder
  legacy_review_reason?: string
  events: PaymentEvent[]
  debts: PaymentDebt[]
  ledger: PaymentLedgerEntry[]
  customer_bindings?: PaymentCustomerBinding[]
  customer_binding_retirements?: PaymentCustomerBindingRetirement[]
  operations_audits?: PaymentOperationsAudit[]
}

export interface PaymentAuditFilters {
  status: string
  provider: string
  tradeNo: string
}

export interface PaymentOrderAuditActionRequest {
  trade_no: string
  expected_version: number
  reason: string
}

export interface PaymentCredentialIncidentActionRequest extends PaymentOrderAuditActionRequest {}

export interface RetireStripeCustomerBindingRequest {
  binding_id: number
  user_id: number
  expected_version: number
  reason: string
}

export interface RetireStripeCustomerBindingResult {
  retirement: PaymentCustomerBindingRetirement
  duplicate: boolean
}

export interface ConfirmExternalRefundRequest extends PaymentOrderAuditActionRequest {
  refunded_amount_minor: number
  provider_refund_reference: string
}

export interface DismissUnmatchedPaymentEventRequest {
  event_id: number
  reason: string
}

export interface LinkUnmatchedPaymentEventRequest extends DismissUnmatchedPaymentEventRequest {
  target_trade_no: string
  expected_order_version: number
}

export interface RetryLegacyEpayPaymentEventRequest extends DismissUnmatchedPaymentEventRequest {
  expected_event_attempts: number
}

export interface ResolveLegacyEpayTopUpRequest extends RetryLegacyEpayPaymentEventRequest {
  resolution: 'fulfill' | 'external_refund'
  credit_quota: number
  provider_refund_reference: string
}

export interface ResolveLegacySubscriptionRequest extends DismissUnmatchedPaymentEventRequest {
  expected_event_attempts: number
  resolution: 'external_refund'
  provider_refund_reference: string
}

export interface UnmatchedPaymentEventActionResult {
  event: PaymentEvent
  order?: PaymentOrder
  duplicate: boolean
}

export interface ResolveDebtRequest {
  debt_id: number
  expected_outstanding_quota: number
  expected_outstanding_amount_minor: number
  resolution: 'repaid' | 'waived'
  note: string
}

export type {
  StripeInventoryPage,
  StripeLegacySubscription,
} from '@/features/subscriptions/types'

export interface StripeInventoryFilters {
  status: string
  mappingStatus: string
  userId: string
  customerId: string
  subscriptionId: string
}

export interface StripeInventorySyncResult {
  seen: number
  mapped: number
  unmapped: number
}

export interface CancelStripeLegacySubscriptionRequest {
  inventory_id: number
  expected_updated_at: number
  reason: string
}

export interface CancelStripeLegacySubscriptionResult {
  subscription: StripeLegacySubscription
  duplicate: boolean
}

export interface PaymentOperationsOverviewCounts {
  preparing_orders: number
  awaiting_payment_orders: number
  confirming_orders: number
  manual_review_orders: number
  create_task_backlog: number
  reconcile_task_backlog: number
  running_tasks: number
  retry_waiting_tasks: number
  expired_task_leases: number
  oldest_create_task_age_seconds: number
  unmatched_payment_events: number
  unprocessed_payment_events: number
  oldest_unprocessed_event_age_seconds: number
  active_limit_reservations: number
  expired_active_limit_reservations: number
  payment_configuration_version: number
}

export interface PaymentRuntimeInfo {
  schema_version: number
  configuration_version: number
  configuration_fingerprint: string
  payment_secret_key_id: string
  session_secret_fingerprint: string
  database_type: string
  redis_enabled: boolean
  ready: boolean
  readiness_code?: string
}

export interface PaymentOperationsOverviewData {
  operations: PaymentOperationsOverviewCounts
  runtime: PaymentRuntimeInfo
  cluster: {
    ready: boolean
    code: string
  }
}

export interface BillingReservation {
  id: number
  request_id: string
  user_id: number
  token_id: number
  funding_source: 'wallet' | 'subscription' | string
  subscription_id: number
  subscription_reset_at: number
  legacy_adopted: boolean
  resource_type: string
  resource_id: string
  initial_quota: number
  reserved_quota: number
  token_reserved: number
  settled_quota: number
  settlement_target: number
  settlement_pending: boolean
  token_mode: number
  status: string
  version: number
  last_reconciled_at: number
  reconcile_note: string
  created_at: number
  updated_at: number
}

export interface QuotaLedgerEntry {
  id: number
  request_id: string
  phase: string
  revision: number
  user_id: number
  token_id: number
  funding_source: string
  subscription_id: number
  user_quota_delta: number
  token_remain_quota_delta: number
  token_used_quota_delta: number
  subscription_used_delta: number
  subscription_total_used_delta: number
  note: string
  created_at: number
}

export interface BillingReservationAdminResolution {
  id: number
  request_id: string
  revision: number
  expected_version: number
  admin_id: number
  resolution: 'settle' | 'refund'
  actual_quota?: number
  reason: string
  created_at: number
}

export interface BillingReservationPage {
  reservations: BillingReservation[]
  total: number
  stale_before: number
  stale_after_seconds: number
}

export interface BillingReservationDetail {
  reservation: BillingReservation
  ledger: QuotaLedgerEntry[]
  admin_resolutions: BillingReservationAdminResolution[]
}

export interface BillingReservationFilters {
  requestId: string
  userId: string
  resourceType: string
}

export interface ResolveBillingReservationRequest {
  request_id: string
  expected_version: number
  resolution: 'settle' | 'refund'
  actual_quota?: number
  reason: string
}

export interface ResolveBillingReservationResult {
  reservation: BillingReservation
  resolution: BillingReservationAdminResolution
  applied: boolean
}
