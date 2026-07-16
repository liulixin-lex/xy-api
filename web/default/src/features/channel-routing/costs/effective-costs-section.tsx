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
  Cancel01Icon,
  Search01Icon,
  ViewIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import { getRouteApi, Link } from '@tanstack/react-router'
import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

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

import { listChannelRoutingCosts } from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingIdentityText } from '../components/identity-text'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../components/page-state'
import { ChannelRoutingPagination } from '../components/pagination-bar'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import {
  hasKnownCostSemantics,
  isKnownZeroMultiplierCost,
  routingCostUnknownReasonLabel,
} from '../lib/cost-audit'
import { useChannelRoutingFormatters } from '../lib/format'
import type { CostSnapshotSummary } from '../types'
import { ChannelRoutingCostDetailsSheet } from './cost-details-sheet'

const route = getRouteApi('/_authenticated/channel-routing/$section')

function ChannelRoutingCostValue(props: {
  cost: CostSnapshotSummary
  effective?: boolean
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const cost = props.cost
  if (!hasKnownCostSemantics(cost)) return <>{t('Unknown')}</>
  if (isKnownZeroMultiplierCost(cost)) {
    return props.effective ? t('Free 0×') : t('Not required')
  }

  if (cost.expression_pricing) return <>{t('Expression pricing')}</>

  const currency = cost.currency || ''
  const unit = cost.unit || ''
  if (
    typeof cost.display_rate === 'number' &&
    Number.isFinite(cost.display_rate)
  ) {
    let amount = cost.display_rate
    if (props.effective) {
      if (!Number.isFinite(cost.upstream_cost_multiplier)) {
        return <>{t('Unknown')}</>
      }
      amount *= cost.upstream_cost_multiplier
      if (!Number.isFinite(amount)) return <>{t('Unknown')}</>
    }
    return (
      <span className='inline-flex flex-col items-end'>
        <span>
          {t('{{currency}} {{cost}}', {
            currency,
            cost: format.cost(amount),
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

export function EffectiveCostsSection() {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
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
    meta: { handleErrorLocally: true },
  })

  const updateSearch = useCallback(
    (patch: Record<string, string | number | boolean | undefined>) => {
      void navigate({
        search: (previous) => ({ ...previous, ...patch }),
        replace: true,
      })
    },
    [navigate]
  )
  useEffect(() => {
    if (!query.data) return
    const totalPages = Math.max(1, Math.ceil(query.data.total / pageSize))
    if (page > totalPages) updateSearch({ page: totalPages })
  }, [page, pageSize, query.data, updateSearch])
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
      <div className='space-y-3 pb-2'>
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
              <HugeiconsIcon
                icon={Search01Icon}
                data-icon='inline-start'
                strokeWidth={2}
                aria-hidden='true'
              />
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
                updateSearch({
                  page: 1,
                  group: '',
                  model: '',
                  known: 'all',
                })
              }
            >
              <HugeiconsIcon
                icon={Cancel01Icon}
                data-icon='inline-start'
                strokeWidth={2}
                aria-hidden='true'
              />
              {t('Clear')}
            </Button>
          ) : null}
        </div>

        {query.isLoading ? <ChannelRoutingLoadingState /> : null}
        {query.isError && !query.data ? (
          <ChannelRoutingErrorState
            error={query.error}
            onRetry={() => void query.refetch()}
          />
        ) : null}
        {query.isRefetchError && query.data ? (
          <ChannelRoutingRefetchErrorAlert
            isFetching={query.isFetching}
            onRetry={() => void query.refetch()}
          />
        ) : null}
        {query.data && query.data.items.length === 0 ? (
          <ChannelRoutingEmptyState
            title={
              query.data.total > 0
                ? t('This cost snapshot page is empty')
                : t('No cost snapshots')
            }
            description={
              query.data.total > 0
                ? t(
                    'Return to the first page to continue browsing cost snapshots.'
                  )
                : t('No upstream cost snapshots match the current filters.')
            }
            action={
              query.data.total > 0 ? (
                <Button
                  type='button'
                  variant='outline'
                  onClick={() => updateSearch({ page: 1 })}
                >
                  {t('First page')}
                </Button>
              ) : undefined
            }
          />
        ) : null}

        {query.data && query.data.items.length > 0 ? (
          <>
            <div className='hidden overflow-hidden rounded-lg border xl:block'>
              <Table
                className='min-w-[84rem]'
                scrollAreaLabel={t('Cost snapshots table')}
              >
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('Group')}</TableHead>
                    <TableHead>{t('Channel')}</TableHead>
                    <TableHead>{t('Model')}</TableHead>
                    <TableHead>{t('Availability')}</TableHead>
                    <TableHead className='text-right'>
                      {t('1× system baseline')}
                    </TableHead>
                    <TableHead className='text-right'>
                      {t('Channel multiplier')}
                    </TableHead>
                    <TableHead className='text-right'>
                      {t('Effective cost')}
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
                          {!costKnown && cost.unknown_reason ? (
                            <p
                              className='text-muted-foreground mt-1 max-w-44 text-xs break-words'
                              title={cost.unknown_reason}
                            >
                              {routingCostUnknownReasonLabel(
                                cost.unknown_reason,
                                t
                              )}
                            </p>
                          ) : null}
                        </TableCell>
                        <TableCell className='text-right font-medium'>
                          <ChannelRoutingCostValue cost={cost} />
                        </TableCell>
                        <TableCell className='text-right font-medium'>
                          {Number.isFinite(cost.upstream_cost_multiplier)
                            ? `${format.cost(cost.upstream_cost_multiplier)}×`
                            : t('Unknown')}
                        </TableCell>
                        <TableCell className='text-right font-medium'>
                          <ChannelRoutingCostValue cost={cost} effective />
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
                            <HugeiconsIcon
                              icon={ViewIcon}
                              strokeWidth={2}
                              aria-hidden='true'
                            />
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
                    {!costKnown && cost.unknown_reason ? (
                      <p
                        className='text-muted-foreground mt-2 text-xs break-words'
                        title={cost.unknown_reason}
                      >
                        {routingCostUnknownReasonLabel(cost.unknown_reason, t)}
                      </p>
                    ) : null}
                    <dl className='mt-3 grid grid-cols-2 gap-3 text-xs sm:grid-cols-3'>
                      <div>
                        <dt className='text-muted-foreground'>
                          {t('1× system baseline')}
                        </dt>
                        <dd className='mt-1 font-medium'>
                          <ChannelRoutingCostValue cost={cost} />
                        </dd>
                      </div>
                      <div>
                        <dt className='text-muted-foreground'>
                          {t('Channel multiplier')}
                        </dt>
                        <dd className='mt-1 font-medium'>
                          {Number.isFinite(cost.upstream_cost_multiplier)
                            ? `${format.cost(cost.upstream_cost_multiplier)}×`
                            : t('Unknown')}
                        </dd>
                      </div>
                      <div>
                        <dt className='text-muted-foreground'>
                          {t('Effective cost')}
                        </dt>
                        <dd className='mt-1 font-medium'>
                          <ChannelRoutingCostValue cost={cost} effective />
                        </dd>
                      </div>
                      <div className='min-w-0'>
                        <dt className='text-muted-foreground'>
                          {t('Billing Mode')}
                        </dt>
                        <dd className='mt-1 font-medium break-words'>
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
                        <HugeiconsIcon
                          icon={ViewIcon}
                          data-icon='inline-start'
                          strokeWidth={2}
                          aria-hidden='true'
                        />
                        {t('View details')}
                      </Button>
                    </div>
                  </article>
                )
              })}
            </div>
          </>
        ) : null}
        {query.data && query.data.total > 0 ? (
          <ChannelRoutingPagination
            page={page}
            pageSize={pageSize}
            total={query.data.total}
            disabled={query.isRefetchError}
            onPageChange={(nextPage) => updateSearch({ page: nextPage })}
            onPageSizeChange={(nextSize) =>
              updateSearch({ page: 1, pageSize: nextSize })
            }
          />
        ) : null}
      </div>
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
