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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AxiosError } from 'axios'
import {
  CheckCircle2,
  LoaderCircle,
  RotateCcw,
  ShieldAlert,
  TriangleAlert,
} from 'lucide-react'
import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogMedia,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'

import {
  createChannelRoutingIdempotencyKey,
  getChannelRoutingOperation,
  resetChannelRoutingBreaker,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import {
  channelRoutingOperationBreakerResetResult,
  channelRoutingOperationDisplayStatus,
  channelRoutingOperationIsActive,
} from '../lib/operations'
import {
  createChannelRoutingBreakerResetSchema,
  type ChannelRoutingBreakerResetFormValues,
} from '../schemas/breaker-reset'
import type {
  ChannelRoutingBreakerResetRequest,
  ChannelRoutingBreakerResetResponse,
  ChannelRoutingBreakerResetResult,
  ChannelRoutingBreakerResetTarget,
} from '../types'
import { ChannelRoutingStatusBadge } from './status-badge'

function breakerResetTargetLabel(
  target: ChannelRoutingBreakerResetTarget,
  translate: (key: string, options?: Record<string, unknown>) => string
): string {
  if (target.scope === 'endpoint') {
    return `${target.endpoint_authority ?? ''} · ${target.region ?? ''}`
  }
  return translate(
    'Pool #{{pool}} · member #{{member}} · channel #{{channel}} · {{model}}',
    {
      pool: target.pool_id,
      member: target.member_id,
      channel: target.channel_id,
      model: target.model_name,
    }
  )
}

function breakerResetRequestError(
  error: unknown,
  translate: (key: string) => string
) {
  if (!(error instanceof AxiosError)) {
    return translate('The breaker reset could not be submitted. Try again.')
  }
  if (
    error.code === 'ECONNABORTED' ||
    error.code === 'ETIMEDOUT' ||
    error.message.toLowerCase().includes('timeout')
  ) {
    return translate(
      'The breaker reset request timed out. Retrying is safe and keeps the same operation key.'
    )
  }
  if (error.code === 'ERR_NETWORK') {
    return translate(
      'The breaker reset could not reach the server. Check the connection and retry.'
    )
  }
  const responseData = error.response?.data
  const responseCode =
    responseData != null &&
    typeof responseData === 'object' &&
    'code' in responseData &&
    typeof responseData.code === 'string'
      ? responseData.code
      : ''
  switch (error.response?.status) {
    case 403:
      return translate('Your role cannot reset channel routing breakers.')
    case 404:
      return translate(
        'This breaker target is no longer available. Refresh the page before trying again.'
      )
    case 409:
      if (responseCode === 'routing_snapshot_unavailable') {
        return translate(
          'The routing snapshot is temporarily unavailable. Refresh the page before trying again.'
        )
      }
      if (responseCode === 'idempotency_key_conflict') {
        return translate(
          'This operation key was already used for a different reset. Close the dialog and start again.'
        )
      }
      return translate(
        'The routing target changed before the reset was accepted. Refresh the page and review its current state.'
      )
    case 429:
      return translate('Too many reset requests. Wait briefly and try again.')
    default:
      return translate('The breaker reset could not be submitted. Try again.')
  }
}

export function ChannelRoutingBreakerResetDialog(props: {
  request: ChannelRoutingBreakerResetRequest
  targetLabel: string
  disabled?: boolean
  compact?: boolean
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const formId = useId()
  const [open, setOpen] = useState(false)
  const [response, setResponse] =
    useState<ChannelRoutingBreakerResetResponse | null>(null)
  const idempotencyKeyRef = useRef<string | null>(null)
  const refreshedOperationRef = useRef<number | null>(null)
  const schema = useMemo(
    () =>
      createChannelRoutingBreakerResetSchema(
        t('Reason must be 512 characters or fewer.')
      ),
    [t]
  )
  const form = useForm<ChannelRoutingBreakerResetFormValues>({
    resolver: zodResolver(schema),
    defaultValues: { reason: '' },
  })
  const reset = useMutation({
    mutationFn: (values: ChannelRoutingBreakerResetFormValues) => {
      idempotencyKeyRef.current ??=
        createChannelRoutingIdempotencyKey('breaker-reset')
      const reason = values.reason.trim()
      const payload = reason ? { ...props.request, reason } : props.request
      return resetChannelRoutingBreaker(payload, idempotencyKeyRef.current)
    },
    onSuccess: (nextResponse) => {
      setResponse(nextResponse)
      queryClient.setQueryData(
        channelRoutingQueryKeys.operation(nextResponse.operation.id),
        nextResponse.operation
      )
      void queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.operationsRoot(),
      })
    },
  })
  const operationId = response?.operation.id ?? null
  const operationQuery = useQuery({
    queryKey: channelRoutingQueryKeys.operation(operationId ?? 0),
    queryFn: () =>
      getChannelRoutingOperation<ChannelRoutingBreakerResetResult>(
        operationId ?? 0
      ),
    enabled: operationId != null,
    refetchInterval: (query) =>
      channelRoutingOperationIsActive(query.state.data) ? 2_000 : false,
  })
  const operation = operationQuery.data ?? response?.operation
  const resetResult = operation
    ? channelRoutingOperationBreakerResetResult(operation)
    : null
  const normalizedTarget = resetResult?.target ?? response?.target
  const status = operation
    ? channelRoutingOperationDisplayStatus(operation)
    : ''
  const active = reset.isPending || channelRoutingOperationIsActive(operation)
  const completed = status === 'succeeded'
  const notApplied = status === 'failed' || status === 'superseded'
  const terminalOperationId =
    operation && !channelRoutingOperationIsActive(operation)
      ? operation.id
      : null

  useEffect(() => {
    if (
      terminalOperationId == null ||
      refreshedOperationRef.current === terminalOperationId
    ) {
      return
    }
    refreshedOperationRef.current = terminalOperationId
    void Promise.all([
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.overview(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.groupsRoot(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.endpointsRoot(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.probesRoot(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.operationsRoot(),
      }),
    ])
  }, [queryClient, terminalOperationId])

  const clearOperation = () => {
    idempotencyKeyRef.current = null
    refreshedOperationRef.current = null
    setResponse(null)
    reset.reset()
    form.clearErrors()
  }

  return (
    <AlertDialog
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen)
        if (!nextOpen && !active) {
          clearOperation()
          form.reset()
        }
      }}
    >
      <AlertDialogTrigger
        disabled={props.disabled}
        render={
          <Button
            type='button'
            size={props.compact ? 'icon-sm' : 'sm'}
            variant='ghost'
            aria-label={t('Reset breaker for {{target}}', {
              target: props.targetLabel,
            })}
            title={props.compact ? t('Reset breaker') : undefined}
          />
        }
      >
        <RotateCcw aria-hidden='true' />
        {!props.compact ? t('Reset breaker') : null}
      </AlertDialogTrigger>
      <AlertDialogContent className='channel-routing-touch-surface max-h-[min(88dvh,44rem)] w-[calc(100vw-1.5rem)] overflow-y-auto sm:max-w-md'>
        <AlertDialogHeader>
          <AlertDialogMedia className='bg-destructive/10 text-destructive'>
            <ShieldAlert aria-hidden='true' />
          </AlertDialogMedia>
          <AlertDialogTitle>{t('Reset routing breaker?')}</AlertDialogTitle>
          <AlertDialogDescription className='text-left'>
            {t(
              'This clears the current breaker evidence for the selected target. New failures can open it again immediately.'
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>

        <dl className='grid gap-1 border-y py-3 text-sm'>
          <div className='grid min-w-0 grid-cols-[5rem_minmax(0,1fr)] gap-3'>
            <dt className='text-muted-foreground'>{t('Scope')}</dt>
            <dd className='font-medium'>
              {props.request.scope === 'member'
                ? t('Member / model')
                : t('Endpoint / region')}
            </dd>
          </div>
          <div className='grid min-w-0 grid-cols-[5rem_minmax(0,1fr)] gap-3'>
            <dt className='text-muted-foreground'>{t('Target')}</dt>
            <dd className='font-mono text-xs break-all'>{props.targetLabel}</dd>
          </div>
          {operation ? (
            <div className='grid min-w-0 grid-cols-[5rem_minmax(0,1fr)] items-center gap-3'>
              <dt className='text-muted-foreground'>{t('Operation')}</dt>
              <dd className='flex flex-wrap items-center gap-2'>
                <span>#{operation.id}</span>
                <ChannelRoutingStatusBadge status={status} />
              </dd>
            </div>
          ) : null}
          {normalizedTarget ? (
            <div className='grid min-w-0 grid-cols-[5rem_minmax(0,1fr)] gap-3'>
              <dt className='text-muted-foreground'>
                {t('Normalized target')}
              </dt>
              <dd className='font-mono text-xs break-all'>
                {breakerResetTargetLabel(normalizedTarget, t)}
              </dd>
            </div>
          ) : null}
        </dl>

        {!operation ? (
          <form
            id={formId}
            onSubmit={form.handleSubmit((values) => reset.mutate(values))}
          >
            <Field data-invalid={Boolean(form.formState.errors.reason)}>
              <FieldLabel htmlFor={`${formId}-reason`}>
                {t('Reason (optional)')}
              </FieldLabel>
              <Textarea
                id={`${formId}-reason`}
                rows={3}
                maxLength={512}
                placeholder={t('Add context for the operation audit')}
                aria-invalid={Boolean(form.formState.errors.reason)}
                disabled={reset.isPending}
                {...form.register('reason')}
              />
              <FieldDescription>
                {t('Stored with the operation for later review.')}
              </FieldDescription>
              <FieldError errors={[form.formState.errors.reason]} />
            </Field>
          </form>
        ) : null}

        <div aria-live='polite'>
          {reset.isError ? (
            <div
              className='border-destructive/30 bg-destructive/5 text-destructive flex items-start gap-2 rounded-lg border p-3 text-sm'
              role='alert'
            >
              <TriangleAlert
                className='mt-0.5 size-4 shrink-0'
                aria-hidden='true'
              />
              <span>{breakerResetRequestError(reset.error, t)}</span>
            </div>
          ) : null}
          {operationQuery.isError ? (
            <div
              className='border-destructive/30 bg-destructive/5 text-destructive flex flex-wrap items-center gap-2 rounded-lg border p-3 text-sm'
              role='alert'
            >
              <TriangleAlert className='size-4 shrink-0' aria-hidden='true' />
              <span className='min-w-0 flex-1'>
                {t('Could not refresh the breaker reset status.')}
              </span>
              <Button
                type='button'
                size='sm'
                variant='outline'
                onClick={() => void operationQuery.refetch()}
              >
                {t('Retry status')}
              </Button>
            </div>
          ) : null}
          {active && operation ? (
            <div className='text-muted-foreground flex items-center gap-2 text-sm'>
              <LoaderCircle
                className='size-4 animate-spin motion-reduce:animate-none'
                aria-hidden='true'
              />
              <span>
                {t(
                  'The reset is being applied. This status updates automatically.'
                )}
              </span>
            </div>
          ) : null}
          {completed ? (
            <div className='flex items-start gap-2 text-sm' role='status'>
              <CheckCircle2
                className='text-success mt-0.5 size-4 shrink-0'
                aria-hidden='true'
              />
              <span>
                {t('Breaker reset completed at generation {{generation}}.', {
                  generation: resetResult?.generation ?? 0,
                })}
              </span>
            </div>
          ) : null}
          {notApplied ? (
            <div
              className='border-destructive/30 bg-destructive/5 text-destructive flex items-start gap-2 rounded-lg border p-3 text-sm'
              role='alert'
            >
              <TriangleAlert
                className='mt-0.5 size-4 shrink-0'
                aria-hidden='true'
              />
              <span>
                {status === 'superseded'
                  ? t(
                      'The routing target changed before execution, so no breaker state was cleared.'
                    )
                  : t(
                      'The breaker reset failed and no state change was confirmed.'
                    )}
              </span>
            </div>
          ) : null}
        </div>

        <AlertDialogFooter>
          <AlertDialogCancel disabled={reset.isPending}>
            {completed ? t('Done') : t('Cancel')}
          </AlertDialogCancel>
          {!operation ? (
            <Button
              type='submit'
              form={formId}
              variant='destructive'
              disabled={reset.isPending}
            >
              {reset.isPending ? (
                <LoaderCircle
                  className='animate-spin motion-reduce:animate-none'
                  aria-hidden='true'
                />
              ) : (
                <RotateCcw aria-hidden='true' />
              )}
              {reset.isPending ? t('Submitting reset') : t('Confirm reset')}
            </Button>
          ) : null}
          {notApplied ? (
            <Button type='button' variant='outline' onClick={clearOperation}>
              <RotateCcw aria-hidden='true' />
              {t('Prepare another reset')}
            </Button>
          ) : null}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
