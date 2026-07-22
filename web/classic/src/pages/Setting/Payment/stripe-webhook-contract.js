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

const STRIPE_WEBHOOK_SECRET_OVERLAP_FALLBACK_HOURS = 24;

export function resolveStripeWebhookContract(apiVersion, overlapHours) {
  const normalizedAPIVersion =
    typeof apiVersion === 'string' ? apiVersion.trim() : '';
  const parsedOverlapHours =
    typeof overlapHours === 'number' || typeof overlapHours === 'string'
      ? Number(overlapHours)
      : Number.NaN;

  return {
    apiVersion: normalizedAPIVersion,
    overlapHours:
      Number.isSafeInteger(parsedOverlapHours) && parsedOverlapHours > 0
        ? parsedOverlapHours
        : STRIPE_WEBHOOK_SECRET_OVERLAP_FALLBACK_HOURS,
  };
}
