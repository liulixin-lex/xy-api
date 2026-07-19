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
const decimalNumberPattern = /^(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/

function isJsonObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function isFiniteNonNegativeNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}

export function isFiniteNonNegativeNumberMap(value: unknown): boolean {
  return (
    isJsonObject(value) && Object.values(value).every(isFiniteNonNegativeNumber)
  )
}

export function isFiniteNonNegativeNestedNumberMap(value: unknown): boolean {
  return (
    isJsonObject(value) &&
    Object.values(value).every(
      (nestedValue) =>
        isJsonObject(nestedValue) &&
        Object.values(nestedValue).every(isFiniteNonNegativeNumber)
    )
  )
}

export function parseFiniteNonNegativeNumber(value: string): number | null {
  const trimmed = value.trim()
  if (!trimmed || !decimalNumberPattern.test(trimmed)) return null

  const parsed = Number(trimmed)
  return isFiniteNonNegativeNumber(parsed) ? parsed : null
}
