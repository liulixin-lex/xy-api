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
import {
  Alert02Icon,
  ArrowLeft01Icon,
  CheckmarkCircle02Icon,
  Clock03Icon,
  Copy01Icon,
  InformationCircleIcon,
  LinkSquare02Icon,
  QrCodeIcon,
  SmartPhone01Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { Link } from '@tanstack/react-router'
import { QRCodeSVG } from 'qrcode.react'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Separator } from '@/components/ui/separator'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { useSystemConfig } from '@/hooks/use-system-config'
import { cn } from '@/lib/utils'

import { usePaymentOrder } from '../hooks/use-payment-order'
import { getPaymentIcon } from '../lib'
import { formatPaymentDecimalAmount, getStatusConfig } from '../lib/billing'
import {
  detectPaymentBrowserEnvironment,
  getEffectivePaymentStatus,
  getPublicPaymentChannelAliasLabel,
  getPublicPaymentMethodIconType,
  getPublicPaymentMethodLabel,
  getSafePaymentContinueUrl,
  getSafeWeChatAuthorizationUrl,
  getSafePaymentUrl,
  isSafePaymentJSAPIParameters,
  isSafePaymentQrContent,
  isPaymentReturnCancelled,
  navigateToPaymentUrl,
  type PaymentBrowserEnvironment,
} from '../lib/payment'
import type { PaymentOrder } from '../types'

interface WeixinJSBridgeResult {
  err_msg?: string
}

interface WeixinJSBridgeAPI {
  invoke: (
    method: 'getBrandWCPayRequest',
    parameters: Record<string, string>,
    callback: (result: WeixinJSBridgeResult) => void
  ) => void
}

declare global {
  interface Window {
    WeixinJSBridge?: WeixinJSBridgeAPI
  }
}

function invokeWeChatJSAPI(
  parameters: Record<string, string>,
  callback: (result: WeixinJSBridgeResult) => void
): void {
  let finished = false
  let timeoutID = 0
  const cleanup = () => {
    document.removeEventListener('WeixinJSBridgeReady', onBridgeReady)
    document.removeEventListener('onWeixinJSBridgeReady', onBridgeReady)
    if (timeoutID) window.clearTimeout(timeoutID)
  }
  const invoke = () => {
    if (!window.WeixinJSBridge || finished) return false
    finished = true
    cleanup()
    window.WeixinJSBridge.invoke('getBrandWCPayRequest', parameters, callback)
    return true
  }
  const onBridgeReady = () => {
    invoke()
  }
  if (invoke()) return
  document.addEventListener('WeixinJSBridgeReady', onBridgeReady, {
    once: true,
  })
  document.addEventListener('onWeixinJSBridgeReady', onBridgeReady, {
    once: true,
  })
  timeoutID = window.setTimeout(() => {
    if (finished) return
    finished = true
    cleanup()
    callback({ err_msg: 'bridge_unavailable' })
  }, 5_000)
}

interface PaymentCheckoutPageProps {
  tradeNo: string
}

interface CheckoutPanelProps {
  order: PaymentOrder
  environment: PaymentBrowserEnvironment
  refreshing: boolean
  onRefresh: () => void
}

function formatRemainingTime(totalSeconds: number): {
  minutes: string
  seconds: string
} {
  const safeSeconds = Math.max(0, totalSeconds)
  return {
    minutes: String(Math.floor(safeSeconds / 60)).padStart(2, '0'),
    seconds: String(safeSeconds % 60).padStart(2, '0'),
  }
}

function CheckoutPanel({
  order,
  environment,
  refreshing,
  onRefresh,
}: CheckoutPanelProps) {
  const { t } = useTranslation()
  const { copyToClipboard, copiedText } = useCopyToClipboard({ notify: false })
  const checkout = order.checkout
  const isAlipay = order.public_method === 'alipay'
  const isWechatPay = order.public_method === 'wechat_pay'
  const [copyFailed, setCopyFailed] = useState(false)
  const [jsapiState, setJSAPIState] = useState<
    'idle' | 'opening' | 'submitted' | 'cancelled' | 'unavailable'
  >('idle')

  const handleCopyPageLink = async () => {
    const copied = await copyToClipboard(window.location.href)
    setCopyFailed(!copied)
  }

  if (order.status_code === 'succeeded') {
    return (
      <div className='flex min-h-72 flex-col items-center justify-center px-4 py-10 text-center'>
        <span className='mb-4 flex size-14 items-center justify-center rounded-full bg-green-500/10 text-green-600'>
          <HugeiconsIcon icon={CheckmarkCircle02Icon} strokeWidth={1.8} />
        </span>
        <h2 className='text-xl font-semibold'>{t('Payment completed')}</h2>
        <p className='text-muted-foreground mt-2 max-w-sm text-sm'>
          {order.plan_id
            ? t('Your fixed-term access is ready to use.')
            : t('Your balance has been updated.')}
        </p>
        <Button className='mt-6 min-h-11' render={<Link to='/wallet' />}>
          {t('Return to Wallet')}
        </Button>
      </div>
    )
  }

  if (order.status_code === 'confirming') {
    return (
      <div className='flex min-h-72 flex-col items-center justify-center px-4 py-10 text-center'>
        <Spinner
          className='mb-4 size-10 text-blue-600'
          aria-label={t('Confirming payment')}
        />
        <h2 className='text-xl font-semibold'>{t('Confirming payment')}</h2>
        <p className='text-muted-foreground mt-2 max-w-md text-sm'>
          {t(
            'Payment was submitted and is being confirmed. You may keep this page open or return later.'
          )}
        </p>
      </div>
    )
  }

  if (order.status_code === 'expired') {
    return (
      <div className='flex min-h-72 flex-col items-center justify-center px-4 py-10 text-center'>
        <span className='mb-4 flex size-14 items-center justify-center rounded-full bg-amber-500/10 text-amber-600'>
          <HugeiconsIcon icon={Clock03Icon} strokeWidth={1.8} />
        </span>
        <h2 className='text-xl font-semibold'>{t('Payment expired')}</h2>
        <p className='text-muted-foreground mt-2 max-w-sm text-sm'>
          {t(
            'This payment can no longer be completed. Create a new order from your wallet.'
          )}
        </p>
        <Button className='mt-6 min-h-11' render={<Link to='/wallet' />}>
          {t('Create a new payment')}
        </Button>
      </div>
    )
  }

  if (order.status_code === 'temporarily_unavailable') {
    return (
      <div className='flex min-h-72 flex-col items-center justify-center px-4 py-10 text-center'>
        <span className='mb-4 flex size-14 items-center justify-center rounded-full bg-red-500/10 text-red-600'>
          <HugeiconsIcon icon={Alert02Icon} strokeWidth={1.8} />
        </span>
        <h2 className='text-xl font-semibold'>
          {t('Payment temporarily unavailable')}
        </h2>
        <p className='text-muted-foreground mt-2 max-w-sm text-sm'>
          {t(
            'We could not prepare this payment. Refresh the status or create a new order.'
          )}
        </p>
        <div className='mt-6 flex flex-wrap justify-center gap-2'>
          <Button
            className='min-h-11'
            variant='outline'
            onClick={onRefresh}
            disabled={refreshing}
            aria-busy={refreshing}
          >
            {refreshing && <Spinner aria-label={t('Refreshing')} />}
            {t('Refresh status')}
          </Button>
          <Button className='min-h-11' render={<Link to='/wallet' />}>
            {t('Return to Wallet')}
          </Button>
        </div>
      </div>
    )
  }

  if (!checkout || checkout.flow === 'pending') {
    return (
      <div className='flex min-h-72 flex-col items-center justify-center px-4 py-10 text-center'>
        <Spinner
          className='mb-4 size-10 text-blue-600'
          aria-label={t('Preparing payment')}
        />
        <h2 className='text-xl font-semibold'>{t('Preparing your payment')}</h2>
        <p className='text-muted-foreground mt-2 max-w-md text-sm'>
          {t(
            'This usually takes a few seconds. You can safely leave and return with the same order number.'
          )}
        </p>
      </div>
    )
  }

  if (checkout.flow === 'wechat_authorize') {
    const authorizationURL = getSafeWeChatAuthorizationUrl(
      checkout.continue_url,
      order.trade_no
    )
    if (environment !== 'wechat') {
      return (
        <div className='flex min-h-72 flex-col items-center justify-center px-4 py-10 text-center'>
          <span className='mb-4 flex size-14 items-center justify-center rounded-full bg-green-500/10 text-green-600'>
            <HugeiconsIcon icon={SmartPhone01Icon} strokeWidth={1.8} />
          </span>
          <h2 className='text-xl font-semibold'>{t('Open in WeChat')}</h2>
          <p className='text-muted-foreground mt-2 max-w-md text-sm'>
            {t(
              'This payment is available inside WeChat. Open this page in WeChat to continue.'
            )}
          </p>
          {environment === 'mobile' && (
            <div
              className='mt-6 grid justify-items-center gap-2'
              aria-live='polite'
            >
              <Button
                className='min-h-11'
                variant='outline'
                onClick={() => void handleCopyPageLink()}
              >
                <HugeiconsIcon
                  icon={Copy01Icon}
                  strokeWidth={2}
                  data-icon='inline-start'
                  aria-hidden='true'
                />
                {copiedText === window.location.href
                  ? t('Page link copied')
                  : t('Copy page link')}
              </Button>
              {copyFailed && (
                <p className='text-sm text-red-600'>
                  {t('Unable to copy. Copy the browser address manually.')}
                </p>
              )}
            </div>
          )}
        </div>
      )
    }
    return (
      <div className='flex min-h-72 flex-col items-center justify-center px-4 py-10 text-center'>
        <span className='mb-4 flex size-14 items-center justify-center rounded-full bg-green-500/10 text-green-600'>
          <HugeiconsIcon icon={SmartPhone01Icon} strokeWidth={1.8} />
        </span>
        <h2 className='text-xl font-semibold'>
          {t('Continue with WeChat Pay')}
        </h2>
        <p className='text-muted-foreground mt-2 max-w-md text-sm'>
          {t('Confirm your WeChat account to prepare this payment securely.')}
        </p>
        {authorizationURL ? (
          <Button
            className='mt-6 min-h-11 min-w-44 bg-[#07c160] text-white hover:bg-[#06ad56]'
            onClick={() => window.location.assign(authorizationURL.href)}
          >
            {t('Continue in WeChat')}
          </Button>
        ) : (
          <Alert variant='destructive' className='mt-6 max-w-md text-left'>
            <AlertTitle>{t('Payment link unavailable')}</AlertTitle>
            <AlertDescription>
              {t('Refresh the order status before trying again.')}
            </AlertDescription>
          </Alert>
        )}
      </div>
    )
  }

  if (checkout.flow === 'jsapi') {
    const parameters = checkout.jsapi
    const safeParameters = isSafePaymentJSAPIParameters(parameters)
    const invokePayment = () => {
      if (!safeParameters) {
        setJSAPIState('unavailable')
        return
      }
      setJSAPIState('opening')
      invokeWeChatJSAPI(
        {
          appId: parameters.app_id,
          timeStamp: parameters.timestamp,
          nonceStr: parameters.nonce_str,
          package: parameters.package,
          signType: parameters.sign_type,
          paySign: parameters.pay_sign,
        },
        (result) => {
          const message = result.err_msg || ''
          if (message === 'get_brand_wcpay_request:ok') {
            setJSAPIState('submitted')
            onRefresh()
          } else if (message === 'get_brand_wcpay_request:cancel') {
            setJSAPIState('cancelled')
          } else {
            setJSAPIState('unavailable')
          }
        }
      )
    }

    if (environment !== 'wechat') {
      return (
        <div className='flex min-h-72 flex-col items-center justify-center px-4 py-10 text-center'>
          <span className='mb-4 flex size-14 items-center justify-center rounded-full bg-green-500/10 text-green-600'>
            <HugeiconsIcon icon={SmartPhone01Icon} strokeWidth={1.8} />
          </span>
          <h2 className='text-xl font-semibold'>{t('Open in WeChat')}</h2>
          <p className='text-muted-foreground mt-2 max-w-md text-sm'>
            {t(
              'This payment is ready inside WeChat. Reopen this order in WeChat to continue.'
            )}
          </p>
        </div>
      )
    }
    if (!safeParameters) {
      return (
        <div className='flex min-h-72 items-center justify-center p-6'>
          <Alert variant='destructive' className='max-w-md'>
            <AlertTitle>{t('Payment temporarily unavailable')}</AlertTitle>
            <AlertDescription className='grid gap-3'>
              <p>{t('Refresh the order status before trying again.')}</p>
              <Button
                className='min-h-11 justify-self-start'
                variant='outline'
                onClick={onRefresh}
                disabled={refreshing}
                aria-busy={refreshing}
              >
                {refreshing && <Spinner aria-label={t('Refreshing')} />}
                {t('Refresh status')}
              </Button>
            </AlertDescription>
          </Alert>
        </div>
      )
    }
    return (
      <div className='flex min-h-72 flex-col items-center justify-center px-4 py-10 text-center'>
        <span className='mb-4 flex size-14 items-center justify-center rounded-full bg-green-500/10 text-green-600'>
          <HugeiconsIcon icon={SmartPhone01Icon} strokeWidth={1.8} />
        </span>
        <h2 className='text-xl font-semibold'>{t('Pay with WeChat')}</h2>
        <p className='text-muted-foreground mt-2 max-w-md text-sm'>
          {jsapiState === 'submitted'
            ? t('Payment was submitted. Waiting for secure confirmation.')
            : t(
                'WeChat will open the payment panel. Final success is confirmed by this site.'
              )}
        </p>
        <Button
          className='mt-6 min-h-11 min-w-44 bg-[#07c160] text-white hover:bg-[#06ad56]'
          onClick={invokePayment}
          disabled={jsapiState === 'opening' || jsapiState === 'submitted'}
          aria-busy={jsapiState === 'opening'}
        >
          {jsapiState === 'opening' ? t('Opening WeChat Pay') : t('Pay now')}
        </Button>
        {jsapiState === 'cancelled' && (
          <p className='text-muted-foreground mt-3 text-sm'>
            {t('Payment was cancelled. You can try again.')}
          </p>
        )}
        {jsapiState === 'unavailable' && (
          <div className='mt-3 grid justify-items-center gap-3'>
            <p className='text-sm text-red-600'>
              {t('WeChat Pay could not open. Refresh the page and try again.')}
            </p>
            <Button
              className='min-h-11'
              variant='outline'
              onClick={onRefresh}
              disabled={refreshing}
              aria-busy={refreshing}
            >
              {refreshing && <Spinner aria-label={t('Refreshing')} />}
              {t('Refresh status')}
            </Button>
          </div>
        )}
      </div>
    )
  }

  if (checkout.flow === 'hosted_redirect' || checkout.flow === 'form_post') {
    const continueUrl = checkout.continue_url
      ? getSafePaymentContinueUrl(checkout.continue_url, order.trade_no)
      : null
    return (
      <div className='flex min-h-72 flex-col items-center justify-center px-4 py-10 text-center'>
        <span className='mb-4 flex size-14 items-center justify-center rounded-full bg-blue-500/10 text-blue-600'>
          <HugeiconsIcon icon={LinkSquare02Icon} strokeWidth={1.8} />
        </span>
        <h2 className='text-xl font-semibold'>{t('Continue your payment')}</h2>
        <p className='text-muted-foreground mt-2 max-w-md text-sm'>
          {t(
            'Continue through the secure payment page. Final confirmation will appear here.'
          )}
        </p>
        {continueUrl ? (
          <Button
            className='mt-6 min-h-11 min-w-44'
            onClick={() => window.location.assign(continueUrl.href)}
          >
            {t('Continue to payment')}
          </Button>
        ) : (
          <Alert variant='destructive' className='mt-6 max-w-md text-left'>
            <AlertTitle>{t('Payment link unavailable')}</AlertTitle>
            <AlertDescription>
              {t('Refresh the order status before trying again.')}
            </AlertDescription>
          </Alert>
        )}
      </div>
    )
  }

  const qrContent = checkout.qr_content || ''
  const qrIsSafe = isSafePaymentQrContent(qrContent)
  const alipayUrl = isAlipay ? getSafePaymentUrl(qrContent) : null
  let instruction = t('Scan the code with the selected payment app.')
  if (isAlipay) {
    instruction =
      environment === 'desktop'
        ? t('Use Alipay on your phone to scan this code.')
        : t(
            'Open Alipay to pay. If it does not open, use a browser or scan the code from another device.'
          )
  } else if (isWechatPay) {
    if (environment === 'desktop') {
      instruction = t('Use WeChat on your phone to scan this code.')
    } else if (environment === 'wechat') {
      instruction = t(
        'This order currently requires another device to scan the payment code.'
      )
    } else {
      instruction = t(
        'Open this page in WeChat, or use another device to scan the payment code.'
      )
    }
  }

  if (!qrIsSafe) {
    return (
      <div className='flex min-h-72 items-center justify-center p-6'>
        <Alert variant='destructive' className='max-w-md'>
          <AlertTitle>{t('Payment code unavailable')}</AlertTitle>
          <AlertDescription className='grid gap-3'>
            <p>
              {t(
                'This payment code is unavailable. Refresh the status before trying again.'
              )}
            </p>
            <Button
              className='min-h-11 justify-self-start'
              variant='outline'
              onClick={onRefresh}
              disabled={refreshing}
              aria-busy={refreshing}
            >
              {refreshing && <Spinner aria-label={t('Refreshing')} />}
              {t('Refresh status')}
            </Button>
          </AlertDescription>
        </Alert>
      </div>
    )
  }

  return (
    <div className='flex min-h-72 flex-col items-center px-4 py-7 text-center sm:py-9'>
      <div
        className='rounded-xl border bg-white p-4 sm:p-5'
        role='img'
        aria-label={t('Payment QR code')}
      >
        <QRCodeSVG value={qrContent} size={224} level='M' />
      </div>
      <h2 className='mt-5 text-lg font-semibold'>{t('Scan to Pay')}</h2>
      <p className='text-muted-foreground mt-2 max-w-md text-sm'>
        {instruction}
      </p>

      {isAlipay && environment !== 'desktop' && alipayUrl && (
        <div className='mt-5 grid w-full max-w-sm gap-2 sm:grid-cols-2'>
          <Button
            className='min-h-11'
            onClick={() => navigateToPaymentUrl(alipayUrl.href)}
          >
            <HugeiconsIcon
              icon={SmartPhone01Icon}
              strokeWidth={2}
              data-icon='inline-start'
              aria-hidden='true'
            />
            {t('Open Alipay')}
          </Button>
          <Button
            className='min-h-11'
            variant='outline'
            render={
              <a
                href={alipayUrl.href}
                target='_blank'
                rel='noopener noreferrer'
              />
            }
          >
            {t('Open in browser')}
          </Button>
        </div>
      )}

      {isWechatPay && environment === 'mobile' && (
        <div
          className='mt-5 grid justify-items-center gap-2'
          aria-live='polite'
        >
          <Button
            className='min-h-11'
            variant='outline'
            onClick={() => void handleCopyPageLink()}
          >
            <HugeiconsIcon
              icon={Copy01Icon}
              strokeWidth={2}
              data-icon='inline-start'
              aria-hidden='true'
            />
            {copiedText === window.location.href
              ? t('Page link copied')
              : t('Copy page link')}
          </Button>
          {copyFailed && (
            <p className='text-sm text-red-600'>
              {t('Unable to copy. Copy the browser address manually.')}
            </p>
          )}
        </div>
      )}
    </div>
  )
}

export function PaymentCheckoutPage({ tradeNo }: PaymentCheckoutPageProps) {
  const { t } = useTranslation()
  const { systemName, logo } = useSystemConfig()
  const [logoFailed, setLogoFailed] = useState(false)
  const [now, setNow] = useState(() => Date.now())
  const environment = useMemo(() => detectPaymentBrowserEnvironment(), [])
  const returnedCancelled = useMemo(
    () =>
      typeof window !== 'undefined' &&
      isPaymentReturnCancelled(window.location.search),
    []
  )
  const {
    order,
    loading,
    error,
    pollingStoppedReason,
    refresh,
    resumePolling,
  } = usePaymentOrder({ tradeNo, enabled: !!tradeNo })

  useEffect(() => {
    setLogoFailed(false)
  }, [logo])

  useEffect(() => {
    const previousTitle = document.title
    const metaTitle = document.querySelector(
      'meta[name="title"]'
    ) as HTMLMetaElement | null
    const previousMetaTitle = metaTitle?.content
    const title = `${systemName} - ${t('Secure payment')}`
    document.title = title
    metaTitle?.setAttribute('content', title)
    return () => {
      document.title = previousTitle
      if (metaTitle && previousMetaTitle !== undefined) {
        metaTitle.setAttribute('content', previousMetaTitle)
      }
    }
  }, [systemName, t])

  const expiresAt = order?.checkout?.expires_at || order?.expires_at || 0
  useEffect(() => {
    if (!expiresAt) return
    const timer = window.setInterval(() => setNow(Date.now()), 1_000)
    return () => window.clearInterval(timer)
  }, [expiresAt])

  const remainingSeconds = expiresAt
    ? Math.max(0, Math.floor(expiresAt - now / 1000))
    : 0
  const remaining = formatRemainingTime(remainingSeconds)
  const displayedStatus = order
    ? getEffectivePaymentStatus(order.status_code, expiresAt, now)
    : null
  const displayedOrder =
    order && displayedStatus && displayedStatus !== order.status_code
      ? { ...order, status_code: displayedStatus }
      : order
  const statusConfig = displayedStatus ? getStatusConfig(displayedStatus) : null
  const methodLabel = order
    ? getPublicPaymentMethodLabel(order, t)
    : t('Payment method')
  const channelLabel = order
    ? getPublicPaymentChannelAliasLabel(order.channel_alias, t)
    : null
  let methodAccent = 'border-border bg-muted/40 text-foreground'
  if (order?.public_method === 'alipay') {
    methodAccent =
      'border-blue-500/20 bg-blue-500/5 text-blue-700 dark:text-blue-300'
  } else if (order?.public_method === 'wechat_pay') {
    methodAccent =
      'border-green-500/20 bg-green-500/5 text-green-700 dark:text-green-300'
  }

  const handleRefresh = () => {
    if (loading) return
    if (pollingStoppedReason) resumePolling()
    else void refresh()
  }

  let checkoutContent: ReactNode = null
  if (loading && !order) {
    checkoutContent = (
      <div className='flex min-h-96 flex-col items-center justify-center gap-4 p-6'>
        <Spinner
          className='size-9 text-blue-600'
          aria-label={t('Loading payment')}
        />
        <p className='text-muted-foreground text-sm'>{t('Loading payment')}</p>
        <div className='grid w-full max-w-sm gap-3'>
          <Skeleton className='mx-auto h-6 w-40' />
          <Skeleton className='mx-auto h-4 w-64' />
        </div>
      </div>
    )
  } else if (error && !order) {
    checkoutContent = (
      <div className='flex min-h-96 items-center justify-center p-6'>
        <Alert variant='destructive' className='max-w-md'>
          <HugeiconsIcon icon={Alert02Icon} strokeWidth={2} />
          <AlertTitle>{t('Unable to load payment')}</AlertTitle>
          <AlertDescription className='grid gap-3'>
            <p>{t('Check your connection and try again.')}</p>
            <Button
              className='min-h-11 justify-self-start'
              variant='outline'
              onClick={() => void refresh()}
            >
              {t('Retry')}
            </Button>
          </AlertDescription>
        </Alert>
      </div>
    )
  } else if (displayedOrder) {
    checkoutContent = (
      <CheckoutPanel
        order={displayedOrder}
        environment={environment}
        refreshing={loading}
        onRefresh={handleRefresh}
      />
    )
  }

  let amountLabel = t('Not available')
  if (order) {
    amountLabel = formatPaymentDecimalAmount(
      order.payment_amount,
      order.currency
    )
  } else if (loading) {
    amountLabel = t('Preparing')
  }

  return (
    <main
      id='content'
      className='mx-auto w-full max-w-5xl px-3 py-4 sm:px-5 sm:py-7 lg:py-10'
    >
      <div className='mb-4 flex items-center justify-between gap-3 sm:mb-6'>
        <Button
          className='min-h-11'
          variant='ghost'
          render={<Link to='/wallet' />}
        >
          <HugeiconsIcon
            icon={ArrowLeft01Icon}
            strokeWidth={2}
            data-icon='inline-start'
            aria-hidden='true'
          />
          {t('Back to Wallet')}
        </Button>
        <div className='flex min-w-0 items-center gap-2'>
          {logo && !logoFailed && (
            <img
              src={logo}
              alt=''
              className='size-7 rounded-md object-contain'
              referrerPolicy='no-referrer'
              onError={() => setLogoFailed(true)}
            />
          )}
          <span className='truncate text-sm font-medium'>{systemName}</span>
        </div>
      </div>

      {returnedCancelled &&
        (displayedStatus === 'preparing' ||
          displayedStatus === 'awaiting_payment') && (
          <Alert className='mb-4 border-amber-200 bg-amber-50 text-amber-950 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-50'>
            <HugeiconsIcon icon={InformationCircleIcon} strokeWidth={2} />
            <AlertTitle>{t('Payment was cancelled')}</AlertTitle>
            <AlertDescription>
              {t('Payment was cancelled. You can try again.')}
            </AlertDescription>
          </Alert>
        )}

      <Card data-card-hover='false' className='gap-0 overflow-hidden py-0'>
        <CardContent className='p-0'>
          <div className='border-b px-4 py-5 sm:px-6 sm:py-6'>
            <div className='flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between'>
              <div>
                <div className='text-muted-foreground text-sm'>
                  {order?.plan_id ? t('Access purchase') : t('Secure payment')}
                </div>
                <div className='mt-1 text-3xl font-semibold tracking-tight sm:text-4xl'>
                  {amountLabel}
                </div>
              </div>
              <div className='flex flex-wrap items-center gap-2'>
                <span
                  className={cn(
                    'inline-flex min-h-9 items-center gap-2 rounded-lg border px-3 text-sm font-medium',
                    methodAccent
                  )}
                >
                  {order &&
                    getPaymentIcon(
                      getPublicPaymentMethodIconType(order),
                      'h-4 w-4',
                      undefined,
                      methodLabel
                    )}
                  {methodLabel}
                </span>
                {statusConfig && (
                  <StatusBadge
                    label={t(statusConfig.label)}
                    variant={statusConfig.variant}
                    copyable={false}
                    size='lg'
                  />
                )}
              </div>
            </div>
          </div>

          <div className='grid lg:grid-cols-[minmax(0,1fr)_320px]'>
            <section
              className='min-w-0 border-b lg:border-r lg:border-b-0'
              aria-live='polite'
            >
              {checkoutContent}
            </section>

            <aside className='bg-muted/20 grid content-start gap-5 p-4 sm:p-6'>
              <div>
                <h2 className='text-sm font-semibold'>{t('Order details')}</h2>
                <dl className='mt-4 grid gap-3 text-sm'>
                  <div className='grid gap-1'>
                    <dt className='text-muted-foreground'>
                      {t('Order Number')}
                    </dt>
                    <dd className='font-mono text-xs break-all'>{tradeNo}</dd>
                  </div>
                  <div className='flex items-center justify-between gap-3'>
                    <dt className='text-muted-foreground'>
                      {t('Payment Method')}
                    </dt>
                    <dd className='text-right font-medium'>{methodLabel}</dd>
                  </div>
                  {channelLabel && (
                    <div className='flex items-center justify-between gap-3'>
                      <dt className='text-muted-foreground'>
                        {t('Payment option')}
                      </dt>
                      <dd className='text-right font-medium'>{channelLabel}</dd>
                    </div>
                  )}
                  <div className='flex items-center justify-between gap-3'>
                    <dt className='text-muted-foreground'>
                      {t('Time remaining')}
                    </dt>
                    <dd className='font-mono font-semibold tabular-nums'>
                      {expiresAt
                        ? t('{{minutes}} min {{seconds}} sec', remaining)
                        : t('Preparing')}
                    </dd>
                  </div>
                </dl>
              </div>

              <Separator />

              <div className='grid gap-3 text-sm'>
                <div className='flex items-start gap-2'>
                  <HugeiconsIcon
                    icon={InformationCircleIcon}
                    strokeWidth={2}
                    className='mt-0.5 size-4 shrink-0 text-blue-600'
                  />
                  <p className='text-muted-foreground'>
                    {t(
                      'Keep the order number when contacting support about this payment.'
                    )}
                  </p>
                </div>
              </div>

              {(error || pollingStoppedReason) && order && (
                <Alert variant='destructive'>
                  <AlertDescription className='grid gap-3'>
                    <p>
                      {t(
                        'Automatic status updates are paused. Refresh to continue.'
                      )}
                    </p>
                    <Button
                      className='min-h-11 justify-self-start'
                      size='sm'
                      variant='outline'
                      onClick={handleRefresh}
                      disabled={loading}
                    >
                      {t('Refresh status')}
                    </Button>
                  </AlertDescription>
                </Alert>
              )}
            </aside>
          </div>
        </CardContent>
      </Card>

      <p className='text-muted-foreground mt-4 flex items-center justify-center gap-2 text-center text-xs'>
        <HugeiconsIcon
          icon={QrCodeIcon}
          strokeWidth={2}
          className='size-3.5'
          aria-hidden='true'
        />
        {t('This is a payment page from {{site}}.', { site: systemName })}
      </p>
    </main>
  )
}
