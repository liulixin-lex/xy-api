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
import { Banner, Modal, Typography, Card, Skeleton } from '@douyinfe/semi-ui';
import { SiAlipay, SiWechat } from 'react-icons/si';
import { CreditCard } from 'lucide-react';
import {
  formatPaymentDecimal,
  getPaymentSelectionId,
  getPublicPaymentMethod,
  getPublicPaymentMethodLabel,
} from '../payment-utils';

const { Text } = Typography;

const PaymentConfirmModal = ({
  t,
  open,
  onlineTopUp,
  handleCancel,
  confirmLoading,
  topUpCount,
  renderQuotaWithAmount,
  amountLoading,
  renderAmount,
  payWay,
  payMethods,
  // 新增：用于显示折扣明细
  amountNumber,
  discountRate,
  paymentQuote,
  paymentQuoteError,
}) => {
  const hasDiscount =
    !paymentQuote &&
    discountRate &&
    discountRate > 0 &&
    discountRate < 1 &&
    amountNumber > 0;
  const originalAmount = hasDiscount ? amountNumber / discountRate : 0;
  const discountAmount = hasDiscount ? originalAmount - amountNumber : 0;
  const payMethod = payMethods.find(
    (method) => getPaymentSelectionId(method) === payWay,
  );
  const publicMethod = getPublicPaymentMethod(payMethod);
  const paymentMethodLabel = payMethod?.public_label
    ? payMethod.public_label === 'Card'
      ? t('银行卡支付')
      : payMethod.public_label === 'Online payment'
        ? t('在线支付')
        : payMethod.public_label
    : getPublicPaymentMethodLabel(payMethod, t, payMethods);
  return (
    <Modal
      title={
        <div className='flex items-center'>
          <CreditCard className='mr-2' size={18} />
          {t('充值确认')}
        </div>
      }
      visible={open}
      onOk={onlineTopUp}
      onCancel={handleCancel}
      maskClosable={false}
      size='small'
      centered
      confirmLoading={confirmLoading}
      okButtonProps={{ disabled: !!paymentQuoteError }}
      okText={t('继续支付')}
      cancelText={t('取消')}
    >
      <div className='space-y-4'>
        <Card className='!rounded-xl !border-0 bg-slate-50 dark:bg-slate-800'>
          <div className='space-y-3'>
            <div className='flex justify-between items-center'>
              <Text strong className='text-slate-700 dark:text-slate-200'>
                {t('充值数量')}：
              </Text>
              <Text className='text-slate-900 dark:text-slate-100'>
                {renderQuotaWithAmount(topUpCount)}
              </Text>
            </div>
            <div className='flex justify-between items-center'>
              <Text strong className='text-slate-700 dark:text-slate-200'>
                {t('实付金额')}：
              </Text>
              {amountLoading ? (
                <Skeleton.Title style={{ width: '60px', height: '16px' }} />
              ) : (
                <div className='flex items-baseline space-x-2'>
                  <Text strong className='font-bold' style={{ color: 'red' }}>
                    {paymentQuote
                      ? formatPaymentDecimal(
                          paymentQuote.payable_amount,
                          paymentQuote.currency,
                          payMethod?.public_method === 'card'
                            ? 'card'
                            : undefined,
                        )
                      : renderAmount()}
                  </Text>
                  {hasDiscount && (
                    <Text size='small' className='text-rose-500'>
                      {Math.round(discountRate * 100)}%
                    </Text>
                  )}
                </div>
              )}
            </div>
            {hasDiscount && !amountLoading && (
              <>
                <div className='flex justify-between items-center'>
                  <Text className='text-slate-500 dark:text-slate-400'>
                    {t('原价')}：
                  </Text>
                  <Text delete className='text-slate-500 dark:text-slate-400'>
                    {formatPaymentDecimal(originalAmount, 'CNY')}
                  </Text>
                </div>
                <div className='flex justify-between items-center'>
                  <Text className='text-slate-500 dark:text-slate-400'>
                    {t('优惠')}：
                  </Text>
                  <Text className='text-emerald-600 dark:text-emerald-400'>
                    {formatPaymentDecimal(-discountAmount, 'CNY')}
                  </Text>
                </div>
              </>
            )}
            <div className='flex justify-between items-center'>
              <Text strong className='text-slate-700 dark:text-slate-200'>
                {t('支付方式')}：
              </Text>
              <div className='flex items-center'>
                {publicMethod === 'alipay' ? (
                  <SiAlipay className='mr-2' size={16} color='#1677FF' />
                ) : publicMethod === 'wechat_pay' ? (
                  <SiWechat className='mr-2' size={16} color='#07C160' />
                ) : (
                  <CreditCard
                    className='mr-2'
                    size={16}
                    color={payMethod?.color || 'var(--semi-color-text-2)'}
                  />
                )}
                <Text className='text-slate-900 dark:text-slate-100'>
                  {paymentMethodLabel}
                </Text>
              </div>
            </div>
          </div>
        </Card>
        {paymentQuoteError && (
          <Banner
            type='danger'
            description={paymentQuoteError}
            closeIcon={null}
          />
        )}
      </div>
    </Modal>
  );
};

export default PaymentConfirmModal;
