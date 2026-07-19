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
import { useMutation } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { resolveBillingReservation } from './api'
import {
  MAX_BILLING_QUOTA,
  billingReservationResolutionError,
  parseBillingActualQuota,
} from './billing-reservation'
import { formatInteger } from './status'
import type { BillingReservation } from './types'
import { usePaymentOperationVerification } from './verification-context'

type Resolution = 'settle' | 'refund'

export function BillingReservationResolutionDialog(props: {
  reservation: BillingReservation | null
  resolution: Resolution | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onCompleted: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const { runWithVerification, verificationOpen } =
    usePaymentOperationVerification()
  const [reason, setReason] = useState('')
  const [actualQuota, setActualQuota] = useState('')
  const trimmedReason = reason.trim()
  const reasonLength = [...trimmedReason].length
  const parsedActualQuota = parseBillingActualQuota(actualQuota)
  const isSettle = props.resolution === 'settle'
  const reasonValid = reasonLength >= 8 && reasonLength <= 512
  const formValid =
    Boolean(props.reservation && props.resolution) && reasonValid
  const mutation = useMutation({
    mutationFn: (request: Parameters<typeof resolveBillingReservation>[0]) =>
      runWithVerification(
        async () => {
          const result = await resolveBillingReservation(request)
          let message = t(
            'This billing reservation was already resolved by the same action'
          )
          if (result.applied) {
            message = isSettle
              ? t('Billing reservation settled')
              : t('Billing reservation refunded')
          }
          toast.success(message)
          props.onOpenChange(false)
          try {
            await props.onCompleted()
          } catch {
            toast.error(
              t(
                'The action completed, but the latest data could not be loaded. Refresh manually.'
              )
            )
          }
          return result
        },
        {
          preferredMethod: 'passkey',
          title: isSettle
            ? t('Verify billing reservation settlement')
            : t('Verify billing reservation refund'),
          description: t(
            'Confirm your identity before applying this audited billing resolution.'
          ),
        }
      ),
    onError: (error) => {
      toast.error(billingReservationResolutionError(error, t))
    },
  })
  const operationPending = mutation.isPending || verificationOpen

  useEffect(() => {
    if (!props.open || !props.reservation) {
      return
    }
    setReason('')
    setActualQuota(
      String(
        props.reservation.settlement_pending
          ? props.reservation.settlement_target
          : props.reservation.reserved_quota
      )
    )
  }, [props.open, props.reservation, props.resolution])

  const handleOpenChange = (open: boolean) => {
    if (operationPending) {
      return
    }
    props.onOpenChange(open)
  }

  const handleConfirm = () => {
    if (!props.reservation || !props.resolution || !formValid) {
      return
    }
    if (props.resolution === 'settle' && parsedActualQuota === null) {
      return
    }
    mutation.mutate({
      request_id: props.reservation.request_id,
      expected_version: props.reservation.version,
      resolution: props.resolution,
      ...(props.resolution === 'settle'
        ? { actual_quota: parsedActualQuota ?? undefined }
        : {}),
      reason: trimmedReason,
    })
  }

  let confirmLabel = t('Confirm full refund')
  if (isSettle) {
    confirmLabel = t('Confirm exact settlement')
  }
  if (mutation.isPending) {
    confirmLabel = t('Resolving...')
  }

  return (
    <Dialog open={props.open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {isSettle
              ? t('Settle billing reservation')
              : t('Refund billing reservation')}
          </DialogTitle>
          <DialogDescription>
            {t(
              'The reservation version and review marker are checked again while the balance row is locked.'
            )}
          </DialogDescription>
        </DialogHeader>

        <Alert variant='destructive'>
          <AlertDescription>
            {isSettle
              ? t(
                  'Settlement records the exact actual quota below and adjusts the difference against the reserved amount.'
                )
              : t(
                  'Refund returns the entire reserved amount to its original funding source. No actual quota is recorded.'
                )}
          </AlertDescription>
        </Alert>

        <dl className='grid gap-3 rounded-lg border p-3 text-sm sm:grid-cols-2'>
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>{t('Request ID')}</dt>
            <dd className='truncate font-mono'>
              {props.reservation?.request_id || '-'}
            </dd>
          </div>
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Expected Version')}
            </dt>
            <dd className='tabular-nums'>
              {props.reservation?.version ?? '-'}
            </dd>
          </div>
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Reserved Quota')}
            </dt>
            <dd className='tabular-nums'>
              {props.reservation
                ? formatInteger(props.reservation.reserved_quota)
                : '-'}
            </dd>
          </div>
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Funding Source')}
            </dt>
            <dd>
              {props.reservation?.funding_source === 'subscription'
                ? t('Subscription quota')
                : t('Wallet quota')}
            </dd>
          </div>
        </dl>

        {isSettle && (
          <div className='grid gap-2'>
            <Label htmlFor='billing-reservation-actual-quota'>
              {t('Exact Actual Quota')}
            </Label>
            <Input
              id='billing-reservation-actual-quota'
              value={actualQuota}
              inputMode='numeric'
              autoComplete='off'
              placeholder={t('Enter the final whole-number quota')}
              onChange={(event) => setActualQuota(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('Required whole number from 0 to {{maximum}}.', {
                maximum: formatInteger(MAX_BILLING_QUOTA),
              })}
            </p>
          </div>
        )}

        <div className='grid gap-2'>
          <Label htmlFor='billing-reservation-resolution-reason'>
            {t('Administrator evidence and reason')}
          </Label>
          <Textarea
            id='billing-reservation-resolution-reason'
            value={reason}
            minLength={8}
            maxLength={512}
            className='min-h-28'
            placeholder={t(
              'Record the provider evidence, resource outcome, and why this exact financial action is correct.'
            )}
            onChange={(event) => setReason(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
            {t('At least 8 characters are required.')}
          </p>
        </div>

        <DialogFooter>
          <Button
            type='button'
            variant='outline'
            disabled={operationPending}
            onClick={() => handleOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='button'
            variant='destructive'
            disabled={
              operationPending ||
              !formValid ||
              (props.resolution === 'settle' && parsedActualQuota === null)
            }
            onClick={handleConfirm}
          >
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
