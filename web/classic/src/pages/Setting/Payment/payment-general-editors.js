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

export const PAYMENT_PROVIDER_ORDER = [
  'epay',
  'xorpay',
  'stripe',
  'creem',
  'waffo',
  'waffo_pancake',
];

export const PAYMENT_PROVIDER_LABELS = {
  epay: 'Epay',
  xorpay: 'XORPay',
  stripe: 'Stripe',
  creem: 'Creem',
  waffo: 'Waffo',
  waffo_pancake: 'Waffo Pancake',
};

const PAYMENT_METHOD_TYPE_PATTERN = /^[A-Za-z0-9_-]{1,64}$/;
const MAX_CONFIGURED_PAYMENT_METHODS = 27;
const ALLOWED_PROVIDERS = new Set(PAYMENT_PROVIDER_ORDER);
const EPAY_RESERVED_TYPES = new Set([
  'stripe',
  'xorpay_native',
  'xorpay_alipay',
  'xorpay_jsapi',
  'waffo_pancake',
]);

const PAYMENT_TYPE_DEFAULTS = {
  epay: {
    name: 'Alipay',
    type: 'alipay',
    icon: 'SiAlipay',
  },
  xorpay: {
    name: 'Alipay',
    type: 'xorpay_alipay',
    icon: 'SiAlipay',
  },
  stripe: {
    name: 'Stripe',
    type: 'stripe',
    icon: 'SiStripe',
  },
  creem: {
    name: 'Online payment',
    type: 'creem',
    icon: 'LuCreditCard',
  },
  waffo: {
    name: 'Online payment',
    type: 'waffo',
    icon: 'LuCreditCard',
  },
  waffo_pancake: {
    name: 'Waffo Pancake',
    type: 'waffo_pancake',
    icon: 'LuCreditCard',
  },
};

export const PAYMENT_TYPE_OPTIONS = {
  xorpay: [
    {
      labelKey: 'XORPay Alipay',
      name: 'Alipay',
      type: 'xorpay_alipay',
      icon: 'SiAlipay',
    },
    {
      labelKey: 'XORPay WeChat Native',
      name: 'WeChat Pay',
      type: 'xorpay_native',
      icon: 'SiWechat',
    },
    {
      labelKey: 'XORPay WeChat in-app (JSAPI)',
      name: 'WeChat Pay',
      type: 'xorpay_jsapi',
      icon: 'SiWechat',
    },
  ],
  stripe: [
    {
      labelKey: 'Stripe one-time Checkout',
      name: 'Stripe',
      type: 'stripe',
      icon: 'SiStripe',
    },
  ],
  creem: [
    {
      labelKey: 'Creem',
      name: 'Online payment',
      type: 'creem',
      icon: 'LuCreditCard',
    },
  ],
  waffo: [
    {
      labelKey: 'Waffo',
      name: 'Online payment',
      type: 'waffo',
      icon: 'LuCreditCard',
    },
  ],
  waffo_pancake: [
    {
      labelKey: 'Waffo Pancake',
      name: 'Waffo Pancake',
      type: 'waffo_pancake',
      icon: 'LuCreditCard',
    },
  ],
};

function parseJson(value, fallback) {
  if (typeof value !== 'string' || value.trim() === '') {
    return { data: fallback, error: null };
  }
  try {
    return { data: JSON.parse(value), error: null };
  } catch {
    return { data: fallback, error: 'invalid_json' };
  }
}

function inferPaymentProvider(type) {
  if (type === 'stripe') return 'stripe';
  if (type === 'creem') return 'creem';
  if (type === 'waffo') return 'waffo';
  if (type === 'waffo_pancake') return 'waffo_pancake';
  if (type.startsWith('xorpay_')) return 'xorpay';
  return 'epay';
}

function normalizePaymentProvider(provider, type) {
  const normalized = String(provider || '')
    .trim()
    .toLowerCase();
  return ALLOWED_PROVIDERS.has(normalized)
    ? normalized
    : inferPaymentProvider(type);
}

export function getDefaultPaymentMethod(provider = 'epay') {
  const normalizedProvider = ALLOWED_PROVIDERS.has(provider)
    ? provider
    : 'epay';
  return {
    provider: normalizedProvider,
    ...PAYMENT_TYPE_DEFAULTS[normalizedProvider],
    min_topup: '',
  };
}

export function getPaymentMethodIdentity(method) {
  const provider = normalizePaymentProvider(
    method?.provider,
    method?.type || '',
  );
  const rawType = String(method?.type || '').trim();
  const type = provider === 'epay' ? rawType : rawType.toLowerCase();
  return `${provider}\u0000${type}`;
}

export function parsePaymentMethods(value) {
  const parsed = parseJson(value, []);
  if (parsed.error) return { items: [], error: parsed.error };
  if (!Array.isArray(parsed.data)) {
    return { items: [], error: 'payment_methods_not_array' };
  }

  const items = [];
  for (const item of parsed.data) {
    if (
      !item ||
      typeof item !== 'object' ||
      Array.isArray(item) ||
      typeof item.name !== 'string' ||
      typeof item.type !== 'string'
    ) {
      return { items: [], error: 'invalid_payment_method' };
    }
    const type = item.type.trim();
    items.push({
      ...item,
      name: item.name.trim(),
      type,
      provider: normalizePaymentProvider(item.provider, type),
      min_topup:
        item.min_topup === undefined || item.min_topup === null
          ? ''
          : String(item.min_topup).trim(),
    });
  }
  return { items, error: null };
}

export function validatePaymentMethodDraft(draft, methods, editIndex = -1) {
  const provider = normalizePaymentProvider(draft?.provider, draft?.type || '');
  const name = String(draft?.name || '').trim();
  const type = String(draft?.type || '').trim();
  const minimum = String(draft?.min_topup || '').trim();

  if (!name || name.length > 128) return 'invalid_payment_method_name';
  if (!PAYMENT_METHOD_TYPE_PATTERN.test(type)) {
    return 'invalid_payment_method_type';
  }
  if (
    (provider === 'epay' && EPAY_RESERVED_TYPES.has(type)) ||
    (provider === 'stripe' && type !== 'stripe') ||
    (provider === 'creem' && type !== 'creem') ||
    (provider === 'waffo' && type !== 'waffo') ||
    (provider === 'xorpay' &&
      !['xorpay_alipay', 'xorpay_native', 'xorpay_jsapi'].includes(type)) ||
    (provider === 'waffo_pancake' && type !== 'waffo_pancake')
  ) {
    return 'payment_method_provider_mismatch';
  }
  if (minimum) {
    const amount = Number(minimum);
    if (!Number.isSafeInteger(amount) || amount < 1 || amount > 10000) {
      return 'invalid_payment_method_minimum';
    }
  }
  const identity = getPaymentMethodIdentity({ provider, type });
  if (
    methods.some(
      (method, index) =>
        index !== editIndex && getPaymentMethodIdentity(method) === identity,
    )
  ) {
    return 'duplicate_payment_method';
  }
  if (editIndex < 0 && methods.length >= MAX_CONFIGURED_PAYMENT_METHODS) {
    return 'too_many_payment_methods';
  }
  return null;
}

export function upsertPaymentMethod(methods, editIndex, draft) {
  const provider = normalizePaymentProvider(draft.provider, draft.type);
  const type = String(draft.type).trim();
  const existing = editIndex >= 0 ? methods[editIndex] : null;
  const identityChanged =
    existing &&
    getPaymentMethodIdentity(existing) !==
      getPaymentMethodIdentity({ provider, type });
  const method = {
    ...(existing || {}),
    name: String(draft.name).trim(),
    type,
    provider,
  };

  if (!existing && draft.icon) method.icon = draft.icon;
  if (String(draft.min_topup || '').trim()) {
    method.min_topup = String(draft.min_topup).trim();
  } else {
    delete method.min_topup;
  }
  if (identityChanged) {
    delete method.flow;
    delete method.route_id;
    delete method.public_method;
    delete method.channel_alias;
    method.icon = draft.icon || PAYMENT_TYPE_DEFAULTS[provider].icon;
  }

  const next = [...methods];
  if (editIndex >= 0) next[editIndex] = method;
  else next.push(method);
  return next;
}

export function serializeJson(value) {
  return JSON.stringify(value, null, 2);
}

export function parseTopupGroupRatios(value) {
  const parsed = parseJson(value, {});
  if (parsed.error) return { ratios: {}, error: parsed.error };
  if (
    !parsed.data ||
    typeof parsed.data !== 'object' ||
    Array.isArray(parsed.data)
  ) {
    return { ratios: {}, error: 'group_ratios_not_object' };
  }
  for (const [group, ratio] of Object.entries(parsed.data)) {
    if (
      !group.trim() ||
      group.length > 64 ||
      typeof ratio !== 'number' ||
      !Number.isFinite(ratio) ||
      ratio <= 0 ||
      ratio > 1000
    ) {
      return { ratios: {}, error: 'invalid_group_ratio' };
    }
  }
  return { ratios: parsed.data, error: null };
}

export function parseAmountOptions(value) {
  const parsed = parseJson(value, []);
  if (parsed.error) return { amounts: [], error: parsed.error };
  if (!Array.isArray(parsed.data)) {
    return { amounts: [], error: 'amount_options_not_array' };
  }
  if (
    parsed.data.some(
      (amount) => !Number.isSafeInteger(amount) || amount < 1 || amount > 10000,
    )
  ) {
    return { amounts: [], error: 'invalid_amount_option' };
  }
  return {
    amounts: [...new Set(parsed.data)].sort((left, right) => left - right),
    error: null,
  };
}

export function parseAmountDiscounts(value) {
  const parsed = parseJson(value, {});
  if (parsed.error) return { discounts: {}, error: parsed.error };
  if (
    !parsed.data ||
    typeof parsed.data !== 'object' ||
    Array.isArray(parsed.data)
  ) {
    return { discounts: {}, error: 'amount_discounts_not_object' };
  }
  for (const [amount, rate] of Object.entries(parsed.data)) {
    const numericAmount = Number(amount);
    if (
      !Number.isSafeInteger(numericAmount) ||
      numericAmount < 1 ||
      numericAmount > 10000 ||
      typeof rate !== 'number' ||
      !Number.isFinite(rate) ||
      rate <= 0 ||
      rate > 1
    ) {
      return { discounts: {}, error: 'invalid_amount_discount' };
    }
  }
  return { discounts: parsed.data, error: null };
}
