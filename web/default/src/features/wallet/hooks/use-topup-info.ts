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
import i18next from 'i18next'
import { useState, useEffect, useCallback } from 'react'

import { getTopupInfo } from '../api'
import {
  generatePresetAmounts,
  mergePresetAmounts,
  getMinTopupAmount,
  isPublicPaymentRouteId,
  normalizePaymentChannelAlias,
  normalizePublicPaymentMethod,
  filterPaymentMethodsForBrowser,
} from '../lib'
import type {
  TopupInfo,
  PresetAmount,
  PaymentProduct,
  PaymentMethod,
  PaymentRouteOption,
  PaymentCheckoutMode,
} from '../types'

// ============================================================================
// Topup Info Hook
// ============================================================================

function parseJsonArray(data: unknown): unknown[] {
  if (Array.isArray(data)) {
    return data
  }

  if (typeof data === 'string') {
    try {
      const parsed = JSON.parse(data)
      return Array.isArray(parsed) ? parsed : []
    } catch {
      return []
    }
  }

  return []
}

function parsePaymentRoutes(data: unknown): PaymentMethod[] {
  const methods = parseJsonArray(data)
    .filter(
      (item): item is Record<string, unknown> =>
        !!item && typeof item === 'object'
    )
    .map((item): PaymentMethod | null => {
      const rawMinTopup = Number(item.min_topup)
      const normalizedMinTopup =
        Number.isSafeInteger(rawMinTopup) && rawMinTopup > 0 ? rawMinTopup : 0
      const type = typeof item.type === 'string' ? item.type : ''
      const legacyName = typeof item.name === 'string' ? item.name : ''
      if (!isPublicPaymentRouteId(item.route_id)) return null

      const checkoutMode: PaymentCheckoutMode =
        item.checkout_mode === 'product' ||
        item.checkout_mode === 'option' ||
        item.checkout_mode === 'direct'
          ? item.checkout_mode
          : 'quote'
      const currency =
        typeof item.currency === 'string' && /^[A-Za-z]{3}$/.test(item.currency)
          ? item.currency.toUpperCase()
          : undefined
      return {
        route_id: item.route_id,
        public_method: normalizePublicPaymentMethod(
          item.public_method,
          type,
          legacyName
        ),
        channel_alias: normalizePaymentChannelAlias(item.channel_alias),
        checkout_mode: checkoutMode,
        ...(currency ? { currency } : {}),
        min_topup: normalizedMinTopup,
      }
    })
    .filter((item): item is PaymentMethod => item !== null)

  return filterPaymentMethodsForBrowser(methods)
}

function parsePaymentRouteOptions(data: unknown): PaymentRouteOption[] {
  return parseJsonArray(data)
    .filter(
      (item): item is Record<string, unknown> =>
        !!item && typeof item === 'object'
    )
    .map((item): PaymentRouteOption | null => {
      const optionId =
        typeof item.option_id === 'string' ? item.option_id.trim() : ''
      const publicLabel = item.public_label
      if (!/^option_[a-f0-9]{24}$/.test(optionId)) return null
      if (!isPublicPaymentRouteId(item.route_id)) return null
      if (
        publicLabel !== 'Card' &&
        publicLabel !== 'Apple Pay' &&
        publicLabel !== 'Google Pay' &&
        publicLabel !== 'Online payment'
      ) {
        return null
      }
      return {
        option_id: optionId,
        route_id: item.route_id,
        public_label: publicLabel,
      }
    })
    .filter((item): item is PaymentRouteOption => item !== null)
}

function parsePaymentProducts(data: unknown): PaymentProduct[] {
  return parseJsonArray(data)
    .filter(
      (item): item is Record<string, unknown> =>
        !!item && typeof item === 'object'
    )
    .map((item): PaymentProduct | null => {
      const productId =
        typeof item.product_id === 'string' ? item.product_id.trim() : ''
      const name = typeof item.name === 'string' ? item.name.trim() : ''
      const paymentAmount =
        typeof item.payment_amount === 'string'
          ? item.payment_amount.trim()
          : ''
      const topUpAmount = Number(item.top_up_amount)
      const currency =
        typeof item.currency === 'string' ? item.currency.toUpperCase() : ''
      if (!/^product_[a-f0-9]{24}$/.test(productId)) return null
      if (!isPublicPaymentRouteId(item.route_id)) return null
      if (!name || name.length > 128) return null
      if (!/^(?:0|[1-9]\d{0,15})(?:\.\d{1,3})?$/.test(paymentAmount)) {
        return null
      }
      if (!Number.isSafeInteger(topUpAmount) || topUpAmount <= 0) return null
      if (!/^[A-Z]{3}$/.test(currency)) return null
      return {
        product_id: productId,
        route_id: item.route_id,
        name,
        payment_amount: paymentAmount,
        top_up_amount: topUpAmount,
        currency,
      }
    })
    .filter((item): item is PaymentProduct => item !== null)
}

function parseAmountOptions(data: unknown): number[] {
  return parseJsonArray(data)
    .map((item) => Number(item))
    .filter((item) => Number.isFinite(item) && item > 0)
}

function parseDiscountMap(data: unknown): Record<number, number> {
  if (!data) {
    return {}
  }

  let parsedData = data

  if (typeof data === 'string') {
    try {
      parsedData = JSON.parse(data)
    } catch {
      return {}
    }
  }

  if (
    !parsedData ||
    typeof parsedData !== 'object' ||
    Array.isArray(parsedData)
  ) {
    return {}
  }

  return Object.entries(parsedData).reduce<Record<number, number>>(
    (result, [key, value]) => {
      const numericKey = Number(key)
      const numericValue = Number(value)

      if (Number.isFinite(numericKey) && Number.isFinite(numericValue)) {
        result[numericKey] = numericValue
      }

      return result
    },
    {}
  )
}

function parseOptionalNumber(data: unknown): number | undefined {
  const value = Number(data)
  return Number.isFinite(value) ? value : undefined
}

export function normalizePublicTopupInfo(data: unknown): TopupInfo | null {
  if (!data || typeof data !== 'object') return null
  const raw = data as Record<string, unknown>
  const paymentRoutes = parsePaymentRoutes(
    raw.payment_routes ?? raw.pay_methods
  )
  const paymentProducts = parsePaymentProducts(raw.payment_products)
  const paymentRouteOptions = parsePaymentRouteOptions(
    raw.payment_route_options
  )
  const minTopup = Number(raw.min_topup)

  return {
    online_payment_available:
      raw.online_payment_available === true || paymentRoutes.length > 0,
    enable_redemption: raw.enable_redemption !== false,
    payment_compliance_confirmed: raw.payment_compliance_confirmed !== false,
    payment_compliance_terms_version:
      typeof raw.payment_compliance_terms_version === 'string'
        ? raw.payment_compliance_terms_version
        : undefined,
    payment_routes: paymentRoutes,
    payment_products: paymentProducts,
    payment_route_options: paymentRouteOptions,
    min_topup: Number.isSafeInteger(minTopup) && minTopup > 0 ? minTopup : 1,
    amount_options: parseAmountOptions(raw.amount_options),
    discount: parseDiscountMap(raw.discount),
    affiliate_continuous_percent: parseOptionalNumber(
      raw.affiliate_continuous_percent
    ),
    affiliate_first_topup_percent: parseOptionalNumber(
      raw.affiliate_first_topup_percent
    ),
    topup_link: typeof raw.topup_link === 'string' ? raw.topup_link : undefined,
  }
}

export function useTopupInfo() {
  const [topupInfo, setTopupInfo] = useState<TopupInfo | null>(null)
  const [presetAmounts, setPresetAmounts] = useState<PresetAmount[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const fetchTopupInfo = useCallback(async () => {
    try {
      setLoading(true)

      const response = await getTopupInfo()

      if (!response.success || !response.data) {
        setError(i18next.t('Failed to load top-up information'))
        return
      }

      const processedData = normalizePublicTopupInfo(response.data)
      if (!processedData) {
        setError(i18next.t('Failed to load top-up information'))
        return
      }

      setTopupInfo(processedData)
      setError(null)

      if (processedData.amount_options.length > 0) {
        const customPresets = mergePresetAmounts(
          processedData.amount_options,
          processedData.discount || {}
        )
        setPresetAmounts(customPresets)
      } else {
        const minTopup = getMinTopupAmount(processedData)
        const defaultPresets = generatePresetAmounts(minTopup)
        setPresetAmounts(defaultPresets)
      }
    } catch {
      setError(i18next.t('Failed to load top-up information'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    let cancelled = false

    queueMicrotask(() => {
      if (!cancelled) void fetchTopupInfo()
    })

    return () => {
      cancelled = true
    }
  }, [fetchTopupInfo])

  return {
    topupInfo,
    presetAmounts,
    loading,
    error,
    refetch: fetchTopupInfo,
  }
}
