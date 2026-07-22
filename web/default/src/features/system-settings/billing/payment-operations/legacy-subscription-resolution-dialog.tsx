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

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
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
import { getApiErrorMessage } from '@/lib/api-error'

import { resolveLegacySubscription } from './api'
import {
  buildLegacySubscriptionResolutionRequest,
  isStripeLegacyRecurringCheckoutReview,
} from './legacy-subscription-resolution'
import {
  PAYMENT_PROVIDER_REFERENCE_MAX_BYTES,
  isPaymentAuditReasonValid,
  isPaymentProviderReferenceValid,
} from './payment-action-validation'
import { formatMinorAmount } from './status'
import type { PaymentEvent } from './types'
import { usePaymentOperationVerification } from './verification-context'

export function LegacySubscriptionResolutionDialog(props: {
  event: PaymentEvent | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onCompleted: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const { runWithVerification, verificationOpen } =
    usePaymentOperationVerification()
  const [providerRefundReference, setProviderRefundReference] = useState('')
  const [reason, setReason] = useState('')

  const providerRefundReferenceValid = isPaymentProviderReferenceValid(
    providerRefundReference
  )
  const reasonValid = isPaymentAuditReasonValid(reason)
  const request = buildLegacySubscriptionResolutionRequest(
    props.event,
    providerRefundReference,
    reason
  )
  const isStripeRecurringCheckout = isStripeLegacyRecurringCheckoutReview(
    props.event
  )

  useEffect(() => {
    if (!props.open) return
    setProviderRefundReference('')
    setReason('')
  }, [props.event, props.open])

  const mutation = useMutation({
    mutationFn: async () => {
      if (!request) return null
      return runWithVerification(
        async () => {
          const result = await resolveLegacySubscription(request)
          toast.success(
            t(
              'Completed external refund recorded. No subscription access was granted.'
            )
          )
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
          title: t('Verify completed external refund record'),
          description: t(
            'Confirm your identity before recording this permanent financial outcome.'
          ),
        }
      )
    },
    onError: (error) => {
      toast.error(
        getApiErrorMessage(
          error,
          t('Failed to record the legacy subscription external refund.')
        )
      )
      props.onOpenChange(false)
      void Promise.resolve(props.onCompleted()).catch(() => {
        toast.error(
          t(
            'The resolution failed and the latest event state could not be loaded. Refresh manually before trying again.'
          )
        )
      })
    },
  })
  const operationPending = mutation.isPending || verificationOpen

  const handleOpenChange = (open: boolean) => {
    if (operationPending) return
    props.onOpenChange(open)
  }

  return (
    <Dialog open={props.open} onOpenChange={handleOpenChange}>
      <DialogContent
        className='max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-xl'
        showCloseButton={!operationPending}
        aria-busy={operationPending}
      >
        <DialogHeader>
          <DialogTitle>{t('Record completed external refund')}</DialogTitle>
          <DialogDescription>
            {isStripeRecurringCheckout
              ? t(
                  'This historical recurring Stripe Checkout was paid, but subscription access was not safely created. Record only a full refund already completed in Stripe; this system will close the payment without granting access.'
                )
              : t(
                  'This paid legacy subscription cannot be fulfilled because its original plan contract is no longer available. Record only a full refund already completed by the payment provider; this system will close the payment without granting access.'
                )}
          </DialogDescription>
        </DialogHeader>

        <Alert variant='destructive'>
          <AlertTitle>{t('This does not issue a refund')}</AlertTitle>
          <AlertDescription>
            {t(
              'Continue only after the payment provider has completed the full refund. This system records the evidence, closes the legacy payment, and grants no subscription access.'
            )}
          </AlertDescription>
        </Alert>

        <dl className='grid gap-3 rounded-lg border p-3 text-sm sm:grid-cols-2'>
          <div className='grid min-w-0 gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Provider Event')}
            </dt>
            <dd className='truncate font-mono'>
              {props.event
                ? `${props.event.provider} · ${props.event.event_key}`
                : '-'}
            </dd>
          </div>
          <div className='grid min-w-0 gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Trade Number')}
            </dt>
            <dd className='truncate font-mono'>
              {props.event?.trade_no || '-'}
            </dd>
          </div>
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Paid Amount')}
            </dt>
            <dd className='tabular-nums'>
              {props.event
                ? formatMinorAmount(
                    props.event.paid_amount_minor,
                    props.event.currency,
                    props.event.provider
                  )
                : '-'}
            </dd>
          </div>
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Expected Event Attempts')}
            </dt>
            <dd className='tabular-nums'>{props.event?.attempts ?? '-'}</dd>
          </div>
        </dl>

        <div className='grid gap-2'>
          <Label htmlFor='legacy-subscription-provider-refund-reference'>
            {t('Provider refund reference')}
          </Label>
          <Input
            id='legacy-subscription-provider-refund-reference'
            value={providerRefundReference}
            maxLength={PAYMENT_PROVIDER_REFERENCE_MAX_BYTES}
            autoComplete='off'
            required
            aria-required='true'
            disabled={operationPending}
            aria-invalid={
              providerRefundReference.length > 0 &&
              !providerRefundReferenceValid
            }
            aria-describedby='legacy-subscription-provider-refund-reference-help'
            onChange={(event) => setProviderRefundReference(event.target.value)}
          />
          <p
            id='legacy-subscription-provider-refund-reference-help'
            aria-live='polite'
            className={
              providerRefundReference.length > 0 &&
              !providerRefundReferenceValid
                ? 'text-destructive text-xs'
                : 'text-muted-foreground text-xs'
            }
          >
            {providerRefundReference.length > 0 && !providerRefundReferenceValid
              ? t('Use 1 to {{maximum}} UTF-8 bytes.', {
                  maximum: PAYMENT_PROVIDER_REFERENCE_MAX_BYTES,
                })
              : t(
                  'Required. Copy the completed refund reference from the provider record. Use 1 to {{maximum}} UTF-8 bytes.',
                  { maximum: PAYMENT_PROVIDER_REFERENCE_MAX_BYTES }
                )}
          </p>
        </div>

        <div className='grid gap-2'>
          <Label htmlFor='legacy-subscription-resolution-reason'>
            {t('Administrator evidence and reason')}
          </Label>
          <Textarea
            id='legacy-subscription-resolution-reason'
            value={reason}
            minLength={8}
            maxLength={512}
            className='min-h-24'
            required
            aria-required='true'
            disabled={operationPending}
            aria-invalid={reason.length > 0 && !reasonValid}
            aria-describedby='legacy-subscription-resolution-reason-help'
            placeholder={t(
              'Document where the provider shows the completed full refund and why this legacy payment must close without access.'
            )}
            onChange={(event) => setReason(event.target.value)}
          />
          <p
            id='legacy-subscription-resolution-reason-help'
            aria-live='polite'
            className={
              reason.length > 0 && !reasonValid
                ? 'text-destructive text-xs'
                : 'text-muted-foreground text-xs'
            }
          >
            {t('Use 8 to 512 UTF-8 bytes.')}
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
            disabled={operationPending || request === null}
            onClick={() => mutation.mutate()}
          >
            {mutation.isPending
              ? t('Applying...')
              : t('Record completed refund')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
