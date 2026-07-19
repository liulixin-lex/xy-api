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

import React, { useMemo, useRef, useState } from 'react';
import {
  Badge,
  Button,
  Card,
  Divider,
  Modal,
  Select,
  Skeleton,
  Space,
  Tag,
  Tooltip,
  Typography,
} from '@douyinfe/semi-ui';
import { QRCodeSVG } from 'qrcode.react';
import { API, showError, renderQuota } from '../../helpers';
import { RefreshCw, Sparkles } from 'lucide-react';
import SubscriptionPurchaseModal from './modals/SubscriptionPurchaseModal';
import PaymentOrderTracker from './PaymentOrderTracker';
import { usePaymentOrderPolling } from './use-payment-order';
import {
  formatSubscriptionDuration,
  formatSubscriptionResetPeriod,
} from '../../helpers/subscriptionFormat';
import {
  createPaymentRequestId,
  formatPaymentDecimal,
  inferPaymentProvider,
  isEndpointUnavailable,
  isSafeQrContent,
  navigateToPaymentUrl,
  submitPaymentForm,
} from './payment-utils';

const { Text } = Typography;

// 过滤易支付方式
function getEpayMethods(payMethods = [], enableEpay = false) {
  return (payMethods || []).filter((method) => {
    if (!method?.type) return false;
    const provider = inferPaymentProvider(method.type, method.provider);
    return provider === 'xorpay' || (provider === 'epay' && enableEpay);
  });
}

const SubscriptionPlansCard = ({
  t,
  loading = false,
  error = '',
  onRetry,
  plans = [],
  payMethods = [],
  enableOnlineTopUp = false,
  enableStripeTopUp = false,
  enableCreemTopUp = false,
  billingPreference,
  billingPreferenceLoading = false,
  onChangeBillingPreference,
  activeSubscriptions = [],
  allSubscriptions = [],
  reloadSubscriptionSelf,
  reloadUserQuota,
  withCard = true,
}) => {
  const [open, setOpen] = useState(false);
  const [selectedPlan, setSelectedPlan] = useState(null);
  const [paying, setPaying] = useState(false);
  const [selectedEpayMethod, setSelectedEpayMethod] = useState('');
  const [refreshing, setRefreshing] = useState(false);
  const [qrPayment, setQrPayment] = useState(null);
  const [qrQuote, setQrQuote] = useState(null);
  const payingRef = useRef(false);
  const paymentAttemptRef = useRef({
    key: '',
    quote: null,
    requestId: '',
  });

  const {
    payment: pendingPayment,
    order: pendingPaymentOrder,
    error: pendingPaymentError,
    polling: pendingPaymentPolling,
    refreshing: pendingPaymentRefreshing,
    trackPayment,
    refreshPayment: refreshPendingPayment,
    clearPayment: clearPendingPayment,
  } = usePaymentOrderPolling({
    t,
    onSuccess: async () => {
      await Promise.all([reloadSubscriptionSelf?.(), reloadUserQuota?.()]);
    },
  });

  const epayMethods = useMemo(
    () => getEpayMethods(payMethods, enableOnlineTopUp),
    [enableOnlineTopUp, payMethods],
  );
  const stripeCurrency = useMemo(
    () =>
      payMethods
        .find(
          (method) =>
            inferPaymentProvider(method?.type, method?.provider) === 'stripe',
        )
        ?.currency?.toUpperCase(),
    [payMethods],
  );
  const stripeSubscriptionEnabled =
    enableStripeTopUp && (!stripeCurrency || stripeCurrency === 'USD');

  const openBuy = (p) => {
    paymentAttemptRef.current = { key: '', quote: null, requestId: '' };
    setSelectedPlan(p);
    setSelectedEpayMethod(epayMethods?.[0]?.type || '');
    setOpen(true);
  };

  const closeBuy = () => {
    setOpen(false);
    setSelectedPlan(null);
    setPaying(false);
    payingRef.current = false;
  };

  const handleEpayMethodChange = (value) => {
    if (payingRef.current) return;
    paymentAttemptRef.current = { key: '', quote: null, requestId: '' };
    setSelectedEpayMethod(value);
  };

  const handleRefresh = async () => {
    setRefreshing(true);
    try {
      await reloadSubscriptionSelf?.();
    } finally {
      setRefreshing(false);
    }
  };

  const handlePaymentStart = (start, quote) => {
    if (start.flow === 'qr') {
      if (!isSafeQrContent(start.qr_content)) {
        throw new Error(t('支付二维码无效'));
      }
      setQrPayment(start);
      setQrQuote(quote);
      trackPayment(start, 'pending');
      closeBuy();
      return;
    }
    if (start.flow === 'pending') {
      setQrQuote(quote);
      trackPayment(start, 'processing');
      closeBuy();
      return;
    }
    if (start.flow === 'hosted_redirect') {
      if (!navigateToPaymentUrl(start.url)) {
        throw new Error(t('支付跳转地址不安全'));
      }
      return;
    }
    if (start.flow === 'form_post') {
      if (!submitPaymentForm(start.action, start.fields)) {
        throw new Error(t('支付跳转地址不安全'));
      }
      return;
    }
    throw new Error(t('支付网关返回了不支持的跳转方式'));
  };

  const startLegacyGateway = async (provider, paymentMethod) => {
    if (provider === 'stripe') {
      const response = await API.post('/api/subscription/stripe/pay', {
        plan_id: selectedPlan.plan.id,
      });
      if (
        (!response.data?.success && response.data?.message !== 'success') ||
        !response.data?.data?.pay_link
      ) {
        throw new Error(response.data?.message || t('支付失败'));
      }
      handlePaymentStart({
        flow: 'hosted_redirect',
        url: response.data.data.pay_link,
      });
      return;
    }
    if (provider !== 'epay') {
      throw new Error(t('当前支付网关需要新版统一支付接口'));
    }
    const response = await API.post('/api/subscription/epay/pay', {
      plan_id: selectedPlan.plan.id,
      payment_method: paymentMethod,
    });
    if (
      (!response.data?.success && response.data?.message !== 'success') ||
      !response.data?.url
    ) {
      throw new Error(response.data?.message || t('支付失败'));
    }
    handlePaymentStart({
      flow: 'form_post',
      action: response.data.url,
      fields: response.data.data || {},
    });
  };

  const payGateway = async (provider, paymentMethod) => {
    if (payingRef.current || !selectedPlan?.plan?.id) return;
    if ((selectedPlan.plan.currency || 'USD').toUpperCase() !== 'USD') {
      showError(t('Stripe、易支付和 XORPay 统一支付仅支持 USD 定价的订阅套餐'));
      return;
    }

    const attemptKey = `${selectedPlan.plan.id}:${provider}:${paymentMethod}`;
    payingRef.current = true;
    setPaying(true);
    try {
      let attempt = paymentAttemptRef.current;
      const quoteExpired =
        attempt.quote?.expires_at &&
        attempt.quote.expires_at <= Date.now() / 1000;
      if (attempt.key !== attemptKey || !attempt.quote || quoteExpired) {
        let quoteResponse;
        try {
          quoteResponse = await API.post('/api/user/payment/quote', {
            order_kind: 'subscription',
            provider,
            payment_method: paymentMethod,
            plan_id: selectedPlan.plan.id,
          });
        } catch (error) {
          if (isEndpointUnavailable(error)) {
            paymentAttemptRef.current = {
              key: '',
              quote: null,
              requestId: '',
            };
            await startLegacyGateway(provider, paymentMethod);
            return;
          }
          throw error;
        }

        const quote = quoteResponse.data?.data;
        if (!quoteResponse.data?.success || !quote) {
          throw new Error(quoteResponse.data?.message || t('获取金额失败'));
        }
        attempt = {
          key: attemptKey,
          quote,
          requestId: createPaymentRequestId(),
        };
        paymentAttemptRef.current = attempt;
      }

      const startResponse = await API.post('/api/user/payment/start', {
        quote_id: attempt.quote.quote_id,
        request_id: attempt.requestId,
      });
      const start = startResponse.data?.data;
      if (!startResponse.data?.success || !start) {
        throw new Error(startResponse.data?.message || t('支付请求失败'));
      }
      handlePaymentStart(start, attempt.quote);
      paymentAttemptRef.current = { key: '', quote: null, requestId: '' };
    } catch (error) {
      showError(error?.message || t('支付请求失败'));
    } finally {
      payingRef.current = false;
      setPaying(false);
    }
  };

  const payStripe = async () => payGateway('stripe', 'stripe');

  const payCreem = async () => {
    if (!selectedPlan?.plan?.creem_product_id) {
      showError(t('该套餐未配置 Creem'));
      return;
    }
    if (payingRef.current) return;
    payingRef.current = true;
    setPaying(true);
    try {
      const res = await API.post('/api/subscription/creem/pay', {
        plan_id: selectedPlan.plan.id,
      });
      if (
        (res.data?.success || res.data?.message === 'success') &&
        res.data.data?.checkout_url
      ) {
        if (!navigateToPaymentUrl(res.data.data.checkout_url)) {
          throw new Error(t('支付跳转地址不安全'));
        }
      } else {
        const errorMsg =
          typeof res.data?.data === 'string'
            ? res.data.data
            : res.data?.message || t('支付失败');
        showError(errorMsg);
      }
    } catch (e) {
      showError(t('支付请求失败'));
    } finally {
      payingRef.current = false;
      setPaying(false);
    }
  };

  const payEpay = async () => {
    if (!selectedEpayMethod) {
      showError(t('请选择支付方式'));
      return;
    }
    const method = epayMethods.find(
      (candidate) => candidate.type === selectedEpayMethod,
    );
    if (!method) return;
    const provider = inferPaymentProvider(method.type, method.provider);
    await payGateway(provider, method.type);
  };

  // 当前订阅信息 - 支持多个订阅
  const hasActiveSubscription = activeSubscriptions.length > 0;
  const hasAnySubscription = allSubscriptions.length > 0;
  const disableSubscriptionPreference = !hasActiveSubscription;
  const isSubscriptionPreference =
    billingPreference === 'subscription_first' ||
    billingPreference === 'subscription_only';
  const displayBillingPreference =
    disableSubscriptionPreference && isSubscriptionPreference
      ? 'wallet_first'
      : billingPreference;
  const subscriptionPreferenceLabel =
    billingPreference === 'subscription_only' ? t('仅用订阅') : t('优先订阅');

  const planPurchaseCountMap = useMemo(() => {
    const map = new Map();
    (allSubscriptions || []).forEach((sub) => {
      const planId = sub?.subscription?.plan_id;
      if (!planId) return;
      map.set(planId, (map.get(planId) || 0) + 1);
    });
    return map;
  }, [allSubscriptions]);

  const planTitleMap = useMemo(() => {
    const map = new Map();
    (plans || []).forEach((p) => {
      const plan = p?.plan;
      if (!plan?.id) return;
      map.set(plan.id, plan.title || '');
    });
    return map;
  }, [plans]);

  const getPlanPurchaseCount = (planId) =>
    planPurchaseCountMap.get(planId) || 0;

  // 计算单个订阅的剩余天数
  const getRemainingDays = (sub) => {
    if (!sub?.subscription?.end_time) return 0;
    const now = Date.now() / 1000;
    const remaining = sub.subscription.end_time - now;
    return Math.max(0, Math.ceil(remaining / 86400));
  };

  // 计算单个订阅的使用进度
  const getUsagePercent = (sub) => {
    const total = Number(sub?.subscription?.amount_total || 0);
    const used = Number(sub?.subscription?.amount_used || 0);
    if (total <= 0) return 0;
    return Math.round((used / total) * 100);
  };

  const cardContent = (
    <>
      {/* 卡片头部 */}
      {loading ? (
        <div className='space-y-4'>
          {/* 我的订阅骨架屏 */}
          <Card className='!rounded-xl w-full' bodyStyle={{ padding: '12px' }}>
            <div className='flex items-center justify-between mb-3'>
              <Skeleton.Title style={{ width: 100, height: 20 }} />
              <Skeleton.Button style={{ width: 24, height: 24 }} />
            </div>
            <div className='space-y-2'>
              <Skeleton.Paragraph rows={2} />
            </div>
          </Card>
          {/* 套餐列表骨架屏 */}
          <div className='grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-2 xl:grid-cols-3 gap-5 w-full px-1'>
            {[1, 2, 3].map((i) => (
              <Card
                key={i}
                className='!rounded-xl w-full h-full'
                bodyStyle={{ padding: 16 }}
              >
                <Skeleton.Title
                  style={{ width: '60%', height: 24, marginBottom: 8 }}
                />
                <Skeleton.Paragraph rows={1} style={{ marginBottom: 12 }} />
                <div className='text-center py-4'>
                  <Skeleton.Title
                    style={{ width: '40%', height: 32, margin: '0 auto' }}
                  />
                </div>
                <Skeleton.Paragraph rows={3} style={{ marginTop: 12 }} />
                <Skeleton.Button
                  style={{ marginTop: 16, width: '100%', height: 32 }}
                />
              </Card>
            ))}
          </div>
        </div>
      ) : (
        <Space vertical style={{ width: '100%' }} spacing={8}>
          {error && (
            <Banner
              type='danger'
              title={t('订阅信息加载失败')}
              description={
                <div className='flex flex-col items-start gap-2'>
                  <span>{error}</span>
                  <Button size='small' theme='outline' onClick={onRetry}>
                    {t('重新加载')}
                  </Button>
                </div>
              }
              closeIcon={null}
            />
          )}

          <PaymentOrderTracker
            t={t}
            payment={pendingPayment}
            order={pendingPaymentOrder}
            error={pendingPaymentError}
            polling={pendingPaymentPolling}
            refreshing={pendingPaymentRefreshing}
            onRefresh={refreshPendingPayment}
            onDismiss={clearPendingPayment}
          />

          {/* 当前订阅状态 */}
          <Card className='!rounded-xl w-full' bodyStyle={{ padding: '12px' }}>
            <div className='flex items-center justify-between mb-2 gap-3'>
              <div className='flex items-center gap-2 flex-1 min-w-0'>
                <Text strong>{t('我的订阅')}</Text>
                {hasActiveSubscription ? (
                  <Tag
                    color='white'
                    size='small'
                    shape='circle'
                    prefixIcon={<Badge dot type='success' />}
                  >
                    {activeSubscriptions.length} {t('个生效中')}
                  </Tag>
                ) : (
                  <Tag color='white' size='small' shape='circle'>
                    {t('无生效')}
                  </Tag>
                )}
                {allSubscriptions.length > activeSubscriptions.length && (
                  <Tag color='white' size='small' shape='circle'>
                    {allSubscriptions.length - activeSubscriptions.length}{' '}
                    {t('个已过期')}
                  </Tag>
                )}
              </div>
              <div className='flex items-center gap-2'>
                <Select
                  value={displayBillingPreference}
                  onChange={onChangeBillingPreference}
                  disabled={billingPreferenceLoading}
                  loading={billingPreferenceLoading}
                  size='small'
                  optionList={[
                    {
                      value: 'subscription_first',
                      label: disableSubscriptionPreference
                        ? `${t('优先订阅')} (${t('无生效')})`
                        : t('优先订阅'),
                      disabled: disableSubscriptionPreference,
                    },
                    { value: 'wallet_first', label: t('优先钱包') },
                    {
                      value: 'subscription_only',
                      label: disableSubscriptionPreference
                        ? `${t('仅用订阅')} (${t('无生效')})`
                        : t('仅用订阅'),
                      disabled: disableSubscriptionPreference,
                    },
                    { value: 'wallet_only', label: t('仅用钱包') },
                  ]}
                />
                <Button
                  size='small'
                  theme='light'
                  type='tertiary'
                  icon={
                    <RefreshCw
                      size={12}
                      className={refreshing ? 'animate-spin' : ''}
                    />
                  }
                  onClick={handleRefresh}
                  loading={refreshing}
                  disabled={refreshing}
                  aria-label={t('刷新订阅状态')}
                />
              </div>
            </div>
            {disableSubscriptionPreference && isSubscriptionPreference && (
              <Text type='tertiary' size='small'>
                {t('已保存偏好为')}
                {subscriptionPreferenceLabel}
                {t('，当前无生效订阅，将自动使用钱包')}
              </Text>
            )}

            {hasAnySubscription ? (
              <>
                <Divider margin={8} />
                <div className='max-h-64 overflow-y-auto pr-1 semi-table-body'>
                  {allSubscriptions.map((sub, subIndex) => {
                    const isLast = subIndex === allSubscriptions.length - 1;
                    const subscription = sub.subscription;
                    const totalAmount = Number(subscription?.amount_total || 0);
                    const usedAmount = Number(subscription?.amount_used || 0);
                    const remainAmount =
                      totalAmount > 0
                        ? Math.max(0, totalAmount - usedAmount)
                        : 0;
                    const planTitle =
                      planTitleMap.get(subscription?.plan_id) || '';
                    const remainDays = getRemainingDays(sub);
                    const usagePercent = getUsagePercent(sub);
                    const now = Date.now() / 1000;
                    const isExpired = (subscription?.end_time || 0) < now;
                    const isCancelled = subscription?.status === 'cancelled';
                    const isActive =
                      subscription?.status === 'active' && !isExpired;

                    return (
                      <div key={subscription?.id || subIndex}>
                        {/* 订阅概要 */}
                        <div className='flex items-center justify-between text-xs mb-2'>
                          <div className='flex items-center gap-2'>
                            <span className='font-medium'>
                              {planTitle
                                ? `${planTitle} · ${t('订阅')} #${subscription?.id}`
                                : `${t('订阅')} #${subscription?.id}`}
                            </span>
                            {isActive ? (
                              <Tag
                                color='white'
                                size='small'
                                shape='circle'
                                prefixIcon={<Badge dot type='success' />}
                              >
                                {t('生效')}
                              </Tag>
                            ) : isCancelled ? (
                              <Tag color='white' size='small' shape='circle'>
                                {t('已作废')}
                              </Tag>
                            ) : (
                              <Tag color='white' size='small' shape='circle'>
                                {t('已过期')}
                              </Tag>
                            )}
                          </div>
                          {isActive && (
                            <span className='text-gray-500'>
                              {t('剩余')} {remainDays} {t('天')}
                            </span>
                          )}
                        </div>
                        <div className='text-xs text-gray-500 mb-2'>
                          {isActive
                            ? t('至')
                            : isCancelled
                              ? t('作废于')
                              : t('过期于')}{' '}
                          {new Date(
                            (subscription?.end_time || 0) * 1000,
                          ).toLocaleString()}
                        </div>
                        {isActive && subscription?.next_reset_time > 0 && (
                          <div className='text-xs text-gray-500 mb-2'>
                            {t('下一次重置')}:{' '}
                            {new Date(
                              subscription.next_reset_time * 1000,
                            ).toLocaleString()}
                          </div>
                        )}
                        <div className='text-xs text-gray-500 mb-2'>
                          {t('总额度')}:{' '}
                          {totalAmount > 0 ? (
                            <Tooltip
                              content={`${t('原生额度')}：${usedAmount}/${totalAmount} · ${t('剩余')} ${remainAmount}`}
                            >
                              <span>
                                {renderQuota(usedAmount)}/
                                {renderQuota(totalAmount)} · {t('剩余')}{' '}
                                {renderQuota(remainAmount)}
                              </span>
                            </Tooltip>
                          ) : (
                            t('不限')
                          )}
                          {totalAmount > 0 && (
                            <span className='ml-2'>
                              {t('已用')} {usagePercent}%
                            </span>
                          )}
                        </div>
                        {!isLast && <Divider margin={12} />}
                      </div>
                    );
                  })}
                </div>
              </>
            ) : (
              <div className='text-xs text-gray-500'>
                {t('购买套餐后即可享受模型权益')}
              </div>
            )}
          </Card>

          {/* 可购买套餐 - 标准定价卡片 */}
          {plans.length > 0 ? (
            <div className='grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-2 xl:grid-cols-3 gap-5 w-full px-1'>
              {plans.map((p, index) => {
                const plan = p?.plan;
                const totalAmount = Number(plan?.total_amount || 0);
                const price = Number(plan?.price_amount || 0);
                const displayPrice = formatPaymentDecimal(
                  price,
                  plan?.currency || 'USD',
                );
                const isPopular = index === 0 && plans.length > 1;
                const limit = Number(plan?.max_purchase_per_user || 0);
                const limitLabel = limit > 0 ? `${t('限购')} ${limit}` : null;
                const totalLabel =
                  totalAmount > 0
                    ? `${t('总额度')}: ${renderQuota(totalAmount)}`
                    : `${t('总额度')}: ${t('不限')}`;
                const upgradeLabel = plan?.upgrade_group
                  ? `${t('升级分组')}: ${plan.upgrade_group}`
                  : null;
                const resetLabel =
                  formatSubscriptionResetPeriod(plan, t) === t('不重置')
                    ? null
                    : `${t('额度重置')}: ${formatSubscriptionResetPeriod(plan, t)}`;
                const planBenefits = [
                  {
                    label: `${t('有效期')}: ${formatSubscriptionDuration(plan, t)}`,
                  },
                  resetLabel ? { label: resetLabel } : null,
                  totalAmount > 0
                    ? {
                        label: totalLabel,
                        tooltip: `${t('原生额度')}：${totalAmount}`,
                      }
                    : { label: totalLabel },
                  limitLabel ? { label: limitLabel } : null,
                  upgradeLabel ? { label: upgradeLabel } : null,
                ].filter(Boolean);

                return (
                  <Card
                    key={plan?.id}
                    className={`!rounded-xl transition-all hover:shadow-lg w-full h-full ${
                      isPopular ? 'ring-2 ring-purple-500' : ''
                    }`}
                    bodyStyle={{ padding: 0 }}
                  >
                    <div className='p-4 h-full flex flex-col'>
                      {/* 推荐标签 */}
                      {isPopular && (
                        <div className='mb-2'>
                          <Tag color='purple' shape='circle' size='small'>
                            <Sparkles size={10} className='mr-1' />
                            {t('推荐')}
                          </Tag>
                        </div>
                      )}
                      {/* 套餐名称 */}
                      <div className='mb-3'>
                        <Typography.Title
                          heading={5}
                          ellipsis={{ rows: 1, showTooltip: true }}
                          style={{ margin: 0 }}
                        >
                          {plan?.title || t('订阅套餐')}
                        </Typography.Title>
                        {plan?.subtitle && (
                          <Text
                            type='tertiary'
                            size='small'
                            ellipsis={{ rows: 1, showTooltip: true }}
                            style={{ display: 'block' }}
                          >
                            {plan.subtitle}
                          </Text>
                        )}
                      </div>

                      {/* 价格区域 */}
                      <div className='py-2'>
                        <div className='flex items-baseline justify-start'>
                          <span className='text-3xl font-bold text-purple-600'>
                            {displayPrice}
                          </span>
                        </div>
                      </div>

                      {/* 套餐权益描述 */}
                      <div className='flex flex-col items-start gap-1 pb-2'>
                        {planBenefits.map((item) => {
                          const content = (
                            <div className='flex items-center gap-2 text-xs text-gray-500'>
                              <Badge dot type='tertiary' />
                              <span>{item.label}</span>
                            </div>
                          );
                          if (!item.tooltip) {
                            return (
                              <div
                                key={item.label}
                                className='w-full flex justify-start'
                              >
                                {content}
                              </div>
                            );
                          }
                          return (
                            <Tooltip key={item.label} content={item.tooltip}>
                              <div className='w-full flex justify-start'>
                                {content}
                              </div>
                            </Tooltip>
                          );
                        })}
                      </div>

                      <div className='mt-auto'>
                        <Divider margin={12} />

                        {/* 购买按钮 */}
                        {(() => {
                          const count = getPlanPurchaseCount(p?.plan?.id);
                          const reached = limit > 0 && count >= limit;
                          const tip = reached
                            ? t('已达到购买上限') + ` (${count}/${limit})`
                            : '';
                          const buttonEl = (
                            <Button
                              theme='outline'
                              type='primary'
                              block
                              disabled={reached}
                              onClick={() => {
                                if (!reached) openBuy(p);
                              }}
                            >
                              {reached ? t('已达上限') : t('立即订阅')}
                            </Button>
                          );
                          return reached ? (
                            <Tooltip content={tip} position='top'>
                              {buttonEl}
                            </Tooltip>
                          ) : (
                            buttonEl
                          );
                        })()}
                      </div>
                    </div>
                  </Card>
                );
              })}
            </div>
          ) : (
            <div className='text-center text-gray-400 text-sm py-4'>
              {t('暂无可购买套餐')}
            </div>
          )}
        </Space>
      )}
    </>
  );

  return (
    <>
      {withCard ? (
        <Card className='!rounded-2xl shadow-sm border-0'>{cardContent}</Card>
      ) : (
        <div className='space-y-3'>{cardContent}</div>
      )}

      <Modal
        title={t('扫码支付')}
        visible={!!qrPayment}
        onCancel={() => {
          setQrPayment(null);
          setQrQuote(null);
        }}
        footer={null}
        size='small'
        centered
      >
        <div className='flex flex-col items-center gap-4 py-3'>
          {qrPayment && isSafeQrContent(qrPayment.qr_content) ? (
            <div
              className='rounded-xl bg-white p-4'
              role='img'
              aria-label={t('支付二维码')}
            >
              <QRCodeSVG value={qrPayment.qr_content} size={220} level='M' />
            </div>
          ) : (
            <Text type='danger'>{t('支付二维码无效')}</Text>
          )}
          {qrQuote && (
            <Text strong>
              {formatPaymentDecimal(
                qrQuote.payable_amount,
                qrQuote.currency,
                qrQuote.provider,
              )}
            </Text>
          )}
          <Text type='tertiary'>
            {t('请使用对应的支付应用扫码，到账状态以服务器确认为准')}
          </Text>
          <Text>
            {t('支付状态')}:{' '}
            {t(
              pendingPayment?.trade_no === qrPayment?.trade_no
                ? pendingPaymentOrder?.status || 'pending'
                : 'pending',
            )}
          </Text>
        </div>
      </Modal>

      {/* 购买确认弹窗 */}
      <SubscriptionPurchaseModal
        t={t}
        visible={open}
        onCancel={closeBuy}
        selectedPlan={selectedPlan}
        paying={paying}
        selectedEpayMethod={selectedEpayMethod}
        setSelectedEpayMethod={handleEpayMethodChange}
        epayMethods={epayMethods}
        enableStripeTopUp={stripeSubscriptionEnabled}
        enableCreemTopUp={enableCreemTopUp}
        purchaseLimitInfo={
          selectedPlan?.plan?.id
            ? {
                limit: Number(selectedPlan?.plan?.max_purchase_per_user || 0),
                count: getPlanPurchaseCount(selectedPlan?.plan?.id),
              }
            : null
        }
        onPayStripe={payStripe}
        onPayCreem={payCreem}
        onPayEpay={payEpay}
      />
    </>
  );
};

export default SubscriptionPlansCard;
