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
import { useSearchParams } from 'react-router-dom';
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
import { Modal, Toast } from '@douyinfe/semi-ui';
import { QRCodeSVG } from 'qrcode.react';
import { useTranslation } from 'react-i18next';
import { UserContext } from '../../context/User';
import { StatusContext } from '../../context/Status';

import RechargeCard from './RechargeCard';
import InvitationCard from './InvitationCard';
import TransferModal from './modals/TransferModal';
import PaymentConfirmModal from './modals/PaymentConfirmModal';
import TopupHistoryModal from './modals/TopupHistoryModal';
import PaymentOrderTracker from './PaymentOrderTracker';
import { usePaymentOrderPolling } from './use-payment-order';
import {
  createPaymentRequestId,
  getSafePaymentUrl,
  inferPaymentProvider,
  isEndpointUnavailable,
  isSafeQrContent,
  navigateToPaymentUrl,
  normalizePaymentMethod,
  formatPaymentDecimal,
  submitPaymentForm,
} from './payment-utils';

const TopUp = () => {
  const { t } = useTranslation();
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
  const [enableOnlineTopUp, setEnableOnlineTopUp] = useState(
    statusState?.status?.enable_online_topup || false,
  );
  const [enableStripeTopUp, setEnableStripeTopUp] = useState(
    statusState?.status?.enable_stripe_topup || false,
  );
  const [enableXorPayTopUp, setEnableXorPayTopUp] = useState(
    statusState?.status?.enable_xorpay_topup || false,
  );
  const [statusLoading, setStatusLoading] = useState(true);

  // Creem 相关状态
  const [creemProducts, setCreemProducts] = useState([]);
  const [enableCreemTopUp, setEnableCreemTopUp] = useState(false);
  const [creemOpen, setCreemOpen] = useState(false);
  const [selectedCreemProduct, setSelectedCreemProduct] = useState(null);

  // Waffo 相关状态
  const [enableWaffoTopUp, setEnableWaffoTopUp] = useState(false);
  const [waffoPayMethods, setWaffoPayMethods] = useState([]);
  const [waffoMinTopUp, setWaffoMinTopUp] = useState(1);
  const [enableWaffoPancakeTopUp, setEnableWaffoPancakeTopUp] = useState(false);
  const [waffoPancakeMinTopUp, setWaffoPancakeMinTopUp] = useState(1);

  const [isSubmitting, setIsSubmitting] = useState(false);
  const [open, setOpen] = useState(false);
  const [payWay, setPayWay] = useState('');
  const [amountLoading, setAmountLoading] = useState(false);
  const [paymentLoading, setPaymentLoading] = useState(false);
  const [confirmLoading, setConfirmLoading] = useState(false);
  const [payMethods, setPayMethods] = useState([]);
  const [paymentQuote, setPaymentQuote] = useState(null);
  const [paymentQuoteError, setPaymentQuoteError] = useState('');
  const [qrPayment, setQrPayment] = useState(null);
  const quoteSequenceRef = useRef(0);
  const quoteAbortRef = useRef(null);
  const paymentSelectionRef = useRef('');
  const paymentSelectionInFlightRef = useRef(false);
  const paymentStartInFlightRef = useRef(false);
  const paymentStartRequestRef = useRef({ quoteId: '', requestId: '' });

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
      await Promise.all([getUserQuota(), getSubscriptionSelf()]);
    },
    onTerminal: async (order) => {
      if (order.status === 'success') {
        showSuccess(t('支付成功'));
      } else {
        showInfo(t('支付状态已更新') + `: ${t(order.status)}`);
      }
      setOpenHistory(true);
    },
  });

  const confirmPayMethods = [
    ...payMethods,
    ...waffoPayMethods.map((method, index) => ({
      ...method,
      type: `waffo:${index}`,
      min_topup: waffoMinTopUp,
      color: method.color || 'rgba(var(--semi-primary-5), 1)',
    })),
  ];

  const getPayMethodConfig = (payment) =>
    confirmPayMethods.find((method) => method.type === payment);

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
    const method = getPayMethodConfig(payment) || {
      type: payment,
      provider: inferPaymentProvider(payment),
    };
    const provider = inferPaymentProvider(method.type, method.provider);
    if (!['epay', 'stripe', 'xorpay'].includes(provider)) return null;

    quoteAbortRef.current?.abort?.();
    const controller = new AbortController();
    quoteAbortRef.current = controller;
    const sequence = ++quoteSequenceRef.current;
    setPaymentQuote(null);
    setPaymentQuoteError('');
    setAmountLoading(true);
    try {
      let response;
      try {
        response = await API.post(
          '/api/user/payment/quote',
          {
            order_kind: 'topup',
            provider,
            payment_method: method.type,
            amount: Number(value),
          },
          { signal: controller.signal },
        );
      } catch (error) {
        if (!isEndpointUnavailable(error) || provider === 'xorpay') throw error;
        const legacyResponse = await API.post(
          provider === 'stripe'
            ? '/api/user/stripe/amount'
            : '/api/user/amount',
          { amount: Number(value) },
        );
        const legacyAmount = Number(legacyResponse.data?.data);
        if (
          !legacyResponse.data?.success &&
          legacyResponse.data?.message !== 'success'
        ) {
          throw new Error(legacyResponse.data?.message || t('获取金额失败'));
        }
        response = {
          data: {
            success: true,
            data: {
              quote_id: `legacy:${provider}:${method.type}:${Date.now()}`,
              order_kind: 'topup',
              provider,
              payment_method: method.type,
              requested_amount: Number(value),
              credit_quota: 0,
              expected_amount_minor: Math.round(legacyAmount * 100),
              payable_amount: legacyAmount.toFixed(2),
              currency:
                method.currency || (provider === 'stripe' ? 'USD' : 'CNY'),
              expires_at: Math.floor(Date.now() / 1000) + 300,
              legacy: true,
            },
          },
        };
      }

      const quote = response.data?.data;
      if (
        sequence !== quoteSequenceRef.current ||
        !response.data?.success ||
        !quote
      ) {
        throw new Error(response.data?.message || t('获取金额失败'));
      }
      setPaymentQuote(quote);
      setAmount(Number(quote.payable_amount) || 0);
      paymentStartRequestRef.current = {
        quoteId: quote.quote_id,
        requestId: createPaymentRequestId(),
      };
      return quote;
    } catch (error) {
      if (sequence !== quoteSequenceRef.current) return null;
      if (error?.code === 'ERR_CANCELED') return null;
      setPaymentQuote(null);
      setPaymentQuoteError(error?.message || t('获取金额失败'));
      throw error;
    } finally {
      if (sequence === quoteSequenceRef.current) {
        setAmountLoading(false);
        if (quoteAbortRef.current === controller) quoteAbortRef.current = null;
      }
    }
  };

  const requestAmountByPayment = async (payment, value) => {
    const method = getPayMethodConfig(payment);
    const provider = inferPaymentProvider(
      method?.type || payment,
      method?.provider,
    );
    if (['epay', 'stripe', 'xorpay'].includes(provider)) {
      return requestUnifiedQuote(payment, value ?? topUpCount);
    }
    if (payment === 'stripe') {
      return getStripeAmount(value);
    }
    if (payment === 'waffo_pancake') {
      return getWaffoPancakeAmount(value);
    }
    if (typeof payment === 'string' && payment.startsWith('waffo:')) {
      return getWaffoAmount(value);
    }
    return getAmount(value);
  };

  const topUp = async () => {
    if (redemptionCode === '') {
      showInfo(t('请输入兑换码！'));
      return;
    }
    setIsSubmitting(true);
    try {
      const res = await API.post('/api/user/topup', {
        key: redemptionCode,
      });
      const { success, message, data } = res.data;
      if (success) {
        showSuccess(t('兑换成功！'));
        Modal.success({
          title: t('兑换成功！'),
          content: t('成功兑换额度：') + renderQuota(data),
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
    const provider = inferPaymentProvider(
      method?.type || payment,
      method?.provider,
    );
    if (provider === 'stripe') {
      if (!enableStripeTopUp) {
        showError(t('管理员未开启Stripe充值！'));
        return;
      }
    } else if (provider === 'xorpay') {
      if (!enableXorPayTopUp || !method) {
        showError(t('管理员未开启在线充值！'));
        return;
      }
    } else if (payment === 'waffo_pancake') {
      if (!enableWaffoPancakeTopUp) {
        showError(t('管理员未开启 Waffo Pancake 充值！'));
        return;
      }
    } else if (payment.startsWith('waffo:')) {
      if (!enableWaffoTopUp) {
        showError(t('管理员未开启 Waffo 充值！'));
        return;
      }
    } else if (provider === 'epay') {
      if (!enableOnlineTopUp) {
        showError(t('管理员未开启在线充值！'));
        return;
      }
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
      if (['epay', 'stripe', 'xorpay'].includes(provider) && !quote) return;
      if (paymentSelectionRef.current === payment) setOpen(true);
    } catch (error) {
      showError(error?.message || t('获取金额失败'));
    } finally {
      paymentSelectionInFlightRef.current = false;
      setPaymentLoading(false);
    }
  };

  const onlineTopUp = async () => {
    if (payWay === 'waffo_pancake') {
      setConfirmLoading(true);
      try {
        await waffoPancakeTopUp();
      } finally {
        setOpen(false);
        setConfirmLoading(false);
      }
      return;
    }

    if (payWay.startsWith('waffo:')) {
      const payMethodIndex = Number(payWay.split(':')[1]);
      setConfirmLoading(true);
      try {
        await waffoTopUp(Number.isFinite(payMethodIndex) ? payMethodIndex : 0);
      } finally {
        setOpen(false);
        setConfirmLoading(false);
      }
      return;
    }

    const selectedMethod = getPayMethodConfig(payWay);
    const selectedProvider = inferPaymentProvider(
      selectedMethod?.type || payWay,
      selectedMethod?.provider,
    );
    if (
      ['epay', 'stripe', 'xorpay'].includes(selectedProvider) &&
      paymentQuote &&
      !paymentQuote.legacy
    ) {
      const quoteMatchesSelection =
        paymentQuote.provider === selectedProvider &&
        paymentQuote.payment_method === (selectedMethod?.type || payWay) &&
        Number(paymentQuote.requested_amount) === Number(topUpCount);
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
        const response = await API.post('/api/user/payment/start', {
          quote_id: paymentQuote.quote_id,
          request_id: paymentStartRequestRef.current.requestId,
        });
        const start = response.data?.data;
        if (!response.data?.success || !start) {
          throw new Error(response.data?.message || t('支付请求失败'));
        }
        if (start.flow === 'qr') {
          if (!isSafeQrContent(start.qr_content)) {
            throw new Error(t('支付二维码无效'));
          }
          setQrPayment(start);
          trackPayment(start, 'pending');
        } else if (start.flow === 'pending') {
          trackPayment(start, 'processing');
        } else if (start.flow === 'hosted_redirect') {
          if (!navigateToPaymentUrl(start.url)) {
            throw new Error(t('支付跳转地址不安全'));
          }
        } else if (start.flow === 'form_post') {
          if (!submitPaymentForm(start.action, start.fields)) {
            throw new Error(t('支付跳转地址不安全'));
          }
        } else {
          throw new Error(t('支付网关返回了不支持的跳转方式'));
        }
        paymentStarted = true;
      } catch (error) {
        showError(error?.message || t('支付请求失败'));
      } finally {
        if (paymentStarted) setOpen(false);
        paymentStartInFlightRef.current = false;
        setConfirmLoading(false);
      }
      return;
    }

    if (payWay === 'stripe') {
      // Stripe 支付处理
      if (amount === 0) {
        await getStripeAmount();
      }
    } else {
      // 普通支付处理
      if (amount === 0) {
        await getAmount();
      }
    }

    if (topUpCount < minTopUp) {
      showError('充值数量不能小于' + minTopUp);
      return;
    }
    setConfirmLoading(true);
    try {
      let res;
      if (payWay === 'stripe') {
        // Stripe 支付请求
        res = await API.post('/api/user/stripe/pay', {
          amount: parseInt(topUpCount),
          payment_method: 'stripe',
        });
      } else {
        // 普通支付请求
        res = await API.post('/api/user/pay', {
          amount: parseInt(topUpCount),
          payment_method: payWay,
        });
      }

      if (res !== undefined) {
        const { message, data } = res.data;
        if (message === 'success') {
          if (payWay === 'stripe') {
            if (!navigateToPaymentUrl(data.pay_link)) {
              showError(t('支付跳转地址不安全'));
            }
          } else {
            if (!submitPaymentForm(res.data.url, data)) {
              showError(t('支付跳转地址不安全'));
            }
          }
        } else {
          const errorMsg =
            typeof data === 'string' ? data : message || t('支付失败');
          showError(errorMsg);
        }
      } else {
        showError(res);
      }
    } catch (err) {
      showError(t('支付请求失败'));
    } finally {
      setOpen(false);
      setConfirmLoading(false);
    }
  };

  const creemPreTopUp = async (product) => {
    if (!enableCreemTopUp) {
      showError(t('管理员未开启 Creem 充值！'));
      return;
    }
    setSelectedCreemProduct(product);
    setCreemOpen(true);
  };

  const onlineCreemTopUp = async () => {
    if (!selectedCreemProduct) {
      showError(t('请选择产品'));
      return;
    }
    // Validate product has required fields
    if (!selectedCreemProduct.productId) {
      showError(t('产品配置错误，请联系管理员'));
      return;
    }
    setConfirmLoading(true);
    try {
      const res = await API.post('/api/user/creem/pay', {
        product_id: selectedCreemProduct.productId,
        payment_method: 'creem',
      });
      if (res !== undefined) {
        const { message, data } = res.data;
        if (message === 'success') {
          processCreemCallback(data);
        } else {
          const errorMsg =
            typeof data === 'string' ? data : message || t('支付失败');
          showError(errorMsg);
        }
      } else {
        showError(res);
      }
    } catch (err) {
      showError(t('支付请求失败'));
    } finally {
      setCreemOpen(false);
      setConfirmLoading(false);
    }
  };

  const waffoTopUp = async (payMethodIndex) => {
    try {
      if (topUpCount < waffoMinTopUp) {
        showError(t('充值数量不能小于') + waffoMinTopUp);
        return;
      }
      setPaymentLoading(true);
      const requestBody = {
        amount: parseInt(topUpCount),
      };
      if (payMethodIndex != null) {
        requestBody.pay_method_index = payMethodIndex;
      }
      const res = await API.post('/api/user/waffo/pay', requestBody);
      if (res !== undefined) {
        const { message, data } = res.data;
        if (message === 'success' && data?.payment_url) {
          if (!navigateToPaymentUrl(data.payment_url)) {
            showError(t('支付跳转地址不安全'));
          }
        } else {
          showError(data || t('支付请求失败'));
        }
      } else {
        showError(res);
      }
    } catch (e) {
      showError(t('支付请求失败'));
    } finally {
      setPaymentLoading(false);
    }
  };

  const getWaffoAmount = async (value) => {
    if (value === undefined) {
      value = topUpCount;
    }
    setAmountLoading(true);
    try {
      const res = await API.post('/api/user/waffo/amount', {
        amount: parseInt(value),
      });
      if (res !== undefined) {
        const { message, data } = res.data;
        if (message === 'success') {
          setAmount(parseFloat(data));
        } else {
          setAmount(0);
          Toast.error({ content: '错误：' + data, id: 'getAmount' });
        }
      } else {
        showError(res);
      }
    } catch (err) {
      // amount fetch failed silently
    } finally {
      setAmountLoading(false);
    }
  };

  const waffoPancakeTopUp = async () => {
    const minTopUpValue = Number(waffoPancakeMinTopUp || 1);
    if (topUpCount < minTopUpValue) {
      showError(t('充值数量不能小于') + minTopUpValue);
      return;
    }

    setPaymentLoading(true);
    try {
      const res = await API.post('/api/user/waffo-pancake/pay', {
        amount: parseInt(topUpCount),
      });
      if (res !== undefined) {
        const { message, data } = res.data;
        if (message === 'success') {
          const checkoutUrl = data?.checkout_url || '';
          if (checkoutUrl && navigateToPaymentUrl(checkoutUrl)) {
          } else if (checkoutUrl) {
            showError(t('支付跳转地址不安全'));
          } else {
            showError(t('支付请求失败'));
          }
        } else {
          const errorMsg =
            typeof data === 'string' ? data : message || t('支付请求失败');
          showError(errorMsg);
        }
      } else {
        showError(res);
      }
    } catch (e) {
      showError(t('支付请求失败'));
    } finally {
      setPaymentLoading(false);
    }
  };

  const getWaffoPancakeAmount = async (value) => {
    if (value === undefined) {
      value = topUpCount;
    }
    setAmountLoading(true);
    try {
      const res = await API.post('/api/user/waffo-pancake/amount', {
        amount: parseInt(value),
      });
      if (res !== undefined) {
        const { message, data } = res.data;
        if (message === 'success') {
          setAmount(parseFloat(data));
        } else {
          setAmount(0);
          Toast.error({ content: '错误：' + data, id: 'getAmount' });
        }
      } else {
        showError(res);
      }
    } catch (err) {
      // amount fetch failed silently
    } finally {
      setAmountLoading(false);
    }
  };

  const processCreemCallback = (data) => {
    if (!navigateToPaymentUrl(data.checkout_url)) {
      showError(t('支付跳转地址不安全'));
    }
  };

  const getUserQuota = async () => {
    try {
      const res = await API.get('/api/user/self');
      const { success, message, data } = res.data;
      if (success) {
        userDispatch({ type: 'login', payload: data });
        return true;
      }
      showError(message || t('刷新账户信息失败'));
    } catch {
      showError(t('刷新账户信息失败'));
    }
    return false;
  };

  const getSubscriptionPlans = async () => {
    setSubscriptionLoading(true);
    setSubscriptionPlansError('');
    try {
      const res = await API.get('/api/subscription/plans');
      if (res.data?.success) {
        setSubscriptionPlans(res.data.data || []);
      } else {
        setSubscriptionPlans([]);
        setSubscriptionPlansError(res.data?.message || t('加载订阅套餐失败'));
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
      const res = await API.get('/api/subscription/self');
      if (res.data?.success) {
        setBillingPreference(
          res.data.data?.billing_preference || 'subscription_first',
        );
        // Active subscriptions
        const activeSubs = res.data.data?.subscriptions || [];
        setActiveSubscriptions(activeSubs);
        // All subscriptions (including expired)
        const allSubs = res.data.data?.all_subscriptions || [];
        setAllSubscriptions(allSubs);
        return true;
      }
      setSubscriptionSelfError(res.data?.message || t('加载订阅状态失败'));
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
      const res = await API.put('/api/subscription/self/preference', {
        billing_preference: pref,
      });
      if (res.data?.success) {
        showSuccess(t('更新成功'));
        const normalizedPref =
          res.data?.data?.billing_preference || pref || previousPref;
        setBillingPreference(normalizedPref);
      } else {
        showError(res.data?.message || t('更新失败'));
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
      const res = await API.get('/api/user/topup/info');
      const { message, data, success } = res.data;
      if (success) {
        setTopupInfo({
          amount_options: data.amount_options || [],
          discount: data.discount || {},
        });

        // 处理支付方式
        let payMethods = data.pay_methods || [];
        try {
          if (typeof payMethods === 'string') {
            payMethods = JSON.parse(payMethods);
          }
          if (payMethods && payMethods.length > 0) {
            // 检查name和type是否为空
            payMethods = payMethods.filter((method) => {
              return method.name && method.type;
            });
            // 如果没有color，则设置默认颜色
            payMethods = payMethods.map((rawMethod) => {
              const method = normalizePaymentMethod(rawMethod);
              // 规范化最小充值数
              const normalizedMinTopup = Number(method.min_topup);
              method.min_topup = Number.isFinite(normalizedMinTopup)
                ? normalizedMinTopup
                : 0;

              // Stripe 的最小充值从后端字段回填
              if (
                method.type === 'stripe' &&
                (!method.min_topup || method.min_topup <= 0)
              ) {
                const stripeMin = Number(data.stripe_min_topup);
                if (Number.isFinite(stripeMin)) {
                  method.min_topup = stripeMin;
                }
              }

              if (
                method.provider === 'xorpay' &&
                (!method.min_topup || method.min_topup <= 0)
              ) {
                const xorpayMin = Number(data.xorpay_min_topup);
                if (Number.isFinite(xorpayMin)) method.min_topup = xorpayMin;
              }

              if (!method.color) {
                if (method.type === 'alipay') {
                  method.color = 'rgba(var(--semi-blue-5), 1)';
                } else if (method.type === 'wxpay') {
                  method.color = 'rgba(var(--semi-green-5), 1)';
                } else if (method.type === 'stripe') {
                  method.color = 'rgba(var(--semi-purple-5), 1)';
                } else {
                  method.color = 'rgba(var(--semi-primary-5), 1)';
                }
              }
              return method;
            });
          } else {
            payMethods = [];
          }

          // 如果启用了 Stripe 支付，添加到支付方法列表
          // 这个逻辑现在由后端处理，如果 Stripe 启用，后端会在 pay_methods 中包含它

          setPayMethods(payMethods);
          const enableStripeTopUp = data.enable_stripe_topup || false;
          const enableXorPayTopUp = data.enable_xorpay_topup || false;
          const enableOnlineTopUp = data.enable_online_topup || false;
          const enableCreemTopUp = data.enable_creem_topup || false;
          const enableWaffoTopUp = data.enable_waffo_topup || false;
          const enableWaffoPancakeTopUp =
            data.enable_waffo_pancake_topup || false;
          const minTopUpValue = enableOnlineTopUp
            ? data.min_topup
            : enableStripeTopUp
              ? data.stripe_min_topup
              : enableXorPayTopUp
                ? data.xorpay_min_topup
                : enableWaffoTopUp
                  ? data.waffo_min_topup
                  : enableWaffoPancakeTopUp
                    ? data.waffo_pancake_min_topup
                    : 1;
          setEnableOnlineTopUp(enableOnlineTopUp);
          setEnableStripeTopUp(enableStripeTopUp);
          setEnableXorPayTopUp(enableXorPayTopUp);
          setEnableCreemTopUp(enableCreemTopUp);
          setEnableWaffoTopUp(enableWaffoTopUp);
          setWaffoPayMethods(data.waffo_pay_methods || []);
          setWaffoMinTopUp(data.waffo_min_topup || 1);
          setEnableWaffoPancakeTopUp(enableWaffoPancakeTopUp);
          setWaffoPancakeMinTopUp(data.waffo_pancake_min_topup || 1);
          setMinTopUp(minTopUpValue);
          setTopUpCount(minTopUpValue);
          setTopUpLink(data.topup_link || '');
          setTopupInfo((prev) => ({
            ...prev,
            enable_redemption: data.enable_redemption !== false,
            payment_compliance_confirmed:
              data.payment_compliance_confirmed !== false,
            payment_compliance_terms_version:
              data.payment_compliance_terms_version || '',
          }));

          // 设置 Creem 产品
          try {
            const products = JSON.parse(data.creem_products || '[]');
            setCreemProducts(products);
          } catch (e) {
            setCreemProducts([]);
          }

          // 如果没有自定义充值数量选项，根据最小充值金额生成预设充值额度选项
          if (!data.amount_options || data.amount_options.length === 0) {
            setPresetAmounts(generatePresetAmounts(minTopUpValue));
          }

          setAmount(0);
        } catch (e) {
          setPayMethods([]);
          setTopupInfoError(t('充值配置格式无效，请联系管理员'));
        }

        // 如果有自定义充值数量选项，使用它们替换默认的预设选项
        if (data.amount_options && data.amount_options.length > 0) {
          const customPresets = data.amount_options
            .filter(
              (amount) =>
                Number.isSafeInteger(Number(amount)) &&
                Number(amount) >= 1 &&
                Number(amount) <= 10000,
            )
            .map((amount) => ({
              value: Number(amount),
              discount: data.discount[amount] || 1.0,
            }));
          setPresetAmounts(customPresets);
        }
      } else {
        const errorMessage =
          message ||
          (typeof data === 'string' ? data : '') ||
          t('获取充值配置失败');
        setTopupInfoError(errorMessage);
        showError(errorMessage);
      }
    } catch (error) {
      setTopupInfoError(t('获取充值配置异常'));
      showError(t('获取充值配置异常'));
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
      showError(t('划转金额最低为') + ' ' + renderQuota(getQuotaPerUnit()));
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
      return formatPaymentDecimal(
        paymentQuote.payable_amount,
        paymentQuote.currency,
        paymentQuote.provider,
      );
    }
    if (payWay && amount > 0) return amount + ' ' + t('元');
    return t('选择支付方式后显示服务端报价');
  };

  const getAmount = async (value) => {
    if (value === undefined) {
      value = topUpCount;
    }
    setAmountLoading(true);
    try {
      const res = await API.post('/api/user/amount', {
        amount: parseFloat(value),
      });
      if (res !== undefined) {
        const { message, data } = res.data;
        if (message === 'success') {
          setAmount(parseFloat(data));
        } else {
          setAmount(0);
          Toast.error({ content: '错误：' + data, id: 'getAmount' });
        }
      } else {
        showError(res);
      }
    } catch (err) {
      // amount fetch failed silently
    }
    setAmountLoading(false);
  };

  const getStripeAmount = async (value) => {
    if (value === undefined) {
      value = topUpCount;
    }
    setAmountLoading(true);
    try {
      const res = await API.post('/api/user/stripe/amount', {
        amount: parseFloat(value),
      });
      if (res !== undefined) {
        const { message, data } = res.data;
        if (message === 'success') {
          setAmount(parseFloat(data));
        } else {
          setAmount(0);
          Toast.error({ content: '错误：' + data, id: 'getAmount' });
        }
      } else {
        showError(res);
      }
    } catch (err) {
      // amount fetch failed silently
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
    setSelectedCreemProduct(null);
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

      <Modal
        title={t('扫码支付')}
        visible={!!qrPayment}
        onCancel={() => {
          setQrPayment(null);
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
            <div className='text-red-500'>{t('支付二维码无效')}</div>
          )}
          <div className='text-sm text-gray-500 text-center'>
            {t('请使用对应的支付应用扫码，到账状态以服务器确认为准')}
          </div>
          <div className='text-sm'>
            {t('订单号')}: {qrPayment?.trade_no || '-'}
          </div>
          <div className='text-sm'>
            {t('支付状态')}:{' '}
            {t(
              pendingPayment?.trade_no === qrPayment?.trade_no
                ? pendingPaymentOrder?.status || 'pending'
                : 'pending',
            )}
          </div>
        </div>
      </Modal>

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
        {selectedCreemProduct && (
          <>
            <p>
              {t('产品名称')}：{selectedCreemProduct.name}
            </p>
            <p>
              {t('价格')}：{selectedCreemProduct.currency === 'EUR' ? '€' : '$'}
              {selectedCreemProduct.price}
            </p>
            <p>
              {t('充值额度')}：{selectedCreemProduct.quota}
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
          enableOnlineTopUp={enableOnlineTopUp}
          enableStripeTopUp={enableStripeTopUp}
          enableXorPayTopUp={enableXorPayTopUp}
          enableCreemTopUp={enableCreemTopUp}
          creemProducts={creemProducts}
          creemPreTopUp={creemPreTopUp}
          enableWaffoTopUp={enableWaffoTopUp}
          enableWaffoPancakeTopUp={enableWaffoPancakeTopUp}
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
