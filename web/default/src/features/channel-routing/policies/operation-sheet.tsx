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
import {
  Activity01Icon,
  Cancel01Icon,
  Download01Icon,
  RefreshIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from '@tanstack/react-query'
import { useId, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  sideDrawerContentClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import {
  cancelChannelRoutingOperation,
  downloadChannelRoutingAuditExport,
  getChannelRoutingOperation,
  getChannelRoutingPolicyApiError,
  retryChannelRoutingOperation,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import {
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
} from '../components/page-state'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import {
  channelRoutingOperationAuditExportId,
  channelRoutingOperationCanCancel,
  channelRoutingOperationCanRetry,
  channelRoutingOperationDisplayStatus,
  channelRoutingOperationIsActive,
  channelRoutingOperationRetentionLabel,
  channelRoutingOperationSourceLabel,
  channelRoutingOperationTypeLabel,
} from '../lib/operations'
import type { RoutingOperation } from '../types'
import {
  ChannelRoutingOperationResultSection,
  ChannelRoutingOperationTechnicalSection,
} from './operation-detail-sections'

type OperationAction = 'cancel' | 'retry'

export function ChannelRoutingOperationSheet(props: {
  operationId: number | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onOperationChange?: (operationId: number) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const queryClient = useQueryClient()
  const user = useAuthStore((state) => state.auth.user)
  const [action, setAction] = useState<OperationAction | null>(null)
  const [reason, setReason] = useState('')
  const [reasonError, setReasonError] = useState('')
  const reasonId = useId()
  const reasonHelpId = useId()
  const reasonErrorId = useId()
  const reasonRef = useRef<HTMLTextAreaElement>(null)
  const canOperate = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )
  const canAuditExport = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.AUDIT_EXPORT
  )
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.operation(props.operationId ?? 0),
    queryFn: ({ signal }) =>
      getChannelRoutingOperation(props.operationId ?? 0, signal),
    enabled: props.open && props.operationId != null,
    refetchInterval: (operationQuery) =>
      channelRoutingOperationIsActive(operationQuery.state.data)
        ? 3_000
        : false,
  })
  const actionMutation = useMutation({
    mutationFn: async (request: {
      action: OperationAction
      operationId: number
      reason: string
    }) => {
      if (request.action === 'retry') {
        const response = await retryChannelRoutingOperation(
          request.operationId,
          request.reason
        )
        return {
          action: request.action,
          operation: response.operation,
          created: response.created,
        }
      }
      return {
        action: request.action,
        operation: await cancelChannelRoutingOperation(
          request.operationId,
          request.reason
        ),
        created: false,
      }
    },
    onSuccess: (result, request) => {
      invalidateOperationViews(
        queryClient,
        request.operationId,
        result.operation.id
      )
      setAction(null)
      setReason('')
      setReasonError('')
      if (result.action === 'retry') {
        toast.success(
          result.created
            ? t('Retry operation #{{id}} was created', {
                id: result.operation.id,
              })
            : t('Existing retry operation reused')
        )
        props.onOperationChange?.(result.operation.id)
        return
      }
      toast.success(t('Operation cancelled'))
    },
    meta: { handleErrorLocally: true },
  })
  const operation = query.data
  const displayStatus = operation
    ? channelRoutingOperationDisplayStatus(operation)
    : ''
  const auditExportId = operation
    ? channelRoutingOperationAuditExportId(operation)
    : null
  const canRetry = canOperate && channelRoutingOperationCanRetry(operation)
  const canCancel = canOperate && channelRoutingOperationCanCancel(operation)
  const actionAllowed = operationActionAllowed(action, canRetry, canCancel)
  const actionError = actionMutation.error
    ? getChannelRoutingPolicyApiError(actionMutation.error).message ||
      t('The operation action could not be completed.')
    : ''
  const downloadExport = useMutation({
    mutationFn: downloadChannelRoutingAuditExport,
    onSuccess: () => toast.success(t('Audit export downloaded')),
  })
  let actionButtonIcon: ReactNode = null
  if (actionMutation.isPending) {
    actionButtonIcon = <Spinner data-icon='inline-start' />
  } else if (action === 'retry') {
    actionButtonIcon = (
      <HugeiconsIcon
        icon={RefreshIcon}
        data-icon='inline-start'
        strokeWidth={2}
        aria-hidden='true'
      />
    )
  } else if (action === 'cancel') {
    actionButtonIcon = (
      <HugeiconsIcon
        icon={Cancel01Icon}
        data-icon='inline-start'
        strokeWidth={2}
        aria-hidden='true'
      />
    )
  }

  function closeActionEditor() {
    if (actionMutation.isPending) return
    setAction(null)
    setReason('')
    setReasonError('')
    actionMutation.reset()
  }

  function handleActionSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const normalizedReason = reason.trim()
    if (!action || !operation || !actionAllowed) return
    if (normalizedReason === '') {
      setReasonError(t('Enter a reason before continuing.'))
      reasonRef.current?.focus()
      return
    }
    setReasonError('')
    actionMutation.mutate({
      action,
      operationId: operation.id,
      reason: normalizedReason,
    })
  }

  return (
    <Sheet
      open={props.open}
      onOpenChange={(open) => {
        if (!open) closeActionEditor()
        props.onOpenChange(open)
      }}
    >
      <SheetContent
        className={sideDrawerContentClassName(
          'channel-routing-touch-surface max-w-none max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11 sm:!max-w-3xl'
        )}
      >
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle className='flex items-center gap-2'>
            <HugeiconsIcon
              icon={Activity01Icon}
              className='size-4'
              strokeWidth={2}
              aria-hidden='true'
            />
            {t('Operation #{{id}}', { id: props.operationId })}
          </SheetTitle>
          <SheetDescription>
            {operation
              ? t('{{type}}, started {{time}}', {
                  type: t(channelRoutingOperationTypeLabel(operation.type)),
                  time: format.timestamp(operation.created_time_ms),
                })
              : t('Persistent channel routing operation details')}
          </SheetDescription>
        </SheetHeader>

        <div className={sideDrawerFormClassName('gap-5')}>
          {query.isLoading ? <ChannelRoutingLoadingState rows={6} /> : null}
          {query.isError ? (
            <ChannelRoutingErrorState
              error={query.error}
              onRetry={() => void query.refetch()}
            />
          ) : null}
          {operation ? (
            <>
              <div className='flex flex-wrap items-center gap-2'>
                <ChannelRoutingStatusBadge status={displayStatus} />
                <Badge variant='outline'>
                  {t(channelRoutingOperationTypeLabel(operation.type))}
                </Badge>
              </div>

              {operation.needs_attention ? (
                <Alert>
                  <AlertTitle>{t('Operator attention required')}</AlertTitle>
                  <AlertDescription>
                    {t(
                      'Review the summary and last error before retrying or resolving the related control issue.'
                    )}
                  </AlertDescription>
                </Alert>
              ) : null}

              {canRetry || canCancel ? (
                <section className='flex flex-col gap-3 border-b pb-5'>
                  <div className='flex flex-wrap gap-2'>
                    {canRetry ? (
                      <Button
                        type='button'
                        size='sm'
                        variant='outline'
                        disabled={actionMutation.isPending}
                        onClick={() => {
                          setAction('retry')
                          setReason('')
                          setReasonError('')
                          actionMutation.reset()
                        }}
                      >
                        <HugeiconsIcon
                          icon={RefreshIcon}
                          data-icon='inline-start'
                          strokeWidth={2}
                          aria-hidden='true'
                        />
                        {t('Retry operation')}
                      </Button>
                    ) : null}
                    {canCancel ? (
                      <Button
                        type='button'
                        size='sm'
                        variant='outline'
                        disabled={actionMutation.isPending}
                        onClick={() => {
                          setAction('cancel')
                          setReason('')
                          setReasonError('')
                          actionMutation.reset()
                        }}
                      >
                        <HugeiconsIcon
                          icon={Cancel01Icon}
                          data-icon='inline-start'
                          strokeWidth={2}
                          aria-hidden='true'
                        />
                        {t('Cancel operation')}
                      </Button>
                    ) : null}
                  </div>

                  {action && actionAllowed ? (
                    <form
                      className='bg-muted/30 flex flex-col gap-3 rounded-lg border p-3'
                      onSubmit={handleActionSubmit}
                    >
                      <div className='flex flex-col gap-2'>
                        <Label htmlFor={reasonId}>
                          {action === 'retry'
                            ? t('Retry reason')
                            : t('Cancellation reason')}
                        </Label>
                        <Textarea
                          ref={reasonRef}
                          id={reasonId}
                          value={reason}
                          maxLength={512}
                          rows={3}
                          aria-invalid={reasonError !== ''}
                          aria-describedby={
                            reasonError
                              ? `${reasonHelpId} ${reasonErrorId}`
                              : reasonHelpId
                          }
                          placeholder={t(
                            'Record the operational reason for this action'
                          )}
                          disabled={actionMutation.isPending}
                          onChange={(event) => {
                            setReason(event.target.value)
                            if (reasonError) setReasonError('')
                          }}
                        />
                        <p
                          id={reasonHelpId}
                          className='text-muted-foreground text-xs'
                        >
                          {t(
                            'This reason is stored in the immutable control audit.'
                          )}
                        </p>
                        {reasonError ? (
                          <p
                            id={reasonErrorId}
                            className='text-destructive text-xs'
                            role='alert'
                          >
                            {reasonError}
                          </p>
                        ) : null}
                      </div>
                      {actionError ? (
                        <Alert variant='destructive'>
                          <AlertTitle>
                            {t('Operation action failed')}
                          </AlertTitle>
                          <AlertDescription>{t(actionError)}</AlertDescription>
                        </Alert>
                      ) : null}
                      <div className='flex flex-wrap justify-end gap-2'>
                        <Button
                          type='button'
                          size='sm'
                          variant='ghost'
                          disabled={actionMutation.isPending}
                          onClick={closeActionEditor}
                        >
                          {t('Keep operation unchanged')}
                        </Button>
                        <Button
                          type='submit'
                          size='sm'
                          variant={
                            action === 'cancel' ? 'destructive' : 'default'
                          }
                          disabled={actionMutation.isPending}
                        >
                          {actionButtonIcon}
                          {action === 'retry'
                            ? t('Create retry')
                            : t('Confirm cancellation')}
                        </Button>
                      </div>
                    </form>
                  ) : null}
                </section>
              ) : null}

              <OperationFacts operation={operation} />

              <section className='flex flex-col gap-2 border-t pt-4'>
                <h3 className='text-sm font-semibold'>{t('Summary')}</h3>
                <p className='text-muted-foreground text-sm break-words'>
                  {operation.summary || t('No summary recorded')}
                </p>
              </section>

              <section className='flex flex-col gap-2 border-t pt-4'>
                <h3 className='text-sm font-semibold'>{t('Reason')}</h3>
                <p className='text-muted-foreground text-sm break-words'>
                  {operation.reason || t('Not available')}
                </p>
              </section>

              {operation.last_error ? (
                <Alert variant='destructive'>
                  <AlertTitle>{t('Last error')}</AlertTitle>
                  <AlertDescription className='break-words'>
                    {operation.last_error}
                  </AlertDescription>
                </Alert>
              ) : null}

              <ChannelRoutingOperationResultSection operation={operation} />

              {canAuditExport && auditExportId ? (
                <Button
                  type='button'
                  variant='outline'
                  className='w-fit'
                  disabled={downloadExport.isPending}
                  onClick={() => downloadExport.mutate(auditExportId)}
                >
                  {downloadExport.isPending ? (
                    <Spinner data-icon='inline-start' />
                  ) : (
                    <HugeiconsIcon
                      icon={Download01Icon}
                      data-icon='inline-start'
                      strokeWidth={2}
                      aria-hidden='true'
                    />
                  )}
                  {downloadExport.isPending
                    ? t('Downloading')
                    : t('Download JSON')}
                </Button>
              ) : null}

              {canAuditExport ? (
                <ChannelRoutingOperationTechnicalSection
                  operationId={operation.id}
                  sheetOpen={props.open}
                />
              ) : null}
            </>
          ) : null}
        </div>
      </SheetContent>
    </Sheet>
  )
}

function OperationFacts(props: { operation: RoutingOperation }) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const operation = props.operation
  const facts: Array<{ label: string; value: ReactNode }> = [
    {
      label: t('Subject'),
      value: `${operation.subject_type} #${operation.subject_id}`,
    },
    {
      label: t('Pool'),
      value: operation.pool_id > 0 ? `#${operation.pool_id}` : t('All'),
    },
    { label: t('Actor'), value: `#${operation.actor_id}` },
    {
      label: t('Source'),
      value: t(
        channelRoutingOperationSourceLabel(operation.source || 'system')
      ),
    },
    { label: t('Attempts'), value: format.number(operation.attempts) },
    {
      label: t('Retention'),
      value: operation.retention_category
        ? t(channelRoutingOperationRetentionLabel(operation.retention_category))
        : t('Not available'),
    },
    { label: t('Started'), value: format.timestamp(operation.created_time_ms) },
    { label: t('Updated'), value: format.timestamp(operation.updated_time_ms) },
    {
      label: t('Completed'),
      value:
        operation.completed_time_ms > 0
          ? format.timestamp(operation.completed_time_ms)
          : t('Pending'),
    },
  ]
  if (operation.next_retry_ms > 0) {
    facts.push({
      label: t('Next retry'),
      value: format.timestamp(operation.next_retry_ms),
    })
  }
  if (operation.expected_revision > 0) {
    facts.push({
      label: t('Expected revision'),
      value: `r${operation.expected_revision}`,
    })
  }
  if (operation.expected_activation_id > 0) {
    facts.push({
      label: t('Expected activation'),
      value: `#${operation.expected_activation_id}`,
    })
  }
  if (operation.result_revision > 0) {
    facts.push({
      label: t('Result revision'),
      value: `r${operation.result_revision}`,
    })
  }
  if (operation.result_activation_id > 0) {
    facts.push({
      label: t('Result activation'),
      value: `#${operation.result_activation_id}`,
    })
  }
  if (operation.terminal_actor_id) {
    facts.push({
      label: t('Terminal actor'),
      value: `#${operation.terminal_actor_id}`,
    })
  }

  return (
    <>
      <dl className='grid grid-cols-2 gap-x-6 gap-y-4 text-sm sm:grid-cols-3'>
        {facts.map((fact) => (
          <div key={fact.label} className='min-w-0'>
            <dt className='text-muted-foreground text-xs'>{fact.label}</dt>
            <dd className='mt-1 font-medium break-words'>{fact.value}</dd>
          </div>
        ))}
      </dl>
      {operation.correlation_id ||
      operation.parent_operation_id ||
      operation.retry_of_operation_id ? (
        <section className='flex flex-col gap-3 border-t pt-4'>
          <h3 className='text-sm font-semibold'>{t('Operation chain')}</h3>
          <dl className='grid grid-cols-1 gap-x-6 gap-y-3 text-sm sm:grid-cols-3'>
            {operation.correlation_id ? (
              <div className='min-w-0 sm:col-span-3'>
                <dt className='text-muted-foreground text-xs'>
                  {t('Correlation ID')}
                </dt>
                <dd className='mt-1 font-mono text-xs break-all'>
                  {operation.correlation_id}
                </dd>
              </div>
            ) : null}
            {operation.parent_operation_id ? (
              <div>
                <dt className='text-muted-foreground text-xs'>
                  {t('Parent operation')}
                </dt>
                <dd className='mt-1 font-medium'>
                  #{operation.parent_operation_id}
                </dd>
              </div>
            ) : null}
            {operation.retry_of_operation_id ? (
              <div>
                <dt className='text-muted-foreground text-xs'>
                  {t('Retry of operation')}
                </dt>
                <dd className='mt-1 font-medium'>
                  #{operation.retry_of_operation_id}
                </dd>
              </div>
            ) : null}
            {operation.retry_sequence ? (
              <div>
                <dt className='text-muted-foreground text-xs'>
                  {t('Retry sequence')}
                </dt>
                <dd className='mt-1 font-medium'>
                  {format.number(operation.retry_sequence)}
                </dd>
              </div>
            ) : null}
          </dl>
        </section>
      ) : null}
    </>
  )
}

function invalidateOperationViews(
  queryClient: QueryClient,
  previousOperationId: number,
  nextOperationId: number
) {
  void Promise.all([
    queryClient.invalidateQueries({
      queryKey: channelRoutingQueryKeys.operationsRoot(),
    }),
    queryClient.invalidateQueries({
      queryKey: channelRoutingQueryKeys.controlAuditsRoot(),
    }),
    queryClient.invalidateQueries({
      queryKey: channelRoutingQueryKeys.operation(previousOperationId),
    }),
    queryClient.invalidateQueries({
      queryKey: channelRoutingQueryKeys.operation(nextOperationId),
    }),
  ])
}

function operationActionAllowed(
  action: OperationAction | null,
  canRetry: boolean,
  canCancel: boolean
): boolean {
  if (action === 'retry') return canRetry
  if (action === 'cancel') return canCancel
  return false
}
