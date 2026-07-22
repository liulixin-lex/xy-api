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
import type { StatusBadgeProps } from '@/components/status-badge'
import { formatTimestampToDate } from '@/lib/format'

import type { BillingRecordStatus } from '../types'

// ============================================================================
// Billing Utility Functions
// ============================================================================

interface StatusConfig {
  variant: StatusBadgeProps['variant']
  label: string
}

/**
 * Status badge configuration
 */
export const STATUS_CONFIG: Record<BillingRecordStatus, StatusConfig> = {
  preparing: {
    variant: 'warning',
    label: 'Preparing payment',
  },
  awaiting_payment: {
    variant: 'warning',
    label: 'Waiting for payment',
  },
  confirming: {
    variant: 'info',
    label: 'Confirming payment',
  },
  succeeded: {
    variant: 'success',
    label: 'Payment completed',
  },
  temporarily_unavailable: {
    variant: 'danger',
    label: 'Payment temporarily unavailable',
  },
  expired: {
    variant: 'danger',
    label: 'Expired',
  },
}

/**
 * Get status badge configuration
 */
export function getStatusConfig(status: BillingRecordStatus): StatusConfig {
  return STATUS_CONFIG[status] || STATUS_CONFIG.temporarily_unavailable
}

export function getOrderKindName(
  orderKind: string | undefined,
  t?: (key: string) => string
): string {
  const name = orderKind === 'subscription' ? 'Subscription' : 'Top-up'
  return t ? t(name) : name
}

function usesStripeTwoDecimalMinorUnit(
  currency: string,
  provider?: string
): boolean {
  return (
    provider?.toLowerCase() === 'stripe' &&
    (currency === 'ISK' || currency === 'UGX')
  )
}

export function formatPaymentMinorAmount(
  amountMinor: number,
  currency = 'USD',
  provider?: string
): string {
  let normalizedCurrency = /^[A-Z]{3}$/.test(currency.toUpperCase())
    ? currency.toUpperCase()
    : 'USD'
  const stripeUsesTwoDecimalMinorUnit = usesStripeTwoDecimalMinorUnit(
    normalizedCurrency,
    provider
  )
  let formatter: Intl.NumberFormat
  try {
    formatter = new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency: normalizedCurrency,
      ...(stripeUsesTwoDecimalMinorUnit
        ? { minimumFractionDigits: 2, maximumFractionDigits: 2 }
        : {}),
    })
  } catch {
    normalizedCurrency = 'USD'
    formatter = new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency: normalizedCurrency,
    })
  }
  const fractionDigits = stripeUsesTwoDecimalMinorUnit
    ? 2
    : (formatter.resolvedOptions().maximumFractionDigits ?? 2)
  const divisor = 10 ** fractionDigits
  return formatter.format(
    (Number.isFinite(amountMinor) ? amountMinor : 0) / divisor
  )
}

export function formatPaymentDecimalAmount(
  amount: string | number,
  currency = 'USD',
  provider?: string
): string {
  const numericAmount = Number(amount)
  let normalizedCurrency = /^[A-Z]{3}$/.test(currency.toUpperCase())
    ? currency.toUpperCase()
    : 'USD'
  let formatter: Intl.NumberFormat
  const stripeUsesTwoDecimalMinorUnit = usesStripeTwoDecimalMinorUnit(
    normalizedCurrency,
    provider
  )
  try {
    formatter = new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency: normalizedCurrency,
      ...(stripeUsesTwoDecimalMinorUnit
        ? { minimumFractionDigits: 2, maximumFractionDigits: 2 }
        : {}),
    })
  } catch {
    normalizedCurrency = 'USD'
    formatter = new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency: normalizedCurrency,
    })
  }
  return formatter.format(Number.isFinite(numericAmount) ? numericAmount : 0)
}

/**
 * Format timestamp to readable date string
 */
export function formatTimestamp(timestamp: number): string {
  return formatTimestampToDate(timestamp)
}
