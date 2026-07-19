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
import { useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import {
  calculateAmount,
  calculateStripeAmount,
  calculateWaffoPancakeAmount,
  createPaymentQuote,
  isApiSuccess,
  requestPayment,
  requestStripePayment,
  startPayment,
} from '../api'
import { MAX_TOPUP_AMOUNT } from '../constants'
import {
  getSafePaymentUrl,
  isUnifiedPaymentMethod,
  navigateToPaymentUrl,
  submitPaymentForm,
} from '../lib'
import type { PaymentMethod, ClientPaymentQuote, PaymentStart } from '../types'

const QUOTE_DEBOUNCE_MS = 300
const LEGACY_QUOTE_TTL_SECONDS = 5 * 60

function getHttpStatus(error: unknown): number | undefined {
  if (!error || typeof error !== 'object' || !('response' in error)) {
    return undefined
  }
  const response = (error as { response?: { status?: unknown } }).response
  return typeof response?.status === 'number' ? response.status : undefined
}

function isEndpointUnavailable(error: unknown): boolean {
  const status = getHttpStatus(error)
  return status === 404 || status === 405 || status === 501
}

function isRequestCancelled(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false
  const candidate = error as { code?: unknown; name?: unknown }
  return candidate.code === 'ERR_CANCELED' || candidate.name === 'AbortError'
}

function createRequestId(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
    return crypto.randomUUID()
  }
  return `${Date.now()}-${Math.random().toString(36).slice(2)}`
}

async function createLegacyQuote(
  topupAmount: number,
  method: PaymentMethod,
  signal?: AbortSignal
): Promise<ClientPaymentQuote> {
  if (signal?.aborted) throw new DOMException('Aborted', 'AbortError')

  let response
  if (method.provider === 'stripe') {
    response = await calculateStripeAmount({ amount: topupAmount })
  } else if (method.provider === 'waffo_pancake') {
    response = await calculateWaffoPancakeAmount({ amount: topupAmount })
  } else {
    response = await calculateAmount({ amount: topupAmount })
  }

  if (!isApiSuccess(response) || !response.data) {
    throw new Error(response.message || i18next.t('Payment quote failed'))
  }

  const payableAmount = Number.parseFloat(response.data)
  if (!Number.isFinite(payableAmount) || payableAmount <= 0) {
    throw new Error(i18next.t('Payment quote is invalid'))
  }

  const expiresAt = Math.floor(Date.now() / 1000) + LEGACY_QUOTE_TTL_SECONDS
  return {
    quote_id: `legacy:${method.provider}:${method.type}:${topupAmount}:${expiresAt}`,
    order_kind: 'topup',
    provider: method.provider,
    payment_method: method.type,
    requested_amount: topupAmount,
    credit_quota: 0,
    expected_amount_minor: Math.round(payableAmount * 100),
    payable_amount: payableAmount.toFixed(2),
    currency: method.currency || (method.provider === 'stripe' ? 'USD' : 'CNY'),
    expires_at: expiresAt,
    legacy: true,
  }
}

async function requestQuote(
  topupAmount: number,
  method: PaymentMethod,
  signal: AbortSignal
): Promise<ClientPaymentQuote> {
  if (!isUnifiedPaymentMethod(method)) {
    return createLegacyQuote(topupAmount, method, signal)
  }
  try {
    const response = await createPaymentQuote(
      {
        order_kind: 'topup',
        provider: method.provider,
        payment_method: method.type,
        amount: topupAmount,
      },
      signal
    )

    if (!isApiSuccess(response) || !response.data) {
      throw new Error(response.message || i18next.t('Payment quote failed'))
    }
    return response.data
  } catch (error) {
    if (!isEndpointUnavailable(error) || method.provider === 'xorpay') {
      throw error
    }
    return createLegacyQuote(topupAmount, method, signal)
  }
}

async function startLegacyPayment(
  quote: ClientPaymentQuote
): Promise<PaymentStart> {
  const request = {
    amount: Math.floor(quote.requested_amount),
    payment_method: quote.payment_method,
  }

  if (quote.provider === 'stripe') {
    const response = await requestStripePayment({
      ...request,
      payment_method: 'stripe',
    })
    if (!isApiSuccess(response) || !response.data?.pay_link) {
      throw new Error(response.message || i18next.t('Payment request failed'))
    }
    return {
      flow: 'hosted_redirect',
      trade_no: '',
      url: response.data.pay_link,
      expires_at: quote.expires_at,
    }
  }

  const response = await requestPayment(request)
  if (!isApiSuccess(response) || !response.url || !response.data) {
    throw new Error(response.message || i18next.t('Payment request failed'))
  }
  return {
    flow: 'form_post',
    trade_no: '',
    action: response.url,
    fields: Object.fromEntries(
      Object.entries(response.data).map(([key, value]) => [key, String(value)])
    ),
    expires_at: quote.expires_at,
  }
}

export function usePayment() {
  const [quote, setQuote] = useState<ClientPaymentQuote | null>(null)
  const [quoteError, setQuoteError] = useState<string | null>(null)
  const [calculating, setCalculating] = useState(false)
  const [processing, setProcessing] = useState(false)
  const requestSequenceRef = useRef(0)
  const abortControllerRef = useRef<AbortController | null>(null)
  const debounceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const paymentRequestRef = useRef<{
    quoteId: string
    requestId: string
  } | null>(null)
  const processingRef = useRef(false)

  const cancelPendingQuote = useCallback(() => {
    requestSequenceRef.current += 1
    abortControllerRef.current?.abort()
    abortControllerRef.current = null
    if (debounceTimerRef.current) {
      clearTimeout(debounceTimerRef.current)
      debounceTimerRef.current = null
    }
    paymentRequestRef.current = null
  }, [])

  const applyQuote = useCallback(
    async (
      topupAmount: number,
      method: PaymentMethod,
      sequence: number
    ): Promise<ClientPaymentQuote | null> => {
      if (
        !Number.isSafeInteger(topupAmount) ||
        topupAmount < 1 ||
        topupAmount > MAX_TOPUP_AMOUNT
      ) {
        if (sequence === requestSequenceRef.current) {
          setQuote(null)
          setQuoteError(
            i18next.t('Enter a whole-number amount between 1 and 10000')
          )
          setCalculating(false)
        }
        return null
      }
      if (
        !isUnifiedPaymentMethod(method) &&
        method.provider !== 'waffo_pancake'
      ) {
        if (sequence === requestSequenceRef.current) {
          setQuote(null)
          setQuoteError(i18next.t('This payment method uses a legacy flow'))
          setCalculating(false)
        }
        return null
      }

      const controller = new AbortController()
      abortControllerRef.current = controller
      try {
        const nextQuote = await requestQuote(
          topupAmount,
          method,
          controller.signal
        )
        if (sequence !== requestSequenceRef.current) return null
        setQuote(nextQuote)
        setQuoteError(null)
        return nextQuote
      } catch (error) {
        if (
          sequence !== requestSequenceRef.current ||
          isRequestCancelled(error)
        ) {
          return null
        }
        const message =
          error instanceof Error && error.message
            ? error.message
            : i18next.t('Payment quote failed')
        setQuote(null)
        setQuoteError(message)
        return null
      } finally {
        if (sequence === requestSequenceRef.current) {
          setCalculating(false)
          abortControllerRef.current = null
        }
      }
    },
    []
  )

  const calculatePaymentQuote = useCallback(
    async (topupAmount: number, method: PaymentMethod) => {
      cancelPendingQuote()
      const sequence = requestSequenceRef.current
      setQuote(null)
      setQuoteError(null)
      setCalculating(true)
      return applyQuote(topupAmount, method, sequence)
    },
    [applyQuote, cancelPendingQuote]
  )

  const schedulePaymentQuote = useCallback(
    (topupAmount: number, method: PaymentMethod) => {
      cancelPendingQuote()
      const sequence = requestSequenceRef.current
      setQuote(null)
      setQuoteError(null)
      setCalculating(true)
      debounceTimerRef.current = setTimeout(() => {
        debounceTimerRef.current = null
        void applyQuote(topupAmount, method, sequence)
      }, QUOTE_DEBOUNCE_MS)
    },
    [applyQuote, cancelPendingQuote]
  )

  const processPayment = useCallback(
    async (currentQuote: ClientPaymentQuote) => {
      if (processingRef.current) return null
      if (currentQuote.expires_at <= Math.floor(Date.now() / 1000)) {
        toast.error(
          i18next.t('Payment quote expired. Please request a new quote.')
        )
        return null
      }

      processingRef.current = true
      setProcessing(true)
      try {
        let paymentStart: PaymentStart
        if (currentQuote.provider === 'waffo_pancake') {
          throw new Error(i18next.t('This payment method uses a legacy flow'))
        }
        if (currentQuote.quote_id.startsWith('legacy:')) {
          paymentStart = await startLegacyPayment(currentQuote)
        } else {
          if (paymentRequestRef.current?.quoteId !== currentQuote.quote_id) {
            paymentRequestRef.current = {
              quoteId: currentQuote.quote_id,
              requestId: createRequestId(),
            }
          }
          const response = await startPayment({
            quote_id: currentQuote.quote_id,
            request_id: paymentRequestRef.current.requestId,
          })
          if (!isApiSuccess(response) || !response.data) {
            throw new Error(
              response.message || i18next.t('Payment request failed')
            )
          }
          paymentStart = response.data
        }

        if (paymentStart.flow === 'qr') {
          return paymentStart
        }
        if (paymentStart.flow === 'pending') {
          return paymentStart
        }
        if (paymentStart.flow === 'hosted_redirect') {
          if (!getSafePaymentUrl(paymentStart.url)) {
            throw new Error(i18next.t('Invalid payment redirect URL'))
          }
          toast.success(i18next.t('Redirecting to payment page...'))
          navigateToPaymentUrl(paymentStart.url)
          return paymentStart
        }
        if (!submitPaymentForm(paymentStart.action, paymentStart.fields)) {
          throw new Error(i18next.t('Invalid payment redirect URL'))
        }
        toast.success(i18next.t('Redirecting to payment page...'))
        return paymentStart
      } catch (error) {
        toast.error(
          error instanceof Error && error.message
            ? error.message
            : i18next.t('Payment request failed')
        )
        return null
      } finally {
        processingRef.current = false
        setProcessing(false)
      }
    },
    []
  )

  useEffect(() => cancelPendingQuote, [cancelPendingQuote])

  return {
    quote,
    quoteError,
    amount: Number.parseFloat(quote?.payable_amount || '0') || 0,
    calculating,
    processing,
    calculatePaymentQuote,
    schedulePaymentQuote,
    processPayment,
    cancelPendingQuote,
  }
}
