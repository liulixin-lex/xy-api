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
import { Activity01Icon, Download01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  sideDrawerContentClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import {
  downloadChannelRoutingAuditExport,
  getChannelRoutingOperation,
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
  channelRoutingOperationBreakerResetResult,
  channelRoutingOperationDisplayStatus,
  channelRoutingOperationIsActive,
  channelRoutingOperationTypeLabel,
} from '../lib/operations'

export function ChannelRoutingOperationSheet(props: {
  operationId: number | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const user = useAuthStore((state) => state.auth.user)
  const canAuditExport = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.AUDIT_EXPORT
  )
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.operation(props.operationId ?? 0),
    queryFn: () => getChannelRoutingOperation(props.operationId ?? 0),
    enabled: props.open && props.operationId != null,
    refetchInterval: (operationQuery) =>
      channelRoutingOperationIsActive(operationQuery.state.data)
        ? 3_000
        : false,
  })
  const downloadExport = useMutation({
    mutationFn: downloadChannelRoutingAuditExport,
    onSuccess: () => toast.success(t('Audit export downloaded')),
    onError: () => toast.error(t('Could not download the audit export.')),
  })
  const displayStatus = query.data
    ? channelRoutingOperationDisplayStatus(query.data)
    : ''
  const auditExportId = query.data
    ? channelRoutingOperationAuditExportId(query.data)
    : null
  const breakerResetResult = query.data
    ? channelRoutingOperationBreakerResetResult(query.data)
    : null

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
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
            {query.data
              ? t('{{type}} · started {{time}}', {
                  type: query.data.type,
                  time: format.timestamp(query.data.created_time_ms),
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
          {query.data ? (
            <>
              <div className='flex flex-wrap items-center gap-2'>
                <ChannelRoutingStatusBadge status={displayStatus} />
                <ChannelRoutingStatusBadge
                  status={query.data.type}
                  label={t(channelRoutingOperationTypeLabel(query.data.type))}
                />
              </div>

              <dl className='grid grid-cols-2 gap-x-6 gap-y-4 text-sm sm:grid-cols-3'>
                {[
                  [
                    t('Subject'),
                    `${query.data.subject_type} #${query.data.subject_id}`,
                  ],
                  [
                    t('Pool'),
                    query.data.pool_id > 0
                      ? `#${query.data.pool_id}`
                      : t('All'),
                  ],
                  [t('Actor'), `#${query.data.actor_id}`],
                  [t('Expected revision'), `r${query.data.expected_revision}`],
                  [t('Attempts'), format.number(query.data.attempts)],
                  [
                    t('Completed'),
                    query.data.completed_time_ms > 0
                      ? format.timestamp(query.data.completed_time_ms)
                      : t('Pending'),
                  ],
                  [
                    t('Result revision'),
                    query.data.result_revision > 0
                      ? `r${query.data.result_revision}`
                      : t('Not available'),
                  ],
                  [
                    t('Result activation'),
                    query.data.result_activation_id > 0
                      ? `#${query.data.result_activation_id}`
                      : t('Not available'),
                  ],
                  [
                    t('Evaluation hash'),
                    format.shortHash(query.data.evaluation_hash),
                  ],
                ].map(([label, value]) => (
                  <div key={label} className='min-w-0'>
                    <dt className='text-muted-foreground text-xs'>{label}</dt>
                    <dd className='mt-1 font-medium break-words'>{value}</dd>
                  </div>
                ))}
              </dl>

              {displayStatus === 'partial' ? (
                <p
                  role='status'
                  className='border-warning/30 bg-warning/10 text-warning-foreground rounded-lg border p-3 text-sm'
                >
                  {t(
                    'This operation completed with partial results. Review its result and error details.'
                  )}
                </p>
              ) : null}

              {canAuditExport && auditExportId ? (
                <Button
                  type='button'
                  variant='outline'
                  className='w-fit'
                  disabled={downloadExport.isPending}
                  onClick={() => downloadExport.mutate(auditExportId)}
                >
                  <HugeiconsIcon
                    icon={Download01Icon}
                    data-icon='inline-start'
                    strokeWidth={2}
                    aria-hidden='true'
                  />
                  {downloadExport.isPending
                    ? t('Downloading')
                    : t('Download JSON')}
                </Button>
              ) : null}

              <section className='space-y-2 border-t pt-4'>
                <h3 className='text-sm font-semibold'>{t('Reason')}</h3>
                <p className='text-muted-foreground text-sm break-words'>
                  {query.data.reason || t('Not available')}
                </p>
              </section>

              {query.data.last_error ? (
                <section className='border-destructive/30 bg-destructive/5 text-destructive space-y-2 rounded-lg border p-3'>
                  <h3 className='text-sm font-semibold'>{t('Last error')}</h3>
                  <p className='text-sm break-words'>{query.data.last_error}</p>
                </section>
              ) : null}

              {breakerResetResult && (
                <section className='space-y-3 border-t pt-4'>
                  <h3 className='text-sm font-semibold'>
                    {t('Breaker reset result')}
                  </h3>
                  <dl className='grid grid-cols-2 gap-x-6 gap-y-3 text-sm sm:grid-cols-3'>
                    <div>
                      <dt className='text-muted-foreground text-xs'>
                        {t('Scope')}
                      </dt>
                      <dd className='mt-1 font-medium'>
                        {breakerResetResult.scope === 'member'
                          ? t('Member / model')
                          : t('Endpoint / region')}
                      </dd>
                    </div>
                    <div>
                      <dt className='text-muted-foreground text-xs'>
                        {t('Generation')}
                      </dt>
                      <dd className='mt-1 font-medium'>
                        {format.number(breakerResetResult.generation)}
                      </dd>
                    </div>
                    <div>
                      <dt className='text-muted-foreground text-xs'>
                        {t('Outbox')}
                      </dt>
                      <dd className='mt-1 font-medium'>
                        #{breakerResetResult.outbox_id}
                      </dd>
                    </div>
                  </dl>
                  <div className='border-t pt-3'>
                    <h4 className='text-muted-foreground text-xs font-medium'>
                      {t('Normalized target')}
                    </h4>
                    <dl className='mt-2 grid grid-cols-2 gap-x-6 gap-y-3 text-sm sm:grid-cols-3'>
                      {breakerResetResult.target.scope === 'member' ? (
                        <>
                          <div>
                            <dt className='text-muted-foreground text-xs'>
                              {t('Pool')}
                            </dt>
                            <dd className='mt-1 font-medium'>
                              #{breakerResetResult.target.pool_id}
                            </dd>
                          </div>
                          <div>
                            <dt className='text-muted-foreground text-xs'>
                              {t('Member')}
                            </dt>
                            <dd className='mt-1 font-medium'>
                              #{breakerResetResult.target.member_id}
                            </dd>
                          </div>
                          <div>
                            <dt className='text-muted-foreground text-xs'>
                              {t('Channel')}
                            </dt>
                            <dd className='mt-1 font-medium'>
                              #{breakerResetResult.target.channel_id}
                            </dd>
                          </div>
                          <div className='min-w-0'>
                            <dt className='text-muted-foreground text-xs'>
                              {t('Model')}
                            </dt>
                            <dd className='mt-1 font-medium break-words'>
                              {breakerResetResult.target.model_name}
                            </dd>
                          </div>
                          <div className='min-w-0'>
                            <dt className='text-muted-foreground text-xs'>
                              {t('Group')}
                            </dt>
                            <dd className='mt-1 font-medium break-words'>
                              {breakerResetResult.target.group_name}
                            </dd>
                          </div>
                          <div>
                            <dt className='text-muted-foreground text-xs'>
                              {t('Key index')}
                            </dt>
                            <dd className='mt-1 font-medium'>
                              {format.number(
                                breakerResetResult.target.api_key_index
                              )}
                            </dd>
                          </div>
                        </>
                      ) : (
                        <>
                          <div className='col-span-2 min-w-0 sm:col-span-3'>
                            <dt className='text-muted-foreground text-xs'>
                              {t('Endpoint authority')}
                            </dt>
                            <dd className='mt-1 font-mono text-xs break-all'>
                              {breakerResetResult.target.endpoint_authority}
                            </dd>
                          </div>
                          <div className='min-w-0'>
                            <dt className='text-muted-foreground text-xs'>
                              {t('Host')}
                            </dt>
                            <dd className='mt-1 font-medium break-all'>
                              {breakerResetResult.target.endpoint_host}
                            </dd>
                          </div>
                          <div className='min-w-0'>
                            <dt className='text-muted-foreground text-xs'>
                              {t('Region')}
                            </dt>
                            <dd className='mt-1 font-medium break-words'>
                              {breakerResetResult.target.region}
                            </dd>
                          </div>
                        </>
                      )}
                    </dl>
                  </div>
                </section>
              )}

              {!breakerResetResult && query.data.result != null && (
                <section className='space-y-2 border-t pt-4'>
                  <h3 className='text-sm font-semibold'>
                    {t('Operation result')}
                  </h3>
                  <pre className='bg-muted/40 max-h-96 overflow-auto rounded-lg border p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap'>
                    {JSON.stringify(query.data.result, null, 2)}
                  </pre>
                </section>
              )}
            </>
          ) : null}
        </div>
      </SheetContent>
    </Sheet>
  )
}
