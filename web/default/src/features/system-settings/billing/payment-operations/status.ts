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

import type { StatusVariant } from '@/components/status-badge'
import { formatPaymentMinorAmount } from '@/features/wallet/lib/billing'

import type { PaymentEvent } from './types'

type StatusMeta = {
  label: string
  variant: StatusVariant
}

export function getPaymentStatusMeta(status: string, t: TFunction): StatusMeta {
  switch (status) {
    case 'pending':
      return { label: t('Pending'), variant: 'neutral' }
    case 'processing':
      return { label: t('Processing'), variant: 'info' }
    case 'paid':
      return { label: t('Paid'), variant: 'info' }
    case 'fulfilled':
      return { label: t('Fulfilled'), variant: 'success' }
    case 'failed':
      return { label: t('Failed'), variant: 'danger' }
    case 'expired':
      return { label: t('Expired'), variant: 'neutral' }
    case 'manual_review':
      return { label: t('Manual Review'), variant: 'warning' }
    case 'refund_pending':
      return { label: t('Refund Pending'), variant: 'warning' }
    case 'refunded':
      return { label: t('Refunded'), variant: 'neutral' }
    case 'disputed':
      return { label: t('Disputed'), variant: 'danger' }
    case 'debt':
      return { label: t('Payment Debt'), variant: 'danger' }
    default:
      return { label: status || t('Unknown'), variant: 'neutral' }
  }
}

export function getEventStatusMeta(status: string, t: TFunction): StatusMeta {
  switch (status) {
    case 'received':
      return { label: t('Received'), variant: 'neutral' }
    case 'processing':
      return { label: t('Processing'), variant: 'info' }
    case 'processed':
      return { label: t('Processed'), variant: 'success' }
    case 'manual_review':
      return { label: t('Manual Review'), variant: 'warning' }
    case 'credential_revoked':
      return { label: t('Credential Revoked'), variant: 'danger' }
    case 'dismissed':
      return { label: t('Dismissed'), variant: 'neutral' }
    case 'failed':
      return { label: t('Failed'), variant: 'danger' }
    default:
      return { label: status || t('Unknown'), variant: 'neutral' }
  }
}

export type UnmatchedPaymentEventAction =
  | 'dismiss'
  | 'link'
  | 'retry_legacy'
  | 'resolve_legacy_subscription'
  | 'resolve_legacy_topup'

export function isPaymentEventActionAvailable(
  event: Pick<PaymentEvent, 'available_actions'>,
  action: UnmatchedPaymentEventAction
): boolean {
  return event.available_actions?.includes(action) ?? false
}

export function getCredentialIncidentStatusMeta(
  status: string,
  t: TFunction
): StatusMeta {
  switch (status) {
    case 'open':
      return { label: t('Open'), variant: 'danger' }
    case 'acknowledged':
      return { label: t('Acknowledged'), variant: 'warning' }
    case 'resolved':
      return { label: t('Resolved'), variant: 'success' }
    default:
      return { label: status || t('Unknown'), variant: 'neutral' }
  }
}

export function getCredentialIncidentActions(
  status: string,
  incidentOpen: boolean
): Array<'acknowledge' | 'resolve'> {
  if (!incidentOpen) return []
  if (status === 'open') return ['acknowledge', 'resolve']
  if (status === 'acknowledged') return ['resolve']
  return []
}

export function getMappingStatusMeta(status: string, t: TFunction): StatusMeta {
  switch (status) {
    case 'mapped':
      return { label: t('Mapped'), variant: 'success' }
    case 'unmapped':
      return { label: t('Unmapped'), variant: 'warning' }
    case 'unmapped_user':
      return { label: t('User Unmapped'), variant: 'warning' }
    case 'unmapped_plan':
      return { label: t('Plan Unmapped'), variant: 'warning' }
    case 'ambiguous_user':
      return { label: t('Ambiguous User'), variant: 'danger' }
    case 'ambiguous_plan':
      return { label: t('Ambiguous Plan'), variant: 'danger' }
    default:
      return { label: status || t('Unknown'), variant: 'neutral' }
  }
}

export function getStripeStatusMeta(status: string, t: TFunction): StatusMeta {
  switch (status) {
    case 'active':
      return { label: t('Active'), variant: 'success' }
    case 'trialing':
      return { label: t('Trialing'), variant: 'info' }
    case 'past_due':
      return { label: t('Past Due'), variant: 'warning' }
    case 'unpaid':
      return { label: t('Unpaid'), variant: 'danger' }
    case 'incomplete':
      return { label: t('Incomplete'), variant: 'warning' }
    case 'incomplete_expired':
      return { label: t('Incomplete Expired'), variant: 'neutral' }
    case 'paused':
      return { label: t('Paused'), variant: 'neutral' }
    case 'canceled':
      return { label: t('Canceled'), variant: 'neutral' }
    default:
      return { label: status || t('Unknown'), variant: 'neutral' }
  }
}

export function formatMinorAmount(
  amount: number,
  currency?: string,
  provider?: string
): string {
  return formatPaymentMinorAmount(amount, currency || 'USD', provider)
}

export function formatUnixTime(value?: number): string {
  if (!value || !Number.isFinite(value)) return '-'
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(new Date(value * 1000))
}

export function formatInteger(value: number): string {
  return new Intl.NumberFormat().format(value)
}
