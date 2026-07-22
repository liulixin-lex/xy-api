/*
Copyright (C) 2025 QuantumNous

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

const terminalStripeSubscriptionStatuses = new Set([
  'canceled',
  'incomplete_expired',
]);

export const canScheduleStripeSubscriptionCancellation = (item) =>
  Boolean(
    item &&
    Number(item.id || 0) > 0 &&
    typeof item.status === 'string' &&
    item.status.trim() &&
    !item.cancel_at_period_end &&
    Number(item.ended_at || 0) <= 0 &&
    Number(item.expected_updated_at || 0) > 0 &&
    !terminalStripeSubscriptionStatuses.has(item.status.trim().toLowerCase()),
  );

export const isStripeCancellationReasonValid = (reason) => {
  if (typeof reason !== 'string') return false;
  const length = new TextEncoder().encode(reason.trim()).length;
  return length >= 8 && length <= 512;
};
