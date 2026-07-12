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

import { useQuery } from '@tanstack/react-query'
import { RefreshCw, ShieldAlert, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { getChannelRoutingGroupErrorBudget } from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import type { ErrorBudgetBurn, ErrorBudgetWindow } from '../types'

type ErrorBudgetWindowItem = {
  label: string
  value: ErrorBudgetWindow
}

function errorBudgetReasonLabel(
  reason: ErrorBudgetBurn['reason'],
  translate: (key: string) => string
): string {
  switch (reason) {
    case 'within_multi_window_budget':
      return translate('Within multi-window budget')
    case 'fast_multi_window_burn':
      return translate('Fast multi-window burn')
    case 'slow_multi_window_burn':
      return translate('Slow multi-window burn')
    case 'revision_isolation_unavailable':
      return translate('Revision isolation unavailable')
    default:
      return translate('Insufficient reliability volume')
  }
}

function errorBudgetObservedCounts(window: ErrorBudgetWindow): {
  requests: number
  failures: number
} {
  if (window.revision_isolated) {
    return {
      requests: window.request_count,
      failures: window.failure_count,
    }
  }
  return {
    requests: window.unisolated_request_count,
    failures: window.unisolated_failure_count,
  }
}

export function ChannelRoutingErrorBudgetSection(props: {
  poolId: number
  snapshotRevision: number
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.errorBudget(props.poolId),
    queryFn: () => getChannelRoutingGroupErrorBudget(props.poolId),
  })
  const current = query.data?.current
  const persisted = query.data?.persisted
  const stale =
    query.data != null &&
    query.data.snapshot_revision !== props.snapshotRevision
  const persistedStale =
    persisted != null &&
    current != null &&
    persisted.policy_revision !== current.policy_revision
  const windows: ErrorBudgetWindowItem[] = current
    ? [
        { label: t('Fast burn (5m)'), value: current.fast_short },
        { label: t('Fast burn (1h)'), value: current.fast_long },
        { label: t('Slow burn (30m)'), value: current.slow_short },
        { label: t('Slow burn (6h)'), value: current.slow_long },
      ]
    : []

  return (
    <section
      className='space-y-3 border-t pt-4'
      aria-labelledby='error-budget-title'
    >
      <div className='flex items-start justify-between gap-3'>
        <div className='min-w-0'>
          <h2 id='error-budget-title' className='text-sm font-semibold'>
            {t('Error budget')}
          </h2>
          {current ? (
            <p className='text-muted-foreground mt-1 text-xs text-pretty'>
              {errorBudgetReasonLabel(current.reason, t)}
            </p>
          ) : null}
        </div>
        <div className='flex flex-none items-center gap-2'>
          {current ? (
            <ChannelRoutingStatusBadge status={current.status} />
          ) : null}
          {stale ? <ChannelRoutingStatusBadge status='stale' /> : null}
          <Button
            type='button'
            size='icon-sm'
            variant='outline'
            className='size-11 sm:size-7'
            aria-label={t('Refresh error budget')}
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            <RefreshCw
              aria-hidden='true'
              className={
                query.isFetching
                  ? 'animate-spin motion-reduce:animate-none'
                  : undefined
              }
            />
          </Button>
        </div>
      </div>

      {query.isLoading ? (
        <div className='space-y-2' aria-busy='true' aria-live='polite'>
          <Skeleton className='h-16 w-full' />
          {Array.from({ length: 4 }, (_, index) => (
            <Skeleton
              key={`error-budget-window-${index}`}
              className='h-12 w-full'
            />
          ))}
        </div>
      ) : null}

      {query.isError ? (
        <Alert variant='destructive'>
          <TriangleAlert aria-hidden='true' />
          <AlertTitle>{t('Could not load the error budget')}</AlertTitle>
          <AlertDescription className='space-y-2'>
            <p>{t('The latest SLO burn evaluation is unavailable.')}</p>
            <Button
              type='button'
              size='sm'
              variant='outline'
              className='min-h-11 sm:min-h-7'
              onClick={() => void query.refetch()}
            >
              <RefreshCw aria-hidden='true' />
              {t('Retry')}
            </Button>
          </AlertDescription>
        </Alert>
      ) : null}

      {current && query.data ? (
        <>
          <dl className='bg-border grid grid-cols-2 gap-px overflow-hidden rounded-lg border lg:grid-cols-4'>
            {[
              [t('Policy revision'), `r${current.policy_revision}`],
              [
                t('Availability target'),
                format.percent(current.availability_target),
              ],
              [t('Error budget'), format.percent(current.error_budget)],
              [t('Evaluated'), format.timestamp(current.evaluated_at_ms)],
            ].map(([label, value]) => (
              <div key={label} className='bg-background min-w-0 p-3'>
                <dt className='text-muted-foreground text-xs'>{label}</dt>
                <dd className='mt-1 text-sm font-semibold break-words'>
                  {value}
                </dd>
              </div>
            ))}
          </dl>

          <div className='text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-2 text-xs'>
            <span className='inline-flex items-center gap-2'>
              {t('Persisted state')}:
              {persisted ? (
                <ChannelRoutingStatusBadge
                  status={persisted.evaluation.status}
                  className='max-w-full'
                />
              ) : (
                <span>{t('Not available')}</span>
              )}
            </span>
            {persisted ? (
              <>
                <span>
                  {t('Stored revision')}: r{persisted.policy_revision}
                </span>
                <span>
                  {t('Last changed')}:{' '}
                  {format.timestamp(persisted.last_changed_at_ms)}
                </span>
              </>
            ) : null}
            {persistedStale ? (
              <ChannelRoutingStatusBadge status='stale' />
            ) : null}
          </div>

          {stale ? (
            <Alert role='status'>
              <TriangleAlert aria-hidden='true' />
              <AlertTitle>{t('Error budget snapshot is stale')}</AlertTitle>
              <AlertDescription>
                {t(
                  'Error budget snapshot r{{actual}} does not match group snapshot r{{expected}}.',
                  {
                    actual: query.data.snapshot_revision,
                    expected: props.snapshotRevision,
                  }
                )}
              </AlertDescription>
            </Alert>
          ) : null}

          {current.reason === 'revision_isolation_unavailable' ? (
            <Alert role='status'>
              <ShieldAlert aria-hidden='true' />
              <AlertTitle>{t('Revision isolation unavailable')}</AlertTitle>
              <AlertDescription>
                {t(
                  'This evaluation fails closed because the reliability data mixes policy revisions. It will remain insufficient until revision-isolated samples are available.'
                )}
              </AlertDescription>
            </Alert>
          ) : null}

          <div className='hidden overflow-hidden rounded-lg border xl:block'>
            <Table scrollAreaLabel={t('Error budget windows table')}>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Window')}</TableHead>
                  <TableHead className='text-right'>{t('Burn rate')}</TableHead>
                  <TableHead className='text-right'>
                    {t('Error rate')}
                  </TableHead>
                  <TableHead className='text-right'>
                    {t('Requests / failures')}
                  </TableHead>
                  <TableHead>{t('Samples')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {windows.map((window) => {
                  const counts = errorBudgetObservedCounts(window.value)
                  return (
                    <TableRow key={window.label}>
                      <TableCell className='font-medium'>
                        {window.label}
                      </TableCell>
                      <TableCell className='text-right font-medium'>
                        {format.number(window.value.burn_rate)}x
                      </TableCell>
                      <TableCell className='text-right'>
                        {format.percent(window.value.error_rate)}
                      </TableCell>
                      <TableCell className='text-right'>
                        {format.number(counts.requests)} /{' '}
                        {format.number(counts.failures)}
                      </TableCell>
                      <TableCell>
                        <div className='flex flex-wrap items-center gap-2'>
                          <ChannelRoutingStatusBadge
                            status={
                              window.value.sufficient
                                ? 'healthy'
                                : 'insufficient_data'
                            }
                            label={
                              window.value.sufficient
                                ? t('Sufficient samples')
                                : t('Insufficient samples')
                            }
                          />
                          <span className='text-muted-foreground text-xs'>
                            {t('Minimum {{count}} requests', {
                              count: format.number(window.value.minimum_volume),
                            })}
                          </span>
                        </div>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </div>

          <div className='divide-y rounded-lg border xl:hidden'>
            {windows.map((window) => {
              const counts = errorBudgetObservedCounts(window.value)
              return (
                <article key={window.label} className='p-3'>
                  <div className='flex items-start justify-between gap-3'>
                    <h3 className='text-sm font-medium'>{window.label}</h3>
                    <ChannelRoutingStatusBadge
                      status={
                        window.value.sufficient
                          ? 'healthy'
                          : 'insufficient_data'
                      }
                      label={
                        window.value.sufficient
                          ? t('Sufficient samples')
                          : t('Insufficient samples')
                      }
                    />
                  </div>
                  <dl className='mt-3 grid grid-cols-2 gap-3 text-xs'>
                    <div>
                      <dt className='text-muted-foreground'>
                        {t('Burn rate')}
                      </dt>
                      <dd className='mt-1 font-medium'>
                        {format.number(window.value.burn_rate)}x
                      </dd>
                    </div>
                    <div>
                      <dt className='text-muted-foreground'>
                        {t('Error rate')}
                      </dt>
                      <dd className='mt-1 font-medium'>
                        {format.percent(window.value.error_rate)}
                      </dd>
                    </div>
                    <div>
                      <dt className='text-muted-foreground'>{t('Requests')}</dt>
                      <dd className='mt-1 font-medium'>
                        {format.number(counts.requests)}
                      </dd>
                    </div>
                    <div>
                      <dt className='text-muted-foreground'>{t('Failures')}</dt>
                      <dd className='mt-1 font-medium'>
                        {format.number(counts.failures)}
                      </dd>
                    </div>
                  </dl>
                  <div className='text-muted-foreground mt-3 flex flex-wrap gap-x-3 gap-y-1 text-xs'>
                    <span>
                      {t('Minimum {{count}} requests', {
                        count: format.number(window.value.minimum_volume),
                      })}
                    </span>
                    <span>
                      {window.value.revision_isolated
                        ? t('Revision isolated')
                        : t('Mixed revisions')}
                    </span>
                  </div>
                </article>
              )
            })}
          </div>

          <p className='text-muted-foreground text-xs'>
            {t('Snapshot r{{revision}} · built {{time}}', {
              revision: query.data.snapshot_revision,
              time: format.timestamp(query.data.snapshot_built_at),
            })}
          </p>
        </>
      ) : null}
    </section>
  )
}
