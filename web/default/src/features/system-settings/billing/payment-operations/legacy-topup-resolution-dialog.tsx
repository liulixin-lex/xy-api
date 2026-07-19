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
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { getApiErrorMessage } from '@/lib/api-error'

import { resolveLegacyEpayTopUp } from './api'
import {
  buildLegacyTopUpResolutionRequest,
  type LegacyTopUpResolution,
} from './legacy-topup-resolution'
import {
  LEGACY_TOPUP_CREDIT_QUOTA_MAX,
  PAYMENT_PROVIDER_REFERENCE_MAX_BYTES,
  isPaymentAuditReasonValid,
  isPaymentProviderReferenceValid,
  parseLegacyTopUpCreditQuota,
} from './payment-action-validation'
import { formatInteger, formatMinorAmount } from './status'
import type { PaymentEvent } from './types'
import { usePaymentOperationVerification } from './verification-context'

export function LegacyTopUpResolutionDialog(props: {
  event: PaymentEvent | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onCompleted: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const { runWithVerification, verificationOpen } =
    usePaymentOperationVerification()
  const [resolution, setResolution] = useState<LegacyTopUpResolution | ''>('')
  const [creditQuota, setCreditQuota] = useState('')
  const [providerRefundReference, setProviderRefundReference] = useState('')
  const [reason, setReason] = useState('')

  const creditQuotaValid = parseLegacyTopUpCreditQuota(creditQuota) !== null
  const providerRefundReferenceValid = isPaymentProviderReferenceValid(
    providerRefundReference
  )
  const reasonValid = isPaymentAuditReasonValid(reason)
  const request = buildLegacyTopUpResolutionRequest(props.event, {
    resolution,
    creditQuota,
    providerRefundReference,
    reason,
  })

  useEffect(() => {
    if (!props.open) return
    setResolution('')
    setCreditQuota('')
    setProviderRefundReference('')
    setReason('')
  }, [props.event, props.open])

  const mutation = useMutation({
    mutationFn: async () => {
      if (!request) return null
      return runWithVerification(
        async () => {
          const result = await resolveLegacyEpayTopUp(request)
          toast.success(
            request.resolution === 'fulfill'
              ? t('Legacy top-up fulfilled with explicit quota')
              : t('Legacy top-up external refund recorded')
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
          title:
            request.resolution === 'fulfill'
              ? t('Verify explicit legacy top-up fulfillment')
              : t('Verify legacy top-up external refund'),
          description: t(
            'Confirm your identity before applying this audited financial resolution.'
          ),
        }
      )
    },
    onError: (error) => {
      toast.error(
        getApiErrorMessage(error, t('Failed to resolve legacy Epay top-up'))
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

  let confirmLabel = t('Select a terminal resolution')
  if (resolution === 'fulfill') {
    confirmLabel = t('Confirm explicit quota credit')
  } else if (resolution === 'external_refund') {
    confirmLabel = t('Confirm external refund')
  }
  if (mutation.isPending) confirmLabel = t('Applying...')

  let riskDescription = t(
    'Use preserved provider and accounting evidence to choose the outcome. The server will reject stale or ineligible events.'
  )
  if (resolution === 'fulfill') {
    riskDescription = t(
      'The account receives exactly the quota entered below. Current QPU is never read or recalculated.'
    )
  } else if (resolution === 'external_refund') {
    riskDescription = t(
      'This records a refund that was already completed by the provider. It does not initiate or verify a refund automatically.'
    )
  }

  return (
    <Dialog open={props.open} onOpenChange={handleOpenChange}>
      <DialogContent
        className='max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-xl'
        showCloseButton={!operationPending}
        aria-busy={operationPending}
      >
        <DialogHeader>
          <DialogTitle>{t('Resolve legacy Epay top-up')}</DialogTitle>
          <DialogDescription>
            {t(
              'Choose one terminal outcome for a paid legacy event whose original quota snapshot is unavailable.'
            )}
          </DialogDescription>
        </DialogHeader>

        <Alert variant='destructive'>
          <AlertTitle>{t('Permanent financial action')}</AlertTitle>
          <AlertDescription>{riskDescription}</AlertDescription>
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
          <Label htmlFor='legacy-topup-resolution'>
            {t('Terminal resolution')}
          </Label>
          <NativeSelect
            id='legacy-topup-resolution'
            className='w-full'
            value={resolution}
            required
            aria-required='true'
            disabled={operationPending}
            onChange={(event) =>
              setResolution(event.target.value as LegacyTopUpResolution | '')
            }
          >
            <NativeSelectOption value='' disabled>
              {t('Select a terminal resolution')}
            </NativeSelectOption>
            <NativeSelectOption value='fulfill'>
              {t('Fulfill with explicit quota')}
            </NativeSelectOption>
            <NativeSelectOption value='external_refund'>
              {t('Confirm completed external refund')}
            </NativeSelectOption>
          </NativeSelect>
        </div>

        {resolution === 'fulfill' && (
          <div className='grid gap-2'>
            <Label htmlFor='legacy-topup-credit-quota'>
              {t('Explicit credit quota')}
            </Label>
            <Input
              id='legacy-topup-credit-quota'
              value={creditQuota}
              inputMode='numeric'
              autoComplete='off'
              maxLength={10}
              required
              pattern='[0-9]*'
              aria-required='true'
              disabled={operationPending}
              aria-invalid={creditQuota.length > 0 && !creditQuotaValid}
              aria-describedby='legacy-topup-credit-quota-help'
              onChange={(event) => setCreditQuota(event.target.value)}
            />
            <p
              id='legacy-topup-credit-quota-help'
              aria-live='polite'
              className={
                creditQuota.length > 0 && !creditQuotaValid
                  ? 'text-destructive text-xs'
                  : 'text-muted-foreground text-xs'
              }
            >
              {creditQuota.length > 0 && !creditQuotaValid
                ? t('Enter a whole number from 1 to {{maximum}}.', {
                    maximum: formatInteger(LEGACY_TOPUP_CREDIT_QUOTA_MAX),
                  })
                : t(
                    'Enter the exact quota from preserved historical evidence, from 1 to {{maximum}}. Current QPU is not used.',
                    {
                      maximum: formatInteger(LEGACY_TOPUP_CREDIT_QUOTA_MAX),
                    }
                  )}
            </p>
          </div>
        )}

        {resolution === 'external_refund' && (
          <div className='grid gap-2'>
            <Label htmlFor='legacy-topup-provider-refund-reference'>
              {t('Provider refund reference')}
            </Label>
            <Input
              id='legacy-topup-provider-refund-reference'
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
              aria-describedby='legacy-topup-provider-refund-reference-help'
              onChange={(event) =>
                setProviderRefundReference(event.target.value)
              }
            />
            <p
              id='legacy-topup-provider-refund-reference-help'
              aria-live='polite'
              className={
                providerRefundReference.length > 0 &&
                !providerRefundReferenceValid
                  ? 'text-destructive text-xs'
                  : 'text-muted-foreground text-xs'
              }
            >
              {providerRefundReference.length > 0 &&
              !providerRefundReferenceValid
                ? t('Use 1 to {{maximum}} UTF-8 bytes.', {
                    maximum: PAYMENT_PROVIDER_REFERENCE_MAX_BYTES,
                  })
                : t(
                    'Required. Copy the completed refund reference from the provider record. Use 1 to {{maximum}} UTF-8 bytes.',
                    { maximum: PAYMENT_PROVIDER_REFERENCE_MAX_BYTES }
                  )}
            </p>
          </div>
        )}

        <div className='grid gap-2'>
          <Label htmlFor='legacy-topup-resolution-reason'>
            {t('Administrator evidence and reason')}
          </Label>
          <Textarea
            id='legacy-topup-resolution-reason'
            value={reason}
            minLength={8}
            maxLength={512}
            className='min-h-24'
            required
            aria-required='true'
            disabled={operationPending}
            aria-invalid={reason.length > 0 && !reasonValid}
            aria-describedby='legacy-topup-resolution-reason-help'
            placeholder={t(
              'Record the historical evidence, provider verification, and why this exact terminal outcome is correct.'
            )}
            onChange={(event) => setReason(event.target.value)}
          />
          <p
            id='legacy-topup-resolution-reason-help'
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
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
