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

import React, { useEffect, useState, useContext, useRef } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
  API,
  showError,
  showInfo,
  showSuccess,
  renderQuota,
  renderQuotaWithAmount,
  copy,
  getQuotaPerUnit,
} from '../../helpers';
import { Button, Modal, Typography } from '@douyinfe/semi-ui';
import { useTranslation } from 'react-i18next';
import { UserContext } from '../../context/User';
import { StatusContext } from '../../context/Status';
import {
  normalizePublicSubscriptionPlans,
  normalizePublicSubscriptionSelf,
} from '../../helpers/subscription-public';

import RechargeCard from './RechargeCard';
import InvitationCard from './InvitationCard';
import TransferModal from './modals/TransferModal';
import PaymentConfirmModal from './modals/PaymentConfirmModal';
import TopupHistoryModal from './modals/TopupHistoryModal';
import PaymentOrderTracker from './PaymentOrderTracker';
import { usePaymentOrderPolling } from './use-payment-order';
import {
  createPaymentRequestId,
  filterPaymentMethodsForBrowser,
  getPaymentQuoteRoutePayload,
  getPaymentRouteId,
  getPaymentSelectionId,
  getSafePaymentUrl,
  getSafeUserPaymentError,
  normalizePaymentMethod,
  normalizePublicPaymentQuote,
  normalizePublicTopupInfo,
  formatPaymentDecimal,
} from './payment-utils';

const { Text } = Typography;

const TopUp = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const [userState, userDispatch] = useContext(UserContext);
  const [statusState] = useContext(StatusContext);

  const [redemptionCode, setRedemptionCode] = useState('');
  const [amount, setAmount] = useState(0.0);
  const [minTopUp, setMinTopUp] = useState(statusState?.status?.min_topup || 1);
  const [topUpCount, setTopUpCount] = useState(
    statusState?.status?.min_topup || 1,
  );
  const [topUpLink, setTopUpLink] = useState('');
  const [statusLoading, setStatusLoading] = useState(true);

  const [paymentProducts, setPaymentProducts] = useState([]);
  const [creemOpen, setCreemOpen] = useState(false);
  const [selectedPaymentProduct, setSelectedPaymentProduct] = useState(null);

  const [paymentRouteOptions, setPaymentRouteOptions] = useState([]);

  const [isSubmitting, setIsSubmitting] = useState(false);
  const [open, setOpen] = useState(false);
  const [payWay, setPayWay] = useState('');
  const [amountLoading, setAmountLoading] = useState(false);
  const [paymentLoading, setPaymentLoading] = useState(false);
  const [confirmLoading, setConfirmLoading] = useState(false);
  const [payMethods, setPayMethods] = useState([]);
  const [paymentQuote, setPaymentQuote] = useState(null);
  const [paymentQuoteError, setPaymentQuoteError] = useState('');
  const quoteSequenceRef = useRef(0);
  const quoteAbortRef = useRef(null);
  const paymentSelectionRef = useRef('');
  const paymentSelectionInFlightRef = useRef(false);
  const paymentStartInFlightRef = useRef(false);
  const paymentStartRequestRef = useRef({ quoteId: '', requestId: '' });
  const productPaymentAttemptRef = useRef({
    key: '',
    quoteId: '',
    requestId: '',
    expiresAt: 0,
  });

  const affFetchedRef = useRef(false);

  // 邀请相关状态
  const [affLink, setAffLink] = useState('');
  const [openTransfer, setOpenTransfer] = useState(false);
  const [transferAmount, setTransferAmount] = useState(0);

  // 账单Modal状态
  const [openHistory, setOpenHistory] = useState(false);

  // 订阅相关
  const [subscriptionPlans, setSubscriptionPlans] = useState([]);
  const [subscriptionLoading, setSubscriptionLoading] = useState(true);
  const [subscriptionPlansError, setSubscriptionPlansError] = useState('');
  const [subscriptionSelfError, setSubscriptionSelfError] = useState('');
  const [billingPreference, setBillingPreference] =
    useState('subscription_first');
  const [billingPreferenceLoading, setBillingPreferenceLoading] =
    useState(false);
  const billingPreferenceInFlightRef = useRef(false);
  const [activeSubscriptions, setActiveSubscriptions] = useState([]);
  const [allSubscriptions, setAllSubscriptions] = useState([]);
  const [topupInfoError, setTopupInfoError] = useState('');
  const subscriptionError = subscriptionPlansError || subscriptionSelfError;

  // 预设充值额度选项
  const [presetAmounts, setPresetAmounts] = useState([]);
  const [selectedPreset, setSelectedPreset] = useState(null);

  // 充值配置信息
  const [topupInfo, setTopupInfo] = useState({
    online_payment_available: false,
    payment_routes: [],
    payment_products: [],
    payment_route_options: [],
    min_topup: 1,
    amount_options: [],
    discount: {},
    enable_redemption: true,
    payment_compliance_confirmed: true,
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
      await Promise.all([
        getUserQuota({ silent: true }),
        getSubscriptionSelf(),
      ]);
    },
  });

  const optionRoute = topupInfo.payment_routes.find(
    (method) => method.checkout_mode === 'option',
  );
  const confirmPayMethods = [
    ...payMethods,
    ...paymentRouteOptions
      .filter((option) => option.route_id === optionRoute?.route_id)
      .map((option) =>
        normalizePaymentMethod({
          ...optionRoute,
          option_id: option.option_id,
          public_label: option.public_label,
        }),
      ),
  ];

  const getPayMethodConfig = (payment) =>
    confirmPayMethods.find(
      (method) => getPaymentSelectionId(method) === payment,
    );

  const getPaymentMinTopUp = (payment) => {
    const configuredMinTopUp = Number(getPayMethodConfig(payment)?.min_topup);
    return Number.isFinite(configuredMinTopUp) && configuredMinTopUp > 0
      ? configuredMinTopUp
      : minTopUp;
  };

  const invalidatePaymentQuote = () => {
    quoteSequenceRef.current += 1;
    quoteAbortRef.current?.abort?.();
    quoteAbortRef.current = null;
    paymentStartRequestRef.current = { quoteId: '', requestId: '' };
    setPaymentQuote(null);
    setPaymentQuoteError('');
    setAmount(0);
    setAmountLoading(false);
  };

  const requestUnifiedQuote = async (payment, value) => {
    const method = getPayMethodConfig(payment);
    if (
      !method ||
      !['quote', 'direct', 'option'].includes(method.checkout_mode)
    ) {
      return null;
    }

    quoteAbortRef.current?.abort?.();
    const controller = new AbortController();
    quoteAbortRef.current = controller;
    const sequence = ++quoteSequenceRef.current;
    setPaymentQuote(null);
    setPaymentQuoteError('');
    setAmountLoading(true);
    try {
      const response = await API.post(
        '/api/user/payment/quote',
        {
          order_kind: 'topup',
          ...getPaymentQuoteRoutePayload(method),
          amount: Number(value),
          ...(method.option_id ? { option_id: method.option_id } : {}),
        },
        { signal: controller.signal, skipErrorHandler: true },
      );

      const quote = response.data?.data;
      if (
        sequence !== quoteSequenceRef.current ||
        !response.data?.success ||
        !quote
      ) {
        throw new Error(t('获取金额失败'));
      }
      const publicQuote = normalizePublicPaymentQuote(quote, method, value);
      if (!publicQuote) throw new Error(t('获取金额失败'));
      setPaymentQuote(publicQuote);
      setAmount(Number(publicQuote.payable_amount) || 0);
      paymentStartRequestRef.current = {
        quoteId: publicQuote.quote_id,
        requestId: createPaymentRequestId(),
      };
      return publicQuote;
    } catch (error) {
      if (sequence !== quoteSequenceRef.current) return null;
      if (error?.code === 'ERR_CANCELED') return null;
      const safeError = getSafeUserPaymentError(error, t, '获取金额失败');
      setPaymentQuote(null);
      setPaymentQuoteError(safeError);
      return null;
    } finally {
      if (sequence === quoteSequenceRef.current) {
        setAmountLoading(false);
        if (quoteAbortRef.current === controller) quoteAbortRef.current = null;
      }
    }
  };

  const requestAmountByPayment = async (payment, value) => {
    const method = getPayMethodConfig(payment);
    if (!method) return null;
    if (['quote', 'direct', 'option'].includes(method.checkout_mode)) {
      return requestUnifiedQuote(payment, value ?? topUpCount);
    }
    return null;
  };

  const topUp = async () => {
    if (redemptionCode === '') {
      showInfo(t('请输入兑换码！'));
      return;
    }
    setIsSubmitting(true);
    try {
      const res = await API.post(
        '/api/user/topup',
        {
          key: redemptionCode,
        },
        { skipErrorHandler: true },
      );
      const { success, message, data } = res.data;
      if (success) {
        showSuccess(t('兑换成功！'));
        Modal.success({
          title: t('兑换成功！'),
          content: t('Redemption successful! Added: {{quota}}', {
            quota: renderQuota(data),
          }),
          centered: true,
        });
        if (userState.user) {
          const updatedUser = {
            ...userState.user,
            quota: userState.user.quota + data,
          };
          userDispatch({ type: 'login', payload: updatedUser });
        }
        setRedemptionCode('');
      } else {
        showError(message);
      }
    } catch (err) {
      showError(t('请求失败'));
    } finally {
      setIsSubmitting(false);
    }
  };

  const openTopUpLink = () => {
    if (!topUpLink) {
      showError(t('超级管理员未设置充值链接！'));
      return;
    }
    const safeUrl = getSafePaymentUrl(topUpLink);
    if (!safeUrl) {
      showError(t('支付跳转地址不安全'));
      return;
    }
    window.open(safeUrl.href, '_blank', 'noopener,noreferrer');
  };

  const preTopUp = async (payment) => {
    if (paymentSelectionInFlightRef.current) return;

    const method = getPayMethodConfig(payment);
    if (!method) {
      showError(t('当前支付方式暂时不可用，请重新选择'));
      return;
    }
    if (!['quote', 'option', 'direct'].includes(method.checkout_mode)) {
      showError(t('当前支付方式暂时不可用，请重新选择'));
      return;
    }

    const selectedMinTopUp = getPaymentMinTopUp(payment);
    if (
      !Number.isSafeInteger(Number(topUpCount)) ||
      Number(topUpCount) < selectedMinTopUp ||
      Number(topUpCount) > 10000
    ) {
      showError(
        t('充值数量必须是 {{min}} 到 10000 之间的整数', {
          min: selectedMinTopUp,
        }),
      );
      return;
    }

    paymentSelectionInFlightRef.current = true;
    paymentSelectionRef.current = payment;
    invalidatePaymentQuote();
    setPayWay(payment);
    setPaymentLoading(true);
    try {
      const quote = await requestAmountByPayment(payment, topUpCount);
      if (!quote) {
        return;
      }
      if (paymentSelectionRef.current === payment) setOpen(true);
    } catch (error) {
      showError(
        error?.response || error?.request
          ? getSafeUserPaymentError(error, t, '获取金额失败')
          : error?.message || t('获取金额失败'),
      );
    } finally {
      paymentSelectionInFlightRef.current = false;
      setPaymentLoading(false);
    }
  };

  const onlineTopUp = async () => {
    const selectedMethod = getPayMethodConfig(payWay);
    if (!selectedMethod) {
      showError(t('当前支付方式暂时不可用，请重新选择'));
      setOpen(false);
      return;
    }

    if (
      !['quote', 'direct', 'option'].includes(selectedMethod.checkout_mode) ||
      !paymentQuote
    ) {
      showError(t('请重新获取支付报价后再继续'));
      setOpen(false);
      return;
    }
    const quoteMatchesSelection =
      paymentQuote.route_id === selectedMethod.route_id &&
      Number(paymentQuote.top_up_amount) === Number(topUpCount);
    if (!quoteMatchesSelection) {
      showError(t('支付方式或充值数量已变化，请重新获取报价'));
      invalidatePaymentQuote();
      setOpen(false);
      return;
    }
    if (paymentQuote.expires_at <= Date.now() / 1000) {
      showError(t('报价已过期，请重新获取'));
      setOpen(false);
      return;
    }
    if (paymentStartInFlightRef.current) return;

    paymentStartInFlightRef.current = true;
    setConfirmLoading(true);
    let paymentStarted = false;
    try {
      if (
        paymentStartRequestRef.current.quoteId !== paymentQuote.quote_id ||
        !paymentStartRequestRef.current.requestId
      ) {
        paymentStartRequestRef.current = {
          quoteId: paymentQuote.quote_id,
          requestId: createPaymentRequestId(),
        };
      }
      const response = await API.post(
        '/api/user/payment/start',
        {
          quote_id: paymentQuote.quote_id,
          request_id: paymentStartRequestRef.current.requestId,
        },
        { skipErrorHandler: true },
      );
      const start = response.data?.data;
      if (!response.data?.success || !start?.trade_no) {
        throw new Error(t('支付请求失败'));
      }
      navigate(`/payment/${encodeURIComponent(start.trade_no)}`);
      paymentStarted = true;
    } catch (error) {
      showError(
        error?.response || error?.request
          ? getSafeUserPaymentError(error, t, '支付请求失败')
          : error?.message || t('支付请求失败'),
      );
    } finally {
      if (paymentStarted) setOpen(false);
      paymentStartInFlightRef.current = false;
      setConfirmLoading(false);
    }
  };

  const paymentProductPreTopUp = async (product) => {
    const key = `${product?.route_id || ''}:${product?.product_id || ''}`;
    if (productPaymentAttemptRef.current.key !== key) {
      productPaymentAttemptRef.current = {
        key,
        quoteId: '',
        requestId: '',
        expiresAt: 0,
      };
    }
    setSelectedPaymentProduct(product);
    setCreemOpen(true);
  };

  const onlineCreemTopUp = async () => {
    if (!selectedPaymentProduct) {
      showError(t('请选择产品'));
      return;
    }
    if (!selectedPaymentProduct.product_id) {
      showError(t('产品配置错误，请联系管理员'));
      return;
    }
    if (!selectedPaymentProduct.route_id) {
      showError(t('当前支付方式暂时不可用，请重新选择'));
      return;
    }
    setConfirmLoading(true);
    let paymentStarted = false;
    try {
      const key = `${selectedPaymentProduct.route_id}:${selectedPaymentProduct.product_id}`;
      let attempt = productPaymentAttemptRef.current;
      if (
        attempt.key !== key ||
        !attempt.quoteId ||
        attempt.expiresAt <= Date.now() / 1000
      ) {
        const quoteResponse = await API.post(
          '/api/user/payment/quote',
          {
            order_kind: 'topup',
            route_id: selectedPaymentProduct.route_id,
            product_id: selectedPaymentProduct.product_id,
          },
          { skipErrorHandler: true },
        );
        const quote = quoteResponse.data?.data;
        if (!quoteResponse.data?.success || !quote?.quote_id) {
          throw new Error(t('获取金额失败'));
        }
        attempt = {
          key,
          quoteId: quote.quote_id,
          requestId: createPaymentRequestId(),
          expiresAt: Number(quote.expires_at) || 0,
        };
        productPaymentAttemptRef.current = attempt;
      }

      const startResponse = await API.post(
        '/api/user/payment/start',
        { quote_id: attempt.quoteId, request_id: attempt.requestId },
        { skipErrorHandler: true },
      );
      const start = startResponse.data?.data;
      if (!startResponse.data?.success || !start?.trade_no) {
        throw new Error(t('支付请求失败'));
      }
      navigate(`/payment/${encodeURIComponent(start.trade_no)}`);
      paymentStarted = true;
    } catch (err) {
      showError(getSafeUserPaymentError(err, t, '支付请求失败'));
    } finally {
      if (paymentStarted) setCreemOpen(false);
      setConfirmLoading(false);
    }
  };

  const getUserQuota = async ({ silent = false } = {}) => {
    try {
      const res = await API.get('/api/user/self', {
        skipErrorHandler: true,
      });
      const { success, message, data } = res.data;
      if (success) {
        userDispatch({ type: 'login', payload: data });
        return true;
      }
      if (!silent) showError(message || t('刷新账户信息失败'));
    } catch {
      if (!silent) showError(t('刷新账户信息失败'));
    }
    return false;
  };

  const getSubscriptionPlans = async () => {
    setSubscriptionLoading(true);
    setSubscriptionPlansError('');
    try {
      const res = await API.get('/api/subscription/plans', {
        skipErrorHandler: true,
      });
      if (res.data?.success) {
        setSubscriptionPlans(normalizePublicSubscriptionPlans(res.data.data));
      } else {
        setSubscriptionPlans([]);
        setSubscriptionPlansError(t('加载订阅套餐失败'));
      }
    } catch (e) {
      setSubscriptionPlans([]);
      setSubscriptionPlansError(t('加载订阅套餐失败'));
    } finally {
      setSubscriptionLoading(false);
    }
  };

  const getSubscriptionSelf = async () => {
    setSubscriptionSelfError('');
    try {
      const res = await API.get('/api/subscription/self', {
        skipErrorHandler: true,
      });
      if (res.data?.success) {
        const publicData = normalizePublicSubscriptionSelf(res.data.data);
        setBillingPreference(
          publicData.billing_preference || 'subscription_first',
        );
        // Active subscriptions
        const activeSubs = publicData.subscriptions;
        setActiveSubscriptions(activeSubs);
        // All subscriptions (including expired)
        const allSubs = publicData.all_subscriptions;
        setAllSubscriptions(allSubs);
        return true;
      }
      setSubscriptionSelfError(t('加载订阅状态失败'));
    } catch (e) {
      setSubscriptionSelfError(t('加载订阅状态失败'));
    }
    return false;
  };

  const updateBillingPreference = async (pref) => {
    if (billingPreferenceInFlightRef.current || pref === billingPreference) {
      return;
    }
    billingPreferenceInFlightRef.current = true;
    setBillingPreferenceLoading(true);
    const previousPref = billingPreference;
    setBillingPreference(pref);
    try {
      const res = await API.put(
        '/api/subscription/self/preference',
        {
          billing_preference: pref,
        },
        { skipErrorHandler: true },
      );
      if (res.data?.success) {
        showSuccess(t('更新成功'));
        const normalizedPref =
          res.data?.data?.billing_preference || pref || previousPref;
        setBillingPreference(normalizedPref);
      } else {
        showError(t('更新失败'));
        setBillingPreference(previousPref);
      }
    } catch (e) {
      showError(t('请求失败'));
      setBillingPreference(previousPref);
    } finally {
      billingPreferenceInFlightRef.current = false;
      setBillingPreferenceLoading(false);
    }
  };

  // 获取充值配置信息
  const getTopupInfo = async () => {
    setStatusLoading(true);
    setTopupInfoError('');
    try {
      const res = await API.get('/api/user/topup/info', {
        skipErrorHandler: true,
      });
      const { data, success } = res.data;
      if (success) {
        const publicInfo = normalizePublicTopupInfo(data);
        if (!publicInfo) {
          setPayMethods([]);
          setTopupInfoError(t('充值配置格式无效，请联系管理员'));
          return;
        }
        publicInfo.payment_routes = filterPaymentMethodsForBrowser(
          publicInfo.payment_routes,
        );
        const amountRoutes = publicInfo.payment_routes.filter(
          (method) =>
            method.checkout_mode === 'quote' ||
            method.checkout_mode === 'direct',
        );
        const routeMinimums = publicInfo.payment_routes
          .filter((method) => method.checkout_mode !== 'product')
          .map((method) => Number(method.min_topup))
          .filter((minimum) => Number.isFinite(minimum) && minimum > 0);
        const minTopUpValue =
          routeMinimums.length > 0
            ? Math.min(...routeMinimums)
            : publicInfo.min_topup;

        setTopupInfo(publicInfo);
        setPayMethods(amountRoutes);
        setPaymentProducts(publicInfo.payment_products);
        setPaymentRouteOptions(publicInfo.payment_route_options);
        setMinTopUp(minTopUpValue);
        setTopUpCount(minTopUpValue);
        setTopUpLink(publicInfo.topup_link || '');
        setPresetAmounts(
          publicInfo.amount_options.length > 0
            ? publicInfo.amount_options.map((value) => ({
                value,
                discount: publicInfo.discount[value] || 1.0,
              }))
            : generatePresetAmounts(minTopUpValue),
        );
        setAmount(0);
      } else {
        const errorMessage = t('获取充值配置失败');
        setTopupInfoError(errorMessage);
      }
    } catch (error) {
      setTopupInfoError(getSafeUserPaymentError(error, t, '获取充值配置异常'));
    } finally {
      setStatusLoading(false);
    }
  };

  // 获取邀请链接
  const getAffLink = async () => {
    const res = await API.get('/api/user/aff');
    const { success, message, data } = res.data;
    if (success) {
      let link = `${window.location.origin}/register?aff=${data}`;
      setAffLink(link);
    } else {
      showError(message);
    }
  };

  // 划转邀请额度
  const transfer = async () => {
    if (transferAmount < getQuotaPerUnit()) {
      showError(
        t('Minimum transfer amount: {{amount}}', {
          amount: renderQuota(getQuotaPerUnit()),
        }),
      );
      return;
    }
    const res = await API.post(`/api/user/aff_transfer`, {
      quota: transferAmount,
    });
    const { success, message } = res.data;
    if (success) {
      showSuccess(message);
      setOpenTransfer(false);
      getUserQuota().then();
    } else {
      showError(message);
    }
  };

  // 复制邀请链接
  const handleAffLinkClick = async () => {
    await copy(affLink);
    showSuccess(t('邀请链接已复制到剪切板'));
  };

  useEffect(
    () => () => {
      quoteAbortRef.current?.abort?.();
    },
    [],
  );

  // URL 参数自动打开账单弹窗（支付回跳时触发）
  useEffect(() => {
    const nextSearchParams = new URLSearchParams(searchParams);
    if (nextSearchParams.get('show_history') === 'true') {
      setOpenHistory(true);
      nextSearchParams.delete('show_history');
    }
    if (nextSearchParams.get('pay')) {
      setOpenHistory(true);
      nextSearchParams.delete('pay');
    }
    const tradeNo = nextSearchParams.get('trade_no');
    if (tradeNo) {
      trackPayment({ trade_no: tradeNo, flow: 'pending' }, 'processing');
    }
    nextSearchParams.delete('payment_result');
    nextSearchParams.delete('trade_no');
    setSearchParams(nextSearchParams, { replace: true });
  }, []);

  useEffect(() => {
    // 始终获取最新用户数据，确保余额等统计信息准确
    getUserQuota().then();
    setTransferAmount(getQuotaPerUnit());
  }, []);

  useEffect(() => {
    if (affFetchedRef.current) return;
    affFetchedRef.current = true;
    getAffLink().then();
  }, []);

  // 在 statusState 可用时获取充值信息
  useEffect(() => {
    getTopupInfo().then();
    getSubscriptionPlans().then();
    getSubscriptionSelf().then();
  }, []);

  const renderAmount = () => {
    if (paymentQuote) {
      const selectedMethod = getPayMethodConfig(payWay);
      return formatPaymentDecimal(
        paymentQuote.payable_amount,
        paymentQuote.currency,
        selectedMethod?.public_method === 'card' ? 'card' : undefined,
      );
    }
    if (payWay && amount > 0) return formatPaymentDecimal(amount, 'CNY');
    return t('选择支付方式后显示服务端报价');
  };

  const getAmount = async (value) => {
    if (value === undefined) {
      value = topUpCount;
    }
    setAmountLoading(true);
    try {
      const res = await API.post(
        '/api/user/amount',
        {
          amount: parseFloat(value),
        },
        { skipErrorHandler: true },
      );
      if (res !== undefined) {
        const { message, data } = res.data;
        if (message === 'success') {
          setAmount(parseFloat(data));
          setPaymentQuoteError('');
        } else {
          setAmount(0);
          setPaymentQuoteError(t('获取金额失败'));
        }
      } else {
        setPaymentQuoteError(t('获取金额失败'));
      }
    } catch (error) {
      setAmount(0);
      setPaymentQuoteError(getSafeUserPaymentError(error, t, '获取金额失败'));
    }
    setAmountLoading(false);
  };

  const getStripeAmount = async (value) => {
    if (value === undefined) {
      value = topUpCount;
    }
    setAmountLoading(true);
    try {
      const res = await API.post(
        '/api/user/stripe/amount',
        {
          amount: parseFloat(value),
        },
        { skipErrorHandler: true },
      );
      if (res !== undefined) {
        const { message, data } = res.data;
        if (message === 'success') {
          setAmount(parseFloat(data));
          setPaymentQuoteError('');
        } else {
          setAmount(0);
          setPaymentQuoteError(t('获取金额失败'));
        }
      } else {
        setPaymentQuoteError(t('获取金额失败'));
      }
    } catch (error) {
      setAmount(0);
      setPaymentQuoteError(getSafeUserPaymentError(error, t, '获取金额失败'));
    } finally {
      setAmountLoading(false);
    }
  };

  const refreshSelectedPaymentAmount = async (value) => {
    invalidatePaymentQuote();
    if (!payWay) {
      setAmount(0);
      return;
    }
    try {
      await requestAmountByPayment(payWay, value);
    } catch {
      // The quote error is already stored for the confirmation surface.
    }
  };

  const handleCancel = () => {
    setOpen(false);
  };

  const handleTransferCancel = () => {
    setOpenTransfer(false);
  };

  const handleOpenHistory = () => {
    setOpenHistory(true);
  };

  const handleHistoryCancel = () => {
    setOpenHistory(false);
  };

  const handleCreemCancel = () => {
    setCreemOpen(false);
    setSelectedPaymentProduct(null);
  };

  // 选择预设充值额度
  const selectPresetAmount = (preset) => {
    setTopUpCount(preset.value);
    setSelectedPreset(preset.value);
    void refreshSelectedPaymentAmount(preset.value);
  };

  // 格式化大数字显示
  const formatLargeNumber = (num) => {
    return num.toString();
  };

  // 根据最小充值金额生成预设充值额度选项
  const generatePresetAmounts = (minAmount) => {
    const multipliers = [1, 5, 10, 30, 50, 100, 300, 500];
    return multipliers
      .map((multiplier) => ({
        value: minAmount * multiplier,
      }))
      .filter((preset) => preset.value >= minAmount && preset.value <= 10000);
  };

  return (
    <div className='w-full max-w-7xl mx-auto relative min-h-screen lg:min-h-0 mt-[60px] px-2'>
      {/* 划转模态框 */}
      <TransferModal
        t={t}
        openTransfer={openTransfer}
        transfer={transfer}
        handleTransferCancel={handleTransferCancel}
        userState={userState}
        renderQuota={renderQuota}
        getQuotaPerUnit={getQuotaPerUnit}
        transferAmount={transferAmount}
        setTransferAmount={setTransferAmount}
      />

      {/* 充值确认模态框 */}
      <PaymentConfirmModal
        t={t}
        open={open}
        onlineTopUp={onlineTopUp}
        handleCancel={handleCancel}
        confirmLoading={confirmLoading}
        topUpCount={topUpCount}
        renderQuotaWithAmount={renderQuotaWithAmount}
        amountLoading={amountLoading}
        renderAmount={renderAmount}
        payWay={payWay}
        payMethods={confirmPayMethods}
        amountNumber={amount}
        discountRate={topupInfo?.discount?.[topUpCount] || 1.0}
        paymentQuote={paymentQuote}
        paymentQuoteError={paymentQuoteError}
      />

      {/* 充值账单模态框 */}
      <TopupHistoryModal
        visible={openHistory}
        onCancel={handleHistoryCancel}
        t={t}
      />

      {/* Creem 充值确认模态框 */}
      <Modal
        title={t('确定要充值 $')}
        visible={creemOpen}
        onOk={onlineCreemTopUp}
        onCancel={handleCreemCancel}
        maskClosable={false}
        size='small'
        centered
        confirmLoading={confirmLoading}
      >
        {selectedPaymentProduct && (
          <>
            <p>
              {t('产品名称')}：{selectedPaymentProduct.name}
            </p>
            <p>
              {t('价格')}：
              {new Intl.NumberFormat(undefined, {
                style: 'currency',
                currency: selectedPaymentProduct.currency,
              }).format(Number(selectedPaymentProduct.payment_amount))}
            </p>
            <p>
              {t('充值额度')}：{selectedPaymentProduct.top_up_amount}
            </p>
            <p>{t('是否确认充值？')}</p>
          </>
        )}
      </Modal>

      {pendingPayment && (
        <div className='mb-4'>
          <PaymentOrderTracker
            t={t}
            payment={pendingPayment}
            order={pendingPaymentOrder}
            error={pendingPaymentError}
            polling={pendingPaymentPolling}
            refreshing={pendingPaymentRefreshing}
            onRefresh={refreshPendingPayment}
            onOpenHistory={handleOpenHistory}
            onDismiss={clearPendingPayment}
          />
        </div>
      )}

      {/* 主布局区域 */}
      <div className='grid grid-cols-1 lg:grid-cols-2 gap-6'>
        <RechargeCard
          t={t}
          paymentProducts={paymentProducts}
          paymentProductPreTopUp={paymentProductPreTopUp}
          presetAmounts={presetAmounts}
          selectedPreset={selectedPreset}
          selectPresetAmount={selectPresetAmount}
          formatLargeNumber={formatLargeNumber}
          topUpCount={topUpCount}
          minTopUp={minTopUp}
          renderQuotaWithAmount={renderQuotaWithAmount}
          getAmount={refreshSelectedPaymentAmount}
          setTopUpCount={setTopUpCount}
          setSelectedPreset={setSelectedPreset}
          renderAmount={renderAmount}
          amountLoading={amountLoading}
          payMethods={confirmPayMethods}
          preTopUp={preTopUp}
          paymentLoading={paymentLoading}
          payWay={payWay}
          redemptionCode={redemptionCode}
          setRedemptionCode={setRedemptionCode}
          topUp={topUp}
          isSubmitting={isSubmitting}
          topUpLink={topUpLink}
          openTopUpLink={openTopUpLink}
          userState={userState}
          renderQuota={renderQuota}
          statusLoading={statusLoading}
          topupInfoError={topupInfoError}
          onRetryTopupInfo={getTopupInfo}
          topupInfo={topupInfo}
          onOpenHistory={handleOpenHistory}
          subscriptionLoading={subscriptionLoading}
          subscriptionError={subscriptionError}
          onRetrySubscriptions={async () => {
            setSubscriptionPlansError('');
            setSubscriptionSelfError('');
            await Promise.all([getSubscriptionPlans(), getSubscriptionSelf()]);
          }}
          subscriptionPlans={subscriptionPlans}
          billingPreference={billingPreference}
          billingPreferenceLoading={billingPreferenceLoading}
          onChangeBillingPreference={updateBillingPreference}
          activeSubscriptions={activeSubscriptions}
          allSubscriptions={allSubscriptions}
          reloadSubscriptionSelf={getSubscriptionSelf}
          reloadUserQuota={getUserQuota}
          enableRedemption={topupInfo.enable_redemption !== false}
        />
        <InvitationCard
          t={t}
          userState={userState}
          renderQuota={renderQuota}
          setOpenTransfer={setOpenTransfer}
          affLink={affLink}
          handleAffLinkClick={handleAffLinkClick}
          complianceConfirmed={topupInfo.payment_compliance_confirmed !== false}
        />
      </div>
    </div>
  );
};

export default TopUp;
