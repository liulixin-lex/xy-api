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

import React from 'react';
import { Banner, Button, Space, Typography } from '@douyinfe/semi-ui';
import { isPaymentOrderPending } from './use-payment-order';

const { Text } = Typography;

const STATUS_CONFIG = {
  pending: { type: 'warning', label: '待支付' },
  processing: { type: 'info', label: '处理中' },
  success: { type: 'success', label: '支付成功' },
  failed: { type: 'danger', label: '支付失败' },
  expired: { type: 'warning', label: '已过期' },
  manual_review: { type: 'warning', label: '人工复核' },
  refunded: { type: 'info', label: '已退款' },
  refund_pending: { type: 'warning', label: '退款处理中' },
  disputed: { type: 'danger', label: '争议中' },
  debt: { type: 'danger', label: '欠款冻结' },
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
  if (!payment?.trade_no) return null;

  const status = order?.status || 'processing';
  const config = STATUS_CONFIG[status] || {
    type: 'info',
    label: status,
  };
  const pending = isPaymentOrderPending(status);

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
            <Text type='tertiary'>{t('支付状态')}:</Text>
            <Text>{t(config.label)}</Text>
          </div>
          {error ? (
            <Text type='danger'>{error}</Text>
          ) : order?.status_reason ? (
            <Text type='tertiary'>{t(order.status_reason)}</Text>
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
