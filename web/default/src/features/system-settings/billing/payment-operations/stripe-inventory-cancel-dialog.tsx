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
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { isVerificationRequiredError } from '@/lib/secure-verification'

import {
  getPaymentAdminErrorCode,
  getPaymentAdminErrorMessage,
} from '../../payment-admin-errors'
import { cancelAdminStripeSubscriptionAtPeriodEnd } from './api'
import { formatUnixTime } from './status'
import {
  canScheduleStripeSubscriptionCancellation,
  isStripeCancellationReasonValid,
} from './stripe-inventory-cancellation'
import type { StripeLegacySubscription } from './types'
import { usePaymentOperationVerification } from './verification-context'

export function StripeInventoryCancelDialog(props: {
  item: StripeLegacySubscription | null
  onOpenChange: (open: boolean) => void
  onCompleted: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const { runWithVerification, verificationOpen } =
    usePaymentOperationVerification()
  const [reason, setReason] = useState('')
  const item = props.item
  const trimmedReason = reason.trim()
  const reasonByteLength = new TextEncoder().encode(trimmedReason).length
  const reasonValid = isStripeCancellationReasonValid(reason)
  const itemEligible = item
    ? canScheduleStripeSubscriptionCancellation(item)
    : false

  useEffect(() => {
    if (item) setReason('')
  }, [item])

  const mutation = useMutation({
    mutationFn: async () => {
      if (!item || !itemEligible || !reasonValid) return null
      const performCancellation = async () => {
        try {
          const result = await cancelAdminStripeSubscriptionAtPeriodEnd({
            inventory_id: item.id,
            expected_updated_at: item.expected_updated_at,
            reason: trimmedReason,
          })
          props.onOpenChange(false)
          try {
            await props.onCompleted()
            toast.success(
              result.duplicate
                ? t('Stripe cancellation was already scheduled')
                : t('Stripe cancellation scheduled for the period end')
            )
          } catch {
            toast.warning(
              t(
                'Stripe accepted the cancellation, but the refreshed inventory could not be loaded.'
              )
            )
          }
          return result
        } catch (error) {
          if (isVerificationRequiredError(error)) throw error
          const safeError = new Error(
            getPaymentAdminErrorMessage(
              error,
              t,
              t('Failed to schedule Stripe subscription cancellation')
            )
          ) as Error & { code?: string; skipGlobalError: true }
          safeError.skipGlobalError = true
          const code = getPaymentAdminErrorCode(error)
          if (code) {
            safeError.code = code
            if (
              code === 'stripe_inventory_cancel_conflict' ||
              code === 'stripe_inventory_subscription_not_found'
            ) {
              void props.onCompleted()
            }
          }
          throw safeError
        }
      }
      return runWithVerification(performCancellation, {
        preferredMethod: 'passkey',
        title: t('Verify Stripe subscription cancellation'),
        description: t(
          'Confirm your identity before scheduling this audited change in Stripe.'
        ),
      })
    },
    onError: (error) => {
      toast.error(
        getPaymentAdminErrorMessage(
          error,
          t,
          t('Failed to schedule Stripe subscription cancellation')
        )
      )
    },
  })
  const operationPending = mutation.isPending || verificationOpen

  const handleOpenChange = (open: boolean) => {
    if (operationPending) return
    if (!open) setReason('')
    props.onOpenChange(open)
  }

  return (
    <Dialog open={Boolean(item)} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('Cancel Stripe renewal at period end?')}</DialogTitle>
          <DialogDescription>
            {t(
              'Stripe will stop renewing this legacy subscription after its current billing period.'
            )}
          </DialogDescription>
        </DialogHeader>

        <Alert variant='destructive'>
          <AlertDescription>
            {t(
              'This changes Stripe renewal state only. It does not issue a refund or change local access.'
            )}
          </AlertDescription>
        </Alert>

        <dl className='grid gap-3 rounded-lg border p-3 text-sm'>
          <div className='grid min-w-0 gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Stripe Subscription ID')}
            </dt>
            <dd className='truncate font-mono'>
              {item?.stripe_subscription_id || '-'}
            </dd>
          </div>
          <div className='grid min-w-0 gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Current Period End')}
            </dt>
            <dd>{formatUnixTime(item?.current_period_end || 0)}</dd>
          </div>
        </dl>

        <div className='grid gap-2'>
          <Label htmlFor='stripe-cancellation-reason'>
            {t('Cancellation reason')}
          </Label>
          <Textarea
            id='stripe-cancellation-reason'
            value={reason}
            rows={4}
            disabled={operationPending}
            aria-invalid={reasonByteLength > 0 && !reasonValid}
            placeholder={t(
              'Explain why this legacy renewal should stop (8-512 UTF-8 bytes)'
            )}
            onChange={(event) => setReason(event.target.value)}
          />
          <div className='flex items-start justify-between gap-3 text-xs'>
            <span
              className={
                reasonByteLength > 0 && !reasonValid
                  ? 'text-destructive'
                  : 'text-muted-foreground'
              }
            >
              {reasonByteLength > 0 && !reasonValid
                ? t('Reason must be between 8 and 512 UTF-8 bytes')
                : t('This reason is stored in the payment operations audit.')}
            </span>
            <span className='text-muted-foreground shrink-0 tabular-nums'>
              {reasonByteLength} / 512
            </span>
          </div>
        </div>

        <DialogFooter>
          <Button
            type='button'
            variant='outline'
            disabled={operationPending}
            onClick={() => handleOpenChange(false)}
          >
            {t('Keep subscription active')}
          </Button>
          <Button
            type='button'
            variant='destructive'
            disabled={operationPending || !reasonValid || !itemEligible}
            onClick={() => mutation.mutate()}
          >
            {mutation.isPending
              ? t('Scheduling cancellation...')
              : t('Cancel at period end')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
