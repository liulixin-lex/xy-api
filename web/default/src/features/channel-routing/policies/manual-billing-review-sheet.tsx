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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import {
  CheckCircle2,
  FileCheck2,
  LockKeyhole,
  RefreshCw,
  ShieldAlert,
  XCircle,
} from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  revealSideDrawerAlert,
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
  SideDrawerSection,
  SideDrawerSectionHeader,
} from '@/components/drawer-layout'
import { RiskAcknowledgementDialog } from '@/components/risk-acknowledgement-dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'

import {
  getManualBillingReviewApiError,
  resolveManualBillingReview,
} from '../api/billing-reviews'
import type {
  ManualBillingReviewApiError,
  ManualBillingReviewItem,
  ManualBillingReviewResolutionRequest,
} from '../billing-review-types'
import { useChannelRoutingFormatters } from '../lib/format'
import {
  buildManualBillingReviewResolution,
  createManualBillingReviewSchema,
  getManualBillingReviewEvidenceWindowStart,
  getManualBillingReviewConfirmationImpact,
  getManualBillingReviewKindDisplay,
  getManualBillingReviewProviderStatus,
  ManualBillingReviewSession,
  manualBillingReviewHasUnknownBlocker,
  manualBillingReviewKindIsSupported,
  manualBillingReviewIsOverage,
  manualBillingReviewNeedsRefresh,
  toLocalDateTimeInput,
  type ManualBillingReviewFormInput,
  type ManualBillingReviewFormValues,
  type ManualBillingReviewValidationMessages,
} from '../lib/manual-billing-review'
import { ManualBillingReviewCaseDetails } from './manual-billing-review-details'

type FrozenDecision = {
  review: ManualBillingReviewItem
  values: ManualBillingReviewFormValues
  payload: ManualBillingReviewResolutionRequest
  signature: string
}

type ResolveVariables = FrozenDecision & {
  generation: number
  idempotencyKey: string
  signal: AbortSignal
}

export function ManualBillingReviewSheet(props: {
  review: ManualBillingReviewItem | null
  open: boolean
  canResolve: boolean
  onOpenChange: (open: boolean) => void
  onRefreshReview: (
    reservationId: number
  ) => Promise<ManualBillingReviewItem | null>
  onResolved: () => Promise<void>
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const sessionRef = useRef(new ManualBillingReviewSession())
  const [sessionGeneration, setSessionGeneration] = useState(0)
  const [workingReview, setWorkingReview] =
    useState<ManualBillingReviewItem | null>(null)
  const [confirmation, setConfirmation] = useState<FrozenDecision | null>(null)
  const [requestError, setRequestError] =
    useState<ManualBillingReviewApiError | null>(null)
  const [resolvedElsewhere, setResolvedElsewhere] = useState(false)

  const validationMessages = useMemo<ManualBillingReviewValidationMessages>(
    () => ({
      actionRequired: t('Select an accept or reject decision'),
      actionUnavailable: t(
        'This decision is blocked by the latest server evidence'
      ),
      upstreamTaskRequired: t(
        'The frozen upstream task ID is required for this decision'
      ),
      upstreamTaskTooLong: t('Upstream task ID must be 191 bytes or fewer'),
      evidenceRequired: t('Provider evidence reference is required'),
      evidenceTooLong: t(
        'Provider evidence reference must be 512 bytes or fewer'
      ),
      reasonRequired: t('Decision reason is required'),
      reasonTooLong: t('Decision reason must be 1024 bytes or fewer'),
      singleLineRequired: t('Use a single line without control characters'),
      checkedTimeRequired: t('Enter when the provider evidence was checked'),
      checkedTimeTooEarly: t(
        'Provider checked time cannot be earlier than the review or authorization time'
      ),
      checkedTimeFuture: t(
        'Provider checked time cannot be more than five minutes in the future'
      ),
    }),
    [t]
  )
  const schema = useMemo(
    () =>
      createManualBillingReviewSchema(
        workingReview ??
          props.review ?? {
            reservation_id: 0,
            kind: '',
            review_kind: 'send_outcome',
            public_task_id: '',
            user_id: 0,
            state: '',
            current_quota: 0,
            accepted_quota: 0,
            review_version: 0,
            etag: '',
            manual_review_since_ms: 0,
            reason: '',
            can_accept: false,
            can_reject: false,
            blockers: [],
            financial_consequences: {
              current_charge: 0,
              accept_additional_charge: 0,
              accept_final_charge: 0,
              reject_refund: 0,
              reject_final_charge: 0,
              reject_write_off: 0,
            },
            attempts: [],
          },
        validationMessages
      ),
    [props.review, validationMessages, workingReview]
  )
  const form = useForm<
    ManualBillingReviewFormInput,
    unknown,
    ManualBillingReviewFormValues
  >({
    resolver: zodResolver(schema),
    defaultValues: {
      action: null,
      rejection_provider_status: 'confirmed_rejected',
      upstream_task_id: '',
      provider_checked_at: toLocalDateTimeInput(Date.now()),
      evidence_reference: '',
      reason: '',
    },
  })
  const selectedAction = useWatch({ control: form.control, name: 'action' })
  const rejectionProviderStatus = useWatch({
    control: form.control,
    name: 'rejection_provider_status',
  })

  const resolveReview = useMutation({
    mutationFn: (variables: ResolveVariables) =>
      resolveManualBillingReview(
        variables.review,
        variables.payload,
        variables.idempotencyKey,
        variables.signal
      ),
    onSuccess: async (_result, variables) => {
      if (!sessionRef.current.isCurrent(variables.generation)) return
      sessionRef.current.completeSubmission(
        variables.generation,
        variables.signature
      )
      setConfirmation(null)
      setRequestError(null)
      await props.onResolved().catch(() => undefined)
      if (!sessionRef.current.isCurrent(variables.generation)) return
      let successMessage = t('Billing review rejected')
      if (variables.review.review_kind === 'terminal_usage') {
        successMessage = t('Terminal usage verified')
      } else if (variables.payload.action === 'confirmed_accepted') {
        successMessage = t('Billing review accepted')
      }
      toast.success(successMessage)
      props.onOpenChange(false)
    },
    onError: async (error, variables) => {
      sessionRef.current.releaseSubmission(
        variables.generation,
        variables.signature
      )
      if (!sessionRef.current.isCurrent(variables.generation)) return
      setConfirmation(null)
      const apiError = getManualBillingReviewApiError(error)
      setRequestError(apiError)
      if (!manualBillingReviewNeedsRefresh(apiError)) return

      try {
        const latest = await props.onRefreshReview(
          variables.review.reservation_id
        )
        if (!sessionRef.current.isCurrent(variables.generation)) return
        if (latest) {
          setWorkingReview(latest)
          if (latest.review_kind !== 'send_outcome') {
            form.setValue('upstream_task_id', latest.upstream_task_id ?? '', {
              shouldDirty: false,
            })
          }
          setResolvedElsewhere(false)
          return
        }
        setResolvedElsewhere(true)
      } catch {
        if (!sessionRef.current.isCurrent(variables.generation)) return
        setRequestError(
          apiError.status === 403
            ? apiError
            : {
                status: apiError.status,
                code: 'review_refresh_failed',
              }
        )
      }
    },
  })
  const resetMutation = resolveReview.reset

  useEffect(() => {
    const review = props.review
    if (!props.open || !review) {
      sessionRef.current.close()
      return
    }
    const generation = sessionRef.current.open()
    setSessionGeneration(generation)
    setWorkingReview(review)
    setConfirmation(null)
    setRequestError(null)
    setResolvedElsewhere(false)
    resetMutation()
    form.reset({
      action: null,
      rejection_provider_status: 'confirmed_rejected',
      upstream_task_id: review.upstream_task_id ?? '',
      provider_checked_at: toLocalDateTimeInput(Date.now()),
      evidence_reference: '',
      reason: '',
    })
  }, [form, props.open, props.review, resetMutation])

  const freezeDecision = (values: ManualBillingReviewFormValues) => {
    if (!workingReview || resolvedElsewhere) return
    const payload = buildManualBillingReviewResolution(workingReview, values)
    const signature = JSON.stringify({
      reservation_id: workingReview.reservation_id,
      etag: workingReview.etag,
      payload,
    })
    setRequestError(null)
    setConfirmation({
      review: workingReview,
      values,
      payload,
      signature,
    })
  }

  const submitFrozenDecision = () => {
    if (!confirmation) return
    const claimed = sessionRef.current.claimSubmission(confirmation.signature)
    if (!claimed) return
    resolveReview.mutate({
      ...confirmation,
      generation: claimed.generation,
      idempotencyKey: claimed.key,
      signal: claimed.signal,
    })
  }

  const isSubmitting =
    resolveReview.isPending &&
    resolveReview.variables?.generation === sessionGeneration
  const reviewCanResolve =
    props.canResolve &&
    !resolvedElsewhere &&
    workingReview != null &&
    manualBillingReviewKindIsSupported(workingReview.review_kind) &&
    !manualBillingReviewHasUnknownBlocker(workingReview) &&
    workingReview.etag.length > 0 &&
    workingReview.review_version > 0 &&
    (workingReview.review_kind !== 'terminal_usage' ||
      (workingReview.upstream_task_id ?? '').trim().length > 0) &&
    (workingReview.can_accept || workingReview.can_reject)

  let requestErrorTitle = t('Billing review decision failed')
  let requestErrorDescription = t(
    'The decision was not applied. Review the evidence and try again.'
  )
  if (requestError?.code === 'review_precondition_failed') {
    requestErrorTitle = t('Billing review changed')
    requestErrorDescription = t(
      'The latest server record has been loaded and your draft was preserved. Review the updated consequences before trying again.'
    )
  } else if (requestError?.code === 'decision_conflict') {
    requestErrorTitle = t('A conflicting decision exists')
    requestErrorDescription = t(
      'The queue was refreshed and your draft was preserved. Confirm the latest record before continuing.'
    )
  } else if (requestError?.code === 'insufficient_quota') {
    requestErrorTitle = t('Additional quota is unavailable')
    requestErrorDescription = t(
      'The charge was not applied. Review the latest balance and choose the appropriate verified outcome.'
    )
  } else if (requestError?.code === 'decision_blocked') {
    requestErrorTitle = t('Provider evidence is incomplete')
    requestErrorDescription = t(
      'The server still blocks this decision. The latest blockers are shown below and your draft was preserved.'
    )
  } else if (requestError?.code === 'review_not_found') {
    requestErrorTitle = t('Billing review no longer pending')
    requestErrorDescription = t(
      'This reservation was resolved elsewhere. No decision was applied from this sheet.'
    )
  } else if (requestError?.code === 'invalid_manual_decision') {
    requestErrorTitle = t('Decision evidence was rejected')
    requestErrorDescription = t(
      'Check the provider status, evidence time, reference, and reason before trying again.'
    )
  } else if (requestError?.code === 'review_refresh_failed') {
    requestErrorTitle = t('Could not refresh the billing review')
    requestErrorDescription = t(
      'The decision was not confirmed. Close this sheet and refresh the queue before retrying.'
    )
  } else if (requestError?.status === 403) {
    requestErrorTitle = t('Resolve permission is required')
    requestErrorDescription = t(
      'Your current role can view this review but cannot submit a billing decision.'
    )
  }
  const requestErrorIsWarning = [
    'review_precondition_failed',
    'decision_conflict',
    'insufficient_quota',
    'review_not_found',
  ].includes(requestError?.code ?? '')

  const confirmationReview = confirmation?.review
  const confirmationAction = confirmation?.payload.action
  let confirmationImpact = ''
  const confirmationImpactDetails =
    confirmationReview && confirmationAction
      ? getManualBillingReviewConfirmationImpact(
          confirmationReview,
          confirmationAction
        )
      : null
  if (confirmationImpactDetails?.kind === 'overage_accept') {
    confirmationImpact = t(
      'This applies an additional charge of {{additional}} and sets the final charge to {{final}}.',
      {
        additional: format.number(confirmationImpactDetails.additional),
        final: format.number(confirmationImpactDetails.final),
      }
    )
  } else if (confirmationImpactDetails?.kind === 'overage_reject') {
    confirmationImpact = t(
      'This writes off {{writeOff}} while keeping the final charge at {{final}}.',
      {
        writeOff: format.number(confirmationImpactDetails.writeOff),
        final: format.number(confirmationImpactDetails.final),
      }
    )
  } else if (confirmationImpactDetails?.kind === 'accepted_handoff') {
    confirmationImpact = t(
      'This confirms the accepted handoff, applies a charge adjustment of {{adjustment}}, and sets the final charge to {{final}}.',
      {
        adjustment: format.number(confirmationImpactDetails.adjustment),
        final: format.number(confirmationImpactDetails.final),
      }
    )
  } else if (confirmationImpactDetails?.kind === 'terminal_usage') {
    confirmationImpact = t(
      'This verifies terminal usage for the frozen upstream task and keeps the final charge at {{final}}. No refund or additional charge is created.',
      { final: format.number(confirmationImpactDetails.final) }
    )
  } else if (confirmationImpactDetails?.kind === 'accepted') {
    confirmationImpact = t(
      'This confirms the upstream task and sets the final charge to {{final}}.',
      {
        final: format.number(confirmationImpactDetails.final),
      }
    )
  } else if (confirmationImpactDetails?.kind === 'rejected') {
    confirmationImpact = t(
      'This records a refund of {{refund}} and sets the final charge to {{final}}.',
      {
        refund: format.number(confirmationImpactDetails.refund),
        final: format.number(confirmationImpactDetails.final),
      }
    )
  }

  let providerStatusLabel = t('Select a decision')
  if (workingReview && selectedAction) {
    providerStatusLabel =
      getManualBillingReviewProviderStatus(
        workingReview.review_kind,
        selectedAction,
        rejectionProviderStatus
      ) ?? t('Unknown')
  }
  let acceptDecisionDescription = t(
    'Confirm that the upstream task was accepted'
  )
  let rejectDecisionDescription = t(
    'Confirm rejection or provider not-found evidence'
  )
  if (
    workingReview &&
    manualBillingReviewIsOverage(workingReview.review_kind)
  ) {
    acceptDecisionDescription = t('Apply the verified additional charge')
    rejectDecisionDescription = t('Write off the verified overage')
  } else if (workingReview?.review_kind === 'accepted_handoff') {
    acceptDecisionDescription = t('Confirm the accepted task handoff')
    rejectDecisionDescription = t(
      'Accepted handoff reviews can only be accepted'
    )
  } else if (workingReview?.review_kind === 'terminal_usage') {
    acceptDecisionDescription = t(
      'Verify terminal usage and retain the current charge'
    )
    rejectDecisionDescription = t(
      'Terminal usage verification cannot be rejected or refunded'
    )
  }
  const decisionContractSupported =
    workingReview != null &&
    manualBillingReviewKindIsSupported(workingReview.review_kind) &&
    !manualBillingReviewHasUnknownBlocker(workingReview)
  const terminalUsageContextAvailable =
    workingReview?.review_kind !== 'terminal_usage' ||
    (workingReview.upstream_task_id ?? '').trim().length > 0
  const acceptDecisionAvailable =
    workingReview?.can_accept === true &&
    decisionContractSupported &&
    terminalUsageContextAvailable
  const rejectDecisionAvailable =
    workingReview?.can_reject === true &&
    decisionContractSupported &&
    workingReview.review_kind !== 'accepted_handoff' &&
    workingReview.review_kind !== 'terminal_usage'
  const evidenceWindowStart = workingReview
    ? getManualBillingReviewEvidenceWindowStart(workingReview)
    : 0
  let confirmationTitle = t('Confirm rejected billing outcome')
  let confirmationChecklist = t(
    'I understand this rejection applies the server-calculated refund or write-off outcome.'
  )
  let confirmationButtonText = t('Reject verified outcome')
  if (confirmationReview?.review_kind === 'terminal_usage') {
    confirmationTitle = t('Confirm terminal usage verification')
    confirmationChecklist = t(
      'I understand this verification retains the current charge without a refund or additional charge.'
    )
    confirmationButtonText = t('Verify terminal usage')
  } else if (confirmationAction === 'confirmed_accepted') {
    confirmationTitle = t('Confirm accepted billing outcome')
    confirmationChecklist = t(
      'I understand this acceptance applies the server-calculated financial outcome.'
    )
    confirmationButtonText = t('Accept verified outcome')
  }

  return (
    <>
      <Sheet
        open={props.open}
        onOpenChange={(open) => {
          if (!open && isSubmitting) return
          props.onOpenChange(open)
        }}
      >
        <SheetContent
          className={sideDrawerContentClassName(
            'w-full sm:max-w-xl lg:max-w-2xl'
          )}
          showCloseButton={!isSubmitting}
          aria-busy={isSubmitting}
        >
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle className='pr-8 break-words'>
              {workingReview
                ? t('Billing review #{{id}}', {
                    id: workingReview.reservation_id,
                  })
                : t('Billing review')}
            </SheetTitle>
            <SheetDescription className='line-clamp-2 break-all'>
              {workingReview
                ? t('{{type}} · Task {{task}}', {
                    type: getManualBillingReviewKindDisplay(
                      workingReview.review_kind,
                      t
                    ),
                    task: workingReview.public_task_id,
                  })
                : t('Review server evidence before making a billing decision.')}
            </SheetDescription>
          </SheetHeader>

          <div
            className={sideDrawerFormClassName()}
            tabIndex={0}
            aria-label={t('Billing review')}
          >
            {resolvedElsewhere ? (
              <Alert role='status'>
                <LockKeyhole aria-hidden='true' />
                <AlertTitle>{t('Resolved in another session')}</AlertTitle>
                <AlertDescription>
                  {t(
                    'This reservation is no longer in the manual review queue. Your draft remains visible, but submitting is disabled.'
                  )}
                </AlertDescription>
              </Alert>
            ) : null}
            {requestError ? (
              <Alert
                ref={revealSideDrawerAlert}
                role='alert'
                variant={requestErrorIsWarning ? 'default' : 'destructive'}
                tabIndex={-1}
                className={cn(
                  'focus-visible:ring-ring/50 focus-visible:ring-2 focus-visible:outline-none',
                  requestErrorIsWarning &&
                    'border-amber-500/30 bg-amber-500/5 [&>svg]:text-amber-700 dark:[&>svg]:text-amber-300'
                )}
              >
                <ShieldAlert aria-hidden='true' />
                <AlertTitle>{requestErrorTitle}</AlertTitle>
                <AlertDescription>{requestErrorDescription}</AlertDescription>
              </Alert>
            ) : null}

            {workingReview ? (
              <ManualBillingReviewCaseDetails review={workingReview} />
            ) : null}

            {workingReview &&
            !manualBillingReviewKindIsSupported(workingReview.review_kind) ? (
              <Alert role='status'>
                <LockKeyhole aria-hidden='true' />
                <AlertTitle>
                  {t('This billing review type is not supported')}
                </AlertTitle>
                <AlertDescription className='break-all'>
                  {workingReview.review_kind || t('Unknown')}
                </AlertDescription>
              </Alert>
            ) : null}

            {workingReview?.review_kind === 'terminal_usage' &&
            !terminalUsageContextAvailable ? (
              <Alert variant='destructive' role='alert'>
                <ShieldAlert aria-hidden='true' />
                <AlertTitle>
                  {t(
                    'The frozen upstream task ID is required for this decision'
                  )}
                </AlertTitle>
                <AlertDescription>
                  {t('This decision is blocked by the latest server evidence')}
                </AlertDescription>
              </Alert>
            ) : null}

            {workingReview &&
            props.canResolve &&
            manualBillingReviewKindIsSupported(workingReview.review_kind) ? (
              <Form {...form}>
                <form
                  id='manual-billing-review-form'
                  noValidate
                  onSubmit={form.handleSubmit(freezeDecision)}
                >
                  <SideDrawerSection>
                    <SideDrawerSectionHeader
                      icon={
                        <FileCheck2 className='size-4' aria-hidden='true' />
                      }
                      title={t('Provider decision evidence')}
                      description={t(
                        'Record only a provider case, audit reference, or console identifier. Never paste credentials, access tokens, or secret URLs.'
                      )}
                    />

                    <FormField
                      control={form.control}
                      name='action'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Decision')}</FormLabel>
                          <FormControl>
                            <RadioGroup
                              value={field.value ?? ''}
                              onValueChange={(value) => {
                                if (
                                  value === 'confirmed_accepted' ||
                                  value === 'confirmed_rejected'
                                ) {
                                  field.onChange(value)
                                }
                              }}
                              className='grid gap-2 sm:grid-cols-2'
                              aria-invalid={
                                field.value == null &&
                                !!form.formState.errors.action
                              }
                            >
                              <label
                                className={cn(
                                  'border-input bg-background hover:bg-accent/40 has-data-checked:border-emerald-600 has-data-checked:bg-emerald-500/5 flex min-w-0 cursor-pointer items-start gap-3 rounded-md border p-3 text-sm',
                                  !acceptDecisionAvailable &&
                                    'cursor-not-allowed opacity-50'
                                )}
                              >
                                <RadioGroupItem
                                  value='confirmed_accepted'
                                  disabled={!acceptDecisionAvailable}
                                  aria-label={t('Accept verified outcome')}
                                />
                                <span className='min-w-0'>
                                  <span className='flex items-center gap-1.5 font-medium'>
                                    <CheckCircle2
                                      className='size-4 text-emerald-700 dark:text-emerald-300'
                                      aria-hidden='true'
                                    />
                                    {t('Accept')}
                                  </span>
                                  <span className='text-muted-foreground mt-1 block text-xs leading-5'>
                                    {acceptDecisionDescription}
                                  </span>
                                </span>
                              </label>
                              <label
                                className={cn(
                                  'border-input bg-background hover:bg-accent/40 has-data-checked:border-destructive has-data-checked:bg-destructive/5 flex min-w-0 cursor-pointer items-start gap-3 rounded-md border p-3 text-sm',
                                  !rejectDecisionAvailable &&
                                    'cursor-not-allowed opacity-50'
                                )}
                              >
                                <RadioGroupItem
                                  value='confirmed_rejected'
                                  disabled={!rejectDecisionAvailable}
                                  aria-label={t('Reject verified outcome')}
                                />
                                <span className='min-w-0'>
                                  <span className='flex items-center gap-1.5 font-medium'>
                                    <XCircle
                                      className='text-destructive size-4'
                                      aria-hidden='true'
                                    />
                                    {t('Reject')}
                                  </span>
                                  <span className='text-muted-foreground mt-1 block text-xs leading-5'>
                                    {rejectDecisionDescription}
                                  </span>
                                </span>
                              </label>
                            </RadioGroup>
                          </FormControl>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    {workingReview.review_kind === 'send_outcome' &&
                    selectedAction === 'confirmed_rejected' ? (
                      <FormField
                        control={form.control}
                        name='rejection_provider_status'
                        render={({ field }) => (
                          <FormItem>
                            <FormLabel>{t('Provider outcome')}</FormLabel>
                            <FormControl>
                              <NativeSelect
                                value={field.value}
                                onChange={field.onChange}
                              >
                                <NativeSelectOption value='confirmed_rejected'>
                                  {t('Confirmed rejected')}
                                </NativeSelectOption>
                                <NativeSelectOption value='confirmed_not_found'>
                                  {t('Confirmed not found')}
                                </NativeSelectOption>
                              </NativeSelect>
                            </FormControl>
                            <FormDescription>
                              {t(
                                'Choose the exact status confirmed by the provider.'
                              )}
                            </FormDescription>
                            <FormMessage />
                          </FormItem>
                        )}
                      />
                    ) : null}

                    <div className='grid gap-4 sm:grid-cols-2'>
                      {workingReview.review_kind === 'send_outcome' ? (
                        <FormField
                          control={form.control}
                          name='upstream_task_id'
                          render={({ field }) => (
                            <FormItem>
                              <FormLabel>
                                {t('Upstream task ID')}
                                {selectedAction === 'confirmed_accepted'
                                  ? ` ${t('(required)')}`
                                  : null}
                              </FormLabel>
                              <FormControl>
                                <Input
                                  {...field}
                                  autoComplete='off'
                                  spellCheck={false}
                                  maxLength={191}
                                  className='font-mono'
                                />
                              </FormControl>
                              <FormMessage />
                            </FormItem>
                          )}
                        />
                      ) : null}
                      <FormField
                        control={form.control}
                        name='provider_checked_at'
                        render={({ field }) => (
                          <FormItem>
                            <FormLabel>{t('Provider checked time')}</FormLabel>
                            <FormControl>
                              <Input
                                {...field}
                                type='datetime-local'
                                step='1'
                                min={
                                  evidenceWindowStart > 0
                                    ? toLocalDateTimeInput(
                                        Math.ceil(evidenceWindowStart / 1000) *
                                          1000
                                      )
                                    : undefined
                                }
                                max={toLocalDateTimeInput(
                                  Date.now() + 5 * 60 * 1000
                                )}
                              />
                            </FormControl>
                            <FormMessage />
                          </FormItem>
                        )}
                      />
                    </div>

                    <FormField
                      control={form.control}
                      name='evidence_reference'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>
                            {t('Provider evidence reference')}
                          </FormLabel>
                          <FormControl>
                            <Input
                              {...field}
                              autoComplete='off'
                              spellCheck={false}
                              maxLength={512}
                              placeholder={t(
                                'Provider case or audit reference'
                              )}
                            />
                          </FormControl>
                          <FormDescription>
                            {t(
                              'Use a non-secret reference that another administrator can verify.'
                            )}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <FormField
                      control={form.control}
                      name='reason'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Decision reason')}</FormLabel>
                          <FormControl>
                            <Textarea
                              {...field}
                              rows={4}
                              maxLength={1024}
                              className='resize-y'
                              placeholder={t(
                                'Explain why the provider evidence supports this decision'
                              )}
                            />
                          </FormControl>
                          <FormDescription>
                            {t(
                              'Use one concise paragraph without line breaks or secret values.'
                            )}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <div className='border-border/70 bg-muted/30 rounded-lg border px-3 py-2.5 text-xs'>
                      <span className='text-muted-foreground'>
                        {t('Provider status sent to the server')}:{' '}
                      </span>
                      <span className='font-mono break-all'>
                        {providerStatusLabel}
                      </span>
                    </div>
                  </SideDrawerSection>
                </form>
              </Form>
            ) : null}

            {!props.canResolve && workingReview ? (
              <Alert role='status'>
                <LockKeyhole aria-hidden='true' />
                <AlertTitle>{t('Read-only billing review')}</AlertTitle>
                <AlertDescription>
                  {t(
                    'Your current role can inspect evidence and financial outcomes but cannot submit a decision.'
                  )}
                </AlertDescription>
              </Alert>
            ) : null}
          </div>

          <SheetFooter className={sideDrawerFooterClassName()}>
            <Button
              type='button'
              variant='outline'
              disabled={isSubmitting}
              onClick={() => props.onOpenChange(false)}
            >
              {t('Close')}
            </Button>
            {props.canResolve &&
            workingReview &&
            manualBillingReviewKindIsSupported(workingReview.review_kind) ? (
              <Button
                type='submit'
                form='manual-billing-review-form'
                disabled={!reviewCanResolve || isSubmitting}
                className='h-auto min-h-9 whitespace-normal'
              >
                {isSubmitting ? (
                  <RefreshCw
                    className='animate-spin motion-reduce:animate-none'
                    aria-hidden='true'
                  />
                ) : (
                  <FileCheck2 aria-hidden='true' />
                )}
                {t('Review decision')}
              </Button>
            ) : null}
          </SheetFooter>
        </SheetContent>
      </Sheet>

      <RiskAcknowledgementDialog
        open={confirmation != null}
        onOpenChange={(open) => {
          if (!open && !isSubmitting) setConfirmation(null)
        }}
        title={confirmationTitle}
        description={
          <span
            className={cn(
              'font-medium',
              confirmationReview != null &&
                manualBillingReviewIsOverage(confirmationReview.review_kind) &&
                'text-amber-800 dark:text-amber-200'
            )}
          >
            {confirmationImpact}
          </span>
        }
        items={[
          ...(confirmationReview?.review_kind === 'terminal_usage'
            ? [
                t('Frozen upstream task ID: {{task}}', {
                  task: confirmationReview.upstream_task_id ?? '',
                }),
              ]
            : []),
          t('Provider status: {{status}}', {
            status: confirmation?.payload.provider_status ?? '',
          }),
          t('Evidence reference: {{reference}}', {
            reference: confirmation?.payload.evidence_reference ?? '',
          }),
          t('Decision reason: {{reason}}', {
            reason: confirmation?.payload.reason ?? '',
          }),
        ]}
        checklist={[
          t('I verified the provider evidence and decision reason.'),
          confirmationChecklist,
        ]}
        requiredText={String(confirmationReview?.reservation_id ?? '')}
        inputPrompt={t('Type the reservation ID to confirm:')}
        inputPlaceholder={t('Type the reservation ID')}
        mismatchHint={t('The reservation ID does not match.')}
        confirmText={confirmationButtonText}
        destructive={confirmationAction === 'confirmed_rejected'}
        isLoading={isSubmitting}
        onConfirm={submitFrozenDecision}
      />
    </>
  )
}
