/*
Copyright (C) 2025 QuantumNous

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

import { useCallback, useEffect, useRef, useState } from 'react';
import { API } from '../../helpers';

const DEFAULT_ORDER_LIFETIME_SECONDS = 15 * 60;
const MAX_NETWORK_FAILURES = 3;
const PENDING_STATUSES = new Set(['pending', 'processing']);

export function isPaymentOrderPending(status) {
  return PENDING_STATUSES.has(status);
}

function normalizeOrder(order) {
  if (!order) return null;
  return {
    ...order,
    status: ['paid', 'fulfilled'].includes(order.status)
      ? 'success'
      : order.status,
  };
}

export function usePaymentOrderPolling({ t, onSuccess, onTerminal }) {
  const [payment, setPayment] = useState(null);
  const [order, setOrder] = useState(null);
  const [error, setError] = useState('');
  const [polling, setPolling] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [generation, setGeneration] = useState(0);
  const callbacksRef = useRef({ onSuccess, onTerminal });
  const notifiedStatusRef = useRef('');

  callbacksRef.current = { onSuccess, onTerminal };

  const trackPayment = useCallback((nextPayment, initialStatus) => {
    const tradeNo = (nextPayment?.trade_no || '').trim();
    if (!tradeNo) return;

    const expiresAt = Number(nextPayment?.expires_at || 0);
    setPayment({
      ...nextPayment,
      trade_no: tradeNo,
      expires_at: Number.isFinite(expiresAt) ? expiresAt : 0,
    });
    setOrder({
      trade_no: tradeNo,
      expires_at: Number.isFinite(expiresAt) ? expiresAt : 0,
      status:
        initialStatus ||
        (nextPayment?.flow === 'pending' ? 'processing' : 'pending'),
    });
    setError('');
    setPolling(true);
    setRefreshing(false);
    notifiedStatusRef.current = '';
    setGeneration((current) => current + 1);
  }, []);

  const clearPayment = useCallback(() => {
    setPayment(null);
    setOrder(null);
    setError('');
    setPolling(false);
    setRefreshing(false);
    notifiedStatusRef.current = '';
  }, []);

  const refreshPayment = useCallback(() => {
    if (!payment?.trade_no) return;
    setError('');
    setPolling(true);
    setRefreshing(true);
    setGeneration((current) => current + 1);
  }, [payment?.trade_no]);

  useEffect(() => {
    if (!payment?.trade_no) return undefined;

    let stopped = false;
    let timer = null;
    let controller = null;
    let networkFailures = 0;
    const fallbackExpiresAt =
      Math.floor(Date.now() / 1000) + DEFAULT_ORDER_LIFETIME_SECONDS;

    const schedule = (delay) => {
      if (stopped) return;
      timer = window.setTimeout(poll, delay);
    };

    const notifyTerminal = async (nextOrder) => {
      const notificationKey = `${nextOrder.trade_no}:${nextOrder.status}`;
      if (notifiedStatusRef.current === notificationKey) return;
      notifiedStatusRef.current = notificationKey;

      try {
        if (nextOrder.status === 'success') {
          await callbacksRef.current.onSuccess?.(nextOrder);
        }
        await callbacksRef.current.onTerminal?.(nextOrder);
      } catch {
        if (!stopped) {
          setError(t('支付状态已确认，但账户信息刷新失败，请手动刷新页面'));
        }
      }
    };

    const poll = async () => {
      if (stopped) return;
      controller = new AbortController();
      try {
        const response = await API.get(
          `/api/user/payment/orders/${encodeURIComponent(payment.trade_no)}`,
          { signal: controller.signal },
        );
        if (stopped) return;

        if (!response.data?.success || !response.data?.data) {
          setError(response.data?.message || t('支付状态查询失败，请手动刷新'));
          setPolling(false);
          setRefreshing(false);
          return;
        }

        const nextOrder = normalizeOrder(response.data.data);
        networkFailures = 0;
        setOrder(nextOrder);
        setError('');
        setRefreshing(false);

        if (!isPaymentOrderPending(nextOrder.status)) {
          setPolling(false);
          await notifyTerminal(nextOrder);
          return;
        }

        const expiresAt = Number(
          nextOrder.expires_at || payment.expires_at || fallbackExpiresAt,
        );
        const remainingMs = expiresAt * 1000 - Date.now();
        if (remainingMs <= 0) {
          setPolling(false);
          setError(t('订单状态确认超时，请手动刷新'));
          return;
        }

        setPolling(true);
        schedule(Math.max(250, Math.min(2000, remainingMs + 100)));
      } catch (requestError) {
        if (stopped || requestError?.code === 'ERR_CANCELED') return;

        setRefreshing(false);
        if (requestError?.response) {
          setError(
            requestError.response.data?.message ||
              t('支付状态查询失败，请手动刷新'),
          );
          setPolling(false);
          return;
        }

        networkFailures += 1;
        const expiresAt = Number(payment.expires_at || fallbackExpiresAt);
        const remainingMs = expiresAt * 1000 - Date.now();
        if (remainingMs <= 0) {
          setError(t('订单状态确认超时，请手动刷新'));
          setPolling(false);
          return;
        }
        if (networkFailures >= MAX_NETWORK_FAILURES) {
          setError(t('连续无法获取支付状态，请检查网络后手动刷新'));
          setPolling(false);
          return;
        }
        schedule(Math.max(250, Math.min(3000, remainingMs + 100)));
      }
    };

    setPolling(true);
    poll();

    return () => {
      stopped = true;
      if (timer) window.clearTimeout(timer);
      controller?.abort?.();
    };
  }, [generation, payment?.expires_at, payment?.trade_no, t]);

  return {
    payment,
    order,
    error,
    polling,
    refreshing,
    trackPayment,
    refreshPayment,
    clearPayment,
  };
}
