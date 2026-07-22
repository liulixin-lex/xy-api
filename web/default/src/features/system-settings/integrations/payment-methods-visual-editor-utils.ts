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

export const MAX_CONFIGURED_PAYMENT_METHODS = 27

export type PaymentMethodCollectionError =
  | 'too_many_payment_methods'
  | 'duplicate_payment_method'

const EPAY_RESERVED_TYPES = new Set([
  'stripe',
  'xorpay_native',
  'xorpay_alipay',
  'xorpay_jsapi',
  'waffo_pancake',
])

function inferProvider(type: string): PaymentMethodData['provider'] {
  if (type === 'stripe') return 'stripe'
  if (type === 'creem') return 'creem'
  if (type === 'waffo') return 'waffo'
  if (type.startsWith('xorpay_')) return 'xorpay'
  if (type === 'waffo_pancake') return 'waffo_pancake'
  return 'epay'
}

export function getPaymentMethodIdentity(method: {
  provider: string
  type: string
}): string {
  const provider = String(method.provider || 'epay')
    .trim()
    .toLowerCase()
  const rawType = String(method.type || '').trim()
  const type = provider === 'epay' ? rawType : rawType.toLowerCase()
  return `${provider}\u0000${type}`
}

export function validatePaymentMethodCollection(
  methods: Array<{ provider: string; type: string }>
): PaymentMethodCollectionError | null {
  if (methods.length > MAX_CONFIGURED_PAYMENT_METHODS) {
    return 'too_many_payment_methods'
  }
  const identities = new Set<string>()
  for (const method of methods) {
    const identity = getPaymentMethodIdentity(method)
    if (identities.has(identity)) return 'duplicate_payment_method'
    identities.add(identity)
  }
  return null
}

export function removePaymentMethodByIdentity(
  methods: unknown[],
  target: { provider: string; type: string }
): unknown[] {
  const targetIdentity = getPaymentMethodIdentity(target)
  const targetIndex = methods.findIndex((item) => {
    const method = normalizePaymentMethod(item)
    return (
      method !== null && getPaymentMethodIdentity(method) === targetIdentity
    )
  })
  if (targetIndex === -1) return methods
  return [...methods.slice(0, targetIndex), ...methods.slice(targetIndex + 1)]
}

export function isPaymentMethodProviderTypeValid(
  providerValue: string,
  typeValue: string
): boolean {
  const provider = providerValue.trim().toLowerCase()
  const rawType = typeValue.trim()
  const type = provider === 'epay' ? rawType : rawType.toLowerCase()
  if (provider === 'epay') return !EPAY_RESERVED_TYPES.has(type)
  if (provider === 'stripe') return type === 'stripe'
  if (provider === 'creem') return type === 'creem'
  if (provider === 'waffo') return type === 'waffo'
  if (provider === 'waffo_pancake') return type === 'waffo_pancake'
  if (provider === 'xorpay') {
    return ['xorpay_native', 'xorpay_alipay', 'xorpay_jsapi'].includes(type)
  }
  return false
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
  const providerValues = [
    'epay',
    'stripe',
    'xorpay',
    'creem',
    'waffo',
    'waffo_pancake',
  ]
  const configuredProvider =
    'provider' in item &&
    typeof item.provider === 'string' &&
    providerValues.includes(item.provider.trim().toLowerCase())
      ? (item.provider.trim().toLowerCase() as PaymentMethodData['provider'])
      : inferProvider(item.type)
  const method = {
    ...record,
    name: item.name,
    type: item.type,
    provider: configuredProvider,
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
