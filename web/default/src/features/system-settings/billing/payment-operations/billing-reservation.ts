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

import { getApiErrorMessage } from '@/lib/api-error'

import type { BillingReservation } from './types'

export const MAX_BILLING_QUOTA = 2_147_483_647

export function parseBillingActualQuota(value: string): number | null {
  const normalized = value.trim()
  if (!/^\d+$/.test(normalized)) {
    return null
  }
  const quota = Number(normalized)
  if (!Number.isSafeInteger(quota) || quota < 0 || quota > MAX_BILLING_QUOTA) {
    return null
  }
  return quota
}

export function isBillingReservationReviewable(
  reservation: BillingReservation
): boolean {
  return (
    reservation.status === 'reserved' &&
    reservation.last_reconciled_at > 0 &&
    reservation.reconcile_note.trim().length > 0
  )
}

export function billingReservationResolutionError(
  error: unknown,
  t: TFunction
): string {
  const message = getApiErrorMessage(error, '').toLowerCase()
  if (message.includes('version has changed')) {
    return t(
      'Billing reservation changed. Refresh the detail before trying again.'
    )
  }
  if (message.includes('must be marked for administrator review')) {
    return t('This reservation is not marked for administrator review.')
  }
  if (message.includes('already finalized')) {
    return t('This reservation has already been finalized.')
  }
  if (message.includes('conflicts with an existing action')) {
    return t(
      'An administrator action already exists for this reservation version.'
    )
  }
  if (message.includes('not found')) {
    return t('Billing reservation not found.')
  }
  if (message.includes('invalid billing reservation admin resolution')) {
    return t('Invalid billing reservation resolution.')
  }
  if (
    message.includes('insufficient') ||
    message.includes('quota update exceeds') ||
    message.includes('billing account not found') ||
    message.includes('conflict')
  ) {
    return t(
      'The requested settlement cannot be applied to the current balance state.'
    )
  }
  return t('Failed to resolve billing reservation')
}
