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

const MAX_INT64 = 9223372036854775807n;

export const buildStripeCheckoutAllowedHostsUpdate = (value) => ({
  key: 'StripeCheckoutAllowedHosts',
  value: String(value || '').trim(),
});

export const getPaymentCurrencyExponent = (provider, currency) => {
  const normalized = String(currency || '')
    .trim()
    .toUpperCase();
  if (!/^[A-Z]{3}$/.test(normalized)) return 2;
  if (provider === 'stripe' && ['ISK', 'UGX'].includes(normalized)) return 2;
  try {
    return new Intl.NumberFormat('en', {
      style: 'currency',
      currency: normalized,
    }).resolvedOptions().maximumFractionDigits;
  } catch {
    return 2;
  }
};

export const paymentMajorToMinor = (value, exponent) => {
  const normalized = String(value || '').trim();
  if (!normalized || !/^\d+(?:\.\d+)?$/.test(normalized)) return null;
  const [whole, fraction = ''] = normalized.split('.');
  if (fraction.length > exponent) return null;
  const digits =
    (whole.replace(/^0+(?=\d)/, '') || '0') + fraction.padEnd(exponent, '0');
  try {
    const minor = BigInt(digits);
    if (minor < 0n || minor > MAX_INT64) return null;
    return minor.toString();
  } catch {
    return null;
  }
};

export const paymentMinorToMajor = (value, exponent) => {
  let minor;
  try {
    minor = BigInt(value || '0');
  } catch {
    return '0';
  }
  if (exponent === 0) return minor.toString();
  const negative = minor < 0n;
  const digits = (negative ? -minor : minor)
    .toString()
    .padStart(exponent + 1, '0');
  const whole = digits.slice(0, -exponent);
  const fraction = digits.slice(-exponent).replace(/0+$/, '');
  return `${negative ? '-' : ''}${whole}${fraction ? `.${fraction}` : ''}`;
};

export const getPaymentLimitUsagePercent = (limit) => {
  try {
    const daily = BigInt(limit?.daily_limit_minor || '0');
    if (daily <= 0n) return 0;
    const used =
      BigInt(limit?.paid_minor || '0') + BigInt(limit?.reserved_minor || '0');
    const basisPoints = (used * 10000n) / daily;
    return Math.min(100, Math.max(0, Number(basisPoints) / 100));
  } catch {
    return 0;
  }
};

export const formatPaymentAge = (seconds, locale) => {
  const value = Number(seconds);
  if (!Number.isFinite(value) || value <= 0) return '—';
  let unit = 'second';
  let amount = value;
  if (value >= 86400) {
    unit = 'day';
    amount = value / 86400;
  } else if (value >= 3600) {
    unit = 'hour';
    amount = value / 3600;
  } else if (value >= 60) {
    unit = 'minute';
    amount = value / 60;
  }
  return new Intl.NumberFormat(locale, {
    style: 'unit',
    unit,
    unitDisplay: 'short',
    maximumFractionDigits: 0,
  }).format(amount);
};
