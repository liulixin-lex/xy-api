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
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { getApiErrorMessage } from '@/lib/api-error'

import {
  acknowledgePaymentCredentialIncident,
  confirmExternalPaymentRefund,
  dismissUnmatchedPaymentEvent,
  linkUnmatchedPaymentEvent,
  rejectPaymentOrder,
  resolvePaymentCredentialIncident,
  resolvePaymentDebt,
  retireStripeCustomerBinding,
  retryLegacyEpayPaymentEvent,
  voidPaymentOrder,
} from './api'
import {
  PAYMENT_PROVIDER_REFERENCE_MAX_BYTES,
  isExternalRefundAmountValid,
  isPaymentAuditReasonValid,
  isPaymentProviderReferenceValid,
  isPaymentTradeNoValid,
  parsePositiveSafeInteger,
} from './payment-action-validation'
import {
  formatInteger,
  formatMinorAmount,
  getCredentialIncidentActions,
  isPaymentEventActionAvailable,
} from './status'
import type {
  PaymentCustomerBinding,
  PaymentDebt,
  PaymentEvent,
  PaymentOrder,
} from './types'
import { usePaymentOperationVerification } from './verification-context'

type PaymentOrderAction = 'reject' | 'void' | 'external-refund'
type CredentialIncidentAction = 'acknowledge' | 'resolve'
type UnmatchedEventAction = 'dismiss' | 'link' | 'retry_legacy'

async function refreshAfterFinancialAction(
  refresh: () => void | Promise<void>,
  refreshErrorMessage: string
) {
  try {
    await refresh()
  } catch {
    toast.error(refreshErrorMessage)
  }
}

export function PaymentOrderActionDialog(props: {
  order: PaymentOrder | null
  action: PaymentOrderAction | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onCompleted: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const { runWithVerification, verificationOpen } =
    usePaymentOperationVerification()
  const [reason, setReason] = useState('')
  const [refundedAmountMinor, setRefundedAmountMinor] = useState('')
  const [providerRefundReference, setProviderRefundReference] = useState('')
  const trimmedReason = reason.trim()
  const parsedRefundedAmount = parsePositiveSafeInteger(refundedAmountMinor)
  const reasonValid = isPaymentAuditReasonValid(reason)
  const refundValid =
    props.action !== 'external-refund' ||
    Boolean(
      props.order &&
      isExternalRefundAmountValid(props.order, parsedRefundedAmount)
    )
  const refundReferenceValid =
    props.action !== 'external-refund' ||
    isPaymentProviderReferenceValid(providerRefundReference)

  useEffect(() => {
    if (!props.open) return
    setReason('')
    setProviderRefundReference('')
    setRefundedAmountMinor(
      props.action === 'external-refund' && props.order
        ? String(props.order.expected_amount_minor)
        : ''
    )
  }, [props.action, props.open, props.order])

  let title = t('Reject payment order')
  let description = t(
    'Reject this manual-review order after confirming that the provider did not complete a valid payment.'
  )
  let successMessage = t('Payment order rejected')
  let failureMessage = t('Failed to reject payment order')
  let confirmLabel = t('Confirm rejection')
  let verificationTitle = t('Verify payment rejection')
  if (props.action === 'void') {
    title = t('Locally void payment order')
    description = t(
      'Only marks the order as void locally. It does not cancel Stripe Checkout or an Epay/XORPay upstream order. If the customer pays later, the payment will enter manual review.'
    )
    successMessage = t('Payment order voided')
    failureMessage = t('Failed to void payment order')
    confirmLabel = t('Confirm local void')
    verificationTitle = t('Verify payment void')
  }
  if (props.action === 'external-refund') {
    title = t('Confirm external refund')
    description = t(
      'Record a refund that has already been completed in the provider dashboard. This does not initiate a provider refund.'
    )
    successMessage = t('External refund confirmed')
    failureMessage = t('Failed to confirm external payment refund')
    confirmLabel = t('Confirm refund record')
    verificationTitle = t('Verify external refund confirmation')
  }

  const mutation = useMutation({
    mutationFn: async () => {
      const order = props.order
      const action = props.action
      if (
        !order ||
        !action ||
        !reasonValid ||
        !refundValid ||
        !refundReferenceValid
      ) {
        return null
      }
      return runWithVerification(
        async () => {
          const request = {
            trade_no: order.trade_no,
            expected_version: order.version,
            reason: trimmedReason,
          }
          let result: PaymentOrder
          if (action === 'reject') {
            result = await rejectPaymentOrder(request)
          } else if (action === 'void') {
            result = await voidPaymentOrder(request)
          } else {
            result = await confirmExternalPaymentRefund({
              ...request,
              refunded_amount_minor: parsedRefundedAmount as number,
              provider_refund_reference: providerRefundReference.trim(),
            })
          }
          toast.success(successMessage)
          props.onOpenChange(false)
          await refreshAfterFinancialAction(
            props.onCompleted,
            t(
              'The action completed, but the latest data could not be loaded. Refresh manually.'
            )
          )
          return result
        },
        {
          preferredMethod: 'passkey',
          title: verificationTitle,
          description: t(
            'Confirm your identity before applying this audited financial resolution.'
          ),
        }
      )
    },
    onError: (error) => {
      toast.error(getApiErrorMessage(error, failureMessage))
    },
  })
  const operationPending = mutation.isPending || verificationOpen
  const handleOpenChange = (open: boolean) => {
    if (operationPending) return
    if (!open) {
      setReason('')
      setRefundedAmountMinor('')
      setProviderRefundReference('')
    }
    props.onOpenChange(open)
  }

  return (
    <Dialog open={props.open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <Alert variant='destructive'>
          <AlertDescription>
            {props.action === 'external-refund'
              ? t(
                  'The canonical ledger and user entitlement will be reconciled to this cumulative refund amount.'
                )
              : t(
                  'This closes the order without granting its purchased entitlement. The expected order version is checked again.'
                )}
          </AlertDescription>
        </Alert>
        <dl className='grid gap-3 rounded-lg border p-3 text-sm sm:grid-cols-2'>
          <div className='grid min-w-0 gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Trade Number')}
            </dt>
            <dd className='truncate font-mono'>
              {props.order?.trade_no || '-'}
            </dd>
          </div>
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Expected Version')}
            </dt>
            <dd className='tabular-nums'>{props.order?.version ?? '-'}</dd>
          </div>
        </dl>
        {props.action === 'external-refund' && (
          <div className='grid gap-4'>
            <div className='grid gap-2'>
              <Label htmlFor='external-refund-amount-minor'>
                {t('Cumulative refunded amount in minor units')}
              </Label>
              <Input
                id='external-refund-amount-minor'
                value={refundedAmountMinor}
                inputMode='numeric'
                autoComplete='off'
                placeholder={t('Enter a positive whole-number amount')}
                onChange={(event) => setRefundedAmountMinor(event.target.value)}
              />
              <p className='text-muted-foreground text-xs'>
                {props.order
                  ? t(
                      'Enter the provider total after refund: greater than {{current}} and no more than {{maximum}} minor units.',
                      {
                        current: props.order.refunded_amount_minor,
                        maximum: props.order.expected_amount_minor,
                      }
                    )
                  : ''}
              </p>
            </div>
            <div className='grid gap-2'>
              <Label htmlFor='external-refund-reference'>
                {t('Provider refund reference')}
              </Label>
              <Input
                id='external-refund-reference'
                value={providerRefundReference}
                maxLength={PAYMENT_PROVIDER_REFERENCE_MAX_BYTES}
                autoComplete='off'
                onChange={(event) =>
                  setProviderRefundReference(event.target.value)
                }
              />
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Required. Copy the completed refund reference from the provider record. Use 1 to {{maximum}} UTF-8 bytes.',
                  { maximum: PAYMENT_PROVIDER_REFERENCE_MAX_BYTES }
                )}
              </p>
            </div>
          </div>
        )}
        <div className='grid gap-2'>
          <Label htmlFor='payment-order-action-reason'>
            {t('Administrator evidence and reason')}
          </Label>
          <Textarea
            id='payment-order-action-reason'
            value={reason}
            minLength={8}
            maxLength={512}
            className='min-h-24'
            placeholder={t(
              'Record the provider evidence and why this exact financial action is correct.'
            )}
            onChange={(event) => setReason(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
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
            disabled={
              operationPending ||
              !props.order ||
              !props.action ||
              !reasonValid ||
              !refundValid ||
              !refundReferenceValid
            }
            onClick={() => mutation.mutate()}
          >
            {mutation.isPending ? t('Applying...') : confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function CredentialIncidentActionDialog(props: {
  order: PaymentOrder | null
  action: CredentialIncidentAction | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onCompleted: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const { runWithVerification, verificationOpen } =
    usePaymentOperationVerification()
  const [reason, setReason] = useState('')
  const trimmedReason = reason.trim()
  const reasonValid = isPaymentAuditReasonValid(reason)
  const availableActions = props.order
    ? getCredentialIncidentActions(
        props.order.credential_incident_state || '',
        props.order.credential_incident
      )
    : []
  const actionAvailable = props.action
    ? availableActions.includes(props.action)
    : false
  const isResolve = props.action === 'resolve'

  useEffect(() => {
    if (props.open) setReason('')
  }, [props.action, props.open, props.order])

  const mutation = useMutation({
    mutationFn: async () => {
      const order = props.order
      const action = props.action
      if (!order || !action || !actionAvailable || !reasonValid) return null
      return runWithVerification(
        async () => {
          const request = {
            trade_no: order.trade_no,
            expected_version: order.version,
            reason: trimmedReason,
          }
          const result =
            action === 'acknowledge'
              ? await acknowledgePaymentCredentialIncident(request)
              : await resolvePaymentCredentialIncident(request)
          toast.success(
            action === 'acknowledge'
              ? t('Payment credential incident acknowledged')
              : t('Payment credential incident resolved')
          )
          props.onOpenChange(false)
          await refreshAfterFinancialAction(
            props.onCompleted,
            t(
              'The action completed, but the latest data could not be loaded. Refresh manually.'
            )
          )
          return result
        },
        {
          preferredMethod: 'passkey',
          title: isResolve
            ? t('Verify credential incident resolution')
            : t('Verify credential incident acknowledgement'),
          description: t(
            'Confirm your identity before changing the audited state of this payment credential incident.'
          ),
        }
      )
    },
    onError: (error) => {
      toast.error(
        getApiErrorMessage(
          error,
          isResolve
            ? t('Failed to resolve payment credential incident')
            : t('Failed to acknowledge payment credential incident')
        )
      )
    },
  })
  const operationPending = mutation.isPending || verificationOpen
  let incidentConfirmLabel = isResolve
    ? t('Confirm incident resolution')
    : t('Confirm acknowledgement')
  if (mutation.isPending) incidentConfirmLabel = t('Applying...')

  const handleOpenChange = (open: boolean) => {
    if (operationPending) return
    if (!open) setReason('')
    props.onOpenChange(open)
  }

  return (
    <Dialog open={props.open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {isResolve
              ? t('Resolve payment credential incident')
              : t('Acknowledge payment credential incident')}
          </DialogTitle>
          <DialogDescription>
            {isResolve
              ? t(
                  'Resolve only after completing the credential investigation and any required financial review.'
                )
              : t(
                  'Acknowledge that an administrator owns this investigation while keeping the incident open.'
                )}
          </DialogDescription>
        </DialogHeader>
        <Alert variant={isResolve ? 'default' : 'destructive'}>
          <AlertDescription>
            {isResolve
              ? t(
                  'Resolution closes the incident flag but does not change the order economic status or grant entitlement.'
                )
              : t(
                  'Acknowledgement does not make the payment safe. The order remains under incident review.'
                )}
          </AlertDescription>
        </Alert>
        <dl className='grid gap-3 rounded-lg border p-3 text-sm sm:grid-cols-2'>
          <div className='grid min-w-0 gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Trade Number')}
            </dt>
            <dd className='truncate font-mono'>
              {props.order?.trade_no || '-'}
            </dd>
          </div>
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Expected Version')}
            </dt>
            <dd className='tabular-nums'>{props.order?.version ?? '-'}</dd>
          </div>
        </dl>
        <div className='grid gap-2'>
          <Label htmlFor='credential-incident-action-reason'>
            {t('Administrator evidence and reason')}
          </Label>
          <Textarea
            id='credential-incident-action-reason'
            value={reason}
            minLength={8}
            maxLength={512}
            className='min-h-24'
            placeholder={t(
              'Record the investigation evidence, containment steps, and why this incident state is correct.'
            )}
            onChange={(event) => setReason(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
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
            disabled={
              operationPending ||
              !props.order ||
              !props.action ||
              !actionAvailable ||
              !reasonValid
            }
            onClick={() => mutation.mutate()}
          >
            {incidentConfirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function StripeCustomerBindingRetirementDialog(props: {
  binding: PaymentCustomerBinding | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onCompleted: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const { runWithVerification, verificationOpen } =
    usePaymentOperationVerification()
  const [reason, setReason] = useState('')
  const trimmedReason = reason.trim()
  const reasonValid = isPaymentAuditReasonValid(reason)

  useEffect(() => {
    if (props.open) setReason('')
  }, [props.binding, props.open])

  const mutation = useMutation({
    mutationFn: async () => {
      const binding = props.binding
      if (!binding || !reasonValid) return null
      return runWithVerification(
        async () => {
          const result = await retireStripeCustomerBinding({
            binding_id: binding.id,
            user_id: binding.user_id,
            expected_version: binding.version,
            reason: trimmedReason,
          })
          toast.success(t('Stripe customer binding retired'))
          props.onOpenChange(false)
          await refreshAfterFinancialAction(
            props.onCompleted,
            t(
              'The action completed, but the latest data could not be loaded. Refresh manually.'
            )
          )
          return result
        },
        {
          preferredMethod: 'passkey',
          title: t('Verify Stripe customer binding retirement'),
          description: t(
            'Confirm your identity before detaching this Stripe customer from the local user account.'
          ),
        }
      )
    },
    onError: (error) => {
      toast.error(
        getApiErrorMessage(error, t('Failed to retire Stripe customer binding'))
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
    <Dialog open={props.open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('Retire Stripe customer binding')}</DialogTitle>
          <DialogDescription>
            {t(
              'Remove this local ownership binding only when the Stripe customer is no longer safe or correct for the user.'
            )}
          </DialogDescription>
        </DialogHeader>
        <Alert variant='destructive'>
          <AlertDescription>
            {t(
              'This clears the matching local Stripe customer reference and preserves an immutable retirement record. It does not delete the customer in Stripe.'
            )}
          </AlertDescription>
        </Alert>
        <dl className='grid gap-3 rounded-lg border p-3 text-sm sm:grid-cols-2'>
          <div className='grid min-w-0 gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Stripe Customer ID')}
            </dt>
            <dd className='truncate font-mono'>
              {props.binding?.customer_key || '-'}
            </dd>
          </div>
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>{t('User ID')}</dt>
            <dd className='tabular-nums'>{props.binding?.user_id ?? '-'}</dd>
          </div>
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>{t('Binding ID')}</dt>
            <dd className='tabular-nums'>{props.binding?.id ?? '-'}</dd>
          </div>
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Expected Version')}
            </dt>
            <dd className='tabular-nums'>{props.binding?.version ?? '-'}</dd>
          </div>
        </dl>
        <div className='grid gap-2'>
          <Label htmlFor='stripe-binding-retirement-reason'>
            {t('Administrator evidence and reason')}
          </Label>
          <Textarea
            id='stripe-binding-retirement-reason'
            value={reason}
            minLength={8}
            maxLength={512}
            className='min-h-24'
            placeholder={t(
              'Record the verified ownership issue and why this binding must be retired.'
            )}
            onChange={(event) => setReason(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
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
            disabled={operationPending || !props.binding || !reasonValid}
            onClick={() => mutation.mutate()}
          >
            {mutation.isPending
              ? t('Retiring...')
              : t('Confirm binding retirement')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function UnmatchedPaymentEventActionDialog(props: {
  event: PaymentEvent | null
  action: UnmatchedEventAction | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onCompleted: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const { runWithVerification, verificationOpen } =
    usePaymentOperationVerification()
  const [reason, setReason] = useState('')
  const [targetTradeNo, setTargetTradeNo] = useState('')
  const [expectedOrderVersion, setExpectedOrderVersion] = useState('')
  const trimmedReason = reason.trim()
  const trimmedTradeNo = targetTradeNo.trim()
  const parsedOrderVersion = parsePositiveSafeInteger(expectedOrderVersion)
  const reasonValid = isPaymentAuditReasonValid(reason)
  const isLink = props.action === 'link'
  const isRetryLegacy = props.action === 'retry_legacy'
  const eventActionable = Boolean(
    props.event &&
    props.action &&
    isPaymentEventActionAvailable(props.event, props.action)
  )
  const linkValid =
    props.action !== 'link' ||
    (isPaymentTradeNoValid(targetTradeNo) && parsedOrderVersion !== null)

  useEffect(() => {
    if (!props.open) return
    setReason('')
    setTargetTradeNo('')
    setExpectedOrderVersion('')
  }, [props.action, props.event, props.open])

  let dialogTitle = t('Dismiss unmatched callback event')
  let dialogDescription = t(
    'Dismiss only after confirming that this callback must not be applied to any payment order.'
  )
  let riskDescription = t(
    'A dismissed event leaves the unmatched queue and records your reason as its terminal review outcome.'
  )
  let successMessage = t('Unmatched event dismissed')
  let failureMessage = t('Failed to dismiss unmatched payment event')
  let verificationTitle = t('Verify unmatched event dismissal')
  let confirmActionLabel = t('Confirm dismissal')
  if (isLink) {
    dialogTitle = t('Link unmatched callback event')
    dialogDescription = t(
      'Link only after independently confirming that the intact paid event belongs to the exact target order.'
    )
    riskDescription = t(
      'A successful link may fulfill the target order. Provider, amount, currency, identity, and order version are checked by the server.'
    )
    successMessage = t('Unmatched event linked')
    failureMessage = t('Failed to link unmatched payment event')
    verificationTitle = t('Verify unmatched event link')
    confirmActionLabel = t('Confirm link')
  } else if (isRetryLegacy) {
    dialogTitle = t('Safely retry legacy Epay order')
    dialogDescription = t(
      'Re-evaluate this previously signature-verified legacy Epay paid event and create its canonical order only if every server-side invariant still matches.'
    )
    riskDescription = t(
      'A successful retry can grant the purchased quota or entitlement. The server rechecks event integrity, credential generation, amount, currency, payment method, pricing plan, and event version.'
    )
    successMessage = t('Legacy Epay payment safely retried')
    failureMessage = t('Failed to safely retry legacy Epay payment')
    verificationTitle = t('Verify legacy Epay payment retry')
    confirmActionLabel = t('Confirm safe retry')
  }
  const mutation = useMutation({
    mutationFn: async () => {
      const event = props.event
      const action = props.action
      if (!event || !eventActionable || !action || !reasonValid || !linkValid) {
        return null
      }
      return runWithVerification(
        async () => {
          let result
          if (action === 'link') {
            result = await linkUnmatchedPaymentEvent({
              event_id: event.id,
              target_trade_no: trimmedTradeNo,
              expected_order_version: parsedOrderVersion as number,
              reason: trimmedReason,
            })
          } else if (action === 'retry_legacy') {
            result = await retryLegacyEpayPaymentEvent({
              event_id: event.id,
              expected_event_attempts: event.attempts,
              reason: trimmedReason,
            })
          } else {
            result = await dismissUnmatchedPaymentEvent({
              event_id: event.id,
              reason: trimmedReason,
            })
          }
          toast.success(successMessage)
          props.onOpenChange(false)
          await refreshAfterFinancialAction(
            props.onCompleted,
            t(
              'The action completed, but the latest data could not be loaded. Refresh manually.'
            )
          )
          return result
        },
        {
          preferredMethod: 'passkey',
          title: verificationTitle,
          description: t(
            'Confirm your identity before changing the canonical handling of this callback event.'
          ),
        }
      )
    },
    onError: (error) => {
      toast.error(getApiErrorMessage(error, failureMessage))
      if (isRetryLegacy) {
        props.onOpenChange(false)
        void refreshAfterFinancialAction(
          props.onCompleted,
          t(
            'The retry failed and the latest event state could not be loaded. Refresh manually before trying again.'
          )
        )
      }
    },
  })
  const operationPending = mutation.isPending || verificationOpen
  if (mutation.isPending) confirmActionLabel = t('Applying...')

  const handleOpenChange = (open: boolean) => {
    if (operationPending) return
    props.onOpenChange(open)
  }

  return (
    <Dialog open={props.open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{dialogTitle}</DialogTitle>
          <DialogDescription>{dialogDescription}</DialogDescription>
        </DialogHeader>
        <Alert variant='destructive'>
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
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>{t('Event ID')}</dt>
            <dd className='tabular-nums'>{props.event?.id ?? '-'}</dd>
          </div>
          {isRetryLegacy && (
            <div className='grid gap-1'>
              <dt className='text-muted-foreground text-xs'>
                {t('Expected Event Attempts')}
              </dt>
              <dd className='tabular-nums'>{props.event?.attempts ?? '-'}</dd>
            </div>
          )}
        </dl>
        {isLink && (
          <div className='grid gap-4 sm:grid-cols-[1fr_180px]'>
            <div className='grid gap-2'>
              <Label htmlFor='unmatched-target-trade-no'>
                {t('Exact target trade number')}
              </Label>
              <Input
                id='unmatched-target-trade-no'
                value={targetTradeNo}
                maxLength={128}
                autoComplete='off'
                onChange={(event) => setTargetTradeNo(event.target.value)}
              />
            </div>
            <div className='grid gap-2'>
              <Label htmlFor='unmatched-target-order-version'>
                {t('Expected order version')}
              </Label>
              <Input
                id='unmatched-target-order-version'
                value={expectedOrderVersion}
                inputMode='numeric'
                autoComplete='off'
                onChange={(event) =>
                  setExpectedOrderVersion(event.target.value)
                }
              />
            </div>
          </div>
        )}
        <div className='grid gap-2'>
          <Label htmlFor='unmatched-event-action-reason'>
            {t('Administrator evidence and reason')}
          </Label>
          <Textarea
            id='unmatched-event-action-reason'
            value={reason}
            minLength={8}
            maxLength={512}
            className='min-h-24'
            onChange={(event) => setReason(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
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
            disabled={
              operationPending ||
              !props.event ||
              !eventActionable ||
              !props.action ||
              !reasonValid ||
              !linkValid
            }
            onClick={() => mutation.mutate()}
          >
            {confirmActionLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function ResolveDebtDialog(props: {
  debt: PaymentDebt | null
  provider?: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onCompleted: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const { runWithVerification, verificationOpen } =
    usePaymentOperationVerification()
  const [resolution, setResolution] = useState<'repaid' | 'waived'>('repaid')
  const [note, setNote] = useState('')
  const trimmedNote = note.trim()
  const noteValid = isPaymentAuditReasonValid(note)
  const mutation = useMutation({
    mutationFn: async () => {
      const debt = props.debt
      if (!debt || !noteValid) return null
      return runWithVerification(
        async () => {
          const result = await resolvePaymentDebt({
            debt_id: debt.id,
            expected_outstanding_quota: debt.outstanding_quota,
            expected_outstanding_amount_minor: debt.outstanding_amount_minor,
            resolution,
            note: trimmedNote,
          })
          toast.success(t('Payment debt resolved'))
          setResolution('repaid')
          setNote('')
          props.onOpenChange(false)
          await refreshAfterFinancialAction(
            props.onCompleted,
            t(
              'The action completed, but the latest data could not be loaded. Refresh manually.'
            )
          )
          return result
        },
        {
          preferredMethod: 'passkey',
          title: t('Verify payment debt resolution'),
          description: t(
            'Confirm your identity before closing or waiving this payment debt.'
          ),
        }
      )
    },
    onError: (error) => {
      toast.error(
        getApiErrorMessage(error, t('Failed to resolve payment debt'))
      )
    },
  })
  const operationPending = mutation.isPending || verificationOpen

  const handleOpenChange = (open: boolean) => {
    if (operationPending) return
    if (!open) {
      setResolution('repaid')
      setNote('')
    }
    props.onOpenChange(open)
  }

  return (
    <Dialog open={props.open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('Resolve payment debt')}</DialogTitle>
          <DialogDescription>
            {t(
              'Outstanding values are checked again before resolution. Refresh the order if another administrator changed this debt.'
            )}
          </DialogDescription>
        </DialogHeader>
        <div className='grid gap-3 rounded-lg border p-3 text-sm'>
          <div className='flex items-center justify-between gap-4'>
            <span className='text-muted-foreground'>
              {t('Outstanding Amount')}
            </span>
            <span className='tabular-nums'>
              {props.debt
                ? formatMinorAmount(
                    props.debt.outstanding_amount_minor,
                    props.debt.currency,
                    props.provider
                  )
                : '-'}
            </span>
          </div>
          <div className='flex items-center justify-between gap-4'>
            <span className='text-muted-foreground'>
              {t('Outstanding Quota')}
            </span>
            <span className='tabular-nums'>
              {props.debt ? formatInteger(props.debt.outstanding_quota) : '-'}
            </span>
          </div>
        </div>
        <div className='grid gap-2'>
          <Label htmlFor='payment-debt-resolution'>{t('Resolution')}</Label>
          <NativeSelect
            id='payment-debt-resolution'
            value={resolution}
            onChange={(event) =>
              setResolution(event.target.value as 'repaid' | 'waived')
            }
          >
            <NativeSelectOption value='repaid'>
              {t('Repaid')}
            </NativeSelectOption>
            <NativeSelectOption value='waived'>
              {t('Waived')}
            </NativeSelectOption>
          </NativeSelect>
        </div>
        {resolution === 'waived' && (
          <Alert variant='destructive'>
            <AlertDescription>
              {t(
                'Waiving closes the debt without collecting the outstanding amount or quota.'
              )}
            </AlertDescription>
          </Alert>
        )}
        <div className='grid gap-2'>
          <Label htmlFor='payment-debt-note'>{t('Resolution note')}</Label>
          <Textarea
            id='payment-debt-note'
            value={note}
            minLength={8}
            maxLength={512}
            className='min-h-24'
            placeholder={t(
              'Record the evidence and reason for this resolution.'
            )}
            onChange={(event) => setNote(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
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
            variant={resolution === 'waived' ? 'destructive' : 'default'}
            disabled={operationPending || !props.debt || !noteValid}
            onClick={() => mutation.mutate()}
          >
            {mutation.isPending ? t('Resolving...') : t('Confirm resolution')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
