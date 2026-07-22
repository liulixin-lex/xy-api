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
import { useNavigate } from 'react-router-dom';
import {
  Badge,
  Button,
  Card,
  Divider,
  Select,
  Skeleton,
  Space,
  Tag,
  Tooltip,
  Typography,
} from '@douyinfe/semi-ui';
import { API, showError, renderQuota } from '../../helpers';
import { RefreshCw, Sparkles } from 'lucide-react';
import SubscriptionPurchaseModal from './modals/SubscriptionPurchaseModal';
import {
  formatSubscriptionDuration,
  formatSubscriptionResetPeriod,
} from '../../helpers/subscriptionFormat';
import {
  createPaymentRequestId,
  filterEligibleSubscriptionQuoteMethods,
  formatPaymentDecimal,
  getPaymentQuoteRoutePayload,
  getPaymentRouteId,
  getSafeUserPaymentError,
  navigateToPaymentUrl,
  normalizePaymentMethod,
  submitPaymentForm,
  filterPaymentMethodsForBrowser,
} from './payment-utils';

const { Text } = Typography;

function getRouteMethods(paymentRoutes = []) {
  return filterPaymentMethodsForBrowser(paymentRoutes).filter(
    (method) => method.checkout_mode === 'quote',
  );
}

const SubscriptionPlansCard = ({
  t,
  loading = false,
  error = '',
  onRetry,
  plans = [],
  paymentRoutes = [],
  billingPreference,
  billingPreferenceLoading = false,
  onChangeBillingPreference,
  activeSubscriptions = [],
  allSubscriptions = [],
  reloadSubscriptionSelf,
  withCard = true,
}) => {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [selectedPlan, setSelectedPlan] = useState(null);
  const [paying, setPaying] = useState(false);
  const [selectedEpayMethod, setSelectedEpayMethod] = useState('');
  const [refreshing, setRefreshing] = useState(false);
  const payingRef = useRef(false);
  const paymentAttemptRef = useRef({
    key: '',
    quote: null,
    requestId: '',
  });

  const routeMethods = useMemo(
    () => getRouteMethods(paymentRoutes),
    [paymentRoutes],
  );
  const eligibleQuoteMethods = useMemo(
    () =>
      filterEligibleSubscriptionQuoteMethods(
        routeMethods,
        selectedPlan?.plan?.external_payment_route_ids,
      ),
    [routeMethods, selectedPlan?.plan?.external_payment_route_ids],
  );
  const openBuy = (p) => {
    paymentAttemptRef.current = { key: '', quote: null, requestId: '' };
    setSelectedPlan(p);
    const eligibleMethods = filterEligibleSubscriptionQuoteMethods(
      routeMethods,
      p?.plan?.external_payment_route_ids,
    );
    setSelectedEpayMethod(
      eligibleMethods[0] ? getPaymentRouteId(eligibleMethods[0]) : '',
    );
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

  const handlePaymentStart = (start, method) => {
    const publicStart = {
      ...start,
      route_id: start.route_id || method?.route_id,
      public_method: start.public_method || method?.public_method,
      channel_alias: start.channel_alias || method?.channel_alias,
    };
    if (publicStart.trade_no) {
      closeBuy();
      navigate(`/payment/${encodeURIComponent(publicStart.trade_no)}`);
      return;
    }
    const flow =
      publicStart.flow ||
      (publicStart.qr_content
        ? 'qr'
        : publicStart.url
          ? 'hosted_redirect'
          : publicStart.action
            ? 'form_post'
            : 'pending');
    if (flow === 'hosted_redirect') {
      if (!navigateToPaymentUrl(publicStart.url)) {
        throw new Error(t('支付跳转地址不安全'));
      }
      return;
    }
    if (flow === 'form_post') {
      if (!submitPaymentForm(publicStart.action, publicStart.fields)) {
        throw new Error(t('支付跳转地址不安全'));
      }
      return;
    }
    throw new Error(t('支付服务暂时不可用，请稍后重试'));
  };

  const payGateway = async (rawMethod) => {
    if (payingRef.current || !selectedPlan?.plan?.id) return;
    const method = normalizePaymentMethod(rawMethod);
    if ((selectedPlan.plan.currency || 'USD').toUpperCase() !== 'USD') {
      showError(t('当前套餐暂不支持所选支付方式'));
      return;
    }

    const attemptKey = `${selectedPlan.plan.id}:${method.route_id}`;
    payingRef.current = true;
    setPaying(true);
    try {
      let attempt = paymentAttemptRef.current;
      const quoteExpired =
        attempt.quote?.expires_at &&
        attempt.quote.expires_at <= Date.now() / 1000;
      if (attempt.key !== attemptKey || !attempt.quote || quoteExpired) {
        const quoteResponse = await API.post(
          '/api/user/payment/quote',
          {
            order_kind: 'subscription',
            ...getPaymentQuoteRoutePayload(method),
            plan_id: selectedPlan.plan.id,
          },
          { skipErrorHandler: true },
        );

        const quote = quoteResponse.data?.data;
        if (!quoteResponse.data?.success || !quote) {
          throw new Error(t('获取金额失败'));
        }
        attempt = {
          key: attemptKey,
          quote: {
            ...quote,
            route_id: quote.route_id || method.route_id,
            public_method: quote.public_method || method.public_method,
            channel_alias: quote.channel_alias || method.channel_alias,
          },
          requestId: createPaymentRequestId(),
        };
        paymentAttemptRef.current = attempt;
      }

      const startResponse = await API.post(
        '/api/user/payment/start',
        {
          quote_id: attempt.quote.quote_id,
          request_id: attempt.requestId,
        },
        { skipErrorHandler: true },
      );
      const start = startResponse.data?.data;
      if (!startResponse.data?.success || !start) {
        throw new Error(t('支付请求失败'));
      }
      handlePaymentStart(start, method);
      paymentAttemptRef.current = { key: '', quote: null, requestId: '' };
    } catch (error) {
      showError(
        error?.response || error?.request
          ? getSafeUserPaymentError(error, t, '支付请求失败')
          : error?.message || t('支付请求失败'),
      );
    } finally {
      payingRef.current = false;
      setPaying(false);
    }
  };

  const payCreem = async () => {
    const allowedRouteIDs = new Set(
      selectedPlan?.plan?.external_payment_route_ids || [],
    );
    const productRoute = paymentRoutes.find(
      (method) =>
        method.checkout_mode === 'product' &&
        allowedRouteIDs.has(method.route_id),
    );
    if (!productRoute) {
      showError(t('该套餐暂不支持此在线支付方式'));
      return;
    }
    await payGateway(productRoute);
  };

  const payDirectCheckout = async () => {
    const allowedRouteIDs = new Set(
      selectedPlan?.plan?.external_payment_route_ids || [],
    );
    const directRoute = paymentRoutes.find(
      (method) =>
        method.checkout_mode === 'direct' &&
        allowedRouteIDs.has(method.route_id),
    );
    if (!directRoute) {
      showError(t('该套餐暂不支持此在线支付方式'));
      return;
    }
    await payGateway(directRoute);
  };

  const payEpay = async () => {
    if (!selectedEpayMethod) {
      showError(t('请选择支付方式'));
      return;
    }
    const method = eligibleQuoteMethods.find(
      (candidate) => getPaymentRouteId(candidate) === selectedEpayMethod,
    );
    if (!method) return;
    await payGateway(method);
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
    billingPreference === 'subscription_only'
      ? t('仅使用权益')
      : t('优先使用权益');

  const planPurchaseCountMap = useMemo(() => {
    const map = new Map();
    (allSubscriptions || []).forEach((sub) => {
      const planId = sub?.subscription?.plan_id;
      if (!planId) return;
      map.set(planId, (map.get(planId) || 0) + 1);
    });
    return map;
  }, [allSubscriptions]);

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
              title={t('权益信息加载失败')}
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

          {/* 当前订阅状态 */}
          <Card className='!rounded-xl w-full' bodyStyle={{ padding: '12px' }}>
            <div className='flex items-center justify-between mb-2 gap-3'>
              <div className='flex items-center gap-2 flex-1 min-w-0'>
                <Text strong>{t('我的权益')}</Text>
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
                        ? t('{{preference}}（{{status}}）', {
                            preference: t('优先使用权益'),
                            status: t('无生效'),
                          })
                        : t('优先使用权益'),
                      disabled: disableSubscriptionPreference,
                    },
                    { value: 'wallet_first', label: t('优先钱包') },
                    {
                      value: 'subscription_only',
                      label: disableSubscriptionPreference
                        ? t('{{preference}}（{{status}}）', {
                            preference: t('仅使用权益'),
                            status: t('无生效'),
                          })
                        : t('仅使用权益'),
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
                  aria-label={t('刷新权益状态')}
                />
              </div>
            </div>
            {disableSubscriptionPreference && isSubscriptionPreference && (
              <Text type='tertiary' size='small'>
                {t(
                  '已保存偏好为 {{preference}}，当前无生效权益，将自动使用钱包',
                  { preference: subscriptionPreferenceLabel },
                )}
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
                    const planTitle = subscription?.plan_title || '';
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
                                ? t('{{plan}} 权益 #{{id}}', {
                                    plan: planTitle,
                                    id: subscription?.id,
                                  })
                                : t('权益 #{{id}}', {
                                    id: subscription?.id,
                                  })}
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
                              {t('{{count}} days remaining', {
                                count: remainDays,
                              })}
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
                              content={t(
                                'Raw quota: {{used}}/{{total}}. Remaining {{remaining}}',
                                {
                                  used: usedAmount,
                                  total: totalAmount,
                                  remaining: remainAmount,
                                },
                              )}
                            >
                              <span>
                                {t(
                                  '{{used}}/{{total}}. Remaining {{remaining}}',
                                  {
                                    used: renderQuota(usedAmount),
                                    total: renderQuota(totalAmount),
                                    remaining: renderQuota(remainAmount),
                                  },
                                )}
                              </span>
                            </Tooltip>
                          ) : (
                            t('不限')
                          )}
                          {totalAmount > 0 && (
                            <span className='ml-2'>
                              {t('Used {{percent}}%', {
                                percent: usagePercent,
                              })}
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
                const limitLabel =
                  limit > 0
                    ? t('Purchase limit: {{count}}', { count: limit })
                    : null;
                const totalLabel =
                  totalAmount > 0
                    ? t('Total quota: {{value}}', {
                        value: renderQuota(totalAmount),
                      })
                    : t('Total quota: {{value}}', { value: t('不限') });
                const upgradeLabel = plan?.includes_expanded_access
                  ? t('Includes expanded model access')
                  : null;
                const resetLabel =
                  formatSubscriptionResetPeriod(plan, t) === t('不重置')
                    ? null
                    : t('Quota reset: {{value}}', {
                        value: formatSubscriptionResetPeriod(plan, t),
                      });
                const planBenefits = [
                  {
                    label: t('Validity period: {{value}}', {
                      value: formatSubscriptionDuration(plan, t),
                    }),
                  },
                  resetLabel ? { label: resetLabel } : null,
                  totalAmount > 0
                    ? {
                        label: totalLabel,
                        tooltip: t('Raw quota: {{amount}}', {
                          amount: totalAmount,
                        }),
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
                          {plan?.title || t('权益套餐')}
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
                            ? t(
                                'Purchase limit reached ({{count}}/{{limit}})',
                                { count, limit },
                              )
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
                              {reached ? t('已达上限') : t('立即购买')}
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

      {/* 购买确认弹窗 */}
      <SubscriptionPurchaseModal
        t={t}
        visible={open}
        onCancel={closeBuy}
        selectedPlan={selectedPlan}
        paying={paying}
        selectedEpayMethod={selectedEpayMethod}
        setSelectedEpayMethod={handleEpayMethodChange}
        paymentRoutes={paymentRoutes}
        quoteMethods={eligibleQuoteMethods}
        purchaseLimitInfo={
          selectedPlan?.plan?.id
            ? {
                limit: Number(selectedPlan?.plan?.max_purchase_per_user || 0),
                count: getPlanPurchaseCount(selectedPlan?.plan?.id),
              }
            : null
        }
        onPayProductCheckout={payCreem}
        onPayDirectCheckout={payDirectCheckout}
        onPayQuote={payEpay}
      />
    </>
  );
};

export default SubscriptionPlansCard;
