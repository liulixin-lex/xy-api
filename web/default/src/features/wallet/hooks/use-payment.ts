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

import { createPaymentQuote, isApiSuccess, startPayment } from '../api'
import { MAX_TOPUP_AMOUNT } from '../constants'
import {
  createPaymentError,
  getPaymentErrorMessage,
  getSafePaymentUrl,
  isUnifiedPaymentMethod,
  navigateToPaymentUrl,
  submitPaymentForm,
} from '../lib'
import type { PaymentMethod, ClientPaymentQuote, PaymentStart } from '../types'

const QUOTE_DEBOUNCE_MS = 300

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

async function requestQuote(
  topupAmount: number,
  method: PaymentMethod,
  signal: AbortSignal
): Promise<ClientPaymentQuote> {
  if (!isUnifiedPaymentMethod(method)) {
    throw new Error(i18next.t('This payment method is temporarily unavailable'))
  }
  const response = await createPaymentQuote(
    {
      order_kind: 'topup',
      route_id: method.route_id,
      amount: topupAmount,
    },
    signal
  )

  if (!isApiSuccess(response) || !response.data) {
    throw createPaymentError(response)
  }
  return {
    quote_id: response.data.quote_id,
    route_id: response.data.route_id || method.route_id,
    public_method: response.data.public_method || method.public_method,
    channel_alias: response.data.channel_alias || method.channel_alias,
    top_up_amount: response.data.top_up_amount,
    plan_id: response.data.plan_id,
    payable_amount: response.data.payable_amount,
    currency: response.data.currency,
    expires_at: response.data.expires_at,
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
      if (!isUnifiedPaymentMethod(method)) {
        if (sequence === requestSequenceRef.current) {
          setQuote(null)
          setQuoteError(
            i18next.t('This payment method is temporarily unavailable')
          )
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
        setQuote(null)
        setQuoteError(getPaymentErrorMessage(error, i18next.t.bind(i18next)))
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
    async (currentQuote: ClientPaymentQuote, method: PaymentMethod) => {
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
        if (!isUnifiedPaymentMethod(method)) {
          throw new Error(
            i18next.t('Payment is temporarily unavailable. Try again.')
          )
        }
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
          throw createPaymentError(response)
        }
        const paymentStart: PaymentStart = response.data

        if (paymentStart.flow === 'qr') {
          return paymentStart
        }
        if (paymentStart.flow === 'pending') {
          return paymentStart
        }
        if (paymentStart.flow === 'hosted_redirect') {
          if (!getSafePaymentUrl(paymentStart.url)) {
            throw new Error(
              i18next.t('Payment is temporarily unavailable. Try again.')
            )
          }
          navigateToPaymentUrl(paymentStart.url)
          return paymentStart
        }
        if (!submitPaymentForm(paymentStart.action, paymentStart.fields)) {
          throw new Error(
            i18next.t('Payment is temporarily unavailable. Try again.')
          )
        }
        return paymentStart
      } catch (error) {
        toast.error(getPaymentErrorMessage(error, i18next.t.bind(i18next)))
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
