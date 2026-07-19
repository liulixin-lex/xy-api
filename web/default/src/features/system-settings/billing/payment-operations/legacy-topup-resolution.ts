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
  isPaymentAuditReasonValid,
  isPaymentProviderReferenceValid,
  parseLegacyTopUpCreditQuota,
} from './payment-action-validation'
import { isPaymentEventActionAvailable } from './status'
import type { PaymentEvent, ResolveLegacyEpayTopUpRequest } from './types'

export type LegacyTopUpResolution = 'fulfill' | 'external_refund'

export interface LegacyTopUpResolutionFormValues {
  resolution: LegacyTopUpResolution | ''
  creditQuota: string
  providerRefundReference: string
  reason: string
}

export function buildLegacyTopUpResolutionRequest(
  event: PaymentEvent | null,
  values: LegacyTopUpResolutionFormValues
): ResolveLegacyEpayTopUpRequest | null {
  if (
    !event ||
    !isPaymentEventActionAvailable(event, 'resolve_legacy_topup') ||
    !isPaymentAuditReasonValid(values.reason)
  ) {
    return null
  }

  if (values.resolution === 'fulfill') {
    const creditQuota = parseLegacyTopUpCreditQuota(values.creditQuota)
    if (creditQuota === null) return null
    return {
      event_id: event.id,
      expected_event_attempts: event.attempts,
      resolution: values.resolution,
      credit_quota: creditQuota,
      provider_refund_reference: '',
      reason: values.reason.trim(),
    }
  }

  if (
    values.resolution === 'external_refund' &&
    isPaymentProviderReferenceValid(values.providerRefundReference)
  ) {
    return {
      event_id: event.id,
      expected_event_attempts: event.attempts,
      resolution: values.resolution,
      credit_quota: 0,
      provider_refund_reference: values.providerRefundReference.trim(),
      reason: values.reason.trim(),
    }
  }

  return null
}
