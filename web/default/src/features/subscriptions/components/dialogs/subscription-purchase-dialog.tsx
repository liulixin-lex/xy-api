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
import { Crown, CalendarClock, Package } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { GroupBadge } from '@/components/group-badge'
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
import {
  createPaymentQuote,
  isApiSuccess,
  startPayment,
} from '@/features/wallet/api'
import { PaymentQrDialog } from '@/features/wallet/components/dialogs/payment-qr-dialog'
import { PaymentResultAlert } from '@/features/wallet/components/payment-result-alert'
import { formatPaymentDecimalAmount } from '@/features/wallet/lib/billing'
import {
  navigateToPaymentUrl,
  submitPaymentForm,
} from '@/features/wallet/lib/payment'
import type {
  PaymentMethod,
  PaymentOrder,
  PaymentProvider,
  PaymentQrStart,
  PaymentQuote,
  PaymentStart,
} from '@/features/wallet/types'
import { useSystemConfig } from '@/hooks/use-system-config'
import { formatQuota } from '@/lib/format'
import { DEFAULT_CURRENCY_CONFIG } from '@/stores/system-config-store'

import {
  paySubscriptionStripe,
  paySubscriptionCreem,
  paySubscriptionEpay,
  paySubscriptionWaffoPancake,
  paySubscriptionBalance,
} from '../../api'
import { formatDuration, formatResetPeriod } from '../../lib'
import type { PlanRecord } from '../../types'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  plan: PlanRecord | null
  enableStripe?: boolean
  enableCreem?: boolean
  enableWaffoPancake?: boolean
  paymentMethods?: PaymentMethod[]
  purchaseLimit?: number
  purchaseCount?: number
  userQuota?: number
  onPurchaseSuccess?: () => void | Promise<void>
}

export function SubscriptionPurchaseDialog(props: Props) {
  const { t } = useTranslation()
  const { currency } = useSystemConfig()
  const [paying, setPaying] = useState(false)
  const [selectedPaymentMethod, setSelectedPaymentMethod] = useState('')
  const [qrOpen, setQrOpen] = useState(false)
  const [qrStart, setQrStart] = useState<PaymentQrStart | null>(null)
  const [qrQuote, setQrQuote] = useState<PaymentQuote | null>(null)
  const [pendingTradeNo, setPendingTradeNo] = useState('')
  const paymentInFlightRef = useRef(false)
  const gatewayQuoteRef = useRef<{
    key: string
    quote: PaymentQuote
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
    if (props.open && props.paymentMethods && props.paymentMethods.length > 0) {
      setSelectedPaymentMethod(props.paymentMethods[0].type)
    } else if (!props.open) {
      setSelectedPaymentMethod('')
    }
  }, [props.open, props.paymentMethods])

  useEffect(() => {
    gatewayQuoteRef.current = null
    gatewayStartRequestRef.current = null
    balanceRequestRef.current = null
  }, [props.plan?.plan.id])

  const plan = props.plan?.plan
  if (!plan) return null

  const planCurrency = (plan.currency || 'USD').toUpperCase()
  const externalCurrencySupported = planCurrency === 'USD'
  const hasStripe = props.enableStripe && externalCurrencySupported
  const hasCreem = props.enableCreem && !!plan.creem_product_id
  const hasWaffoPancake =
    props.enableWaffoPancake && !!plan.waffo_pancake_product_id
  const unifiedMethods = externalCurrencySupported
    ? props.paymentMethods || []
    : []
  const hasGatewayMethods = unifiedMethods.length > 0
  const hasAnyPayment =
    hasStripe || hasCreem || hasWaffoPancake || hasGatewayMethods
  const selectedPaymentMethodLabel =
    unifiedMethods.find((m) => m.type === selectedPaymentMethod)?.name ||
    selectedPaymentMethod ||
    t('Select payment method')
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

  const isEndpointUnavailable = (error: unknown) => {
    if (!error || typeof error !== 'object' || !('response' in error)) {
      return false
    }
    const status = (error as { response?: { status?: unknown } }).response
      ?.status
    return status === 404 || status === 405 || status === 501
  }

  const handlePaymentStart = (paymentStart: PaymentStart) => {
    if (paymentStart.flow === 'qr') {
      setQrStart(paymentStart)
      setQrOpen(true)
      props.onOpenChange(false)
      return true
    }
    if (paymentStart.flow === 'hosted_redirect') {
      if (!navigateToPaymentUrl(paymentStart.url)) {
        throw new Error(t('Invalid payment redirect URL'))
      }
      return true
    }
    if (paymentStart.flow === 'pending') {
      setPendingTradeNo(paymentStart.trade_no)
      props.onOpenChange(false)
      return true
    }
    if (!submitPaymentForm(paymentStart.action, paymentStart.fields)) {
      throw new Error(t('Invalid payment redirect URL'))
    }
    return true
  }

  const startLegacyGatewayPayment = async (
    provider: PaymentProvider,
    paymentMethod: string
  ) => {
    if (provider === 'stripe') {
      const response = await paySubscriptionStripe({ plan_id: plan.id })
      if (!isApiSuccess(response) || !response.data?.pay_link) {
        throw new Error(response.message || t('Payment request failed'))
      }
      return handlePaymentStart({
        flow: 'hosted_redirect',
        trade_no: '',
        url: response.data.pay_link,
        expires_at: 0,
      })
    }
    if (provider !== 'epay') {
      throw new Error(
        t('This payment gateway requires the unified payment API')
      )
    }
    const response = await paySubscriptionEpay({
      plan_id: plan.id,
      payment_method: paymentMethod,
    })
    if (!isApiSuccess(response) || !response.url || !response.data) {
      throw new Error(response.message || t('Payment request failed'))
    }
    return handlePaymentStart({
      flow: 'form_post',
      trade_no: '',
      action: response.url,
      fields: Object.fromEntries(
        Object.entries(response.data).map(([key, value]) => [
          key,
          String(value),
        ])
      ),
      expires_at: 0,
    })
  }

  const handlePayGateway = async (
    provider: PaymentProvider,
    paymentMethod: string
  ) => {
    if (paymentInFlightRef.current) return
    paymentInFlightRef.current = true
    setPaying(true)
    try {
      const quoteKey = `${plan.id}:${provider}:${paymentMethod}`
      let paymentQuote = gatewayQuoteRef.current?.quote
      if (
        gatewayQuoteRef.current?.key !== quoteKey ||
        !paymentQuote ||
        paymentQuote.expires_at <= Math.floor(Date.now() / 1000)
      ) {
        let quoteResponse
        try {
          quoteResponse = await createPaymentQuote({
            order_kind: 'subscription',
            provider,
            payment_method: paymentMethod,
            plan_id: plan.id,
          })
        } catch (error) {
          if (isEndpointUnavailable(error)) {
            await startLegacyGatewayPayment(provider, paymentMethod)
            return
          }
          throw error
        }

        if (!isApiSuccess(quoteResponse) || !quoteResponse.data) {
          throw new Error(quoteResponse.message || t('Payment quote failed'))
        }
        paymentQuote = quoteResponse.data
        gatewayQuoteRef.current = { key: quoteKey, quote: paymentQuote }
        gatewayStartRequestRef.current = null
      }
      setQrQuote(paymentQuote)

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
        throw new Error(startResponse.message || t('Payment request failed'))
      }
      gatewayQuoteRef.current = null
      gatewayStartRequestRef.current = null
      toast.success(t('Payment initiated'))
      handlePaymentStart(startResponse.data)
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t('Payment request failed')
      )
    } finally {
      paymentInFlightRef.current = false
      setPaying(false)
    }
  }

  const handlePayCreem = async () => {
    if (paymentInFlightRef.current) return
    paymentInFlightRef.current = true
    setPaying(true)
    try {
      const res = await paySubscriptionCreem({ plan_id: plan.id })
      if (isApiSuccess(res) && res.data?.checkout_url) {
        if (!navigateToPaymentUrl(res.data.checkout_url)) {
          throw new Error(t('Invalid payment redirect URL'))
        }
      } else {
        toast.error(
          res.message && res.message !== 'success'
            ? res.message
            : t('Payment request failed')
        )
      }
    } catch {
      toast.error(t('Payment request failed'))
    } finally {
      paymentInFlightRef.current = false
      setPaying(false)
    }
  }

  // In-tab redirect (not window.open) — user-gesture context is lost
  // across the await, so a popup would be blocked. Same as the wallet hook.
  const handlePayWaffoPancake = async () => {
    if (paymentInFlightRef.current) return
    paymentInFlightRef.current = true
    setPaying(true)
    try {
      const res = await paySubscriptionWaffoPancake({ plan_id: plan.id })
      if (isApiSuccess(res) && res.data?.checkout_url) {
        toast.success(t('Redirecting to payment page...'))
        if (!navigateToPaymentUrl(res.data.checkout_url)) {
          throw new Error(t('Invalid payment redirect URL'))
        }
      } else {
        toast.error(
          res.message && res.message !== 'success'
            ? res.message
            : t('Payment request failed')
        )
      }
    } catch {
      toast.error(t('Payment request failed'))
    } finally {
      paymentInFlightRef.current = false
      setPaying(false)
    }
  }

  const handlePaySelectedGateway = async () => {
    const method = unifiedMethods.find(
      (candidate) => candidate.type === selectedPaymentMethod
    )
    if (
      !method ||
      (method.provider !== 'epay' && method.provider !== 'xorpay')
    ) {
      toast.error(t('Please select a payment method'))
      return
    }
    await handlePayGateway(method.provider, method.type)
  }

  const handleQrSettled = async (order: PaymentOrder) => {
    if (order.status !== 'success') return
    await props.onPurchaseSuccess?.()
  }

  const handlePendingSettled = async (order: PaymentOrder) => {
    if (order.status !== 'success') return
    await props.onPurchaseSuccess?.()
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
    setPaying(true)
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
        toast.success(t('Subscription purchased successfully'))
        void props.onPurchaseSuccess?.()
        props.onOpenChange(false)
      } else {
        toast.error(
          res.message && res.message !== 'success'
            ? res.message
            : t('Payment request failed')
        )
      }
    } catch {
      toast.error(t('Payment request failed'))
    } finally {
      paymentInFlightRef.current = false
      setPaying(false)
    }
  }

  return (
    <>
      <Dialog
        open={props.open}
        onOpenChange={props.onOpenChange}
        title={
          <>
            <Crown className='h-5 w-5' />
            {t('Purchase Subscription')}
          </>
        }
        contentClassName='max-sm:w-[calc(100vw-1.5rem)] sm:max-w-md'
        titleClassName='flex items-center gap-2'
        contentHeight='auto'
        bodyClassName='space-y-4'
      >
        <div className='space-y-3 sm:space-y-4'>
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
            {plan.upgrade_group && (
              <div className='flex items-center justify-between'>
                <span className='text-muted-foreground text-sm'>
                  {t('Upgrade Group')}
                </span>
                <GroupBadge group={plan.upgrade_group} />
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
            (props.enableStripe || (props.paymentMethods?.length ?? 0) > 0) && (
              <Alert variant='destructive'>
                <AlertDescription>
                  {t(
                    'Online gateway payment is only available for USD subscription plans.'
                  )}
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
                  <span className='text-muted-foreground'>
                    {t('Available')}
                  </span>
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
                  <AlertDescription>
                    {t('Insufficient balance')}
                  </AlertDescription>
                </Alert>
              )}
            <Button
              variant='outline'
              onClick={handlePayBalance}
              disabled={
                paying ||
                limitReached ||
                !allowBalancePay ||
                insufficientBalance
              }
            >
              {t('Pay with Balance')}
            </Button>
          </div>

          {hasAnyPayment && (
            <div className='space-y-3'>
              <p className='text-muted-foreground text-xs'>
                {t('Select payment method')}
              </p>
              {(hasStripe || hasCreem || hasWaffoPancake) && (
                <div className='grid grid-cols-2 gap-2 sm:flex'>
                  {hasStripe && (
                    <Button
                      variant='outline'
                      className='flex-1'
                      onClick={() => void handlePayGateway('stripe', 'stripe')}
                      disabled={paying || limitReached}
                    >
                      Stripe
                    </Button>
                  )}
                  {hasCreem && (
                    <Button
                      variant='outline'
                      className='flex-1'
                      onClick={handlePayCreem}
                      disabled={paying || limitReached}
                    >
                      Creem
                    </Button>
                  )}
                  {hasWaffoPancake && (
                    <Button
                      variant='outline'
                      className='flex-1'
                      onClick={handlePayWaffoPancake}
                      disabled={paying || limitReached}
                    >
                      Waffo Pancake
                    </Button>
                  )}
                </div>
              )}
              {hasGatewayMethods && (
                <div className='grid grid-cols-[minmax(0,1fr)_auto] gap-2'>
                  <Select
                    items={unifiedMethods.map((method) => ({
                      value: method.type,
                      label: method.name || method.type,
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
                          <SelectItem key={method.type} value={method.type}>
                            {method.name || method.type}
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  <Button
                    onClick={() => void handlePaySelectedGateway()}
                    disabled={paying || !selectedPaymentMethod || limitReached}
                  >
                    {t('Pay')}
                  </Button>
                </div>
              )}
            </div>
          )}
        </div>
      </Dialog>
      <PaymentQrDialog
        open={qrOpen}
        onOpenChange={setQrOpen}
        paymentStart={qrStart}
        quote={qrQuote}
        onSettled={handleQrSettled}
        onTrackPending={setPendingTradeNo}
      />
      <Dialog
        open={pendingTradeNo !== ''}
        onOpenChange={(open) => !open && setPendingTradeNo('')}
        title={t('Payment Status')}
        contentClassName='max-sm:w-[calc(100vw-1.5rem)] sm:max-w-lg'
        contentHeight='auto'
      >
        {pendingTradeNo && (
          <PaymentResultAlert
            tradeNo={pendingTradeNo}
            resultHint='pending'
            onDismiss={() => setPendingTradeNo('')}
            onSettled={handlePendingSettled}
          />
        )}
      </Dialog>
    </>
  )
}
