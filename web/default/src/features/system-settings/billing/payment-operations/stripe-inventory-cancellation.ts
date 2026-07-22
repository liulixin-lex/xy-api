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
import type { StripeLegacySubscription } from './types'

const TERMINAL_STRIPE_SUBSCRIPTION_STATUSES = new Set([
  'canceled',
  'incomplete_expired',
])

export function canScheduleStripeSubscriptionCancellation(
  item: Pick<
    StripeLegacySubscription,
    | 'id'
    | 'cancel_at_period_end'
    | 'ended_at'
    | 'expected_updated_at'
    | 'status'
  >
): boolean {
  const status =
    typeof item.status === 'string' ? item.status.trim().toLowerCase() : ''
  return (
    item.id > 0 &&
    status.length > 0 &&
    !item.cancel_at_period_end &&
    item.ended_at <= 0 &&
    item.expected_updated_at > 0 &&
    !TERMINAL_STRIPE_SUBSCRIPTION_STATUSES.has(status)
  )
}

export function isStripeCancellationReasonValid(reason: string): boolean {
  const length = new TextEncoder().encode(reason.trim()).length
  return length >= 8 && length <= 512
}
