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
import type { PaymentMethodData } from './payment-method-dialog'

function inferProvider(type: string): PaymentMethodData['provider'] {
  if (type === 'stripe') return 'stripe'
  if (type.startsWith('xorpay_')) return 'xorpay'
  if (type === 'waffo_pancake') return 'waffo_pancake'
  return 'epay'
}

export function getPaymentMethodIdentity(method: PaymentMethodData): string {
  return `${method.provider}\u0000${method.type}`
}

export function normalizePaymentMethod(
  item: unknown
): PaymentMethodData | null {
  if (
    !item ||
    typeof item !== 'object' ||
    !('name' in item) ||
    !('type' in item) ||
    typeof item.name !== 'string' ||
    typeof item.type !== 'string'
  ) {
    return null
  }

  const record = item as Record<string, unknown>
  const providerValues = ['epay', 'stripe', 'xorpay', 'waffo_pancake']
  const provider =
    'provider' in item &&
    typeof item.provider === 'string' &&
    providerValues.includes(item.provider)
      ? (item.provider as PaymentMethodData['provider'])
      : inferProvider(item.type)
  const method = {
    ...record,
    name: item.name,
    type: item.type,
    provider,
  } as PaymentMethodData
  if (typeof record.icon === 'string') method.icon = record.icon
  else delete method.icon
  if (typeof record.color === 'string') method.color = record.color
  else delete method.color
  if (
    'min_topup' in item &&
    (typeof item.min_topup === 'string' || typeof item.min_topup === 'number')
  ) {
    method.min_topup = String(item.min_topup)
  } else delete method.min_topup
  return method
}

export function mergePaymentMethodEdit(
  existing: PaymentMethodData,
  data: PaymentMethodData
): PaymentMethodData {
  const identityChanged =
    getPaymentMethodIdentity(existing) !== getPaymentMethodIdentity(data)
  const merged = { ...existing, ...data }
  if (identityChanged) {
    delete merged.flow
    delete merged.route_id
    delete merged.public_method
    delete merged.channel_alias
  }
  return merged
}
