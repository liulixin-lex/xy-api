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

export const LEGACY_STRIPE_PRICE_ID_PURPOSE = 'legacy_recurring_mapping_only';

export const normalizeLegacyStripePriceID = (value) =>
  typeof value === 'string' ? value.trim() : '';

export const isValidLegacyStripePriceID = (value) => {
  const normalized = normalizeLegacyStripePriceID(value);
  return (
    normalized === '' ||
    (normalized.startsWith('price_') && normalized.length <= 128)
  );
};

export const hasLegacyStripePriceMapping = (record) =>
  record?.stripe_price_id_purpose === LEGACY_STRIPE_PRICE_ID_PURPOSE &&
  normalizeLegacyStripePriceID(record?.plan?.stripe_price_id) !== '';
