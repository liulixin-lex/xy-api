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
import { api } from '@/lib/api'

import type {
  BillingReservationDetail,
  BillingReservationFilters,
  BillingReservationPage,
  ConfirmExternalRefundRequest,
  DismissUnmatchedPaymentEventRequest,
  LinkUnmatchedPaymentEventRequest,
  ManualFulfillRequest,
  PaymentAuditDetail,
  PaymentAuditFilters,
  PaymentAuditListData,
  PaymentCredentialIncidentActionRequest,
  PaymentDebt,
  PaymentOrder,
  PaymentOrderAuditActionRequest,
  RetireStripeCustomerBindingRequest,
  RetireStripeCustomerBindingResult,
  ResolveDebtRequest,
  ResolveLegacyEpaySubscriptionRequest,
  ResolveLegacyEpayTopUpRequest,
  ResolveBillingReservationRequest,
  ResolveBillingReservationResult,
  RetryLegacyEpayPaymentEventRequest,
  StripeInventoryFilters,
  StripeInventoryPage,
  StripeInventorySyncResult,
  UnmatchedPaymentEventActionResult,
} from './types'

interface ApiResponse<T> {
  success: boolean
  message?: string
  data?: T
}

const requestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
} as const

function unwrap<T>(response: ApiResponse<T>, fallbackMessage: string): T {
  if (!response.success || response.data === undefined) {
    throw new Error(response.message || fallbackMessage)
  }
  return response.data
}

export async function listPaymentAudit(
  filters: PaymentAuditFilters,
  page: number,
  pageSize: number,
  unmatchedPage: number,
  unmatchedPageSize: number
): Promise<PaymentAuditListData> {
  const response = await api.get<ApiResponse<PaymentAuditListData>>(
    '/api/option/payment/audit',
    {
      ...requestConfig,
      params: {
        p: page,
        page_size: pageSize,
        unmatched_page: unmatchedPage,
        unmatched_page_size: unmatchedPageSize,
        status: filters.status || undefined,
        provider: filters.provider.trim() || undefined,
        trade_no: filters.tradeNo.trim() || undefined,
      },
    }
  )
  return unwrap(response.data, 'Failed to load payment audit')
}

export async function getPaymentAudit(
  tradeNo: string
): Promise<PaymentAuditDetail> {
  const response = await api.get<ApiResponse<PaymentAuditDetail>>(
    `/api/option/payment/audit/${encodeURIComponent(tradeNo)}`,
    requestConfig
  )
  return unwrap(response.data, 'Failed to load payment audit detail')
}

export async function fulfillManualPayment(
  request: ManualFulfillRequest
): Promise<PaymentOrder> {
  const response = await api.post<ApiResponse<PaymentOrder>>(
    `/api/option/payment/audit/${encodeURIComponent(request.trade_no)}/fulfill`,
    request,
    requestConfig
  )
  return unwrap(response.data, 'Failed to fulfill payment order')
}

async function applyPaymentOrderAuditAction(
  action: 'reject' | 'void',
  request: PaymentOrderAuditActionRequest
): Promise<PaymentOrder> {
  const response = await api.post<ApiResponse<PaymentOrder>>(
    `/api/option/payment/audit/${encodeURIComponent(request.trade_no)}/${action}`,
    request,
    requestConfig
  )
  return unwrap(response.data, 'Failed to update payment order')
}

export function rejectPaymentOrder(
  request: PaymentOrderAuditActionRequest
): Promise<PaymentOrder> {
  return applyPaymentOrderAuditAction('reject', request)
}

export function voidPaymentOrder(
  request: PaymentOrderAuditActionRequest
): Promise<PaymentOrder> {
  return applyPaymentOrderAuditAction('void', request)
}

export async function confirmExternalPaymentRefund(
  request: ConfirmExternalRefundRequest
): Promise<PaymentOrder> {
  const response = await api.post<ApiResponse<PaymentOrder>>(
    `/api/option/payment/audit/${encodeURIComponent(request.trade_no)}/external-refund`,
    request,
    requestConfig
  )
  return unwrap(response.data, 'Failed to confirm external payment refund')
}

async function applyPaymentCredentialIncidentAction(
  action: 'acknowledge' | 'resolve',
  request: PaymentCredentialIncidentActionRequest
): Promise<PaymentOrder> {
  const response = await api.post<ApiResponse<PaymentOrder>>(
    `/api/option/payment/audit/${encodeURIComponent(request.trade_no)}/credential-incident/${action}`,
    request,
    requestConfig
  )
  return unwrap(response.data, 'Failed to update payment credential incident')
}

export function acknowledgePaymentCredentialIncident(
  request: PaymentCredentialIncidentActionRequest
): Promise<PaymentOrder> {
  return applyPaymentCredentialIncidentAction('acknowledge', request)
}

export function resolvePaymentCredentialIncident(
  request: PaymentCredentialIncidentActionRequest
): Promise<PaymentOrder> {
  return applyPaymentCredentialIncidentAction('resolve', request)
}

export async function retireStripeCustomerBinding(
  request: RetireStripeCustomerBindingRequest
): Promise<RetireStripeCustomerBindingResult> {
  const response = await api.post<
    ApiResponse<RetireStripeCustomerBindingResult>
  >(
    `/api/option/payment/customer-bindings/${request.binding_id}/retire`,
    request,
    requestConfig
  )
  return unwrap(response.data, 'Failed to retire Stripe customer binding')
}

export async function dismissUnmatchedPaymentEvent(
  request: DismissUnmatchedPaymentEventRequest
): Promise<UnmatchedPaymentEventActionResult> {
  const response = await api.post<
    ApiResponse<UnmatchedPaymentEventActionResult>
  >(
    `/api/option/payment/audit/unmatched/${request.event_id}/dismiss`,
    request,
    requestConfig
  )
  return unwrap(response.data, 'Failed to dismiss unmatched payment event')
}

export async function linkUnmatchedPaymentEvent(
  request: LinkUnmatchedPaymentEventRequest
): Promise<UnmatchedPaymentEventActionResult> {
  const response = await api.post<
    ApiResponse<UnmatchedPaymentEventActionResult>
  >(
    `/api/option/payment/audit/unmatched/${request.event_id}/link`,
    request,
    requestConfig
  )
  return unwrap(response.data, 'Failed to link unmatched payment event')
}

export async function retryLegacyEpayPaymentEvent(
  request: RetryLegacyEpayPaymentEventRequest
): Promise<UnmatchedPaymentEventActionResult> {
  const response = await api.post<
    ApiResponse<UnmatchedPaymentEventActionResult>
  >(
    `/api/option/payment/audit/unmatched/${request.event_id}/retry-legacy`,
    request,
    requestConfig
  )
  return unwrap(response.data, 'Failed to safely retry legacy Epay payment')
}

export async function resolveLegacyEpayTopUp(
  request: ResolveLegacyEpayTopUpRequest
): Promise<UnmatchedPaymentEventActionResult> {
  const response = await api.post<
    ApiResponse<UnmatchedPaymentEventActionResult>
  >(
    `/api/option/payment/audit/unmatched/${request.event_id}/resolve-legacy-topup`,
    request,
    requestConfig
  )
  return unwrap(response.data, 'Failed to resolve legacy Epay top-up')
}

export async function resolveLegacyEpaySubscription(
  request: ResolveLegacyEpaySubscriptionRequest
): Promise<UnmatchedPaymentEventActionResult> {
  const response = await api.post<
    ApiResponse<UnmatchedPaymentEventActionResult>
  >(
    `/api/option/payment/audit/unmatched/${request.event_id}/resolve-legacy-subscription`,
    request,
    requestConfig
  )
  return unwrap(response.data, 'Failed to resolve legacy Epay subscription')
}

export async function resolvePaymentDebt(
  request: ResolveDebtRequest
): Promise<PaymentDebt> {
  const response = await api.post<ApiResponse<PaymentDebt>>(
    `/api/option/payment/debts/${request.debt_id}/resolve`,
    request,
    requestConfig
  )
  return unwrap(response.data, 'Failed to resolve payment debt')
}

export async function listAdminStripeInventory(
  filters: StripeInventoryFilters,
  page: number,
  pageSize: number
): Promise<StripeInventoryPage> {
  const response = await api.get<ApiResponse<StripeInventoryPage>>(
    '/api/subscription/admin/stripe/inventory',
    {
      ...requestConfig,
      params: {
        p: page,
        page_size: pageSize,
        status: filters.status || undefined,
        mapping_status: filters.mappingStatus || undefined,
        user_id: filters.userId.trim() || undefined,
        customer_id: filters.customerId.trim() || undefined,
        subscription_id: filters.subscriptionId.trim() || undefined,
      },
    }
  )
  return unwrap(response.data, 'Failed to load Stripe subscription inventory')
}

export async function syncAdminStripeInventory(): Promise<StripeInventorySyncResult> {
  const response = await api.post<ApiResponse<StripeInventorySyncResult>>(
    '/api/subscription/admin/stripe/inventory/sync',
    {},
    requestConfig
  )
  return unwrap(response.data, 'Failed to sync Stripe subscription inventory')
}

export async function listBillingReservations(
  filters: BillingReservationFilters,
  page: number,
  pageSize: number
): Promise<BillingReservationPage> {
  const response = await api.get<ApiResponse<BillingReservationPage>>(
    '/api/option/billing/reservations',
    {
      ...requestConfig,
      params: {
        p: page,
        page_size: pageSize,
        request_id: filters.requestId.trim() || undefined,
        user_id: filters.userId.trim() || undefined,
        resource_type: filters.resourceType || undefined,
      },
    }
  )
  return unwrap(response.data, 'Failed to load billing reservations')
}

export async function getBillingReservation(
  requestId: string
): Promise<BillingReservationDetail> {
  const response = await api.get<ApiResponse<BillingReservationDetail>>(
    `/api/option/billing/reservations/${encodeURIComponent(requestId)}`,
    requestConfig
  )
  return unwrap(response.data, 'Failed to load billing reservation detail')
}

export async function resolveBillingReservation(
  request: ResolveBillingReservationRequest
): Promise<ResolveBillingReservationResult> {
  const response = await api.post<ApiResponse<ResolveBillingReservationResult>>(
    `/api/option/billing/reservations/${encodeURIComponent(request.request_id)}/resolve`,
    request,
    requestConfig
  )
  return unwrap(response.data, 'Failed to resolve billing reservation')
}
