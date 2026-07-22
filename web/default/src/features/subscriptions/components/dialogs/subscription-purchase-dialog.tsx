/*
Copyright (C) 2023-2026 QuantumNous

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
import { useNavigate } from '@tanstack/react-router'
import { Crown, CalendarClock, Package } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Spinner } from '@/components/ui/spinner'
import {
  createPaymentQuote,
  isApiSuccess,
  startPayment,
} from '@/features/wallet/api'
import { formatPaymentDecimalAmount } from '@/features/wallet/lib/billing'
import {
  createPaymentError,
  getPaymentErrorMessage,
  getPublicPaymentChannelLabel,
  getPublicPaymentMethodLabel,
  navigateToPaymentUrl,
  submitPaymentForm,
} from '@/features/wallet/lib/payment'
import type {
  ClientPaymentQuote,
  PaymentMethod,
  PaymentStart,
} from '@/features/wallet/types'
import { useSystemConfig } from '@/hooks/use-system-config'
import { formatQuota } from '@/lib/format'
import { DEFAULT_CURRENCY_CONFIG } from '@/stores/system-config-store'

import { paySubscriptionBalance } from '../../api'
import { formatDuration, formatResetPeriod } from '../../lib'
import type { PublicPlanRecord } from '../../types'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  plan: PublicPlanRecord | null
  paymentRoutes?: PaymentMethod[]
  purchaseLimit?: number
  purchaseCount?: number
  userQuota?: number
  onPurchaseSuccess?: () => void | Promise<void>
}

export function SubscriptionPurchaseDialog(props: Props) {
  const { t } = useTranslation()
  const { currency } = useSystemConfig()
  const navigate = useNavigate()
  const [paymentLoadingKey, setPaymentLoadingKey] = useState('')
  const [selectedPaymentMethod, setSelectedPaymentMethod] = useState('')
  const paymentInFlightRef = useRef(false)
  const gatewayQuoteRef = useRef<{
    key: string
    quote: ClientPaymentQuote
  } | null>(null)
  const gatewayStartRequestRef = useRef<{
    quoteId: string
    requestId: string
  } | null>(null)
  const balanceRequestRef = useRef<{
    planId: number
    requestId: string
  } | null>(null)

  useEffect(() => {
    const quoteRoutes = (props.paymentRoutes || []).filter(
      (method) => method.checkout_mode === 'quote'
    )
    if (props.open && quoteRoutes.length > 0) {
      setSelectedPaymentMethod(quoteRoutes[0].route_id)
    } else if (!props.open) {
      setSelectedPaymentMethod('')
    }
  }, [props.open, props.paymentRoutes])

  useEffect(() => {
    gatewayQuoteRef.current = null
    gatewayStartRequestRef.current = null
    balanceRequestRef.current = null
  }, [props.plan?.plan.id])

  const plan = props.plan?.plan
  if (!plan) return null

  const planCurrency = (plan.currency || 'USD').toUpperCase()
  const externalCurrencySupported = planCurrency === 'USD'
  const externalPaymentRouteIDs = new Set(plan.external_payment_route_ids || [])
  const productRoute = props.paymentRoutes?.find(
    (method) =>
      method.checkout_mode === 'product' &&
      externalPaymentRouteIDs.has(method.route_id)
  )
  const directRoute = props.paymentRoutes?.find(
    (method) =>
      method.checkout_mode === 'direct' &&
      externalPaymentRouteIDs.has(method.route_id)
  )
  const hasProductCheckout = !!productRoute
  const hasDirectCheckout = !!directRoute
  const unifiedMethods = externalCurrencySupported
    ? (props.paymentRoutes || []).filter(
        (method) => method.checkout_mode === 'quote'
      )
    : []
  const hasGatewayMethods = unifiedMethods.length > 0
  const hasAnyPayment =
    hasProductCheckout || hasDirectCheckout || hasGatewayMethods
  const selectedMethod = unifiedMethods.find(
    (method) => method.route_id === selectedPaymentMethod
  )
  const getMethodLabel = (method: PaymentMethod) => {
    const methodLabel = getPublicPaymentMethodLabel(method, t)
    const channelLabel = getPublicPaymentChannelLabel(method, unifiedMethods, t)
    return channelLabel ? `${methodLabel} · ${channelLabel}` : methodLabel
  }
  const selectedPaymentMethodLabel = selectedMethod
    ? getMethodLabel(selectedMethod)
    : t('Select payment method')
  const paying = paymentLoadingKey !== ''
  const totalAmount = Number(plan.total_amount || 0)
  const price = formatPaymentDecimalAmount(
    Number(plan.price_amount || 0),
    plan.currency || 'USD'
  )
  const quotaPerUnit =
    currency?.quotaPerUnit && currency.quotaPerUnit > 0
      ? currency.quotaPerUnit
      : DEFAULT_CURRENCY_CONFIG.quotaPerUnit
  const balanceCost = Math.max(
    0,
    Math.ceil(Number(plan.price_amount || 0) * quotaPerUnit)
  )
  const userQuota = Math.max(0, Number(props.userQuota || 0))
  const balanceCurrencySupported =
    (plan.currency || 'USD').toUpperCase() === 'USD'
  const planAllowsBalancePay = plan.allow_balance_pay !== false
  const allowBalancePay = planAllowsBalancePay && balanceCurrencySupported
  const insufficientBalance = userQuota < balanceCost
  const limitReached =
    (props.purchaseLimit || 0) > 0 &&
    (props.purchaseCount || 0) >= (props.purchaseLimit || 0)

  const handlePaymentStart = async (paymentStart: PaymentStart) => {
    if (paymentStart.flow === 'qr' || paymentStart.flow === 'pending') {
      if (!paymentStart.trade_no) {
        throw new Error('payment_order_missing')
      }
      props.onOpenChange(false)
      await navigate({
        to: '/payment/$tradeNo',
        params: { tradeNo: paymentStart.trade_no },
      })
      return true
    }
    if (paymentStart.flow === 'hosted_redirect') {
      if (!navigateToPaymentUrl(paymentStart.url)) {
        throw new Error(t('Invalid payment redirect URL'))
      }
      return true
    }
    if (!submitPaymentForm(paymentStart.action, paymentStart.fields)) {
      throw new Error(t('Invalid payment redirect URL'))
    }
    return true
  }

  const handlePayGateway = async (method: PaymentMethod) => {
    if (paymentInFlightRef.current) return
    paymentInFlightRef.current = true
    setPaymentLoadingKey(method.route_id)
    try {
      const quoteKey = `${plan.id}:${method.route_id}`
      let paymentQuote = gatewayQuoteRef.current?.quote
      if (
        gatewayQuoteRef.current?.key !== quoteKey ||
        !paymentQuote ||
        paymentQuote.expires_at <= Math.floor(Date.now() / 1000)
      ) {
        const quoteResponse = await createPaymentQuote({
          order_kind: 'subscription',
          route_id: method.route_id,
          plan_id: plan.id,
        })

        if (!isApiSuccess(quoteResponse) || !quoteResponse.data) {
          throw createPaymentError(quoteResponse)
        }
        paymentQuote = quoteResponse.data
        gatewayQuoteRef.current = { key: quoteKey, quote: paymentQuote }
        gatewayStartRequestRef.current = null
      }
      if (gatewayStartRequestRef.current?.quoteId !== paymentQuote.quote_id) {
        gatewayStartRequestRef.current = {
          quoteId: paymentQuote.quote_id,
          requestId:
            typeof crypto !== 'undefined' && 'randomUUID' in crypto
              ? crypto.randomUUID()
              : `${Date.now()}-${Math.random().toString(36).slice(2)}`,
        }
      }

      const startResponse = await startPayment({
        quote_id: paymentQuote.quote_id,
        request_id: gatewayStartRequestRef.current.requestId,
      })
      if (!isApiSuccess(startResponse) || !startResponse.data) {
        gatewayQuoteRef.current = null
        gatewayStartRequestRef.current = null
        throw createPaymentError(startResponse)
      }
      gatewayQuoteRef.current = null
      gatewayStartRequestRef.current = null
      await handlePaymentStart(startResponse.data)
    } catch (error) {
      toast.error(getPaymentErrorMessage(error, t))
    } finally {
      paymentInFlightRef.current = false
      setPaymentLoadingKey('')
    }
  }

  const handlePayCreem = async () => {
    if (productRoute) await handlePayGateway(productRoute)
  }

  const handlePayWaffoPancake = async () => {
    if (directRoute) await handlePayGateway(directRoute)
  }

  const handlePaySelectedGateway = async () => {
    const method = unifiedMethods.find(
      (candidate) => candidate.route_id === selectedPaymentMethod
    )
    if (!method) {
      toast.error(t('Please select a payment method'))
      return
    }
    await handlePayGateway(method)
  }

  const handlePayBalance = async () => {
    if (!balanceCurrencySupported) {
      toast.error(t('Balance payment is only available for USD plans'))
      return
    }
    if (!allowBalancePay) {
      toast.error(t('This plan does not allow balance redemption'))
      return
    }
    if (paymentInFlightRef.current) return
    paymentInFlightRef.current = true
    setPaymentLoadingKey('balance')
    try {
      if (balanceRequestRef.current?.planId !== plan.id) {
        const randomPart =
          typeof globalThis.crypto !== 'undefined' &&
          'randomUUID' in globalThis.crypto
            ? globalThis.crypto.randomUUID().replaceAll('-', '')
            : `${Date.now()}_${Math.random().toString(36).slice(2)}`
        balanceRequestRef.current = {
          planId: plan.id,
          requestId: `balance_${randomPart}`,
        }
      }
      const res = await paySubscriptionBalance({
        plan_id: plan.id,
        request_id: balanceRequestRef.current.requestId,
      })
      balanceRequestRef.current = null
      if (res.success) {
        toast.success(t('Access purchased successfully'))
        void props.onPurchaseSuccess?.()
        props.onOpenChange(false)
      } else {
        toast.error(getPaymentErrorMessage(res, t))
      }
    } catch (error) {
      toast.error(getPaymentErrorMessage(error, t))
    } finally {
      paymentInFlightRef.current = false
      setPaymentLoadingKey('')
    }
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={
        <>
          <Crown className='h-5 w-5' />
          {t('Purchase Access')}
        </>
      }
      contentClassName='max-sm:w-[calc(100vw-1.5rem)] sm:max-w-md'
      titleClassName='flex items-center gap-2'
      contentHeight='auto'
      bodyClassName='space-y-4'
    >
      <div className='space-y-3 sm:space-y-4'>
        <Alert>
          <AlertDescription>
            {t(
              'One-time payment for fixed-term access. It does not renew automatically.'
            )}
          </AlertDescription>
        </Alert>

        <div className='bg-muted/50 space-y-2.5 rounded-lg border p-3 sm:space-y-3 sm:p-4'>
          <div className='flex justify-between'>
            <span className='text-muted-foreground text-sm'>
              {t('Plan Name')}
            </span>
            <span className='max-w-[200px] truncate text-sm font-medium'>
              {plan.title}
            </span>
          </div>
          <div className='flex items-center justify-between'>
            <span className='text-muted-foreground text-sm'>
              {t('Validity Period')}
            </span>
            <span className='flex items-center gap-1 text-sm'>
              <CalendarClock className='h-3.5 w-3.5' />
              {formatDuration(plan, t)}
            </span>
          </div>
          {formatResetPeriod(plan, t) !== t('No Reset') && (
            <div className='flex justify-between'>
              <span className='text-muted-foreground text-sm'>
                {t('Reset Period')}
              </span>
              <span className='text-sm'>{formatResetPeriod(plan, t)}</span>
            </div>
          )}
          <div className='flex items-center justify-between'>
            <span className='text-muted-foreground text-sm'>
              {t('Plan Quota')}
            </span>
            <span className='flex items-center gap-1 text-sm'>
              <Package className='h-3.5 w-3.5' />
              {totalAmount > 0 ? formatQuota(totalAmount) : t('Unlimited')}
            </span>
          </div>
          {plan.includes_expanded_access && (
            <div className='flex items-center justify-between'>
              <span className='text-sm'>
                {t('Includes expanded model access')}
              </span>
            </div>
          )}
          <Separator />
          <div className='flex items-center justify-between'>
            <span className='text-sm font-medium'>{t('Amount Due')}</span>
            <span className='text-primary text-lg font-bold'>{price}</span>
          </div>
        </div>

        {limitReached && (
          <Alert variant='destructive'>
            <AlertDescription>
              {t('Purchase limit reached')} ({props.purchaseCount}/
              {props.purchaseLimit})
            </AlertDescription>
          </Alert>
        )}

        {!externalCurrencySupported &&
          (props.paymentRoutes?.length ?? 0) > 0 && (
            <Alert variant='destructive'>
              <AlertDescription>
                {t('Online payment is only available for USD access plans.')}
              </AlertDescription>
            </Alert>
          )}

        <div className='flex flex-col gap-2 rounded-md border p-3'>
          {balanceCurrencySupported && (
            <>
              <div className='flex items-center justify-between gap-2 text-xs'>
                <span className='text-muted-foreground'>{t('Required')}</span>
                <span>{formatQuota(balanceCost)}</span>
              </div>
              <div className='flex items-center justify-between gap-2 text-xs'>
                <span className='text-muted-foreground'>{t('Available')}</span>
                <span>{formatQuota(userQuota)}</span>
              </div>
            </>
          )}
          {!balanceCurrencySupported && (
            <Alert variant='destructive'>
              <AlertDescription>
                {t(
                  'Balance payment is only available for USD plans because account balance is denominated in USD quota.'
                )}
              </AlertDescription>
            </Alert>
          )}
          {balanceCurrencySupported && !planAllowsBalancePay && (
            <Alert variant='destructive'>
              <AlertDescription>
                {t('This plan does not allow balance redemption')}
              </AlertDescription>
            </Alert>
          )}
          {balanceCurrencySupported &&
            planAllowsBalancePay &&
            insufficientBalance && (
              <Alert variant='destructive'>
                <AlertDescription>{t('Insufficient balance')}</AlertDescription>
              </Alert>
            )}
          <Button
            variant='outline'
            onClick={handlePayBalance}
            disabled={
              paying || limitReached || !allowBalancePay || insufficientBalance
            }
            aria-busy={paymentLoadingKey === 'balance'}
          >
            {paymentLoadingKey === 'balance' && (
              <Spinner aria-label={t('Preparing payment')} />
            )}
            {t('Pay with Balance')}
          </Button>
        </div>

        {hasAnyPayment && (
          <div className='space-y-3'>
            <p className='text-muted-foreground text-xs'>
              {t('Select payment method')}
            </p>
            {(hasProductCheckout || hasDirectCheckout) && (
              <div className='grid grid-cols-2 gap-2 sm:flex'>
                {hasProductCheckout && (
                  <Button
                    variant='outline'
                    className='flex-1'
                    onClick={handlePayCreem}
                    disabled={paying || limitReached}
                    aria-busy={paymentLoadingKey === productRoute?.route_id}
                  >
                    {paymentLoadingKey === productRoute?.route_id && (
                      <Spinner aria-label={t('Preparing payment')} />
                    )}
                    {t('Online payment')}
                  </Button>
                )}
                {hasDirectCheckout && (
                  <Button
                    variant='outline'
                    className='flex-1'
                    onClick={handlePayWaffoPancake}
                    disabled={paying || limitReached}
                    aria-busy={paymentLoadingKey === directRoute?.route_id}
                  >
                    {paymentLoadingKey === directRoute?.route_id && (
                      <Spinner aria-label={t('Preparing payment')} />
                    )}
                    {t('Alternative online payment {{number}}', {
                      number: 1,
                    })}
                  </Button>
                )}
              </div>
            )}
            {hasGatewayMethods && (
              <div className='grid grid-cols-[minmax(0,1fr)_auto] gap-2'>
                <Select
                  items={unifiedMethods.map((method) => ({
                    value: method.route_id,
                    label: getMethodLabel(method),
                  }))}
                  value={selectedPaymentMethod}
                  onValueChange={(value) =>
                    value !== null && setSelectedPaymentMethod(value)
                  }
                  disabled={paying || limitReached}
                >
                  <SelectTrigger className='flex-1'>
                    <SelectValue>{selectedPaymentMethodLabel}</SelectValue>
                  </SelectTrigger>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      {unifiedMethods.map((method) => (
                        <SelectItem
                          key={method.route_id}
                          value={method.route_id}
                        >
                          {getMethodLabel(method)}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <Button
                  onClick={() => void handlePaySelectedGateway()}
                  disabled={paying || !selectedPaymentMethod || limitReached}
                  aria-busy={paymentLoadingKey === selectedPaymentMethod}
                >
                  {paymentLoadingKey === selectedPaymentMethod && (
                    <Spinner aria-label={t('Preparing payment')} />
                  )}
                  {t('Pay')}
                </Button>
              </div>
            )}
          </div>
        )}
      </div>
    </Dialog>
  )
}
