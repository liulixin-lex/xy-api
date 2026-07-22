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

import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Banner,
  Button,
  Card,
  Input,
  Progress,
  Select,
  Spin,
  Switch,
  Tag,
} from '@douyinfe/semi-ui';
import { Info, Pencil, Plus, ShieldCheck } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { showError, showSuccess, showWarning } from '../../../helpers';
import { listPaymentLimits, updatePaymentLimit } from './payment-admin-api';
import { getPaymentAdminErrorMessage } from '../../../helpers/payment-admin-errors';
import {
  getPaymentCurrencyExponent,
  getPaymentLimitUsagePercent,
  paymentMajorToMinor,
  paymentMinorToMajor,
} from './payment-admin-utils';

const EMPTY_DRAFT = {
  provider: 'xorpay',
  paymentMethod: 'xorpay_alipay',
  currency: 'CNY',
  singleLimit: '',
  dailyLimit: '',
  timezone: 'Asia/Shanghai',
  enabled: true,
};

const PAYMENT_PROVIDERS = [
  { value: 'xorpay', label: 'XORPay' },
  { value: 'stripe', label: 'Stripe' },
  { value: 'epay', label: 'Epay' },
  { value: 'creem', label: 'Creem' },
  { value: 'waffo', label: 'Waffo' },
  { value: 'waffo_pancake', label: 'Waffo Pancake' },
];

const MERCHANT_TIMEZONES = [
  'UTC',
  'Asia/Shanghai',
  'Asia/Tokyo',
  'Asia/Ho_Chi_Minh',
  'Europe/Paris',
  'Europe/Moscow',
  'America/New_York',
];

const getPaymentMethodLabel = (limit, t) => {
  if (limit.provider === 'xorpay' && limit.payment_method === 'xorpay_alipay') {
    return t('XORPay: Alipay face-to-face');
  }
  if (limit.provider === 'xorpay' && limit.payment_method === 'xorpay_native') {
    return t('XORPay: WeChat Native');
  }
  if (limit.provider === 'xorpay' && limit.payment_method === 'xorpay_jsapi') {
    return t('XORPay: WeChat JSAPI');
  }
  if (limit.provider === 'stripe') return t('Stripe: one-time Checkout');
  if (limit.provider === 'creem') return 'Creem';
  if (limit.provider === 'waffo') return 'Waffo';
  if (limit.provider === 'waffo_pancake') return 'Waffo Pancake';
  return `${t('Epay')}: ${limit.payment_method}`;
};

const isValidTimeZone = (value) => {
  try {
    new Intl.DateTimeFormat('en', { timeZone: value.trim() }).format();
    return true;
  } catch {
    return false;
  }
};

const PaymentLimitsPanel = ({ withPaymentVerification }) => {
  const { t, i18n } = useTranslation();
  const [limits, setLimits] = useState([]);
  const [draft, setDraft] = useState(EMPTY_DRAFT);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [loadError, setLoadError] = useState('');
  const exponent = useMemo(
    () => getPaymentCurrencyExponent(draft.provider, draft.currency),
    [draft.currency, draft.provider],
  );
  const futureOfficialAlipayProducts = [
    t('Official Alipay PC website payment'),
    t('Official Alipay WAP payment'),
    t('Official Alipay JSAPI'),
  ];

  const loadLimits = useCallback(async () => {
    setLoading(true);
    try {
      const data = await listPaymentLimits();
      setLimits(Array.isArray(data) ? data : []);
      setLoadError('');
      return true;
    } catch (requestError) {
      setLoadError(
        getPaymentAdminErrorMessage(
          requestError,
          t,
          t('Payment limits are unavailable'),
        ),
      );
      return false;
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void loadLimits();
  }, [loadLimits]);

  const setProvider = (provider) => {
    if (provider === 'xorpay') {
      setDraft((current) => ({
        ...current,
        provider,
        paymentMethod: 'xorpay_alipay',
        currency: 'CNY',
        timezone: 'Asia/Shanghai',
      }));
      return;
    }
    if (provider === 'stripe') {
      setDraft((current) => ({
        ...current,
        provider,
        paymentMethod: 'stripe',
        currency: 'USD',
      }));
      return;
    }
    if (provider === 'creem' || provider === 'waffo') {
      setDraft((current) => ({
        ...current,
        provider,
        paymentMethod: provider,
        currency: 'USD',
      }));
      return;
    }
    if (provider === 'waffo_pancake') {
      setDraft((current) => ({
        ...current,
        provider,
        paymentMethod: 'waffo_pancake',
        currency: 'USD',
      }));
      return;
    }
    setDraft((current) => ({
      ...current,
      provider,
      paymentMethod: 'alipay',
      currency: 'CNY',
    }));
  };

  const useMerchantReference = (paymentMethod) => {
    const isAlipay = paymentMethod === 'xorpay_alipay';
    setDraft({
      provider: 'xorpay',
      paymentMethod,
      currency: 'CNY',
      singleLimit: isAlipay ? '1999.99' : '20000',
      dailyLimit: isAlipay ? '19999.99' : '50000',
      timezone: 'Asia/Shanghai',
      enabled: true,
    });
  };

  const editLimit = (limit) => {
    setDraft({
      provider: limit.provider,
      paymentMethod: limit.payment_method,
      currency: limit.currency,
      singleLimit: paymentMinorToMajor(
        limit.single_limit_minor,
        limit.currency_exponent,
      ),
      dailyLimit: paymentMinorToMajor(
        limit.daily_limit_minor,
        limit.currency_exponent,
      ),
      timezone: limit.timezone,
      enabled: Boolean(limit.enabled),
    });
    const reduceMotion = window.matchMedia(
      '(prefers-reduced-motion: reduce)',
    ).matches;
    document.querySelector('#classic-payment-limit-editor')?.scrollIntoView({
      behavior: reduceMotion ? 'auto' : 'smooth',
      block: 'start',
    });
  };

  const saveLimit = async () => {
    if (saving) return;
    const paymentMethod = draft.paymentMethod.trim();
    const currency = draft.currency.trim().toUpperCase();
    const timezone = draft.timezone.trim();
    const singleLimitMinor = paymentMajorToMinor(
      draft.singleLimit || '0',
      exponent,
    );
    const dailyLimitMinor = paymentMajorToMinor(
      draft.dailyLimit || '0',
      exponent,
    );
    if (
      !/^[A-Za-z0-9_-]{1,64}$/.test(paymentMethod) ||
      !/^[A-Z]{3}$/.test(currency)
    ) {
      showError(
        t('Enter a valid payment method and three-letter currency code.'),
      );
      return;
    }
    if (!isValidTimeZone(timezone)) {
      showError(t('Enter a valid IANA time zone.'));
      return;
    }
    if (singleLimitMinor === null || dailyLimitMinor === null) {
      showError(
        t(
          'Enter non-negative limits using no more than {{digits}} decimal places.',
          { digits: exponent },
        ),
      );
      return;
    }
    if (
      BigInt(dailyLimitMinor) > 0n &&
      BigInt(singleLimitMinor) > BigInt(dailyLimitMinor)
    ) {
      showError(t('The single-payment limit cannot exceed the daily limit.'));
      return;
    }

    const request = {
      provider: draft.provider,
      payment_method: paymentMethod,
      currency,
      single_limit_minor: singleLimitMinor,
      daily_limit_minor: dailyLimitMinor,
      timezone,
      enabled: draft.enabled,
    };

    setSaving(true);
    try {
      await withPaymentVerification(async () => {
        const result = await updatePaymentLimit(request);
        if (result?.usage) {
          setLimits((current) => {
            const next = current.filter(
              (item) =>
                !(
                  item.provider === result.usage.provider &&
                  item.payment_method === result.usage.payment_method &&
                  item.currency === result.usage.currency
                ),
            );
            return [...next, result.usage].sort((left, right) =>
              `${left.provider}:${left.payment_method}:${left.currency}`.localeCompare(
                `${right.provider}:${right.payment_method}:${right.currency}`,
              ),
            );
          });
        } else {
          await loadLimits();
        }

        if (result?.saved && result?.refreshed) {
          showSuccess(t('Payment limit saved'));
        } else if (result?.saved) {
          showWarning(
            t(
              'Payment limit was saved, but the latest usage could not be refreshed.',
            ),
          );
        } else {
          throw new Error(t('Failed to save payment limit'));
        }
        return result;
      });
    } catch (error) {
      showError(
        getPaymentAdminErrorMessage(
          error,
          t,
          t('Failed to save payment limit'),
        ),
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className='flex flex-col gap-4'>
      <div>
        <h3 className='m-0 text-lg font-semibold'>{t('Payment Limits')}</h3>
        <p className='mt-1 mb-0 text-sm text-gray-500'>
          {t(
            'Configure merchant capacity per gateway, payment method, currency, and merchant day.',
          )}
        </p>
      </div>

      <Banner
        type='info'
        icon={<Info size={16} />}
        title={t('Limits belong to this merchant configuration')}
        description={t(
          'A limit is enforced per gateway, payment method, currency, and merchant day. Active orders reserve capacity; failed or expired orders release it automatically.',
        )}
        closeIcon={null}
      />

      <div className='flex flex-wrap gap-2'>
        <Button onClick={() => useMerchantReference('xorpay_alipay')}>
          {t('Use current XORPay Alipay merchant reference')}
        </Button>
        <Button onClick={() => useMerchantReference('xorpay_native')}>
          {t('Use current XORPay WeChat merchant reference')}
        </Button>
        <Button onClick={() => useMerchantReference('xorpay_jsapi')}>
          {t('Use current XORPay WeChat JSAPI merchant reference')}
        </Button>
      </div>
      <p className='m-0 text-sm text-gray-500'>
        {t(
          'These presets reflect the currently supplied merchant materials and are not universal XORPay limits. Official Alipay PC, WAP, and Alipay JSAPI reference limits remain disabled because those products are not connected.',
        )}
      </p>

      <Card
        title={t('Future official Alipay products (not enabled)')}
        bodyStyle={{ padding: 16 }}
      >
        <p className='mt-0 text-sm text-gray-500'>
          {t(
            'Read-only merchant material for future official Alipay products. These products are not connected and cannot be selected.',
          )}
        </p>
        <div className='flex flex-col gap-2'>
          {futureOfficialAlipayProducts.map((product) => (
            <div
              key={product}
              className='grid gap-3 rounded-lg bg-gray-50 p-3 sm:grid-cols-[minmax(0,1fr)_auto_auto_auto] sm:items-center dark:bg-white/5'
            >
              <div className='text-sm font-medium'>{product}</div>
              <Tag color='grey'>{t('Not enabled')}</Tag>
              <div className='text-sm tabular-nums'>
                {t('Single-payment reference: {{amount}}', {
                  amount: '50 CNY',
                })}
              </div>
              <div className='text-sm tabular-nums'>
                {t('Daily reference: {{amount}}', { amount: '1000 CNY' })}
              </div>
            </div>
          ))}
        </div>
        <p className='mb-0 text-sm text-gray-500'>
          {t(
            'These reference values are not XORPay Alipay face-to-face limits and are not system-wide defaults.',
          )}
        </p>
      </Card>

      <Card
        id='classic-payment-limit-editor'
        title={t('Limit policy')}
        headerExtraContent={
          <Button
            icon={<Plus size={16} />}
            onClick={() => setDraft(EMPTY_DRAFT)}
          >
            {t('New policy')}
          </Button>
        }
        bodyStyle={{ padding: 16 }}
      >
        <p className='mt-0 text-sm text-gray-500'>
          {t(
            'Set zero to leave the single-payment or daily cap unlimited. Amounts are entered in the selected currency, then stored as integer minor units.',
          )}
        </p>
        <div className='grid gap-4 md:grid-cols-2 xl:grid-cols-3'>
          <label className='flex flex-col gap-2 text-sm font-medium'>
            {t('Gateway')}
            <Select
              value={draft.provider}
              optionList={PAYMENT_PROVIDERS.map((item) => ({
                ...item,
                label: t(item.label),
              }))}
              onChange={setProvider}
            />
          </label>
          <label className='flex flex-col gap-2 text-sm font-medium'>
            {t('Payment method')}
            {draft.provider === 'xorpay' ? (
              <Select
                value={draft.paymentMethod}
                optionList={[
                  {
                    value: 'xorpay_alipay',
                    label: t('Alipay face-to-face'),
                  },
                  {
                    value: 'xorpay_native',
                    label: t('WeChat Native'),
                  },
                  {
                    value: 'xorpay_jsapi',
                    label: t('WeChat JSAPI'),
                  },
                ]}
                onChange={(paymentMethod) =>
                  setDraft((current) => ({ ...current, paymentMethod }))
                }
              />
            ) : (
              <Input
                value={draft.paymentMethod}
                disabled={draft.provider !== 'epay'}
                onChange={(paymentMethod) =>
                  setDraft((current) => ({ ...current, paymentMethod }))
                }
              />
            )}
          </label>
          <label className='flex flex-col gap-2 text-sm font-medium'>
            {t('Currency')}
            <Input
              value={draft.currency}
              maxLength={3}
              onChange={(currency) =>
                setDraft((current) => ({
                  ...current,
                  currency: currency.toUpperCase(),
                }))
              }
            />
          </label>
          <label className='flex flex-col gap-2 text-sm font-medium'>
            {t('Single-payment limit')}
            <Input
              value={draft.singleLimit}
              inputMode='decimal'
              placeholder='0'
              onChange={(singleLimit) =>
                setDraft((current) => ({ ...current, singleLimit }))
              }
            />
          </label>
          <label className='flex flex-col gap-2 text-sm font-medium'>
            {t('Daily limit')}
            <Input
              value={draft.dailyLimit}
              inputMode='decimal'
              placeholder='0'
              onChange={(dailyLimit) =>
                setDraft((current) => ({ ...current, dailyLimit }))
              }
            />
          </label>
          <label className='flex flex-col gap-2 text-sm font-medium'>
            {t('Merchant day timezone')}
            <Select
              value={draft.timezone}
              filter
              allowCreate
              optionList={MERCHANT_TIMEZONES.map((timezone) => ({
                value: timezone,
                label: timezone,
              }))}
              onChange={(timezone) =>
                setDraft((current) => ({ ...current, timezone }))
              }
            />
          </label>
          <div className='flex items-center justify-between gap-4 rounded-lg border px-4 py-3 md:col-span-2 xl:col-span-3'>
            <div id='classic-payment-limit-enabled-description'>
              <div className='font-medium'>{t('Enforce this policy')}</div>
              <div className='mt-1 text-sm text-gray-500'>
                {t(
                  'Disabled policies stay saved but do not reserve or reject payment capacity.',
                )}
              </div>
            </div>
            <Switch
              checked={draft.enabled}
              aria-label={t('Enforce this policy')}
              aria-describedby='classic-payment-limit-enabled-description'
              onChange={(enabled) =>
                setDraft((current) => ({ ...current, enabled }))
              }
            />
          </div>
          <div className='flex justify-end md:col-span-2 xl:col-span-3'>
            <Button
              theme='solid'
              type='primary'
              icon={<ShieldCheck size={16} />}
              loading={saving}
              onClick={() => void saveLimit()}
            >
              {t('Save limit policy')}
            </Button>
          </div>
        </div>
      </Card>

      {loadError && (
        <Banner
          type='danger'
          title={loadError}
          description={
            <div className='flex flex-wrap items-center justify-between gap-3'>
              <span>
                {t(
                  'No settings were changed. Reload the current policies before editing them.',
                )}
              </span>
              <Button onClick={() => void loadLimits()}>{t('Retry')}</Button>
            </div>
          }
          closeIcon={null}
        />
      )}

      <Spin spinning={loading}>
        {!loadError && limits.length === 0 ? (
          <div className='rounded-xl border border-dashed px-5 py-10 text-center text-sm text-gray-500'>
            {t('No payment limit policies have been configured yet.')}
          </div>
        ) : (
          <div className='grid gap-3 lg:grid-cols-2'>
            {limits.map((limit) => {
              const usagePercent = getPaymentLimitUsagePercent(limit);
              const single = paymentMinorToMajor(
                limit.single_limit_minor,
                limit.currency_exponent,
              );
              const daily = paymentMinorToMajor(
                limit.daily_limit_minor,
                limit.currency_exponent,
              );
              const paid = paymentMinorToMajor(
                limit.paid_minor,
                limit.currency_exponent,
              );
              const reserved = paymentMinorToMajor(
                limit.reserved_minor,
                limit.currency_exponent,
              );
              return (
                <Card
                  key={`${limit.provider}:${limit.payment_method}:${limit.currency}`}
                  title={
                    <div className='flex flex-wrap items-center gap-2'>
                      <span>{getPaymentMethodLabel(limit, t)}</span>
                      <Tag color={limit.enabled ? 'green' : 'grey'}>
                        {limit.enabled ? t('Enabled') : t('Disabled')}
                      </Tag>
                    </div>
                  }
                  headerExtraContent={
                    <Button
                      icon={<Pencil size={15} />}
                      aria-label={t('Edit payment limit')}
                      onClick={() => editLimit(limit)}
                    />
                  }
                  bodyStyle={{ padding: 16 }}
                >
                  <div className='mb-4 flex flex-wrap gap-x-3 gap-y-1 text-xs text-gray-500'>
                    <span>{limit.currency}</span>
                    <span>{limit.timezone}</span>
                    <span>{limit.day_key}</span>
                  </div>
                  <div className='grid grid-cols-2 gap-4 text-sm'>
                    <div>
                      <div className='text-gray-500'>
                        {t('Single-payment limit')}
                      </div>
                      <div className='mt-1 font-medium tabular-nums'>
                        {single === '0'
                          ? t('Unlimited')
                          : `${single} ${limit.currency}`}
                      </div>
                    </div>
                    <div>
                      <div className='text-gray-500'>{t('Daily limit')}</div>
                      <div className='mt-1 font-medium tabular-nums'>
                        {daily === '0'
                          ? t('Unlimited')
                          : `${daily} ${limit.currency}`}
                      </div>
                    </div>
                    <div>
                      <div className='text-gray-500'>{t('Paid today')}</div>
                      <div className='mt-1 font-medium tabular-nums'>
                        {paid} {limit.currency}
                      </div>
                    </div>
                    <div>
                      <div className='text-gray-500'>
                        {t('Active reservations')}
                      </div>
                      <div className='mt-1 font-medium tabular-nums'>
                        {reserved} {limit.currency}
                      </div>
                    </div>
                  </div>
                  {daily !== '0' && (
                    <div className='mt-4'>
                      <Progress
                        percent={usagePercent}
                        showInfo
                        aria-label={t('Daily payment capacity used')}
                      />
                      <div className='mt-1 text-xs text-gray-500'>
                        {t('{{percent}}% of daily capacity used', {
                          percent: new Intl.NumberFormat(i18n.language, {
                            maximumFractionDigits: 1,
                          }).format(usagePercent),
                        })}
                      </div>
                    </div>
                  )}
                </Card>
              );
            })}
          </div>
        )}
      </Spin>
    </div>
  );
};

export default PaymentLimitsPanel;
