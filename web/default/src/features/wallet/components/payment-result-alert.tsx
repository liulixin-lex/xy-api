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
import { CheckCircle2, Clock3, X, XCircle } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'

import { usePaymentOrder } from '../hooks/use-payment-order'
import { formatPaymentDecimalAmount, getStatusConfig } from '../lib/billing'
import type { PaymentOrder } from '../types'

interface PaymentResultAlertProps {
  tradeNo: string
  resultHint?: string
  onDismiss: () => void
  onSettled?: (order: PaymentOrder) => void | Promise<void>
  onOpenHistory?: () => void
}

export function PaymentResultAlert(props: PaymentResultAlertProps) {
  const { t } = useTranslation()
  const {
    order,
    loading,
    error,
    pollingStoppedReason,
    refresh,
    resumePolling,
  } = usePaymentOrder({
    tradeNo: props.tradeNo,
    enabled: true,
    onSettled: props.onSettled,
  })
  const statusConfig = order ? getStatusConfig(order.status_code) : null
  const succeeded = order?.status_code === 'succeeded'
  const pending =
    !order ||
    order.status_code === 'preparing' ||
    order.status_code === 'awaiting_payment' ||
    order.status_code === 'confirming'
  const variant =
    error || order?.status_code === 'temporarily_unavailable'
      ? 'destructive'
      : 'default'
  let resultHintLabel = t('Checking your payment')
  if (props.resultHint === 'success') {
    resultHintLabel = t('Payment submitted')
  } else if (props.resultHint === 'cancelled') {
    resultHintLabel = t('Payment was cancelled')
  } else if (props.resultHint === 'failed') {
    resultHintLabel = t('Payment could not be completed')
  } else if (props.resultHint === 'pending') {
    resultHintLabel = t('Payment confirmation is pending')
  }
  let statusIcon = <XCircle className='h-4 w-4' />
  if (succeeded) {
    statusIcon = <CheckCircle2 className='h-4 w-4 text-green-600' />
  } else if (pending && loading) {
    statusIcon = <Spinner aria-label={t('Checking...')} />
  } else if (pending) {
    statusIcon = <Clock3 className='h-4 w-4 text-amber-600' />
  }
  const title =
    order && statusConfig ? t(statusConfig.label) : t('Checking payment status')
  let description = (
    <span>
      {t(
        'Checking the latest order status. Your balance changes only after confirmation.'
      )}
      {` (${resultHintLabel})`}
    </span>
  )
  if (pollingStoppedReason === 'expired') {
    description = (
      <span>
        {t(
          'Automatic status checks stopped because the order expired. Refresh once to confirm the final status.'
        )}
      </span>
    )
  } else if (pollingStoppedReason === 'network') {
    description = (
      <span>
        {t(
          'Automatic status checks paused after repeated failures. Check your network and refresh.'
        )}
      </span>
    )
  } else if (pollingStoppedReason === 'timeout') {
    description = (
      <span>
        {t(
          'Automatic status checks paused. Refresh to check the latest status.'
        )}
      </span>
    )
  } else if (error) {
    description = <span>{t('Unable to refresh payment status')}</span>
  } else if (order) {
    description = (
      <span>
        {t('Order Number')}: {order.trade_no}
        {' · '}
        {formatPaymentDecimalAmount(order.payment_amount, order.currency)}
      </span>
    )
  }

  return (
    <Alert variant={variant} className='gap-y-2' aria-live='polite'>
      {statusIcon}
      <AlertTitle>{title}</AlertTitle>
      <AlertDescription className='min-w-0 break-words'>
        {description}
      </AlertDescription>
      <div className='col-span-full mt-1 flex flex-wrap items-center justify-end gap-2 sm:col-span-1 sm:col-start-2'>
        {(error || pollingStoppedReason) && (
          <Button
            type='button'
            size='sm'
            variant='outline'
            disabled={loading}
            onClick={() => {
              if (pollingStoppedReason) resumePolling()
              else void refresh()
            }}
          >
            {t('Refresh status')}
          </Button>
        )}
        {order && props.onOpenHistory && (
          <Button
            type='button'
            size='sm'
            variant='outline'
            onClick={props.onOpenHistory}
          >
            {t('History')}
          </Button>
        )}
        {!pending && (
          <Button
            type='button'
            size='icon-sm'
            variant='ghost'
            aria-label={t('Dismiss')}
            onClick={props.onDismiss}
          >
            <X className='h-4 w-4' />
          </Button>
        )}
      </div>
    </Alert>
  )
}
