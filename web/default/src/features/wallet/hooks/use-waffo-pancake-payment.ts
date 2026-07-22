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
import { useState, useCallback } from 'react'
import { toast } from 'sonner'

import { requestWaffoPancakePayment, isApiSuccess } from '../api'
import { getPaymentErrorMessage, navigateToPaymentUrl } from '../lib/payment'

function getCheckoutUrl(data: unknown): string | null {
  if (!data || typeof data !== 'object') {
    return null
  }

  if ('checkout_url' in data && typeof data.checkout_url === 'string') {
    return data.checkout_url
  }

  return null
}

/**
 * Hook for the Waffo Pancake hosted-checkout flow.
 *
 * Same-tab redirect (window.location.href) rather than window.open: the
 * user-gesture context is lost across the await, so popups get blocked.
 */
export function useWaffoPancakePayment() {
  const [processing, setProcessing] = useState(false)

  const processWaffoPancakePayment = useCallback(
    async (topupAmount: number) => {
      setProcessing(true)

      try {
        const response = await requestWaffoPancakePayment({
          amount: Math.floor(topupAmount),
        })

        if (isApiSuccess(response)) {
          const checkoutUrl = getCheckoutUrl(response.data)

          if (checkoutUrl) {
            if (navigateToPaymentUrl(checkoutUrl)) return true
          }
        }

        toast.error(getPaymentErrorMessage(response, i18next.t.bind(i18next)))
        return false
      } catch (error) {
        toast.error(getPaymentErrorMessage(error, i18next.t.bind(i18next)))
        return false
      } finally {
        setProcessing(false)
      }
    },
    []
  )

  return { processing, processWaffoPancakePayment }
}
