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

export function normalizePaymentMethod(method) {
  return {
    ...method,
    provider: inferPaymentProvider(method?.type, method?.provider),
  };
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
  const stripeUsesTwoDecimalChargeAmount =
    (provider || '').trim().toLowerCase() === 'stripe' &&
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
