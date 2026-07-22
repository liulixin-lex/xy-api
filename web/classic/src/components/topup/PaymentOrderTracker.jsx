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

import React, { useEffect, useState } from 'react';
import { Banner, Button, Space, Typography } from '@douyinfe/semi-ui';
import { isPaymentOrderPending } from './use-payment-order';
import { getPublicPaymentMethodLabel } from './payment-utils';

const { Text } = Typography;

const STATUS_CONFIG = {
  preparing: { type: 'info', label: '支付准备中' },
  awaiting_payment: { type: 'warning', label: '等待支付' },
  confirming: { type: 'info', label: '确认中' },
  succeeded: { type: 'success', label: '支付成功' },
  expired: { type: 'warning', label: '已过期' },
  temporarily_unavailable: { type: 'danger', label: '暂时不可用' },
};

const PaymentOrderTracker = ({
  t,
  payment,
  order,
  error,
  polling,
  refreshing,
  onRefresh,
  onOpenHistory,
  onDismiss,
}) => {
  const [remainingSeconds, setRemainingSeconds] = useState(0);
  const status = order?.status_code || 'preparing';
  const config = STATUS_CONFIG[status] || {
    type: 'danger',
    label: '暂时不可用',
  };
  const pending = isPaymentOrderPending(status);
  const expiresAt = Number(order?.expires_at || payment?.expires_at || 0);
  const paymentMethodLabel = getPublicPaymentMethodLabel(
    order?.public_method ? order : payment,
    t,
  );

  useEffect(() => {
    if (!expiresAt || !pending) {
      setRemainingSeconds(0);
      return undefined;
    }
    const updateRemaining = () => {
      setRemainingSeconds(
        Math.max(0, Math.ceil(expiresAt - Date.now() / 1000)),
      );
    };
    updateRemaining();
    const timer = window.setInterval(updateRemaining, 1000);
    return () => window.clearInterval(timer);
  }, [expiresAt, pending]);

  const minutes = Math.floor(remainingSeconds / 60);
  const seconds = String(remainingSeconds % 60).padStart(2, '0');

  if (!payment?.trade_no) return null;

  return (
    <Banner
      type={config.type}
      title={t(config.label)}
      closeIcon={null}
      fullMode={false}
      description={
        <div className='space-y-2' role='status' aria-live='polite'>
          <div className='flex flex-wrap items-center gap-x-2 gap-y-1'>
            <Text type='tertiary'>{t('订单号')}:</Text>
            <Text copyable>{payment.trade_no}</Text>
            <Text type='tertiary'>{t('支付方式')}:</Text>
            <Text>{paymentMethodLabel}</Text>
            <Text type='tertiary'>{t('支付状态')}:</Text>
            <Text>{t(config.label)}</Text>
          </div>
          {pending && expiresAt > 0 && (
            <Text type='tertiary'>
              {t('剩余时间 {{minutes}}:{{seconds}}', { minutes, seconds })}
            </Text>
          )}
          {error ? (
            <Text type='danger'>{error}</Text>
          ) : pending ? (
            <Text type='tertiary'>
              {polling
                ? t('正在自动确认支付结果')
                : t('自动确认已暂停，请手动刷新')}
            </Text>
          ) : null}
          <Space wrap spacing='tight'>
            <Button
              size='small'
              theme='outline'
              onClick={onRefresh}
              loading={refreshing}
              disabled={refreshing}
            >
              {t('刷新支付状态')}
            </Button>
            {onOpenHistory && (
              <Button size='small' theme='borderless' onClick={onOpenHistory}>
                {t('查看账单')}
              </Button>
            )}
            {!pending && onDismiss && (
              <Button size='small' theme='borderless' onClick={onDismiss}>
                {t('关闭状态提示')}
              </Button>
            )}
          </Space>
        </div>
      }
    />
  );
};

export default PaymentOrderTracker;
