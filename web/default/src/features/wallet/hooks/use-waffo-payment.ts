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
import { useCallback, useRef, useState } from 'react'
import { toast } from 'sonner'

import { createPaymentQuote, isApiSuccess, startPayment } from '../api'
import { createPaymentError, getPaymentErrorMessage } from '../lib/payment'
import type { PaymentStart } from '../types'

function createRequestId(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
    return crypto.randomUUID()
  }
  return `${Date.now()}-${Math.random().toString(36).slice(2)}`
}

/**
 * Hook for handling Waffo payment processing
 */
export function useWaffoPayment() {
  const [processing, setProcessing] = useState(false)
  const operationRef = useRef<{
    key: string
    quoteId: string
    requestId: string
  } | null>(null)

  const processWaffoPayment = useCallback(
    async (
      routeId: string,
      topupAmount: number,
      optionId: string
    ): Promise<PaymentStart | null> => {
      setProcessing(true)

      try {
        const key = `${routeId}:${topupAmount}:${optionId}`
        if (operationRef.current?.key !== key) {
          const quoteResponse = await createPaymentQuote({
            order_kind: 'topup',
            route_id: routeId,
            amount: Math.floor(topupAmount),
            option_id: optionId,
          })
          if (!isApiSuccess(quoteResponse) || !quoteResponse.data) {
            throw createPaymentError(quoteResponse)
          }
          operationRef.current = {
            key,
            quoteId: quoteResponse.data.quote_id,
            requestId: createRequestId(),
          }
        }

        const response = await startPayment({
          quote_id: operationRef.current.quoteId,
          request_id: operationRef.current.requestId,
        })
        if (!isApiSuccess(response) || !response.data) {
          throw createPaymentError(response)
        }
        return response.data
      } catch (error) {
        toast.error(getPaymentErrorMessage(error, i18next.t.bind(i18next)))
        return null
      } finally {
        setProcessing(false)
      }
    },
    []
  )

  return { processing, processWaffoPayment }
}
