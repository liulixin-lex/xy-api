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

export function inferPaymentProvider(type, configuredProvider) {
  if (
    ['epay', 'stripe', 'xorpay', 'creem', 'waffo', 'waffo_pancake'].includes(
      configuredProvider,
    )
  ) {
    return configuredProvider;
  }
  if (type === 'stripe') return 'stripe';
  if (type?.startsWith('xorpay_')) return 'xorpay';
  if (type === 'creem') return 'creem';
  if (type === 'waffo') return 'waffo';
  if (type === 'waffo_pancake') return 'waffo_pancake';
  return 'epay';
}

export function isPaymentReturnCancelled(search) {
  try {
    return new URLSearchParams(search).get('payment_result') === 'cancelled';
  } catch {
    return false;
  }
}

const PUBLIC_ROUTE_PATTERN = /^[a-z][a-z0-9_]{0,63}$/;
const INTERNAL_PROVIDER_PATTERN = /(xorpay|epay|stripe|creem|waffo)/i;
const INTERNAL_GATEWAY_PATTERN = /(xorpay|epay|stripe)/i;

function normalizeToken(value) {
  return String(value || '')
    .trim()
    .toLowerCase();
}

function createOpaqueRouteId(method) {
  const provider = inferPaymentProvider(method?.type, method?.provider);
  const source = `${provider}\u0000${method?.type || method?.public_method || 'payment'}`;
  let first = 2166136261;
  let second = 2246822507;
  for (let index = 0; index < source.length; index += 1) {
    const code = source.charCodeAt(index);
    first = Math.imul(first ^ code, 16777619);
    second = Math.imul(second ^ code, 3266489917);
  }
  return `pay_${(first >>> 0).toString(36)}${(second >>> 0).toString(36)}`;
}

function getConfiguredRouteId(method) {
  const routeId = normalizeToken(method?.route_id);
  if (
    !PUBLIC_ROUTE_PATTERN.test(routeId) ||
    INTERNAL_PROVIDER_PATTERN.test(routeId)
  ) {
    return '';
  }
  return routeId;
}

export function getPaymentRouteId(method) {
  return getConfiguredRouteId(method) || createOpaqueRouteId(method);
}

export function getPublicPaymentMethod(method) {
  const configured = normalizeToken(method?.public_method);
  if (
    PUBLIC_ROUTE_PATTERN.test(configured) &&
    !INTERNAL_GATEWAY_PATTERN.test(configured)
  ) {
    return configured;
  }

  const provider = inferPaymentProvider(method?.type, method?.provider);
  const type = normalizeToken(method?.type);
  if (type === 'alipay' || type === 'xorpay_alipay') return 'alipay';
  if (
    ['wxpay', 'wechat', 'wechat_pay', 'xorpay_native', 'xorpay_jsapi'].includes(
      type,
    )
  ) {
    return 'wechat_pay';
  }
  if (provider === 'stripe' || type === 'stripe') return 'card';
  return 'payment';
}

export function getPublicPaymentChannel(method) {
  const configured = normalizeToken(method?.channel_alias);
  if (configured) return configured;

  const provider = inferPaymentProvider(method?.type, method?.provider);
  if (provider === 'xorpay') return 'qr';
  if (provider === 'stripe' || provider === 'creem') return 'checkout';
  return 'redirect';
}

export function normalizePaymentMethod(method) {
  const configuredRouteId = getConfiguredRouteId(method);
  const rawMinTopup = Number(method?.min_topup);
  const checkoutMode = ['quote', 'product', 'option', 'direct'].includes(
    method?.checkout_mode,
  )
    ? method.checkout_mode
    : 'quote';
  const currency = String(method?.currency || '')
    .trim()
    .toUpperCase();
  return {
    route_id: configuredRouteId || createOpaqueRouteId(method),
    public_method: getPublicPaymentMethod(method),
    channel_alias: getPublicPaymentChannel(method),
    checkout_mode: checkoutMode,
    min_topup:
      Number.isSafeInteger(rawMinTopup) && rawMinTopup > 0 ? rawMinTopup : 0,
    ...(currency.match(/^[A-Z]{3}$/) ? { currency } : {}),
    ...(typeof method?.option_id === 'string'
      ? { option_id: method.option_id }
      : {}),
    ...(typeof method?.public_label === 'string'
      ? { public_label: method.public_label }
      : {}),
  };
}

export function getPaymentSelectionId(method) {
  const optionId = String(method?.option_id || '').trim();
  return /^option_[a-f0-9]{24}$/.test(optionId)
    ? optionId
    : getPaymentRouteId(method);
}

const PUBLIC_METHOD_LABELS = {
  alipay: '支付宝',
  wechat_pay: '微信支付',
  card: '银行卡支付',
  creem: 'Creem',
  waffo: 'Waffo',
  waffo_pancake: 'Waffo Pancake',
  payment: '在线支付',
};

const PUBLIC_CHANNEL_LABELS = {
  qr: '扫码支付',
  native: '扫码支付',
  jsapi: '微信内支付',
  wechat_browser: '微信内支付',
  redirect: '网页支付',
  checkout: '网页支付',
};

export function getPublicPaymentMethodLabel(method, t, methods = []) {
  const normalized = normalizePaymentMethod(method);
  const methodKey =
    PUBLIC_METHOD_LABELS[normalized.public_method] || '在线支付';
  const methodLabel = t(methodKey);
  const peers = (methods || [])
    .map(normalizePaymentMethod)
    .filter(
      (candidate) => candidate.public_method === normalized.public_method,
    );
  if (peers.length <= 1) return methodLabel;

  const sameChannelCount = peers.filter(
    (candidate) => candidate.channel_alias === normalized.channel_alias,
  ).length;
  const channelKey = PUBLIC_CHANNEL_LABELS[normalized.channel_alias];
  if (channelKey && sameChannelCount === 1) {
    return t('{{method}}（{{channel}}）', {
      method: methodLabel,
      channel: t(channelKey),
    });
  }

  const routeIndex = Math.max(
    0,
    peers.findIndex((candidate) => candidate.route_id === normalized.route_id),
  );
  return t('{{method}}（支付通道 {{index}}）', {
    method: methodLabel,
    index: routeIndex + 1,
  });
}

export function getPaymentQuoteRoutePayload(method) {
  const normalized = normalizePaymentMethod(method);
  return { route_id: normalized.route_id };
}

export function getSafeUserPaymentError(
  error,
  t,
  fallbackKey = '支付服务暂时不可用，请稍后重试',
) {
  const status = Number(error?.response?.status || 0);
  const responsePayload = error?.response?.data;
  const code = normalizeToken(
    responsePayload?.code ||
      responsePayload?.error_code ||
      (typeof responsePayload?.data === 'string' ? responsePayload.data : '') ||
      (typeof responsePayload?.message === 'string' &&
      responsePayload.message.startsWith('payment_')
        ? responsePayload.message
        : ''),
  );

  const messagesByCode = {
    payment_request_invalid: '支付信息无效，请重新发起支付',
    payment_method_unavailable: '当前支付方式暂时不可用，请重新选择',
    payment_product_unavailable: '当前支付方式暂时不可用，请重新选择',
    payment_compliance_required:
      'Payment is temporarily unavailable. Try again later or contact support.',
    payment_redirect_invalid: '支付链接暂不可用',
    payment_amount_invalid: '支付金额不符合要求，请重新输入',
    payment_amount_below_minimum: '支付金额低于当前支付方式的最低限额',
    payment_single_limit_exceeded: '支付金额超过当前支付方式的单笔限额',
    payment_daily_limit_exceeded: '当前支付方式今日可用额度不足，请稍后再试',
    payment_quote_expired: '支付报价已过期，请重新发起支付',
    payment_quote_not_found: '支付报价已失效，请重新发起支付',
    payment_quote_consumed: '该支付报价已使用，请查看订单状态',
    payment_quote_limit_reached: '进行中的支付尝试过多，请稍后再试',
    payment_order_limit_reached:
      '进行中的支付订单过多，请先完成或等待现有订单过期',
    payment_request_conflict: '支付请求与之前的操作冲突，请查看订单状态',
    payment_configuration_changed: '支付设置已更新，请重新发起支付',
    payment_requires_support: '该订单需要客服协助，请保留订单编号',
    payment_account_unavailable: '当前账户暂时无法发起支付，请联系客服',
    payment_confirmation_pending: '支付正在确认，请稍后查看订单状态',
    payment_order_expired: '支付订单已过期，请重新发起支付',
    payment_order_not_found: '未找到该支付订单',
    payment_not_ready: '支付尚未准备完成，请稍后刷新',
    payment_temporarily_unavailable: '支付服务暂时不可用，请稍后重试',
  };
  if (messagesByCode[code]) return t(messagesByCode[code]);

  if (status === 429 || code.includes('limit') || code.includes('rate')) {
    return t('支付请求过于频繁，请稍后再试');
  }
  if (status === 409 || code.includes('conflict')) {
    return t('订单状态已变化，请刷新后重试');
  }
  if (
    status === 400 ||
    status === 422 ||
    code.includes('invalid') ||
    code.includes('unavailable')
  ) {
    return t('当前支付方式暂时不可用，请重新选择');
  }
  if (!error?.response || status === 502 || status === 503 || status === 504) {
    return t('支付服务暂时不可用，请稍后重试');
  }
  return t(fallbackKey);
}

const PUBLIC_PAYMENT_STATUS_MAP = {
  preparing: 'preparing',
  queued: 'preparing',
  creating: 'preparing',
  processing: 'preparing',
  awaiting_payment: 'awaiting_payment',
  pending: 'awaiting_payment',
  ready: 'awaiting_payment',
  new: 'awaiting_payment',
  confirming: 'confirming',
  paid: 'confirming',
  succeeded: 'succeeded',
  success: 'succeeded',
  fulfilled: 'succeeded',
  completed: 'succeeded',
  expired: 'expired',
  temporarily_unavailable: 'temporarily_unavailable',
  failed: 'temporarily_unavailable',
  manual_review: 'temporarily_unavailable',
  disputed: 'temporarily_unavailable',
  debt: 'temporarily_unavailable',
};

export function normalizePublicPaymentStatus(order) {
  const status = normalizeToken(order?.status_code || order?.status);
  return PUBLIC_PAYMENT_STATUS_MAP[status] || 'temporarily_unavailable';
}

function normalizePaymentDecimal(value, fallback = '0') {
  const candidate =
    typeof value === 'string' ? value.trim() : String(value ?? '');
  return /^(?:0|[1-9]\d{0,15})(?:\.\d{1,3})?$/.test(candidate)
    ? candidate
    : fallback;
}

function normalizePaymentCurrency(value, fallback = 'USD') {
  const currency = String(value || '')
    .trim()
    .toUpperCase();
  return /^[A-Z]{3}$/.test(currency) ? currency : fallback;
}

function legacyPaymentMinorToDecimal(value, currency, provider) {
  const minor = Number(value);
  if (!Number.isSafeInteger(minor) || minor < 0) return '0';
  const formatter = getCurrencyFormatter(currency, provider);
  const digits = formatter.resolvedOptions().maximumFractionDigits;
  return (minor / 10 ** digits).toFixed(digits);
}

function normalizePublicCheckout(value) {
  if (!value || typeof value !== 'object') return undefined;
  const flow = normalizeToken(value.flow);
  if (
    ![
      'pending',
      'qr',
      'hosted_redirect',
      'form_post',
      'wechat_authorize',
      'jsapi',
    ].includes(flow)
  ) {
    return undefined;
  }

  const checkout = {
    flow,
    expires_at: Number(value.expires_at) || 0,
  };
  if (flow === 'qr' && typeof value.qr_content === 'string') {
    checkout.qr_content = value.qr_content;
  }
  if (
    ['hosted_redirect', 'form_post', 'wechat_authorize'].includes(flow) &&
    typeof value.continue_url === 'string'
  ) {
    checkout.continue_url = value.continue_url;
  }
  if (flow === 'jsapi' && isSafeJSAPIParameters(value.jsapi)) {
    checkout.jsapi = {
      app_id: value.jsapi.app_id,
      timestamp: value.jsapi.timestamp,
      nonce_str: value.jsapi.nonce_str,
      package: value.jsapi.package,
      sign_type: value.jsapi.sign_type,
      pay_sign: value.jsapi.pay_sign,
    };
  }
  return checkout;
}

export function normalizePublicPaymentOrder(value) {
  if (!value || typeof value !== 'object') return null;
  const tradeNo = String(value.trade_no || '').trim();
  if (!tradeNo || tradeNo.length > 128) return null;

  const provider = inferPaymentProvider(
    value.payment_method,
    value.provider || value.payment_provider,
  );
  const currency = normalizePaymentCurrency(value.currency);
  const isSubscription =
    value.plan_id !== undefined || value.order_kind === 'subscription';
  return {
    trade_no: tradeNo,
    route_id: getPaymentRouteId(value),
    public_method: getPublicPaymentMethod(value),
    channel_alias: getPublicPaymentChannel(value),
    status_code: normalizePublicPaymentStatus(value),
    payment_amount: normalizePaymentDecimal(
      value.payment_amount,
      legacyPaymentMinorToDecimal(
        value.expected_amount_minor,
        currency,
        provider,
      ),
    ),
    ...(isSubscription
      ? {
          plan_id: Number(value.plan_id ?? value.requested_amount) || undefined,
        }
      : {
          top_up_amount:
            Number(value.top_up_amount ?? value.requested_amount) || undefined,
        }),
    currency,
    expires_at: Number(value.expires_at) || 0,
    completed_at: Number(value.completed_at ?? value.settled_at) || undefined,
    checkout: normalizePublicCheckout(value.checkout),
  };
}

export function normalizePublicPaymentQuote(value, method, requestedAmount) {
  if (!value || typeof value !== 'object') return null;
  const quoteId = String(value.quote_id || '').trim();
  if (!quoteId || quoteId.length > 128) return null;
  const normalizedMethod = normalizePaymentMethod(method || {});
  const currency = normalizePaymentCurrency(
    value.currency || normalizedMethod.currency,
  );
  return {
    quote_id: quoteId,
    route_id: getPaymentRouteId({
      route_id: value.route_id || normalizedMethod.route_id,
      public_method: value.public_method || normalizedMethod.public_method,
    }),
    public_method: getPublicPaymentMethod({
      public_method: value.public_method || normalizedMethod.public_method,
    }),
    channel_alias: getPublicPaymentChannel({
      channel_alias: value.channel_alias || normalizedMethod.channel_alias,
    }),
    top_up_amount:
      Number(
        value.top_up_amount ?? value.requested_amount ?? requestedAmount,
      ) || undefined,
    payable_amount: normalizePaymentDecimal(value.payable_amount),
    currency,
    expires_at: Number(value.expires_at) || 0,
    ...(value.legacy === true ? { legacy: true } : {}),
  };
}

export function normalizePublicTopupRecord(value) {
  if (!value || typeof value !== 'object') return null;
  const tradeNo = String(value.trade_no || '').trim();
  if (!tradeNo || tradeNo.length > 255) return null;

  const provider = inferPaymentProvider(
    value.payment_method,
    value.provider || value.payment_provider,
  );
  const currency = normalizePaymentCurrency(value.currency, 'CNY');
  const money = Number(value.money);
  const legacyAmount = Number.isFinite(money)
    ? normalizePaymentDecimal(money.toFixed(2))
    : '0';
  return {
    id: Number(value.id) || 0,
    amount: Number(value.amount) || 0,
    payment_amount: normalizePaymentDecimal(
      value.payment_amount,
      value.expected_amount_minor !== undefined
        ? legacyPaymentMinorToDecimal(
            value.expected_amount_minor,
            currency,
            provider,
          )
        : legacyAmount,
    ),
    trade_no: tradeNo,
    route_id: getPaymentRouteId(value),
    public_method: getPublicPaymentMethod(value),
    channel_alias: getPublicPaymentChannel(value),
    currency,
    status_code: normalizePublicPaymentStatus(value),
    created_at: Number(value.created_at ?? value.create_time) || 0,
    completed_at:
      Number(value.completed_at ?? value.complete_time) || undefined,
  };
}

function parsePublicArray(value) {
  if (Array.isArray(value)) return value;
  if (typeof value !== 'string') return [];
  try {
    const parsed = JSON.parse(value);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function normalizePublicAmountOptions(value) {
  return parsePublicArray(value)
    .map(Number)
    .filter(
      (amount) =>
        Number.isSafeInteger(amount) && amount >= 1 && amount <= 10000,
    );
}

function normalizePublicDiscounts(value) {
  let source = value;
  if (typeof value === 'string') {
    try {
      source = JSON.parse(value);
    } catch {
      return {};
    }
  }
  if (!source || typeof source !== 'object' || Array.isArray(source)) {
    return {};
  }
  return Object.entries(source).reduce((result, [key, discount]) => {
    const amount = Number(key);
    const normalizedDiscount = Number(discount);
    if (
      Number.isSafeInteger(amount) &&
      amount >= 1 &&
      amount <= 10000 &&
      Number.isFinite(normalizedDiscount) &&
      normalizedDiscount > 0
    ) {
      result[amount] = normalizedDiscount;
    }
    return result;
  }, {});
}

export function normalizePublicTopupInfo(value) {
  if (!value || typeof value !== 'object') return null;

  const paymentRoutes = parsePublicArray(
    value.payment_routes ?? value.pay_methods,
  )
    .filter((method) => getConfiguredRouteId(method))
    .map(normalizePaymentMethod);
  const paymentProducts = parsePublicArray(value.payment_products)
    .map((product) => {
      const productId = String(product?.product_id || '').trim();
      const routeId = getConfiguredRouteId({ route_id: product?.route_id });
      const name = String(product?.name || '').trim();
      const paymentAmount = normalizePaymentDecimal(
        product?.payment_amount,
        '',
      );
      const topUpAmount = Number(product?.top_up_amount);
      const currency = normalizePaymentCurrency(product?.currency, '');
      if (
        !/^product_[a-f0-9]{24}$/.test(productId) ||
        !routeId ||
        !name ||
        name.length > 128 ||
        !paymentAmount ||
        !Number.isSafeInteger(topUpAmount) ||
        topUpAmount <= 0 ||
        !currency
      ) {
        return null;
      }
      return {
        product_id: productId,
        route_id: routeId,
        name,
        payment_amount: paymentAmount,
        top_up_amount: topUpAmount,
        currency,
      };
    })
    .filter(Boolean);
  const publicOptionLabels = new Set([
    'Card',
    'Apple Pay',
    'Google Pay',
    'Online payment',
  ]);
  const paymentRouteOptions = parsePublicArray(value.payment_route_options)
    .map((option) => {
      const optionId = String(option?.option_id || '').trim();
      const routeId = getConfiguredRouteId({ route_id: option?.route_id });
      const publicLabel = String(option?.public_label || '').trim();
      if (
        !/^option_[a-f0-9]{24}$/.test(optionId) ||
        !routeId ||
        !publicOptionLabels.has(publicLabel)
      ) {
        return null;
      }
      return {
        option_id: optionId,
        route_id: routeId,
        public_label: publicLabel,
      };
    })
    .filter(Boolean);
  const minTopup = Number(value.min_topup);
  const affiliateContinuousPercent = Number(value.affiliate_continuous_percent);
  const affiliateFirstTopupPercent = Number(
    value.affiliate_first_topup_percent,
  );

  return {
    online_payment_available:
      value.online_payment_available === true || paymentRoutes.length > 0,
    enable_redemption: value.enable_redemption !== false,
    payment_compliance_confirmed: value.payment_compliance_confirmed !== false,
    payment_compliance_terms_version:
      typeof value.payment_compliance_terms_version === 'string'
        ? value.payment_compliance_terms_version
        : '',
    payment_routes: paymentRoutes,
    payment_products: paymentProducts,
    payment_route_options: paymentRouteOptions,
    min_topup: Number.isSafeInteger(minTopup) && minTopup > 0 ? minTopup : 1,
    amount_options: normalizePublicAmountOptions(value.amount_options),
    discount: normalizePublicDiscounts(value.discount),
    ...(Number.isFinite(affiliateContinuousPercent)
      ? { affiliate_continuous_percent: affiliateContinuousPercent }
      : {}),
    ...(Number.isFinite(affiliateFirstTopupPercent)
      ? { affiliate_first_topup_percent: affiliateFirstTopupPercent }
      : {}),
    ...(typeof value.topup_link === 'string'
      ? { topup_link: value.topup_link }
      : {}),
  };
}

const PUBLIC_PAYMENT_STATUS_LABELS = {
  preparing: '支付准备中',
  awaiting_payment: '等待支付',
  confirming: '确认中',
  succeeded: '支付成功',
  expired: '已过期',
  temporarily_unavailable: '暂时不可用',
};

export function getPublicPaymentStatusLabel(order, t) {
  return t(PUBLIC_PAYMENT_STATUS_LABELS[normalizePublicPaymentStatus(order)]);
}

function isLoopback(hostname) {
  return ['localhost', '127.0.0.1', '[::1]'].includes(hostname);
}

export function getSafePaymentUrl(value) {
  const trimmed = (value || '').trim();
  if (!trimmed) return null;
  try {
    const url = new URL(trimmed, window.location.origin);
    if (url.username || url.password) return null;
    if (url.protocol === 'https:') return url;
    if (
      url.protocol === 'http:' &&
      isLoopback(url.hostname) &&
      isLoopback(window.location.hostname)
    ) {
      return url;
    }
  } catch {
    return null;
  }
  return null;
}

export function getSafePaymentContinueUrl(value, tradeNo) {
  const normalizedTradeNo = String(tradeNo || '').trim();
  if (!normalizedTradeNo || normalizedTradeNo.length > 128) return null;

  try {
    const url = new URL(value, window.location.origin);
    const expectedPath = `/api/user/payment/orders/${encodeURIComponent(normalizedTradeNo)}/continue`;
    if (
      url.origin !== window.location.origin ||
      url.pathname !== expectedPath ||
      url.username ||
      url.password ||
      url.search ||
      url.hash
    ) {
      return null;
    }
    return url;
  } catch {
    return null;
  }
}

export function getSafeWeChatAuthorizationUrl(value, tradeNo) {
  const normalizedTradeNo = String(tradeNo || '').trim();
  if (!normalizedTradeNo || normalizedTradeNo.length > 128) return null;

  try {
    const url = new URL(value, window.location.origin);
    const expectedPath = `/api/user/payment/orders/${encodeURIComponent(normalizedTradeNo)}/wechat-authorize`;
    if (
      url.origin !== window.location.origin ||
      url.pathname !== expectedPath ||
      url.username ||
      url.password ||
      url.search ||
      url.hash
    ) {
      return null;
    }
    return url;
  } catch {
    return null;
  }
}

export function isSafeJSAPIParameters(parameters) {
  return Boolean(
    parameters &&
    /^wx[A-Za-z0-9]{16}$/.test(parameters.app_id) &&
    /^\d{1,16}$/.test(parameters.timestamp) &&
    typeof parameters.nonce_str === 'string' &&
    parameters.nonce_str.length > 0 &&
    parameters.nonce_str.length <= 128 &&
    !/[\r\n\0]/.test(parameters.nonce_str) &&
    typeof parameters.package === 'string' &&
    parameters.package.startsWith('prepay_id=') &&
    parameters.package.length <= 256 &&
    !/[\r\n\0]/.test(parameters.package) &&
    ['MD5', 'HMAC-SHA256'].includes(parameters.sign_type) &&
    typeof parameters.pay_sign === 'string' &&
    parameters.pay_sign.length >= 16 &&
    parameters.pay_sign.length <= 256 &&
    !/[\r\n\0]/.test(parameters.pay_sign),
  );
}

export function detectPaymentBrowserEnvironment(
  userAgent = typeof navigator === 'undefined' ? '' : navigator.userAgent,
) {
  if (/MicroMessenger/i.test(userAgent)) return 'wechat';
  if (/Android|iPhone|iPad|iPod|Mobile/i.test(userAgent)) return 'mobile';
  return 'desktop';
}

export function filterPaymentMethodsForBrowser(
  methods = [],
  environment = detectPaymentBrowserEnvironment(),
) {
  const normalizedMethods = methods.map(normalizePaymentMethod);
  const wechatJSAPIGroups = new Set(
    normalizedMethods
      .filter(
        (method) =>
          method.public_method === 'wechat_pay' &&
          ['wechat_browser', 'jsapi'].includes(method.channel_alias),
      )
      .map((method) => method.public_method),
  );

  return normalizedMethods.filter((method) => {
    if (method.public_method !== 'wechat_pay') return true;

    const isJSAPI = ['wechat_browser', 'jsapi'].includes(method.channel_alias);
    if (environment !== 'wechat') return !isJSAPI;

    const isNative = ['qr', 'native'].includes(method.channel_alias);
    return !(isNative && wechatJSAPIGroups.has(method.public_method));
  });
}

export function getSafePaymentIconUrl(value) {
  const trimmed = (value || '').trim();
  if (!trimmed) return null;

  if (
    /^data:image\/(?:png|jpe?g|gif|webp);base64,[a-z0-9+/=\s]+$/i.test(
      trimmed,
    ) &&
    trimmed.length <= 150 * 1024
  ) {
    return trimmed;
  }

  if (!/^https:\/\//i.test(trimmed)) return null;
  try {
    const url = new URL(trimmed);
    if (url.protocol !== 'https:' || url.username || url.password) return null;
    return url.href;
  } catch {
    return null;
  }
}

export function navigateToPaymentUrl(value) {
  const url = getSafePaymentUrl(value);
  if (!url) return false;
  window.location.assign(url.href);
  return true;
}

export function submitPaymentForm(action, fields) {
  const url = getSafePaymentUrl(action);
  if (!url) return false;
  const form = document.createElement('form');
  form.action = url.href;
  form.method = 'POST';
  form.target = '_self';
  form.referrerPolicy = 'no-referrer';
  Object.entries(fields || {}).forEach(([key, value]) => {
    const input = document.createElement('input');
    input.type = 'hidden';
    input.name = key;
    input.value = String(value);
    form.appendChild(input);
  });
  document.body.appendChild(form);
  form.submit();
  document.body.removeChild(form);
  return true;
}

export function createPaymentRequestId() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  return `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export function isEndpointUnavailable(error) {
  return [404, 405, 501].includes(error?.response?.status);
}

export function isSafeQrContent(value) {
  const trimmed = (value || '').trim();
  if (!trimmed || trimmed.length > 4096) return false;
  if (trimmed.startsWith('weixin://wxpay/')) return true;
  try {
    const url = new URL(trimmed);
    return (
      url.protocol === 'https:' &&
      url.hostname.toLowerCase() === 'qr.alipay.com' &&
      !url.username &&
      !url.password &&
      (!url.port || url.port === '443')
    );
  } catch {
    return false;
  }
}

function getCurrencyFormatter(currency, provider) {
  const normalized = /^[A-Z]{3}$/.test((currency || '').toUpperCase())
    ? currency.toUpperCase()
    : 'USD';
  const normalizedPaymentContext = (provider || '').trim().toLowerCase();
  const stripeUsesTwoDecimalChargeAmount =
    ['stripe', 'card'].includes(normalizedPaymentContext) &&
    ['ISK', 'UGX'].includes(normalized);
  const options = {
    style: 'currency',
    currency: normalized,
    ...(stripeUsesTwoDecimalChargeAmount
      ? { minimumFractionDigits: 2, maximumFractionDigits: 2 }
      : {}),
  };
  try {
    return new Intl.NumberFormat(undefined, options);
  } catch {
    return new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency: 'USD',
    });
  }
}

export function formatPaymentDecimal(amount, currency, provider) {
  return getCurrencyFormatter(currency, provider).format(Number(amount) || 0);
}

export function formatPaymentMinor(amountMinor, currency, provider) {
  const formatter = getCurrencyFormatter(currency, provider);
  const digits = formatter.resolvedOptions().maximumFractionDigits;
  return formatter.format((Number(amountMinor) || 0) / 10 ** digits);
}
