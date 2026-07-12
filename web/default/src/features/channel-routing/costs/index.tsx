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

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getRouteApi, Link } from '@tanstack/react-router'
import {
  CheckCircle2,
  Clock3,
  Eye,
  RefreshCw,
  Search,
  TriangleAlert,
  X,
} from 'lucide-react'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import {
  createChannelRoutingIdempotencyKey,
  getChannelRoutingOperation,
  listChannelRoutingCosts,
  syncChannelRoutingCosts,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingIdentityText } from '../components/identity-text'
import { ChannelRoutingPageFrame } from '../components/page-frame'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
} from '../components/page-state'
import { ChannelRoutingPagination } from '../components/pagination-bar'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import {
  channelRoutingOperationDisplayStatus,
  channelRoutingOperationIsActive,
} from '../lib/operations'
import type {
  ChannelRoutingCostSyncResult,
  CostSnapshotSummary,
} from '../types'
import { ChannelRoutingCostDetailsSheet } from './cost-details-sheet'

const route = getRouteApi('/_authenticated/channel-routing/$section')

function hasKnownCostSemantics(cost: CostSnapshotSummary): boolean {
  if (!cost.known) return false
  if (cost.expression_pricing) return Boolean(cost.billing_mode?.trim())
  return (
    typeof cost.display_rate === 'number' &&
    Number.isFinite(cost.display_rate) &&
    Boolean(cost.currency?.trim()) &&
    Boolean(cost.unit?.trim())
  )
}

function ChannelRoutingCostValue(props: { cost: CostSnapshotSummary }) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const cost = props.cost
  if (!hasKnownCostSemantics(cost)) return <>{t('Unknown')}</>

  if (cost.expression_pricing) return <>{t('Expression pricing')}</>

  const currency = cost.currency || ''
  const unit = cost.unit || ''
  if (
    typeof cost.display_rate === 'number' &&
    Number.isFinite(cost.display_rate)
  ) {
    return (
      <span className='inline-flex flex-col items-end'>
        <span>
          {t('{{currency}} {{cost}}', {
            currency,
            cost: format.cost(cost.display_rate),
          })}
        </span>
        <span className='text-muted-foreground text-xs font-normal'>
          {unit}
        </span>
      </span>
    )
  }
  return <>{t('Unknown')}</>
}

export function ChannelRoutingCostsPage() {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const queryClient = useQueryClient()
  const user = useAuthStore((state) => state.auth.user)
  const canOperate = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )
  const costSyncKeyRef = useRef<string | null>(null)
  const refreshedCostSyncRef = useRef<number | null>(null)
  const [selectedCost, setSelectedCost] = useState<CostSnapshotSummary | null>(
    null
  )
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const page = search.page ?? 1
  const pageSize = search.pageSize ?? 20
  const known = search.known ?? 'all'
  const queryParams = {
    page,
    page_size: pageSize,
    group: search.group || undefined,
    model: search.model || undefined,
    known: known === 'all' ? undefined : known,
  }
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.costs(queryParams),
    queryFn: () => listChannelRoutingCosts(queryParams),
    placeholderData: (previous) => previous,
  })
  const costSync = useMutation({
    mutationFn: () => {
      costSyncKeyRef.current ??= createChannelRoutingIdempotencyKey('cost-sync')
      return syncChannelRoutingCosts(costSyncKeyRef.current)
    },
    onSuccess: async (operation) => {
      costSyncKeyRef.current = null
      queryClient.setQueryData(
        channelRoutingQueryKeys.operation(operation.id),
        operation
      )
      await queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.operationsRoot(),
      })
      toast.success(t('Cost sync queued'))
    },
    onError: () => {
      toast.error(t('Could not queue the cost sync. Try again.'))
    },
  })
  const costSyncOperationId = costSync.data?.id ?? null
  const costSyncOperationQuery = useQuery({
    queryKey: channelRoutingQueryKeys.operation(costSyncOperationId ?? 0),
    queryFn: () =>
      getChannelRoutingOperation<ChannelRoutingCostSyncResult>(
        costSyncOperationId ?? 0
      ),
    enabled: costSyncOperationId != null,
    refetchInterval: (operationQuery) =>
      channelRoutingOperationIsActive(operationQuery.state.data)
        ? 3_000
        : false,
  })
  const trackedCostSync = costSyncOperationQuery.data ?? costSync.data
  const costSyncStatus = trackedCostSync
    ? channelRoutingOperationDisplayStatus(trackedCostSync)
    : ''
  const costSyncActive =
    costSync.isPending || channelRoutingOperationIsActive(trackedCostSync)
  const costSyncResult = trackedCostSync?.result
  const costSyncSummary = costSyncResult?.summary
  const systemTaskId =
    trackedCostSync?.system_task_id || costSyncResult?.system_task_id
  let CostSyncIcon = Clock3
  if (costSyncStatus === 'succeeded') CostSyncIcon = CheckCircle2
  if (costSyncStatus === 'failed' || costSyncStatus === 'partial') {
    CostSyncIcon = TriangleAlert
  }
  let costSyncButtonLabel = t('Sync costs')
  if (costSync.isPending) {
    costSyncButtonLabel = t('Queueing sync')
  } else if (channelRoutingOperationIsActive(trackedCostSync)) {
    costSyncButtonLabel = t('Sync in progress')
  }
  const costSyncSummaryItems: Array<[string, number | undefined]> =
    costSyncSummary
      ? [
          [t('Accounts'), costSyncSummary.accounts],
          [t('Snapshots'), costSyncSummary.snapshots],
          [t('Metrics'), costSyncSummary.metrics],
          [t('Errors'), costSyncSummary.errors],
        ]
      : []

  useEffect(() => {
    if (
      !trackedCostSync ||
      channelRoutingOperationIsActive(trackedCostSync) ||
      refreshedCostSyncRef.current === trackedCostSync.id
    ) {
      return
    }
    refreshedCostSyncRef.current = trackedCostSync.id
    void Promise.all([
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.overview(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.channelsRoot(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.costsRoot(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.operationsRoot(),
      }),
    ])
  }, [queryClient, trackedCostSync])

  const updateSearch = (
    patch: Record<string, string | number | boolean | undefined>
  ) => {
    void navigate({
      search: (previous) => ({ ...previous, ...patch }),
      replace: true,
    })
  }
  const handleFilters = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    updateSearch({
      page: 1,
      group: String(form.get('group') ?? '').trim(),
      model: String(form.get('model') ?? '').trim(),
    })
  }

  return (
    <>
      <ChannelRoutingPageFrame
        activeSection='costs'
        title={t('Upstream costs')}
        actions={
          <div className='flex items-center gap-2'>
            <Button
              size='icon-sm'
              variant='outline'
              aria-label={t('Refresh')}
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
            {canOperate ? (
              <Button
                size='sm'
                disabled={costSyncActive}
                onClick={() => costSync.mutate()}
              >
                <RefreshCw
                  aria-hidden='true'
                  className={
                    costSyncActive
                      ? 'animate-spin motion-reduce:animate-none'
                      : undefined
                  }
                />
                {costSyncButtonLabel}
              </Button>
            ) : null}
          </div>
        }
      >
        <div className='space-y-3 pb-2'>
          {trackedCostSync ? (
            <Alert
              role={costSyncStatus === 'failed' ? 'alert' : 'status'}
              variant={costSyncStatus === 'failed' ? 'destructive' : 'default'}
            >
              <CostSyncIcon aria-hidden='true' />
              <AlertTitle className='flex flex-wrap items-center gap-2'>
                <span>{t('Cost sync')}</span>
                <ChannelRoutingStatusBadge status={costSyncStatus} />
              </AlertTitle>
              <AlertDescription className='space-y-2 text-pretty'>
                <div className='flex flex-wrap gap-x-3 gap-y-1'>
                  <span>
                    {t('Operation #{{id}}', { id: trackedCostSync.id })}
                  </span>
                  {systemTaskId ? (
                    <span className='break-all'>
                      {t('System task')}: {systemTaskId}
                    </span>
                  ) : null}
                </div>
                {costSyncStatus === 'partial' ? (
                  <p>
                    {t(
                      'The cost sync completed with partial results. Review the error count before relying on the refreshed costs.'
                    )}
                  </p>
                ) : null}
                {costSyncStatus === 'failed' ? (
                  <p>
                    {trackedCostSync.last_error ||
                      t('The cost sync failed before completion.')}
                  </p>
                ) : null}
                {costSyncSummaryItems.length > 0 ? (
                  <dl className='grid grid-cols-2 gap-2 pt-1 sm:grid-cols-4'>
                    {costSyncSummaryItems.map(([label, value]) => (
                      <div key={label} className='min-w-0'>
                        <dt className='text-xs'>{label}</dt>
                        <dd className='text-foreground mt-0.5 font-medium'>
                          {typeof value === 'number'
                            ? format.number(value)
                            : t('Unknown')}
                        </dd>
                      </div>
                    ))}
                  </dl>
                ) : null}
                {costSyncOperationQuery.isError ? (
                  <div
                    className='flex flex-wrap items-center gap-2'
                    role='alert'
                  >
                    <span>{t('Could not refresh the cost sync status.')}</span>
                    <Button
                      type='button'
                      size='sm'
                      variant='outline'
                      className='min-h-11 sm:min-h-7'
                      onClick={() => void costSyncOperationQuery.refetch()}
                    >
                      {t('Retry')}
                    </Button>
                  </div>
                ) : null}
              </AlertDescription>
            </Alert>
          ) : null}
          <div className='flex flex-wrap items-center gap-2'>
            <form
              key={`${search.group}-${search.model}`}
              className='grid min-w-64 flex-1 gap-2 sm:max-w-2xl sm:grid-cols-[1fr_1fr_auto]'
              onSubmit={handleFilters}
            >
              <Input
                name='group'
                defaultValue={search.group}
                aria-label={t('Group')}
                placeholder={t('Group')}
              />
              <Input
                name='model'
                defaultValue={search.model}
                aria-label={t('Model')}
                placeholder={t('Model')}
              />
              <Button type='submit' size='sm' variant='outline'>
                <Search aria-hidden='true' />
                {t('Apply filters')}
              </Button>
            </form>
            <NativeSelect
              size='sm'
              value={known === 'all' ? 'all' : String(known)}
              aria-label={t('Cost availability')}
              onChange={(event) =>
                updateSearch({
                  page: 1,
                  known:
                    event.target.value === 'all'
                      ? 'all'
                      : event.target.value === 'true',
                })
              }
            >
              <NativeSelectOption value='all'>
                {t('All costs')}
              </NativeSelectOption>
              <NativeSelectOption value='true'>
                {t('Known costs')}
              </NativeSelectOption>
              <NativeSelectOption value='false'>
                {t('Unknown costs')}
              </NativeSelectOption>
            </NativeSelect>
            {search.group || search.model || known !== 'all' ? (
              <Button
                size='sm'
                variant='ghost'
                onClick={() =>
                  updateSearch({ page: 1, group: '', model: '', known: 'all' })
                }
              >
                <X aria-hidden='true' />
                {t('Clear')}
              </Button>
            ) : null}
          </div>

          {query.isLoading ? <ChannelRoutingLoadingState /> : null}
          {query.isError ? (
            <ChannelRoutingErrorState
              error={query.error}
              onRetry={() => void query.refetch()}
            />
          ) : null}
          {query.data && query.data.items.length === 0 ? (
            <ChannelRoutingEmptyState
              title={t('No cost snapshots')}
              description={t(
                'No upstream cost snapshots match the current filters.'
              )}
            />
          ) : null}

          {query.data && query.data.items.length > 0 ? (
            <>
              <div className='hidden overflow-hidden rounded-lg border xl:block'>
                <Table
                  className='min-w-[72rem]'
                  scrollAreaLabel={t('Cost snapshots table')}
                >
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t('Group')}</TableHead>
                      <TableHead>{t('Channel')}</TableHead>
                      <TableHead>{t('Model')}</TableHead>
                      <TableHead>{t('Availability')}</TableHead>
                      <TableHead className='text-right'>
                        {t('Cost value')}
                      </TableHead>
                      <TableHead>{t('Billing Mode')}</TableHead>
                      <TableHead>{t('Confidence / freshness')}</TableHead>
                      <TableHead>{t('Validity')}</TableHead>
                      <TableHead className='w-10'>
                        <span className='sr-only'>{t('Actions')}</span>
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {query.data.items.map((cost) => {
                      const costKnown = hasKnownCostSemantics(cost)
                      return (
                        <TableRow
                          key={`${cost.pool_id}-${cost.member_id}-${cost.model_name}`}
                        >
                          <TableCell>
                            <Link
                              to='/channel-routing/groups/$id'
                              params={{ id: String(cost.pool_id) }}
                              className='font-medium hover:underline'
                            >
                              {cost.group_name}
                            </Link>
                          </TableCell>
                          <TableCell>
                            <div className='font-medium'>
                              {cost.channel_name || `#${cost.channel_id}`}
                            </div>
                            <div className='text-muted-foreground text-xs'>
                              #{cost.channel_id}
                            </div>
                          </TableCell>
                          <TableCell>{cost.model_name}</TableCell>
                          <TableCell>
                            <ChannelRoutingStatusBadge
                              status={costKnown ? 'known' : 'unknown'}
                            />
                          </TableCell>
                          <TableCell className='text-right font-medium'>
                            <ChannelRoutingCostValue cost={cost} />
                          </TableCell>
                          <TableCell>
                            {costKnown
                              ? format.billingMode(cost.billing_mode)
                              : t('Unknown')}
                          </TableCell>
                          <TableCell>
                            <div className='flex flex-wrap gap-1'>
                              <ChannelRoutingStatusBadge
                                status={costKnown ? cost.confidence : 'unknown'}
                              />
                              <ChannelRoutingStatusBadge
                                status={costKnown ? cost.freshness : 'unknown'}
                              />
                            </div>
                          </TableCell>
                          <TableCell className='text-xs'>
                            <div>
                              {t('Effective')}:{' '}
                              {format.timestamp(cost.effective_time)}
                            </div>
                            <div className='text-muted-foreground mt-1'>
                              {t('Expires')}:{' '}
                              {format.timestamp(cost.expires_time)}
                            </div>
                          </TableCell>
                          <TableCell>
                            <Button
                              type='button'
                              size='icon-sm'
                              variant='ghost'
                              aria-label={t('View cost details')}
                              title={t('View cost details')}
                              onClick={() => setSelectedCost(cost)}
                            >
                              <Eye aria-hidden='true' />
                            </Button>
                          </TableCell>
                        </TableRow>
                      )
                    })}
                  </TableBody>
                </Table>
              </div>

              <div className='divide-y rounded-lg border xl:hidden'>
                {query.data.items.map((cost) => {
                  const costKnown = hasKnownCostSemantics(cost)
                  return (
                    <article
                      key={`${cost.pool_id}-${cost.member_id}-${cost.model_name}`}
                      className='p-3'
                    >
                      <div className='flex items-start justify-between gap-3'>
                        <div className='min-w-0'>
                          <h3>
                            <ChannelRoutingIdentityText
                              text={cost.model_name}
                              className='text-sm font-medium'
                            />
                          </h3>
                          <ChannelRoutingIdentityText
                            text={`${cost.group_name} · ${cost.channel_name || `#${cost.channel_id}`}`}
                            className='text-muted-foreground text-xs'
                          />
                        </div>
                        <ChannelRoutingStatusBadge
                          status={costKnown ? 'known' : 'unknown'}
                        />
                      </div>
                      <dl className='mt-3 grid grid-cols-2 gap-3 text-xs sm:grid-cols-4'>
                        <div>
                          <dt className='text-muted-foreground'>
                            {t('Cost value')}
                          </dt>
                          <dd className='mt-1 font-medium'>
                            <ChannelRoutingCostValue cost={cost} />
                          </dd>
                        </div>
                        <div>
                          <dt className='text-muted-foreground'>
                            {t('Billing Mode')}
                          </dt>
                          <dd className='mt-1 font-medium'>
                            {costKnown
                              ? format.billingMode(cost.billing_mode)
                              : t('Unknown')}
                          </dd>
                        </div>
                        <div>
                          <dt className='text-muted-foreground'>
                            {t('Confidence')}
                          </dt>
                          <dd className='mt-1 font-medium'>
                            {costKnown
                              ? `${cost.confidence} · ${cost.freshness}`
                              : t('Unknown')}
                          </dd>
                        </div>
                        <div>
                          <dt className='text-muted-foreground'>
                            {t('Snapshot time')}
                          </dt>
                          <dd className='mt-1 font-medium'>
                            {format.timestamp(cost.effective_time)}
                          </dd>
                        </div>
                      </dl>
                      <div className='mt-3 flex justify-end'>
                        <Button
                          type='button'
                          size='sm'
                          variant='ghost'
                          onClick={() => setSelectedCost(cost)}
                        >
                          <Eye aria-hidden='true' />
                          {t('View details')}
                        </Button>
                      </div>
                    </article>
                  )
                })}
              </div>

              <ChannelRoutingPagination
                page={page}
                pageSize={pageSize}
                total={query.data.total}
                onPageChange={(nextPage) => updateSearch({ page: nextPage })}
                onPageSizeChange={(nextSize) =>
                  updateSearch({ page: 1, pageSize: nextSize })
                }
              />
            </>
          ) : null}
        </div>
      </ChannelRoutingPageFrame>
      <ChannelRoutingCostDetailsSheet
        summary={selectedCost}
        open={selectedCost != null}
        onOpenChange={(open) => {
          if (!open) setSelectedCost(null)
        }}
      />
    </>
  )
}
