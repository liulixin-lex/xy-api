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
import type { PaymentOrder } from './types'

export const PAYMENT_AUDIT_REASON_MIN_BYTES = 8
export const PAYMENT_AUDIT_REASON_MAX_BYTES = 512
export const PAYMENT_TRADE_NO_MAX_BYTES = 128
export const LEGACY_TOPUP_CREDIT_QUOTA_MAX = 2_147_483_647
export const PAYMENT_PROVIDER_REFERENCE_MAX_BYTES = 255

function utf8Length(value: string): number {
  return new TextEncoder().encode(value).length
}

export function isPaymentAuditReasonValid(reason: string): boolean {
  const length = utf8Length(reason.trim())
  return (
    length >= PAYMENT_AUDIT_REASON_MIN_BYTES &&
    length <= PAYMENT_AUDIT_REASON_MAX_BYTES
  )
}

export function isPaymentTradeNoValid(tradeNo: string): boolean {
  const trimmed = tradeNo.trim()
  return trimmed.length > 0 && utf8Length(trimmed) <= PAYMENT_TRADE_NO_MAX_BYTES
}

export function parsePositiveSafeInteger(value: string): number | null {
  const trimmed = value.trim()
  if (!/^\d+$/.test(trimmed)) return null
  const parsed = Number(trimmed)
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : null
}

export function parseLegacyTopUpCreditQuota(value: string): number | null {
  const parsed = parsePositiveSafeInteger(value)
  return parsed !== null && parsed <= LEGACY_TOPUP_CREDIT_QUOTA_MAX
    ? parsed
    : null
}

export function isPaymentProviderReferenceValid(reference: string): boolean {
  const trimmed = reference.trim()
  const length = utf8Length(trimmed)
  return (
    length > 0 &&
    length <= PAYMENT_PROVIDER_REFERENCE_MAX_BYTES &&
    !/\p{Cc}/u.test(trimmed)
  )
}

export function isExternalRefundAmountValid(
  order: PaymentOrder,
  refundedAmountMinor: number | null
): boolean {
  if (
    refundedAmountMinor === null ||
    !Number.isSafeInteger(refundedAmountMinor) ||
    refundedAmountMinor <= order.refunded_amount_minor ||
    refundedAmountMinor > order.expected_amount_minor ||
    order.disputed_amount_minor !== 0
  ) {
    return false
  }
  return (
    order.order_kind !== 'subscription' ||
    refundedAmountMinor === order.expected_amount_minor
  )
}
