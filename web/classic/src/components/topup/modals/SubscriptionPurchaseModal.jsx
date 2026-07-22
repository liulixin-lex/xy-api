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
import {
  Banner,
  Modal,
  Typography,
  Card,
  Button,
  Select,
  Divider,
  Tooltip,
} from '@douyinfe/semi-ui';
import { Crown, CalendarClock, Package } from 'lucide-react';
import { IconCreditCard } from '@douyinfe/semi-icons';
import { renderQuota } from '../../../helpers';
import {
  formatPaymentDecimal,
  getPaymentRouteId,
  getPublicPaymentMethodLabel,
} from '../payment-utils';
import {
  formatSubscriptionDuration,
  formatSubscriptionResetPeriod,
} from '../../../helpers/subscriptionFormat';

const { Text } = Typography;

const SubscriptionPurchaseModal = ({
  t,
  visible,
  onCancel,
  selectedPlan,
  paying,
  selectedEpayMethod,
  setSelectedEpayMethod,
  paymentRoutes = [],
  quoteMethods = [],
  purchaseLimitInfo = null,
  onPayProductCheckout,
  onPayDirectCheckout,
  onPayQuote,
}) => {
  const plan = selectedPlan?.plan;
  const totalAmount = Number(plan?.total_amount || 0);
  const price = plan ? Number(plan.price_amount || 0) : 0;
  const displayPrice = formatPaymentDecimal(price, plan?.currency || 'USD');
  const planCurrency = (plan?.currency || 'USD').toUpperCase();
  const supportsUnifiedPayment = planCurrency === 'USD';
  const externalRouteIDs = new Set(plan?.external_payment_route_ids || []);
  const hasProductCheckout = paymentRoutes.some(
    (route) =>
      route.checkout_mode === 'product' && externalRouteIDs.has(route.route_id),
  );
  const hasDirectCheckout = paymentRoutes.some(
    (route) =>
      route.checkout_mode === 'direct' && externalRouteIDs.has(route.route_id),
  );
  const hasQuote = supportsUnifiedPayment && quoteMethods.length > 0;
  const hasAnyPayment = hasProductCheckout || hasDirectCheckout || hasQuote;
  const purchaseLimit = Number(purchaseLimitInfo?.limit || 0);
  const purchaseCount = Number(purchaseLimitInfo?.count || 0);
  const purchaseLimitReached =
    purchaseLimit > 0 && purchaseCount >= purchaseLimit;

  return (
    <Modal
      title={
        <div className='flex items-center'>
          <Crown className='mr-2' size={18} />
          {t('购买固定期限权益')}
        </div>
      }
      visible={visible}
      onCancel={onCancel}
      footer={null}
      size='small'
      centered
    >
      {plan ? (
        <div className='space-y-4 pb-10'>
          {/* 套餐信息 */}
          <Card className='!rounded-xl !border-0 bg-slate-50 dark:bg-slate-800'>
            <div className='space-y-3'>
              <div className='flex justify-between items-center'>
                <Text strong className='text-slate-700 dark:text-slate-200'>
                  {t('套餐名称')}：
                </Text>
                <Typography.Text
                  ellipsis={{ rows: 1, showTooltip: true }}
                  className='text-slate-900 dark:text-slate-100'
                  style={{ maxWidth: 200 }}
                >
                  {plan.title}
                </Typography.Text>
              </div>
              <div className='flex justify-between items-center'>
                <Text strong className='text-slate-700 dark:text-slate-200'>
                  {t('有效期')}：
                </Text>
                <div className='flex items-center'>
                  <CalendarClock size={14} className='mr-1 text-slate-500' />
                  <Text className='text-slate-900 dark:text-slate-100'>
                    {formatSubscriptionDuration(plan, t)}
                  </Text>
                </div>
              </div>
              {formatSubscriptionResetPeriod(plan, t) !== t('不重置') && (
                <div className='flex justify-between items-center'>
                  <Text strong className='text-slate-700 dark:text-slate-200'>
                    {t('重置周期')}：
                  </Text>
                  <Text className='text-slate-900 dark:text-slate-100'>
                    {formatSubscriptionResetPeriod(plan, t)}
                  </Text>
                </div>
              )}
              <div className='flex justify-between items-center'>
                <Text strong className='text-slate-700 dark:text-slate-200'>
                  {t('总额度')}：
                </Text>
                <div className='flex items-center'>
                  <Package size={14} className='mr-1 text-slate-500' />
                  {totalAmount > 0 ? (
                    <Tooltip
                      content={t('Raw quota: {{amount}}', {
                        amount: totalAmount,
                      })}
                    >
                      <Text className='text-slate-900 dark:text-slate-100'>
                        {renderQuota(totalAmount)}
                      </Text>
                    </Tooltip>
                  ) : (
                    <Text className='text-slate-900 dark:text-slate-100'>
                      {t('不限')}
                    </Text>
                  )}
                </div>
              </div>
              {plan?.includes_expanded_access ? (
                <Text className='text-slate-900 dark:text-slate-100'>
                  {t('Includes expanded model access')}
                </Text>
              ) : null}
              <Divider margin={8} />
              <div className='flex justify-between items-center'>
                <Text strong className='text-slate-700 dark:text-slate-200'>
                  {t('应付金额')}：
                </Text>
                <Text strong className='text-xl text-purple-600'>
                  {displayPrice}
                </Text>
              </div>
            </div>
          </Card>

          {/* 支付方式 */}
          {purchaseLimitReached && (
            <Banner
              type='warning'
              description={t('Purchase limit reached ({{count}}/{{limit}})', {
                count: purchaseCount,
                limit: purchaseLimit,
              })}
              className='!rounded-xl'
              closeIcon={null}
            />
          )}

          <Banner
            type='info'
            description={t(
              '本次购买为一次性付款，将获得固定期限权益，不会自动续费。',
            )}
            className='!rounded-xl'
            closeIcon={null}
          />

          {!supportsUnifiedPayment && (
            <Banner
              type='warning'
              description={t(
                '当前套餐使用 {{currency}} 定价，部分在线支付方式暂不可用。',
                { currency: planCurrency },
              )}
              className='!rounded-xl'
              closeIcon={null}
            />
          )}

          {hasAnyPayment ? (
            <div className='space-y-3'>
              <Text size='small' type='tertiary'>
                {t('选择支付方式')}：
              </Text>

              {(hasProductCheckout || hasDirectCheckout) && (
                <div className='flex gap-2'>
                  {hasProductCheckout && (
                    <Button
                      theme='light'
                      className='flex-1'
                      icon={<IconCreditCard />}
                      onClick={onPayProductCheckout}
                      loading={paying}
                      disabled={purchaseLimitReached || paying}
                    >
                      {t('在线支付')}
                    </Button>
                  )}
                  {hasDirectCheckout && (
                    <Button
                      theme='light'
                      className='flex-1'
                      icon={<IconCreditCard />}
                      onClick={onPayDirectCheckout}
                      loading={paying}
                      disabled={purchaseLimitReached || paying}
                    >
                      {t('在线支付')}
                    </Button>
                  )}
                </div>
              )}

              {hasQuote && (
                <div className='flex gap-2'>
                  <Select
                    value={selectedEpayMethod}
                    onChange={setSelectedEpayMethod}
                    style={{ flex: 1 }}
                    size='default'
                    placeholder={t('选择支付方式')}
                    optionList={quoteMethods.map((m) => ({
                      value: getPaymentRouteId(m),
                      label: getPublicPaymentMethodLabel(m, t, quoteMethods),
                    }))}
                    disabled={purchaseLimitReached || paying}
                  />
                  <Button
                    theme='solid'
                    type='primary'
                    onClick={onPayQuote}
                    loading={paying}
                    disabled={
                      !selectedEpayMethod || purchaseLimitReached || paying
                    }
                  >
                    {t('支付')}
                  </Button>
                </div>
              )}
            </div>
          ) : (
            <Banner
              type='info'
              description={
                supportsUnifiedPayment
                  ? t('管理员未开启在线支付功能，请联系管理员配置。')
                  : t('当前套餐没有可用的兼容支付方式，请联系管理员。')
              }
              className='!rounded-xl'
              closeIcon={null}
            />
          )}
        </div>
      ) : null}
    </Modal>
  );
};

export default SubscriptionPurchaseModal;
