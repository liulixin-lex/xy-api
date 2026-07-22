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
export type WaffoPancakePricingError = 'unit_price' | 'min_top_up' | null

export function getWaffoPancakePricingError(
  unitPrice: number,
  minTopUp: number
): WaffoPancakePricingError {
  if (!Number.isFinite(unitPrice) || unitPrice <= 0 || unitPrice > 1_000_000) {
    return 'unit_price'
  }
  if (!Number.isInteger(minTopUp) || minTopUp < 1 || minTopUp > 10_000) {
    return 'min_top_up'
  }
  return null
}
