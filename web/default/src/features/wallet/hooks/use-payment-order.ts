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

import { getPaymentOrder, isApiSuccess } from '../api'
import type { PaymentOrder, PaymentOrderStatus } from '../types'

const POLL_INTERVAL_MS = 2_000
const FALLBACK_POLL_DURATION_MS = 15 * 60 * 1000

export type PaymentPollingStopReason = 'expired' | 'network' | 'timeout'

export function isPaymentOrderTerminal(status: PaymentOrderStatus): boolean {
  return !['pending', 'processing'].includes(status)
}

interface UsePaymentOrderOptions {
  tradeNo?: string
  enabled?: boolean
  expiresAt?: number
  onSettled?: (order: PaymentOrder) => void | Promise<void>
}

export function usePaymentOrder(options: UsePaymentOrderOptions) {
  const [order, setOrder] = useState<PaymentOrder | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pollingStoppedReason, setPollingStoppedReason] =
    useState<PaymentPollingStopReason | null>(null)
  const [pollingRevision, setPollingRevision] = useState(0)
  const settledTradeNoRef = useRef<string | null>(null)
  const orderRef = useRef<PaymentOrder | null>(null)
  const requestRef = useRef<AbortController | null>(null)
  const onSettledRef = useRef(options.onSettled)

  useEffect(() => {
    onSettledRef.current = options.onSettled
  }, [options.onSettled])

  const refresh = useCallback(async () => {
    if (!options.enabled || !options.tradeNo) return null

    requestRef.current?.abort()
    const controller = new AbortController()
    requestRef.current = controller
    if (!orderRef.current) setLoading(true)
    try {
      const response = await getPaymentOrder(options.tradeNo, controller.signal)
      if (!isApiSuccess(response) || !response.data) {
        setError(response.message || i18next.t('Failed to load payment status'))
        return null
      }
      orderRef.current = response.data
      setOrder(response.data)
      setError(null)
      if (
        isPaymentOrderTerminal(response.data.status) &&
        settledTradeNoRef.current !== response.data.trade_no
      ) {
        settledTradeNoRef.current = response.data.trade_no
        await onSettledRef.current?.(response.data)
      }
      return response.data
    } catch (requestError) {
      const cancelled =
        requestError &&
        typeof requestError === 'object' &&
        'code' in requestError &&
        requestError.code === 'ERR_CANCELED'
      if (!cancelled) {
        setError(i18next.t('Failed to load payment status'))
      }
      return null
    } finally {
      if (requestRef.current === controller) {
        requestRef.current = null
        setLoading(false)
      }
    }
  }, [options.enabled, options.tradeNo])

  useEffect(() => {
    if (!options.enabled || !options.tradeNo) {
      requestRef.current?.abort()
      requestRef.current = null
      orderRef.current = null
      setOrder(null)
      setError(null)
      setPollingStoppedReason(null)
      return
    }

    orderRef.current = null
    settledTradeNoRef.current = null
    setOrder(null)
    setError(null)
    setPollingStoppedReason(null)

    let stopped = false
    let timer: ReturnType<typeof setTimeout> | undefined
    let consecutiveFailures = 0
    const fallbackPollDeadline =
      options.expiresAt && options.expiresAt > 0
        ? options.expiresAt * 1000
        : Date.now() + FALLBACK_POLL_DURATION_MS
    const poll = async () => {
      const nextOrder = await refresh()
      if (stopped || (nextOrder && isPaymentOrderTerminal(nextOrder.status))) {
        return
      }

      if (nextOrder) {
        consecutiveFailures = 0
      } else {
        consecutiveFailures += 1
        if (consecutiveFailures >= 3) {
          setPollingStoppedReason('network')
          return
        }
      }

      const currentOrder = nextOrder ?? orderRef.current
      const expiresAtMs = currentOrder?.expires_at
        ? currentOrder.expires_at * 1000
        : fallbackPollDeadline
      if (Date.now() >= expiresAtMs) {
        setPollingStoppedReason(currentOrder ? 'expired' : 'timeout')
        return
      }

      timer = setTimeout(
        poll,
        Math.min(
          nextOrder
            ? POLL_INTERVAL_MS
            : POLL_INTERVAL_MS * 2 ** consecutiveFailures,
          Math.max(250, expiresAtMs - Date.now())
        )
      )
    }
    void poll()

    const handleVisibilityChange = () => {
      if (!document.hidden && !stopped && !requestRef.current) void refresh()
    }
    document.addEventListener('visibilitychange', handleVisibilityChange)

    return () => {
      stopped = true
      requestRef.current?.abort()
      requestRef.current = null
      if (timer) clearTimeout(timer)
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
  }, [
    options.enabled,
    options.expiresAt,
    options.tradeNo,
    pollingRevision,
    refresh,
  ])

  const resumePolling = useCallback(() => {
    if (!options.enabled || !options.tradeNo) return
    setPollingStoppedReason(null)
    setPollingRevision((revision) => revision + 1)
  }, [options.enabled, options.tradeNo])

  return {
    order,
    loading,
    error,
    pollingStoppedReason,
    refresh,
    resumePolling,
  }
}
