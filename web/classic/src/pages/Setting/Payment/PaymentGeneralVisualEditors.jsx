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

import React, { useMemo, useState } from 'react';
import {
  Banner,
  Button,
  Collapse,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Tag,
  TextArea,
  Typography,
} from '@douyinfe/semi-ui';
import { Pencil, Plus, Trash2, X } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import {
  PAYMENT_PROVIDER_LABELS,
  PAYMENT_PROVIDER_ORDER,
  PAYMENT_TYPE_OPTIONS,
  getDefaultPaymentMethod,
  parseAmountDiscounts,
  parseAmountOptions,
  parsePaymentMethods,
  parseTopupGroupRatios,
  serializeJson,
  upsertPaymentMethod,
  validatePaymentMethodDraft,
} from './payment-general-editors.js';

const { Text } = Typography;

const PROVIDER_COLORS = {
  epay: 'blue',
  xorpay: 'cyan',
  stripe: 'violet',
  creem: 'green',
  waffo: 'light-blue',
  waffo_pancake: 'amber',
};

const panelStyle = {
  border: '1px solid var(--semi-color-border)',
  borderRadius: 12,
  padding: 16,
};

const rowStyle = {
  border: '1px solid var(--semi-color-border)',
  borderRadius: 10,
  padding: 12,
};

function normalizeCollapseKeys(value) {
  if (Array.isArray(value)) return value.filter(Boolean);
  return value ? [value] : [];
}

function getPaymentTypeLabel(type, t) {
  switch (type) {
    case 'xorpay_alipay':
      return t('XORPay Alipay');
    case 'xorpay_native':
      return t('XORPay WeChat Native');
    case 'xorpay_jsapi':
      return t('XORPay WeChat in-app (JSAPI)');
    case 'stripe':
      return t('Stripe one-time Checkout');
    case 'creem':
      return t('Creem');
    case 'waffo':
      return t('Waffo');
    case 'waffo_pancake':
      return 'Waffo Pancake';
    default:
      return type;
  }
}

function getPaymentDraftError(code, t) {
  switch (code) {
    case 'invalid_payment_method_name':
      return t(
        'Enter a payment method name using no more than 128 characters.',
      );
    case 'invalid_payment_method_type':
      return t(
        'Use 1 to 64 letters, numbers, underscores, or hyphens for the payment type key.',
      );
    case 'payment_method_provider_mismatch':
      return t('The payment type key does not match the selected provider.');
    case 'invalid_payment_method_minimum':
      return t('Minimum top-up must be a whole number between 1 and 10000.');
    case 'duplicate_payment_method':
      return t('This provider already has the same payment type key.');
    case 'too_many_payment_methods':
      return t('No more than 27 payment methods can be configured.');
    default:
      return '';
  }
}

function AdvancedJsonEditor({ value, onChange, error, ariaLabel }) {
  const { t } = useTranslation();
  const [activeKeys, setActiveKeys] = useState([]);
  const visibleKeys = error ? ['json'] : activeKeys;

  return (
    <div className='mt-4'>
      {error && (
        <Banner
          type='warning'
          title={t('Visual editor unavailable')}
          description={t(
            'Fix the JSON value below before continuing with the visual editor.',
          )}
          closeIcon={null}
          style={{ marginBottom: 12 }}
        />
      )}
      <Collapse
        keepDOM
        activeKey={visibleKeys}
        onChange={(keys) => setActiveKeys(normalizeCollapseKeys(keys))}
      >
        <Collapse.Panel header={t('Advanced JSON')} itemKey='json'>
          <Text type='secondary' size='small'>
            {t(
              'Use JSON only for advanced fields. Saving still applies server-side validation.',
            )}
          </Text>
          <TextArea
            value={value}
            onChange={onChange}
            autosize={{ minRows: 6, maxRows: 16 }}
            aria-label={ariaLabel}
            style={{ marginTop: 10, fontFamily: 'ui-monospace, monospace' }}
          />
        </Collapse.Panel>
      </Collapse>
    </div>
  );
}

function EditorPanel({ title, description, value, onChange, error, children }) {
  const { t } = useTranslation();

  return (
    <section style={panelStyle} aria-label={title}>
      <div className='mb-4'>
        <h3 className='m-0 text-base font-semibold'>{title}</h3>
        <p className='mt-1 mb-0 text-sm text-semi-color-text-2'>
          {description}
        </p>
      </div>
      {!error && children}
      <AdvancedJsonEditor
        value={value}
        onChange={onChange}
        error={error}
        ariaLabel={t('{{title}} JSON configuration', { title })}
      />
    </section>
  );
}

export function PaymentMethodsEditor({ value, onChange }) {
  const { t } = useTranslation();
  const parsed = useMemo(() => parsePaymentMethods(value), [value]);
  const [dialogVisible, setDialogVisible] = useState(false);
  const [editIndex, setEditIndex] = useState(-1);
  const [draft, setDraft] = useState(getDefaultPaymentMethod());
  const [draftError, setDraftError] = useState('');

  const groupedMethods = useMemo(() => {
    const groups = Object.fromEntries(
      PAYMENT_PROVIDER_ORDER.map((provider) => [provider, []]),
    );
    parsed.items.forEach((method, index) => {
      const provider = PAYMENT_PROVIDER_ORDER.includes(method.provider)
        ? method.provider
        : 'epay';
      groups[provider].push({ method, index });
    });
    return groups;
  }, [parsed.items]);

  const openAddDialog = (provider) => {
    setEditIndex(-1);
    setDraft(getDefaultPaymentMethod(provider));
    setDraftError('');
    setDialogVisible(true);
  };

  const openEditDialog = (method, index) => {
    setEditIndex(index);
    setDraft({ ...method });
    setDraftError('');
    setDialogVisible(true);
  };

  const changeProvider = (provider) => {
    setDraft(getDefaultPaymentMethod(provider));
    setDraftError('');
  };

  const changePresetType = (type) => {
    const option = PAYMENT_TYPE_OPTIONS[draft.provider]?.find(
      (item) => item.type === type,
    );
    if (!option) return;
    setDraft((current) => ({
      ...current,
      type: option.type,
      name: option.name,
      icon: option.icon,
    }));
    setDraftError('');
  };

  const saveDraft = () => {
    const error = validatePaymentMethodDraft(draft, parsed.items, editIndex);
    if (error) {
      setDraftError(error);
      return;
    }
    onChange(
      serializeJson(upsertPaymentMethod(parsed.items, editIndex, draft)),
    );
    setDialogVisible(false);
    setDraftError('');
  };

  const removeMethod = (index) => {
    onChange(
      serializeJson(parsed.items.filter((_, itemIndex) => itemIndex !== index)),
    );
  };

  return (
    <EditorPanel
      title={t('Payment methods')}
      description={t(
        'Configure public payment choices by gateway. Epay, XORPay, Stripe, and other gateways remain independent.',
      )}
      value={value}
      onChange={onChange}
      error={parsed.error}
    >
      <div className='grid gap-3 lg:grid-cols-2'>
        {PAYMENT_PROVIDER_ORDER.map((provider) => {
          const providerLabel = PAYMENT_PROVIDER_LABELS[provider];
          const methods = groupedMethods[provider];
          return (
            <div key={provider} style={rowStyle}>
              <div className='mb-3 flex items-center justify-between gap-3'>
                <div className='flex min-w-0 items-center gap-2'>
                  <Tag color={PROVIDER_COLORS[provider]}>{providerLabel}</Tag>
                  <Text type='tertiary' size='small'>
                    {t('{{count}} methods', { count: methods.length })}
                  </Text>
                </div>
                <Button
                  htmlType='button'
                  icon={<Plus size={16} />}
                  size='small'
                  theme='borderless'
                  aria-label={t('Add payment method')}
                  onClick={() => openAddDialog(provider)}
                  style={{ minWidth: 40, minHeight: 40 }}
                />
              </div>
              {methods.length === 0 ? (
                <div className='rounded-lg border border-dashed p-4 text-center text-sm text-semi-color-text-2'>
                  {t('No payment methods configured for this gateway.')}
                </div>
              ) : (
                <div className='flex flex-col gap-2'>
                  {methods.map(({ method, index }) => (
                    <div
                      key={`${provider}-${method.type}-${index}`}
                      className='flex items-center justify-between gap-3 rounded-lg bg-semi-color-fill-0 p-3'
                    >
                      <div className='min-w-0'>
                        <div className='truncate text-sm font-medium'>
                          {method.name}
                        </div>
                        <div className='mt-1 flex flex-wrap items-center gap-2'>
                          <code className='text-xs text-semi-color-text-2'>
                            {method.type}
                          </code>
                          {method.min_topup && (
                            <Text type='tertiary' size='small'>
                              {t('Minimum {{amount}}', {
                                amount: method.min_topup,
                              })}
                            </Text>
                          )}
                        </div>
                      </div>
                      <div className='flex shrink-0 gap-1'>
                        <Button
                          htmlType='button'
                          icon={<Pencil size={15} />}
                          theme='borderless'
                          aria-label={t('Edit payment method')}
                          onClick={() => openEditDialog(method, index)}
                          style={{ minWidth: 40, minHeight: 40 }}
                        />
                        <Popconfirm
                          title={t('Delete this payment method?')}
                          content={t(
                            'Users will no longer be able to select this configured route after saving.',
                          )}
                          okType='danger'
                          okText={t('Delete')}
                          cancelText={t('Cancel')}
                          onConfirm={() => removeMethod(index)}
                        >
                          <Button
                            htmlType='button'
                            type='danger'
                            icon={<Trash2 size={15} />}
                            theme='borderless'
                            aria-label={t('Delete payment method')}
                            style={{ minWidth: 40, minHeight: 40 }}
                          />
                        </Popconfirm>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>
          );
        })}
      </div>

      <Modal
        visible={dialogVisible}
        title={
          editIndex >= 0 ? t('Edit payment method') : t('Add payment method')
        }
        okText={t('Save')}
        cancelText={t('Cancel')}
        onOk={saveDraft}
        onCancel={() => {
          setDialogVisible(false);
          setDraftError('');
        }}
        centered
      >
        <div className='flex flex-col gap-4'>
          <label className='flex flex-col gap-2'>
            <Text strong>{t('Provider')}</Text>
            <Select
              value={draft.provider}
              optionList={PAYMENT_PROVIDER_ORDER.map((provider) => ({
                value: provider,
                label: PAYMENT_PROVIDER_LABELS[provider],
              }))}
              aria-label={t('Provider')}
              onChange={changeProvider}
              style={{ width: '100%' }}
            />
          </label>

          <label className='flex flex-col gap-2'>
            <Text strong>{t('Payment type key')}</Text>
            {draft.provider === 'epay' ? (
              <Input
                value={draft.type}
                maxLength={64}
                aria-label={t('Payment type key')}
                placeholder={t('For example: alipay, wxpay, or custom1')}
                onChange={(type) => {
                  setDraft((current) => ({ ...current, type }));
                  setDraftError('');
                }}
              />
            ) : (
              <Select
                value={draft.type}
                optionList={(PAYMENT_TYPE_OPTIONS[draft.provider] || []).map(
                  (option) => ({
                    value: option.type,
                    label: getPaymentTypeLabel(option.type, t),
                  }),
                )}
                aria-label={t('Payment type key')}
                onChange={changePresetType}
                style={{ width: '100%' }}
              />
            )}
            <Text type='tertiary' size='small'>
              {t(
                'This internal key selects the gateway product. It is never shown to users.',
              )}
            </Text>
          </label>

          <label className='flex flex-col gap-2'>
            <Text strong>{t('Payment method name')}</Text>
            <Input
              value={draft.name}
              maxLength={128}
              aria-label={t('Payment method name')}
              onChange={(name) => {
                setDraft((current) => ({ ...current, name }));
                setDraftError('');
              }}
            />
          </label>

          <label className='flex flex-col gap-2'>
            <Text strong>{t('Minimum top-up')}</Text>
            <InputNumber
              value={draft.min_topup || undefined}
              min={1}
              max={10000}
              precision={0}
              aria-label={t('Minimum top-up')}
              placeholder={t('Use the gateway default when empty')}
              onChange={(minimum) => {
                setDraft((current) => ({
                  ...current,
                  min_topup:
                    minimum === undefined || minimum === null
                      ? ''
                      : String(minimum),
                }));
                setDraftError('');
              }}
              style={{ width: '100%' }}
            />
          </label>

          {draftError && (
            <Banner
              type='warning'
              description={getPaymentDraftError(draftError, t)}
              closeIcon={null}
            />
          )}
        </div>
      </Modal>
    </EditorPanel>
  );
}

export function TopupGroupRatioEditor({ value, onChange }) {
  const { t } = useTranslation();
  const parsed = useMemo(() => parseTopupGroupRatios(value), [value]);
  const entries = useMemo(
    () =>
      Object.entries(parsed.ratios).sort(([left], [right]) =>
        left.localeCompare(right),
      ),
    [parsed.ratios],
  );
  const [newGroup, setNewGroup] = useState('');
  const [newRatio, setNewRatio] = useState(1);
  const [draftError, setDraftError] = useState('');

  const addGroup = () => {
    const group = newGroup.trim();
    const ratio = Number(newRatio);
    if (!group || group.length > 64) {
      setDraftError(t('Enter a group name using no more than 64 characters.'));
      return;
    }
    if (!Number.isFinite(ratio) || ratio <= 0 || ratio > 1000) {
      setDraftError(t('Enter a ratio greater than 0 and no more than 1000.'));
      return;
    }
    if (Object.prototype.hasOwnProperty.call(parsed.ratios, group)) {
      setDraftError(t('This group already has a top-up ratio.'));
      return;
    }
    if (entries.length >= 100) {
      setDraftError(t('No more than 100 group ratios can be configured.'));
      return;
    }
    onChange(serializeJson({ ...parsed.ratios, [group]: ratio }));
    setNewGroup('');
    setNewRatio(1);
    setDraftError('');
  };

  const updateRatio = (group, nextValue) => {
    const ratio = Number(nextValue);
    if (!Number.isFinite(ratio) || ratio <= 0 || ratio > 1000) return;
    onChange(serializeJson({ ...parsed.ratios, [group]: ratio }));
  };

  const removeGroup = (group) => {
    const next = { ...parsed.ratios };
    delete next[group];
    onChange(serializeJson(next));
  };

  return (
    <EditorPanel
      title={t('Top-up group ratios')}
      description={t(
        'Set the price multiplier applied to each user group. A ratio of 1 uses the standard price.',
      )}
      value={value}
      onChange={onChange}
      error={parsed.error}
    >
      {entries.length === 0 ? (
        <div className='rounded-lg border border-dashed p-5 text-center text-sm text-semi-color-text-2'>
          {t(
            'No group ratios configured. Add at least one group before saving.',
          )}
        </div>
      ) : (
        <div className='flex flex-col gap-2'>
          {entries.map(([group, ratio]) => (
            <div
              key={group}
              className='flex flex-col gap-3 rounded-lg bg-semi-color-fill-0 p-3 sm:flex-row sm:items-center'
            >
              <div className='min-w-0 flex-1 truncate text-sm font-medium'>
                {group}
              </div>
              <InputNumber
                value={ratio}
                min={0.0001}
                max={1000}
                step={0.1}
                precision={4}
                aria-label={t('Ratio for {{group}}', { group })}
                onChange={(nextValue) => updateRatio(group, nextValue)}
                style={{ width: 150 }}
              />
              <Popconfirm
                title={t('Delete this group ratio?')}
                okType='danger'
                okText={t('Delete')}
                cancelText={t('Cancel')}
                onConfirm={() => removeGroup(group)}
              >
                <Button
                  htmlType='button'
                  type='danger'
                  theme='borderless'
                  icon={<Trash2 size={15} />}
                  aria-label={t('Delete group ratio')}
                  style={{ minWidth: 40, minHeight: 40 }}
                />
              </Popconfirm>
            </div>
          ))}
        </div>
      )}

      <div className='mt-4 flex flex-col gap-3 sm:flex-row sm:items-end'>
        <label className='flex min-w-0 flex-1 flex-col gap-2'>
          <Text strong>{t('Group name')}</Text>
          <Input
            value={newGroup}
            maxLength={64}
            aria-label={t('Group name')}
            placeholder={t('For example: default or vip')}
            onChange={(group) => {
              setNewGroup(group);
              setDraftError('');
            }}
          />
        </label>
        <label className='flex flex-col gap-2'>
          <Text strong>{t('Ratio')}</Text>
          <InputNumber
            value={newRatio}
            min={0.0001}
            max={1000}
            step={0.1}
            precision={4}
            aria-label={t('Ratio')}
            onChange={(ratio) => {
              setNewRatio(ratio);
              setDraftError('');
            }}
            style={{ width: 150 }}
          />
        </label>
        <Button
          htmlType='button'
          icon={<Plus size={16} />}
          onClick={addGroup}
          style={{ minHeight: 40 }}
        >
          {t('Add group')}
        </Button>
      </div>
      {draftError && (
        <Text
          type='danger'
          size='small'
          style={{ display: 'block', marginTop: 8 }}
        >
          {draftError}
        </Text>
      )}
    </EditorPanel>
  );
}

export function AmountOptionsEditor({ value, onChange }) {
  const { t, i18n } = useTranslation();
  const parsed = useMemo(() => parseAmountOptions(value), [value]);
  const [newAmount, setNewAmount] = useState('');
  const [draftError, setDraftError] = useState('');
  const formatter = useMemo(
    () => new Intl.NumberFormat(i18n.language),
    [i18n.language],
  );

  const addAmount = () => {
    const amount = Number(newAmount);
    if (!Number.isSafeInteger(amount) || amount < 1 || amount > 10000) {
      setDraftError(t('Enter a whole amount between 1 and 10000.'));
      return;
    }
    if (parsed.amounts.includes(amount)) {
      setDraftError(t('This amount option already exists.'));
      return;
    }
    if (parsed.amounts.length >= 50) {
      setDraftError(t('No more than 50 amount options can be configured.'));
      return;
    }
    onChange(serializeJson([...parsed.amounts, amount].sort((a, b) => a - b)));
    setNewAmount('');
    setDraftError('');
  };

  const removeAmount = (amount) => {
    onChange(serializeJson(parsed.amounts.filter((item) => item !== amount)));
  };

  return (
    <EditorPanel
      title={t('Top-up amount options')}
      description={t('Choose the preset top-up amounts displayed to users.')}
      value={value}
      onChange={onChange}
      error={parsed.error}
    >
      {parsed.amounts.length === 0 ? (
        <div className='rounded-lg border border-dashed p-5 text-center text-sm text-semi-color-text-2'>
          {t('No amount options configured. Add amounts below to get started.')}
        </div>
      ) : (
        <div className='flex flex-wrap gap-2'>
          {parsed.amounts.map((amount) => (
            <div
              key={amount}
              className='flex items-center gap-1 rounded-full bg-semi-color-fill-1 py-1 pr-1 pl-3'
            >
              <span className='text-sm tabular-nums'>
                {formatter.format(amount)}
              </span>
              <Button
                htmlType='button'
                icon={<X size={14} />}
                theme='borderless'
                size='small'
                aria-label={t('Remove amount {{amount}}', { amount })}
                onClick={() => removeAmount(amount)}
                style={{ minWidth: 32, minHeight: 32 }}
              />
            </div>
          ))}
        </div>
      )}

      <div className='mt-4 flex flex-col gap-3 sm:flex-row sm:items-end'>
        <label className='flex min-w-0 flex-1 flex-col gap-2'>
          <Text strong>{t('Add new amount')}</Text>
          <InputNumber
            value={newAmount || undefined}
            min={1}
            max={10000}
            precision={0}
            aria-label={t('Add new amount')}
            placeholder={t('For example: 100')}
            onChange={(amount) => {
              setNewAmount(
                amount === undefined || amount === null ? '' : String(amount),
              );
              setDraftError('');
            }}
            style={{ width: '100%' }}
          />
        </label>
        <Button
          htmlType='button'
          icon={<Plus size={16} />}
          onClick={addAmount}
          style={{ minHeight: 40 }}
        >
          {t('Add amount')}
        </Button>
      </div>
      {draftError && (
        <Text
          type='danger'
          size='small'
          style={{ display: 'block', marginTop: 8 }}
        >
          {draftError}
        </Text>
      )}
    </EditorPanel>
  );
}

export function AmountDiscountEditor({ value, onChange }) {
  const { t, i18n } = useTranslation();
  const parsed = useMemo(() => parseAmountDiscounts(value), [value]);
  const entries = useMemo(
    () =>
      Object.entries(parsed.discounts).sort(
        ([left], [right]) => Number(left) - Number(right),
      ),
    [parsed.discounts],
  );
  const [newAmount, setNewAmount] = useState('');
  const [newRate, setNewRate] = useState(1);
  const [draftError, setDraftError] = useState('');
  const amountFormatter = useMemo(
    () => new Intl.NumberFormat(i18n.language),
    [i18n.language],
  );
  const percentFormatter = useMemo(
    () =>
      new Intl.NumberFormat(i18n.language, {
        style: 'percent',
        maximumFractionDigits: 2,
      }),
    [i18n.language],
  );

  const updateRate = (amount, nextValue) => {
    const rate = Number(nextValue);
    if (!Number.isFinite(rate) || rate <= 0 || rate > 1) return;
    onChange(serializeJson({ ...parsed.discounts, [amount]: rate }));
  };

  const removeDiscount = (amount) => {
    const next = { ...parsed.discounts };
    delete next[amount];
    onChange(serializeJson(next));
  };

  const addDiscount = () => {
    const amount = Number(newAmount);
    const rate = Number(newRate);
    if (!Number.isSafeInteger(amount) || amount < 1 || amount > 10000) {
      setDraftError(t('Enter a whole amount between 1 and 10000.'));
      return;
    }
    if (!Number.isFinite(rate) || rate <= 0 || rate > 1) {
      setDraftError(
        t('Enter a discount rate greater than 0 and no more than 1.'),
      );
      return;
    }
    if (Object.prototype.hasOwnProperty.call(parsed.discounts, amount)) {
      setDraftError(t('This amount already has a discount rate.'));
      return;
    }
    if (entries.length >= 100) {
      setDraftError(t('No more than 100 discount tiers can be configured.'));
      return;
    }
    onChange(serializeJson({ ...parsed.discounts, [amount]: rate }));
    setNewAmount('');
    setNewRate(1);
    setDraftError('');
  };

  return (
    <EditorPanel
      title={t('Top-up discounts')}
      description={t(
        'Set the price rate for specific top-up amounts. A rate of 0.95 means the user pays 95% of the normal price.',
      )}
      value={value}
      onChange={onChange}
      error={parsed.error}
    >
      {entries.length === 0 ? (
        <div className='rounded-lg border border-dashed p-5 text-center text-sm text-semi-color-text-2'>
          {t(
            'No discount tiers configured. Full price applies to every amount.',
          )}
        </div>
      ) : (
        <div className='flex flex-col gap-2'>
          {entries.map(([amount, rate]) => (
            <div
              key={amount}
              className='flex flex-col gap-3 rounded-lg bg-semi-color-fill-0 p-3 sm:flex-row sm:items-center'
            >
              <div className='min-w-0 flex-1'>
                <div className='text-sm font-medium tabular-nums'>
                  {amountFormatter.format(Number(amount))}
                </div>
                <Text type='tertiary' size='small'>
                  {t('User pays {{percentage}}', {
                    percentage: percentFormatter.format(rate),
                  })}
                </Text>
              </div>
              <InputNumber
                value={rate}
                min={0.0001}
                max={1}
                step={0.01}
                precision={4}
                aria-label={t('Discount rate for amount {{amount}}', {
                  amount,
                })}
                onChange={(nextValue) => updateRate(amount, nextValue)}
                style={{ width: 150 }}
              />
              <Popconfirm
                title={t('Delete this discount tier?')}
                okType='danger'
                okText={t('Delete')}
                cancelText={t('Cancel')}
                onConfirm={() => removeDiscount(amount)}
              >
                <Button
                  htmlType='button'
                  type='danger'
                  theme='borderless'
                  icon={<Trash2 size={15} />}
                  aria-label={t('Delete discount tier')}
                  style={{ minWidth: 40, minHeight: 40 }}
                />
              </Popconfirm>
            </div>
          ))}
        </div>
      )}

      <div className='mt-4 grid gap-3 sm:grid-cols-[minmax(0,1fr)_150px_auto] sm:items-end'>
        <label className='flex min-w-0 flex-col gap-2'>
          <Text strong>{t('Top-up amount')}</Text>
          <InputNumber
            value={newAmount || undefined}
            min={1}
            max={10000}
            precision={0}
            aria-label={t('Top-up amount')}
            placeholder={t('For example: 100')}
            onChange={(amount) => {
              setNewAmount(
                amount === undefined || amount === null ? '' : String(amount),
              );
              setDraftError('');
            }}
            style={{ width: '100%' }}
          />
        </label>
        <label className='flex flex-col gap-2'>
          <Text strong>{t('Price rate')}</Text>
          <InputNumber
            value={newRate}
            min={0.0001}
            max={1}
            step={0.01}
            precision={4}
            aria-label={t('Price rate')}
            onChange={(rate) => {
              setNewRate(rate);
              setDraftError('');
            }}
            style={{ width: '100%' }}
          />
        </label>
        <Button
          htmlType='button'
          icon={<Plus size={16} />}
          onClick={addDiscount}
          style={{ minHeight: 40 }}
        >
          {t('Add discount tier')}
        </Button>
      </div>
      {draftError && (
        <Text
          type='danger'
          size='small'
          style={{ display: 'block', marginTop: 8 }}
        >
          {draftError}
        </Text>
      )}
    </EditorPanel>
  );
}

export default function PaymentGeneralVisualEditors({ values, onChange }) {
  return (
    <div className='flex flex-col gap-4'>
      <PaymentMethodsEditor
        value={values.PayMethods}
        onChange={(value) => onChange('PayMethods', value)}
      />
      <TopupGroupRatioEditor
        value={values.TopupGroupRatio}
        onChange={(value) => onChange('TopupGroupRatio', value)}
      />
      <AmountOptionsEditor
        value={values.AmountOptions}
        onChange={(value) => onChange('AmountOptions', value)}
      />
      <AmountDiscountEditor
        value={values.AmountDiscount}
        onChange={(value) => onChange('AmountDiscount', value)}
      />
    </div>
  );
}
