/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/
import {
  Add01Icon,
  InformationCircleIcon,
  PencilEdit01Icon,
  SecurityCheckIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import * as React from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Progress, ProgressValue } from '@/components/ui/progress'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'

import { SettingsSection } from '../components/settings-section'
import { getPaymentAdminErrorMessage } from '../payment-admin-errors'
import {
  listPaymentLimits,
  updatePaymentLimit,
  type PaymentLimitUpdateRequest,
  type PaymentLimitUsage,
} from './payment-limits-api'

type LimitDraft = {
  provider: 'epay' | 'stripe' | 'xorpay' | 'creem' | 'waffo' | 'waffo_pancake'
  paymentMethod: string
  currency: string
  singleLimit: string
  dailyLimit: string
  timezone: string
  enabled: boolean
}

const EMPTY_DRAFT: LimitDraft = {
  provider: 'xorpay',
  paymentMethod: 'xorpay_alipay',
  currency: 'CNY',
  singleLimit: '',
  dailyLimit: '',
  timezone: 'Asia/Shanghai',
  enabled: true,
}

const MAX_INT64 = 9_223_372_036_854_775_807n

function currencyExponent(provider: string, currency: string): number {
  const normalized = currency.trim().toUpperCase()
  if (!/^[A-Z]{3}$/.test(normalized)) return 2
  if (provider === 'stripe' && (normalized === 'ISK' || normalized === 'UGX')) {
    return 2
  }
  try {
    return (
      new Intl.NumberFormat('en', {
        style: 'currency',
        currency: normalized,
      }).resolvedOptions().maximumFractionDigits ?? 2
    )
  } catch {
    return 2
  }
}

function majorToMinor(value: string, exponent: number): string | null {
  const normalized = value.trim()
  if (!normalized || !/^\d+(?:\.\d+)?$/.test(normalized)) return null
  const [whole, fraction = ''] = normalized.split('.')
  if (fraction.length > exponent) return null
  const minor = BigInt(
    (whole.replace(/^0+(?=\d)/, '') || '0') + fraction.padEnd(exponent, '0')
  )
  if (minor < 0n || minor > MAX_INT64) return null
  return minor.toString()
}

function minorToMajor(value: string, exponent: number): string {
  let minor: bigint
  try {
    minor = BigInt(value || '0')
  } catch {
    return '0'
  }
  if (exponent === 0) return minor.toString()
  const negative = minor < 0n
  const digits = (negative ? -minor : minor)
    .toString()
    .padStart(exponent + 1, '0')
  const whole = digits.slice(0, -exponent)
  const fraction = digits.slice(-exponent).replace(/0+$/, '')
  return `${negative ? '-' : ''}${whole}${fraction ? `.${fraction}` : ''}`
}

function paymentMethodLabel(
  limit: Pick<PaymentLimitUsage, 'provider' | 'payment_method'>,
  t: (key: string) => string
): string {
  if (limit.provider === 'xorpay' && limit.payment_method === 'xorpay_alipay') {
    return t('XORPay Alipay face-to-face')
  }
  if (limit.provider === 'xorpay' && limit.payment_method === 'xorpay_native') {
    return t('XORPay WeChat Native')
  }
  if (limit.provider === 'xorpay' && limit.payment_method === 'xorpay_jsapi') {
    return t('XORPay WeChat in-app (JSAPI)')
  }
  if (limit.provider === 'stripe') return t('Stripe one-time Checkout')
  if (limit.provider === 'creem') return t('Creem')
  if (limit.provider === 'waffo') return t('Waffo')
  if (limit.provider === 'waffo_pancake') return t('Waffo Pancake')
  return `${t('Epay')}: ${limit.payment_method}`
}

function usagePercent(limit: PaymentLimitUsage): number {
  try {
    const daily = BigInt(limit.daily_limit_minor)
    if (daily <= 0n) return 0
    const used = BigInt(limit.paid_minor) + BigInt(limit.reserved_minor)
    const basisPoints = (used * 10_000n) / daily
    return Math.min(100, Number(basisPoints) / 100)
  } catch {
    return 0
  }
}

function isValidTimeZone(value: string): boolean {
  try {
    new Intl.DateTimeFormat('en', { timeZone: value.trim() }).format()
    return true
  } catch {
    return false
  }
}

function formatMinorAmount(
  value: string,
  exponent: number,
  currency: string,
  locale: string
): string {
  const major = minorToMajor(value, exponent)
  const negative = major.startsWith('-')
  const unsigned = negative ? major.slice(1) : major
  const [whole, fraction = ''] = unsigned.split('.')
  let formattedWhole = whole
  try {
    formattedWhole = new Intl.NumberFormat(locale).format(BigInt(whole || '0'))
  } catch {
    formattedWhole = whole
  }
  const decimal = new Intl.NumberFormat(locale)
    .formatToParts(1.1)
    .find((part) => part.type === 'decimal')?.value
  const formatted = `${negative ? '-' : ''}${formattedWhole}${fraction ? `${decimal ?? '.'}${fraction}` : ''}`
  return `${formatted} ${currency}`
}

export function PaymentLimitsSection() {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const [draft, setDraft] = React.useState<LimitDraft>(EMPTY_DRAFT)
  const [saving, setSaving] = React.useState(false)
  const {
    open: verificationOpen,
    methods: verificationMethods,
    state: verificationState,
    executeVerification,
    cancel: cancelVerification,
    setCode,
    switchMethod,
    withVerification,
  } = useSecureVerification()
  const limitsQuery = useQuery({
    queryKey: ['payment-limits'],
    queryFn: listPaymentLimits,
    staleTime: 15_000,
  })
  const exponent = currencyExponent(draft.provider, draft.currency)
  const futureOfficialAlipayProducts = [
    t('Official Alipay PC website payment'),
    t('Official Alipay WAP payment'),
    t('Official Alipay JSAPI'),
  ]

  const setProvider = (provider: LimitDraft['provider']) => {
    setDraft((current) => {
      if (provider === 'xorpay') {
        return {
          ...current,
          provider,
          paymentMethod: 'xorpay_alipay',
          currency: 'CNY',
          timezone: 'Asia/Shanghai',
        }
      }
      if (provider === 'stripe') {
        return {
          ...current,
          provider,
          paymentMethod: 'stripe',
          currency: 'USD',
        }
      }
      if (provider === 'creem') {
        return { ...current, provider, paymentMethod: 'creem', currency: 'USD' }
      }
      if (provider === 'waffo') {
        return { ...current, provider, paymentMethod: 'waffo', currency: 'USD' }
      }
      if (provider === 'waffo_pancake') {
        return {
          ...current,
          provider,
          paymentMethod: 'waffo_pancake',
          currency: 'USD',
        }
      }
      return { ...current, provider, paymentMethod: 'alipay', currency: 'CNY' }
    })
  }

  const applyPreset = (
    method: 'xorpay_alipay' | 'xorpay_native' | 'xorpay_jsapi'
  ) => {
    setDraft({
      provider: 'xorpay',
      paymentMethod: method,
      currency: 'CNY',
      singleLimit: method === 'xorpay_alipay' ? '1999.99' : '20000',
      dailyLimit: method === 'xorpay_alipay' ? '19999.99' : '50000',
      timezone: 'Asia/Shanghai',
      enabled: true,
    })
  }

  const editLimit = (limit: PaymentLimitUsage) => {
    setDraft({
      provider: limit.provider as LimitDraft['provider'],
      paymentMethod: limit.payment_method,
      currency: limit.currency,
      singleLimit: minorToMajor(
        limit.single_limit_minor,
        limit.currency_exponent
      ),
      dailyLimit: minorToMajor(
        limit.daily_limit_minor,
        limit.currency_exponent
      ),
      timezone: limit.timezone,
      enabled: limit.enabled,
    })
    const reduceMotion = window.matchMedia(
      '(prefers-reduced-motion: reduce)'
    ).matches
    document.querySelector('#payment-limit-editor')?.scrollIntoView({
      behavior: reduceMotion ? 'auto' : 'smooth',
      block: 'start',
    })
  }

  const saveLimit = async () => {
    if (saving || verificationOpen) return
    const singleLimitMinor = majorToMinor(draft.singleLimit || '0', exponent)
    const dailyLimitMinor = majorToMinor(draft.dailyLimit || '0', exponent)
    if (
      !/^[A-Za-z0-9_-]{1,64}$/.test(draft.paymentMethod.trim()) ||
      !/^[A-Z]{3}$/.test(draft.currency.trim().toUpperCase())
    ) {
      toast.error(
        t('Enter a valid payment method and three-letter currency code.')
      )
      return
    }
    if (!isValidTimeZone(draft.timezone)) {
      toast.error(t('Enter a valid IANA time zone.'))
      return
    }
    if (singleLimitMinor === null || dailyLimitMinor === null) {
      toast.error(
        t(
          'Enter non-negative limits using no more than {{digits}} decimal places.',
          { digits: exponent }
        )
      )
      return
    }
    if (
      BigInt(dailyLimitMinor) > 0n &&
      BigInt(singleLimitMinor) > BigInt(dailyLimitMinor)
    ) {
      toast.error(t('The single-payment limit cannot exceed the daily limit.'))
      return
    }
    const request: PaymentLimitUpdateRequest = {
      provider: draft.provider,
      payment_method: draft.paymentMethod.trim(),
      currency: draft.currency.trim().toUpperCase(),
      single_limit_minor: singleLimitMinor,
      daily_limit_minor: dailyLimitMinor,
      timezone: draft.timezone.trim(),
      enabled: draft.enabled,
    }
    setSaving(true)
    try {
      await withVerification(
        async () => {
          const result = await updatePaymentLimit(request)
          let refreshed = result.refreshed
          try {
            await queryClient.invalidateQueries(
              { queryKey: ['payment-limits'] },
              { throwOnError: true }
            )
          } catch {
            refreshed = false
          }
          if (refreshed) {
            toast.success(t('Payment limit saved'))
          } else {
            toast.warning(
              t(
                'Payment limit was saved, but the latest usage could not be refreshed.'
              )
            )
          }
          return result
        },
        {
          preferredMethod: 'passkey',
          title: t('Verify payment limit update'),
          description: t(
            'Confirm your identity before changing merchant payment capacity.'
          ),
        }
      )
    } catch (error) {
      toast.error(
        getPaymentAdminErrorMessage(error, t, t('Failed to save payment limit'))
      )
    } finally {
      setSaving(false)
    }
  }

  return (
    <SettingsSection title={t('Payment Limits')}>
      <Alert>
        <HugeiconsIcon
          icon={InformationCircleIcon}
          strokeWidth={2}
          aria-hidden='true'
        />
        <AlertTitle>
          {t('Limits belong to this merchant configuration')}
        </AlertTitle>
        <AlertDescription>
          {t(
            'A limit is enforced per gateway, payment method, currency, and merchant day. Active orders reserve capacity; failed or expired orders release it automatically.'
          )}
        </AlertDescription>
      </Alert>

      <div className='flex flex-wrap gap-2'>
        <Button
          type='button'
          size='sm'
          variant='outline'
          onClick={() => applyPreset('xorpay_alipay')}
        >
          {t('Use current XORPay Alipay merchant reference')}
        </Button>
        <Button
          type='button'
          size='sm'
          variant='outline'
          onClick={() => applyPreset('xorpay_native')}
        >
          {t('Use current XORPay WeChat merchant reference')}
        </Button>
        <Button
          type='button'
          size='sm'
          variant='outline'
          onClick={() => applyPreset('xorpay_jsapi')}
        >
          {t('Use current XORPay WeChat JSAPI merchant reference')}
        </Button>
      </div>
      <p className='text-muted-foreground text-sm'>
        {t(
          'These presets reflect the currently supplied merchant materials and are not universal XORPay limits. Official Alipay PC, WAP, and Alipay JSAPI reference limits remain disabled because those products are not connected.'
        )}
      </p>

      <Card>
        <CardHeader>
          <CardTitle>
            {t('Future official Alipay products (not enabled)')}
          </CardTitle>
          <CardDescription>
            {t(
              'Read-only merchant material for future official Alipay products. These products are not connected and cannot be selected.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className='overflow-x-auto'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Product')}</TableHead>
                  <TableHead>{t('Status')}</TableHead>
                  <TableHead>{t('Single-payment reference')}</TableHead>
                  <TableHead>{t('Daily reference')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {futureOfficialAlipayProducts.map((product) => (
                  <TableRow key={product}>
                    <TableCell className='font-medium'>{product}</TableCell>
                    <TableCell>
                      <Badge variant='outline'>{t('Not enabled')}</Badge>
                    </TableCell>
                    <TableCell className='tabular-nums'>50 CNY</TableCell>
                    <TableCell className='tabular-nums'>1000 CNY</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
          <p className='text-muted-foreground mt-4 text-sm'>
            {t(
              'These reference values are not XORPay Alipay face-to-face limits and are not system-wide defaults.'
            )}
          </p>
        </CardContent>
      </Card>

      <Card id='payment-limit-editor'>
        <CardHeader className='border-b'>
          <CardTitle>{t('Limit policy')}</CardTitle>
          <CardDescription>
            {t(
              'Set zero to leave the single-payment or daily cap unlimited. Amounts are entered in the selected currency, then stored as integer minor units.'
            )}
          </CardDescription>
          <CardAction>
            <Button
              type='button'
              size='sm'
              variant='ghost'
              onClick={() => setDraft(EMPTY_DRAFT)}
            >
              <HugeiconsIcon
                icon={Add01Icon}
                strokeWidth={2}
                data-icon='inline-start'
                aria-hidden='true'
              />
              {t('New policy')}
            </Button>
          </CardAction>
        </CardHeader>
        <CardContent className='grid gap-5 md:grid-cols-2 xl:grid-cols-3'>
          <div className='grid gap-2'>
            <Label htmlFor='payment-limit-provider'>{t('Gateway')}</Label>
            <NativeSelect
              id='payment-limit-provider'
              value={draft.provider}
              onChange={(event) =>
                setProvider(event.target.value as LimitDraft['provider'])
              }
            >
              <NativeSelectOption value='epay'>{t('Epay')}</NativeSelectOption>
              <NativeSelectOption value='stripe'>
                {t('Stripe')}
              </NativeSelectOption>
              <NativeSelectOption value='xorpay'>
                {t('XORPay')}
              </NativeSelectOption>
              <NativeSelectOption value='creem'>
                {t('Creem')}
              </NativeSelectOption>
              <NativeSelectOption value='waffo'>
                {t('Waffo')}
              </NativeSelectOption>
              <NativeSelectOption value='waffo_pancake'>
                {t('Waffo Pancake')}
              </NativeSelectOption>
            </NativeSelect>
          </div>
          <div className='grid gap-2'>
            <Label htmlFor='payment-limit-method'>{t('Payment method')}</Label>
            {draft.provider === 'xorpay' ? (
              <NativeSelect
                id='payment-limit-method'
                value={draft.paymentMethod}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    paymentMethod: event.target.value,
                  }))
                }
              >
                <NativeSelectOption value='xorpay_alipay'>
                  {t('Alipay face-to-face')}
                </NativeSelectOption>
                <NativeSelectOption value='xorpay_native'>
                  {t('WeChat Native')}
                </NativeSelectOption>
                <NativeSelectOption value='xorpay_jsapi'>
                  {t('WeChat in-app (JSAPI)')}
                </NativeSelectOption>
              </NativeSelect>
            ) : (
              <Input
                id='payment-limit-method'
                value={draft.paymentMethod}
                disabled={draft.provider !== 'epay'}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    paymentMethod: event.target.value,
                  }))
                }
              />
            )}
          </div>
          <div className='grid gap-2'>
            <Label htmlFor='payment-limit-currency'>{t('Currency')}</Label>
            <Input
              id='payment-limit-currency'
              value={draft.currency}
              maxLength={3}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  currency: event.target.value.toUpperCase(),
                }))
              }
            />
          </div>
          <div className='grid gap-2'>
            <Label htmlFor='payment-limit-single'>
              {t('Single-payment limit')}
            </Label>
            <Input
              id='payment-limit-single'
              inputMode='decimal'
              value={draft.singleLimit}
              placeholder='0'
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  singleLimit: event.target.value,
                }))
              }
            />
          </div>
          <div className='grid gap-2'>
            <Label htmlFor='payment-limit-daily'>{t('Daily limit')}</Label>
            <Input
              id='payment-limit-daily'
              inputMode='decimal'
              value={draft.dailyLimit}
              placeholder='0'
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  dailyLimit: event.target.value,
                }))
              }
            />
          </div>
          <div className='grid gap-2'>
            <Label htmlFor='payment-limit-timezone'>
              {t('Merchant day timezone')}
            </Label>
            <Input
              id='payment-limit-timezone'
              list='payment-limit-timezones'
              value={draft.timezone}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  timezone: event.target.value,
                }))
              }
            />
            <datalist id='payment-limit-timezones'>
              {[
                'UTC',
                'Asia/Shanghai',
                'Asia/Tokyo',
                'Asia/Ho_Chi_Minh',
                'Europe/Paris',
                'Europe/Moscow',
                'America/New_York',
              ].map((timezone) => (
                <option key={timezone} value={timezone} />
              ))}
            </datalist>
          </div>
          <div className='flex items-center justify-between gap-4 rounded-lg border px-4 py-3 md:col-span-2 xl:col-span-3'>
            <div id='payment-limit-enabled-description'>
              <Label htmlFor='payment-limit-enabled'>
                {t('Enforce this policy')}
              </Label>
              <p className='text-muted-foreground text-sm'>
                {t(
                  'Disabled policies stay saved but do not reserve or reject payment capacity.'
                )}
              </p>
            </div>
            <Switch
              id='payment-limit-enabled'
              checked={draft.enabled}
              onCheckedChange={(enabled) =>
                setDraft((current) => ({ ...current, enabled }))
              }
              aria-describedby='payment-limit-enabled-description'
            />
          </div>
          <div className='flex justify-end md:col-span-2 xl:col-span-3'>
            <Button
              type='button'
              disabled={saving || verificationOpen}
              aria-busy={saving}
              onClick={() => void saveLimit()}
            >
              <HugeiconsIcon
                icon={SecurityCheckIcon}
                strokeWidth={2}
                data-icon='inline-start'
                aria-hidden='true'
              />
              {saving ? t('Saving...') : t('Save limit policy')}
            </Button>
          </div>
        </CardContent>
      </Card>

      <div
        className='grid gap-3 lg:grid-cols-2'
        aria-busy={limitsQuery.isLoading}
      >
        {limitsQuery.isLoading &&
          Array.from({ length: 2 }, (_, index) => (
            <Skeleton key={index} className='h-52 rounded-xl' />
          ))}
        {limitsQuery.isError && (
          <Alert variant='destructive' className='lg:col-span-2'>
            <AlertTitle>{t('Payment limits are unavailable')}</AlertTitle>
            <AlertDescription className='flex flex-wrap items-center justify-between gap-3'>
              <span className='grid gap-1'>
                <span>
                  {t(
                    'No settings were changed. Reload the current policies before editing them.'
                  )}
                </span>
                <span className='font-mono text-xs'>
                  {getPaymentAdminErrorMessage(
                    limitsQuery.error,
                    t,
                    t('Failed to load payment limits')
                  )}
                </span>
              </span>
              <Button
                type='button'
                size='sm'
                variant='outline'
                onClick={() => void limitsQuery.refetch()}
              >
                {t('Retry')}
              </Button>
            </AlertDescription>
          </Alert>
        )}
        {limitsQuery.data?.length === 0 && (
          <div className='rounded-xl border border-dashed px-5 py-10 text-center lg:col-span-2'>
            <p className='font-medium'>{t('No payment limit policies')}</p>
            <p className='text-muted-foreground mt-1 text-sm'>
              {t(
                'Create a policy to reserve and enforce capacity for a payment route.'
              )}
            </p>
          </div>
        )}
        {limitsQuery.data?.map((limit) => {
          const dailyMinor = BigInt(limit.daily_limit_minor)
          const single = minorToMajor(
            limit.single_limit_minor,
            limit.currency_exponent
          )
          const percent = usagePercent(limit)
          return (
            <Card
              key={`${limit.provider}:${limit.payment_method}:${limit.currency}`}
              size='sm'
            >
              <CardHeader className='border-b'>
                <CardTitle className='flex flex-wrap items-center gap-2'>
                  {paymentMethodLabel(limit, t)}
                  <Badge variant={limit.enabled ? 'default' : 'secondary'}>
                    {limit.enabled ? t('Enabled') : t('Disabled')}
                  </Badge>
                </CardTitle>
                <CardDescription className='flex flex-wrap gap-x-3 gap-y-1'>
                  <span>{limit.currency}</span>
                  <span>{limit.timezone}</span>
                  <span>{limit.day_key}</span>
                </CardDescription>
                <CardAction>
                  <Button
                    type='button'
                    size='icon-sm'
                    variant='ghost'
                    aria-label={t('Edit payment limit')}
                    onClick={() => editLimit(limit)}
                  >
                    <HugeiconsIcon
                      icon={PencilEdit01Icon}
                      strokeWidth={2}
                      aria-hidden='true'
                    />
                  </Button>
                </CardAction>
              </CardHeader>
              <CardContent className='grid gap-4'>
                <div className='grid grid-cols-2 gap-3 text-sm'>
                  <div>
                    <p className='text-muted-foreground'>
                      {t('Single-payment limit')}
                    </p>
                    <p className='font-medium tabular-nums'>
                      {single === '0'
                        ? t('Unlimited')
                        : formatMinorAmount(
                            limit.single_limit_minor,
                            limit.currency_exponent,
                            limit.currency,
                            i18n.language
                          )}
                    </p>
                  </div>
                  <div>
                    <p className='text-muted-foreground'>{t('Daily limit')}</p>
                    <p className='font-medium tabular-nums'>
                      {dailyMinor === 0n
                        ? t('Unlimited')
                        : formatMinorAmount(
                            limit.daily_limit_minor,
                            limit.currency_exponent,
                            limit.currency,
                            i18n.language
                          )}
                    </p>
                  </div>
                  <div>
                    <p className='text-muted-foreground'>{t('Paid today')}</p>
                    <p className='font-medium tabular-nums'>
                      {formatMinorAmount(
                        limit.paid_minor,
                        limit.currency_exponent,
                        limit.currency,
                        i18n.language
                      )}
                    </p>
                  </div>
                  <div>
                    <p className='text-muted-foreground'>
                      {t('Active reservations')}
                    </p>
                    <p className='font-medium tabular-nums'>
                      {formatMinorAmount(
                        limit.reserved_minor,
                        limit.currency_exponent,
                        limit.currency,
                        i18n.language
                      )}
                    </p>
                  </div>
                </div>
                {dailyMinor > 0n && (
                  <Progress
                    value={percent}
                    aria-label={t('Daily payment capacity used')}
                  >
                    <ProgressValue>
                      {() =>
                        t('{{percent}}% of daily capacity used', {
                          percent: new Intl.NumberFormat(i18n.language, {
                            maximumFractionDigits: 1,
                          }).format(percent),
                        })
                      }
                    </ProgressValue>
                  </Progress>
                )}
              </CardContent>
            </Card>
          )
        })}
      </div>

      <SecureVerificationDialog
        open={verificationOpen}
        onOpenChange={(open) => {
          if (!open) cancelVerification()
        }}
        methods={verificationMethods}
        state={verificationState}
        onVerify={(method, code) => {
          void executeVerification(method, code).catch(() => {})
        }}
        onCancel={cancelVerification}
        onCodeChange={setCode}
        onMethodChange={switchMethod}
      />
    </SettingsSection>
  )
}
