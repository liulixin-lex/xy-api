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
import { CheckCircle2, Clock3, Loader2, ShieldAlert } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'

import {
  isPaymentOrderTerminal,
  usePaymentOrder,
} from '../../hooks/use-payment-order'
import { formatPaymentDecimalAmount, getStatusConfig } from '../../lib/billing'
import { isSafePaymentQrContent } from '../../lib/payment'
import type {
  ClientPaymentQuote,
  PaymentOrder,
  PaymentQrStart,
} from '../../types'

interface PaymentQrDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  paymentStart: PaymentQrStart | null
  quote: ClientPaymentQuote | null
  onSettled?: (order: PaymentOrder) => void | Promise<void>
  onTrackPending?: (tradeNo: string) => void
}

export function PaymentQrDialog(props: PaymentQrDialogProps) {
  const { t } = useTranslation()
  const {
    order,
    loading,
    error,
    pollingStoppedReason,
    refresh,
    resumePolling,
  } = usePaymentOrder({
    tradeNo: props.paymentStart?.trade_no,
    enabled: props.open && !!props.paymentStart?.trade_no,
    expiresAt: props.paymentStart?.expires_at,
    onSettled: props.onSettled,
  })
  const qrContent = props.paymentStart?.qr_content || ''
  const qrIsSafe = isSafePaymentQrContent(qrContent)
  const statusConfig = order ? getStatusConfig(order.status) : null
  const isSuccess = order?.status === 'success'
  let statusIcon = <Clock3 className='h-4 w-4 text-amber-600' />
  if (isSuccess) {
    statusIcon = <CheckCircle2 className='h-4 w-4 text-green-600' />
  } else if (loading) {
    statusIcon = <Loader2 className='h-4 w-4 animate-spin' />
  }
  let pollingMessage = t('Unable to refresh payment status')
  if (pollingStoppedReason === 'expired') {
    pollingMessage = t(
      'Automatic status checks stopped because the order expired. Refresh once to confirm the final status.'
    )
  } else if (pollingStoppedReason === 'network') {
    pollingMessage = t(
      'Automatic status checks paused after repeated failures. Check your network and refresh.'
    )
  } else if (pollingStoppedReason === 'timeout') {
    pollingMessage = t(
      'Automatic status checks paused. Refresh to check the latest status.'
    )
  }
  const handleOpenChange = (open: boolean) => {
    if (
      !open &&
      props.paymentStart?.trade_no &&
      (!order || !isPaymentOrderTerminal(order.status))
    ) {
      props.onTrackPending?.(props.paymentStart.trade_no)
    }
    props.onOpenChange(open)
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={handleOpenChange}
      title={t('Scan to Pay')}
      description={t(
        'Scan the code with the selected payment app. Balance is added only after server confirmation.'
      )}
      contentClassName='max-sm:w-[calc(100vw-1.5rem)] sm:max-w-sm'
      contentHeight='auto'
      footer={
        <Button
          type='button'
          variant='outline'
          onClick={() => handleOpenChange(false)}
        >
          {isSuccess ? t('Done') : t('Close')}
        </Button>
      }
    >
      <div className='space-y-4'>
        {qrIsSafe ? (
          <div className='flex justify-center rounded-xl border bg-white p-5'>
            <QRCodeSVG value={qrContent} size={220} level='M' />
          </div>
        ) : (
          <Alert variant='destructive'>
            <ShieldAlert className='h-4 w-4' />
            <AlertTitle>{t('Invalid payment QR code')}</AlertTitle>
            <AlertDescription>
              {t('The payment gateway returned an unsupported QR code.')}
            </AlertDescription>
          </Alert>
        )}

        {props.quote && (
          <div className='grid grid-cols-2 gap-3 rounded-lg border p-3 text-sm'>
            <span className='text-muted-foreground'>{t('Amount Due')}</span>
            <span className='text-right font-semibold'>
              {formatPaymentDecimalAmount(
                props.quote.payable_amount,
                props.quote.currency,
                props.quote.provider
              )}
            </span>
            <span className='text-muted-foreground'>{t('Order Number')}</span>
            <code className='truncate text-right text-xs'>
              {props.paymentStart?.trade_no}
            </code>
          </div>
        )}

        <div className='flex items-center justify-between rounded-lg border p-3'>
          <div className='flex items-center gap-2 text-sm'>
            {statusIcon}
            <span>{t('Payment Status')}</span>
          </div>
          {statusConfig ? (
            <StatusBadge
              label={t(statusConfig.label)}
              variant={statusConfig.variant}
              copyable={false}
              showDot
            />
          ) : (
            <span className='text-muted-foreground text-sm'>
              {loading ? t('Checking...') : t('Pending')}
            </span>
          )}
        </div>

        {(error || pollingStoppedReason) && (
          <Alert variant='destructive'>
            <AlertDescription className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
              <span>{pollingMessage}</span>
              <Button
                type='button'
                size='sm'
                variant='outline'
                className='self-start sm:self-auto'
                disabled={loading}
                onClick={() => {
                  if (pollingStoppedReason) resumePolling()
                  else void refresh()
                }}
              >
                {t('Refresh status')}
              </Button>
            </AlertDescription>
          </Alert>
        )}
      </div>
    </Dialog>
  )
}
