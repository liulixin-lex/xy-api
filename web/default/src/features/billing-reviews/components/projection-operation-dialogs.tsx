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
import { useEffect, useMemo, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { RiskAcknowledgementDialog } from '@/components/risk-acknowledgement-dialog'
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
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import {
  getBillingProjectionOperationApiError,
  requeueFailedBillingProjection,
  resolveBillingLogSinkConflict,
} from '../api/projection-operations'
import {
  BillingProjectionOperationSession,
  billingProjectionOperationNeedsRefresh,
  createBillingProjectionReasonSchema,
  type BillingProjectionReasonValues,
} from '../lib/projection-operations'
import type {
  BillingLogSinkConflict,
  BillingProjectionOperationApiError,
  FailedBillingProjection,
  FailedBillingProjectionDataset,
} from '../projection-types'

function getOperationErrorMessage(
  error: BillingProjectionOperationApiError,
  translate: (key: string) => string
): string {
  if (error.status === 403) {
    return translate(
      'Your permission changed. The latest access rules are loading.'
    )
  }
  if (error.status === 409) {
    return translate('This idempotency key conflicts with another operation.')
  }
  if (error.status === 412) {
    return translate('The record changed. Refresh and review the latest state.')
  }
  if (error.status === 422) {
    return translate('This record requires a different remediation workflow.')
  }
  if (error.status === 404) {
    return translate('The record is no longer available.')
  }
  return translate(
    'The operation was not confirmed. Retry with the same frozen request.'
  )
}

export type BillingProjectionRequeueTarget = {
  dataset: FailedBillingProjectionDataset
  projection: FailedBillingProjection
}

type FrozenConflict = {
  conflict: BillingLogSinkConflict
  reason: string
  signature: string
}

type OperationVariables =
  | {
      kind: 'requeue'
      target: BillingProjectionRequeueTarget
      signature: string
      generation: number
      idempotencyKey: string
      signal: AbortSignal
    }
  | {
      kind: 'conflict'
      target: FrozenConflict
      signature: string
      generation: number
      idempotencyKey: string
      signal: AbortSignal
    }

export function ProjectionOperationDialogs(props: {
  requeueTarget: BillingProjectionRequeueTarget | null
  conflictTarget: BillingLogSinkConflict | null
  requeueFinalFocus: React.RefObject<HTMLElement | null>
  conflictFinalFocus: React.RefObject<HTMLElement | null>
  onRequeueTargetChange: (target: BillingProjectionRequeueTarget | null) => void
  onConflictTargetChange: (target: BillingLogSinkConflict | null) => void
  onRefresh: () => Promise<void>
  onPermissionRevoked: () => Promise<void>
}) {
  const { t } = useTranslation()
  const sessionRef = useRef(new BillingProjectionOperationSession())
  const [sessionGeneration, setSessionGeneration] = useState(0)
  const [frozenConflict, setFrozenConflict] = useState<FrozenConflict | null>(
    null
  )
  const [operationError, setOperationError] = useState<string | null>(null)
  const reasonSchema = useMemo(
    () =>
      createBillingProjectionReasonSchema({
        required: t('Resolution reason is required'),
        tooLong: t('Resolution reason must be 1024 bytes or fewer'),
        singleLine: t('Use a single line without control characters'),
      }),
    [t]
  )
  const form = useForm<BillingProjectionReasonValues>({
    resolver: zodResolver(reasonSchema),
    defaultValues: { reason: '' },
  })

  const operation = useMutation({
    mutationFn: (variables: OperationVariables) => {
      if (variables.kind === 'requeue') {
        return requeueFailedBillingProjection(
          variables.target.dataset,
          variables.target.projection,
          variables.idempotencyKey,
          variables.signal
        )
      }
      return resolveBillingLogSinkConflict(
        variables.target.conflict,
        variables.target.reason,
        variables.idempotencyKey,
        variables.signal
      )
    },
    onSuccess: async (result, variables) => {
      if (!sessionRef.current.isCurrent(variables.generation)) return
      sessionRef.current.complete(variables.generation, variables.signature)
      setOperationError(null)
      props.onRequeueTargetChange(null)
      props.onConflictTargetChange(null)
      setFrozenConflict(null)
      await props.onRefresh().catch(() => undefined)
      toast.success(
        result.replayed
          ? t('The existing operation result was replayed')
          : t('Billing projection operation completed')
      )
    },
    onError: async (error, variables) => {
      sessionRef.current.release(variables.generation, variables.signature)
      if (!sessionRef.current.isCurrent(variables.generation)) return
      const apiError = getBillingProjectionOperationApiError(error)
      const message = getOperationErrorMessage(apiError, t)
      setOperationError(message)
      if (!billingProjectionOperationNeedsRefresh(apiError)) return

      props.onRequeueTargetChange(null)
      props.onConflictTargetChange(null)
      setFrozenConflict(null)
      await props.onRefresh().catch(() => undefined)
      if (apiError.status === 403) {
        await props.onPermissionRevoked().catch(() => undefined)
      }
      toast.error(message)
    },
  })

  useEffect(() => {
    const target = props.requeueTarget ?? props.conflictTarget
    if (!target) {
      sessionRef.current.close()
      setFrozenConflict(null)
      setOperationError(null)
      form.reset({ reason: '' })
      return
    }
    const generation = sessionRef.current.open()
    setSessionGeneration(generation)
    setFrozenConflict(null)
    setOperationError(null)
    form.reset({ reason: '' })
  }, [form, props.conflictTarget, props.requeueTarget])

  const isSubmitting =
    operation.isPending && operation.variables?.generation === sessionGeneration

  const closeRequeue = () => {
    if (isSubmitting) return
    props.onRequeueTargetChange(null)
  }

  const closeConflict = () => {
    if (isSubmitting) return
    props.onConflictTargetChange(null)
    setFrozenConflict(null)
  }

  const freezeConflict = (values: BillingProjectionReasonValues) => {
    if (!props.conflictTarget) return
    const reason = values.reason
    setFrozenConflict({
      conflict: props.conflictTarget,
      reason,
      signature: JSON.stringify({
        id: props.conflictTarget.id,
        etag: props.conflictTarget.etag,
        version: props.conflictTarget.version,
        reason,
      }),
    })
  }

  const submitRequeue = () => {
    if (!props.requeueTarget) return
    const signature = JSON.stringify({
      dataset: props.requeueTarget.dataset,
      id: props.requeueTarget.projection.id,
      etag: props.requeueTarget.projection.etag,
      failure_code: props.requeueTarget.projection.failure_code,
    })
    const claimed = sessionRef.current.claim(signature)
    if (!claimed) return
    operation.mutate({
      kind: 'requeue',
      target: props.requeueTarget,
      signature,
      generation: claimed.generation,
      idempotencyKey: claimed.key,
      signal: claimed.signal,
    })
  }

  const submitConflict = () => {
    if (!frozenConflict) return
    const claimed = sessionRef.current.claim(frozenConflict.signature)
    if (!claimed) return
    operation.mutate({
      kind: 'conflict',
      target: frozenConflict,
      signature: frozenConflict.signature,
      generation: claimed.generation,
      idempotencyKey: claimed.key,
      signal: claimed.signal,
    })
  }

  return (
    <>
      <Dialog
        open={props.conflictTarget != null && frozenConflict == null}
        onOpenChange={(open) => {
          if (!open) closeConflict()
        }}
      >
        <DialogContent
          className='sm:max-w-lg'
          finalFocus={props.conflictFinalFocus}
        >
          <DialogHeader>
            <DialogTitle>{t('Resolve log sink conflict')}</DialogTitle>
            <DialogDescription>
              {t(
                'Record a concise, non-secret remediation reason before reviewing the frozen conflict operation.'
              )}
            </DialogDescription>
          </DialogHeader>
          <form
            id='billing-conflict-reason-form'
            noValidate
            onSubmit={form.handleSubmit(freezeConflict)}
          >
            <FieldGroup>
              <Field data-invalid={Boolean(form.formState.errors.reason)}>
                <FieldLabel htmlFor='billing-conflict-reason'>
                  {t('Resolution reason')}
                </FieldLabel>
                <Input
                  id='billing-conflict-reason'
                  {...form.register('reason')}
                  autoComplete='off'
                  maxLength={1024}
                  aria-invalid={Boolean(form.formState.errors.reason)}
                  placeholder={t('Verified remediation reason')}
                />
                <FieldDescription>
                  {t(
                    'Use one line without payloads, receipts, credentials, or secret values.'
                  )}
                </FieldDescription>
                <FieldError
                  errors={
                    form.formState.errors.reason
                      ? [form.formState.errors.reason]
                      : undefined
                  }
                />
              </Field>
            </FieldGroup>
          </form>
          <DialogFooter>
            <Button type='button' variant='outline' onClick={closeConflict}>
              {t('Cancel')}
            </Button>
            <Button type='submit' form='billing-conflict-reason-form'>
              {t('Review operation')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <RiskAcknowledgementDialog
        open={props.requeueTarget != null}
        finalFocus={props.requeueFinalFocus}
        onOpenChange={(open) => {
          if (!open) closeRequeue()
        }}
        title={t('Confirm projection requeue')}
        description={
          operationError ? (
            <Alert variant='destructive'>
              <AlertTitle>{t('Projection requeue failed')}</AlertTitle>
              <AlertDescription>{operationError}</AlertDescription>
            </Alert>
          ) : (
            t(
              'This retries the frozen projection after comparing its current failure revision.'
            )
          )
        }
        items={[
          t('Projection ID: {{id}}', {
            id: props.requeueTarget?.projection.id ?? '',
          }),
          t('Projection kind: {{kind}}', {
            kind: props.requeueTarget?.projection.kind ?? '',
          }),
          t('Failure code: {{code}}', {
            code: props.requeueTarget?.projection.failure_code ?? '',
          }),
        ]}
        checklist={[
          t('I reviewed the failure code and sanitized error context.'),
          t(
            'I understand the operation may replay an existing idempotent result.'
          ),
        ]}
        requiredText={String(props.requeueTarget?.projection.id ?? '')}
        inputPrompt={t('Type the projection ID to confirm:')}
        inputPlaceholder={t('Type the projection ID')}
        mismatchHint={t('The projection ID does not match.')}
        confirmText={t('Requeue projection')}
        destructive={false}
        isLoading={isSubmitting}
        onConfirm={submitRequeue}
      />

      <RiskAcknowledgementDialog
        open={frozenConflict != null}
        finalFocus={props.conflictFinalFocus}
        onOpenChange={(open) => {
          if (!open) closeConflict()
        }}
        title={t('Confirm conflict resolution and requeue')}
        description={
          operationError ? (
            <Alert variant='destructive'>
              <AlertTitle>{t('Conflict resolution failed')}</AlertTitle>
              <AlertDescription>{operationError}</AlertDescription>
            </Alert>
          ) : (
            t(
              'This records the frozen reason, resolves the quarantined conflict, and requeues its projection.'
            )
          )
        }
        items={[
          t('Conflict ID: {{id}}', { id: frozenConflict?.conflict.id ?? '' }),
          t('Conflict version: {{version}}', {
            version: frozenConflict?.conflict.version ?? '',
          }),
          t('{{count}} distinct receipts', {
            count: frozenConflict?.conflict.distinct_receipts ?? 0,
          }),
          t('Resolution reason: {{reason}}', {
            reason: frozenConflict?.reason ?? '',
          }),
        ]}
        checklist={[
          t('I verified the receipt counts and remediation reason.'),
          t(
            'I understand this resolves the conflict record and requeues the frozen projection.'
          ),
        ]}
        requiredText={String(frozenConflict?.conflict.id ?? '')}
        inputPrompt={t('Type the conflict ID to confirm:')}
        inputPlaceholder={t('Type the conflict ID')}
        mismatchHint={t('The conflict ID does not match.')}
        confirmText={t('Resolve and requeue')}
        destructive={false}
        isLoading={isSubmitting}
        onConfirm={submitConflict}
      />
    </>
  )
}
