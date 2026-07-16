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
  ArrowRight01Icon,
  ArrowTurnBackwardIcon,
  Cancel01Icon,
  Clock03Icon,
  Location01Icon,
  RefreshIcon,
  Search01Icon,
  TestTube01Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import type { TFunction } from 'i18next'
import type { FormEvent } from 'react'
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

import { listChannelRoutingProbes } from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingIdentityText } from '../components/identity-text'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../components/page-state'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import { CHANNEL_ROUTING_PAGE_SIZES } from '../lib/pagination'
import type { RoutingProbeResult } from '../types'

const route = getRouteApi('/_authenticated/channel-routing/$section')

function probeOutcomeLabel(outcome: string, t: TFunction): string {
  switch (outcome) {
    case 'success':
      return t('Success')
    case 'failure':
      return t('Failed')
    case 'timeout':
      return t('Timeout')
    case 'canceled':
      return t('Canceled')
    case 'local_error':
      return t('Local error')
    default:
      return outcome || t('Unknown')
  }
}

function ProbeResultSummary(props: { probe: RoutingProbeResult }) {
  const { t } = useTranslation()
  return (
    <div className='flex min-w-0 flex-col gap-2'>
      <div className='flex flex-wrap items-center gap-2'>
        <ChannelRoutingStatusBadge
          status={props.probe.outcome}
          label={probeOutcomeLabel(props.probe.outcome, t)}
        />
        {props.probe.status_code > 0 ? (
          <span className='font-mono text-xs'>
            HTTP {props.probe.status_code}
          </span>
        ) : null}
      </div>
      {props.probe.error_code ? (
        <ChannelRoutingIdentityText
          text={props.probe.error_code}
          className='text-destructive font-mono text-xs'
          breakAll
        />
      ) : null}
      {props.probe.error_message ? (
        <p className='text-muted-foreground max-w-72 text-xs [overflow-wrap:anywhere]'>
          {props.probe.error_message}
        </p>
      ) : null}
    </div>
  )
}

function ProbeTargetSummary(props: { probe: RoutingProbeResult }) {
  const { t } = useTranslation()
  return (
    <div className='min-w-0'>
      <ChannelRoutingIdentityText
        text={props.probe.group_name}
        className='font-medium'
      />
      <ChannelRoutingIdentityText
        text={props.probe.model_name}
        className='text-muted-foreground mt-0.5 font-mono text-xs'
      />
      <div className='text-muted-foreground mt-1 text-xs'>
        {t('Channel #{{channel}} · member #{{member}}', {
          channel: props.probe.channel_id,
          member: props.probe.member_id,
        })}
      </div>
      <div className='text-muted-foreground mt-0.5 text-xs'>
        {t('Credential #{{id}}', { id: props.probe.credential_id })}
      </div>
    </div>
  )
}

function ProbeEndpointSummary(props: { probe: RoutingProbeResult }) {
  const { t } = useTranslation()
  return (
    <div className='min-w-0'>
      <ChannelRoutingIdentityText
        text={props.probe.endpoint_authority || props.probe.endpoint_host}
        className='font-mono text-xs'
        breakAll
      />
      <div className='text-muted-foreground mt-1 inline-flex items-center gap-1 text-xs'>
        <HugeiconsIcon
          icon={Location01Icon}
          className='size-3'
          aria-hidden='true'
        />
        {props.probe.region || t('Unknown region')}
      </div>
      <div className='mt-2 flex flex-wrap items-center gap-2'>
        <ChannelRoutingStatusBadge status={props.probe.breaker_state} />
        <span className='text-muted-foreground text-xs'>
          {props.probe.breaker_scope || t('Unknown scope')}
        </span>
      </div>
    </div>
  )
}

function ProbeEvidenceSummary(props: { probe: RoutingProbeResult }) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  return (
    <dl className='grid grid-cols-2 gap-x-3 gap-y-2 text-xs'>
      <div>
        <dt className='text-muted-foreground'>{t('Latency')}</dt>
        <dd className='mt-0.5 font-medium tabular-nums'>
          {format.milliseconds(props.probe.latency_ms)}
        </dd>
      </div>
      <div>
        <dt className='text-muted-foreground'>{t('Evidence')}</dt>
        <dd className='mt-0.5 font-medium tabular-nums'>
          {props.probe.evidence_count}
        </dd>
      </div>
      <div>
        <dt className='text-muted-foreground'>{t('Nodes')}</dt>
        <dd className='mt-0.5 font-medium tabular-nums'>
          {props.probe.node_count}
        </dd>
      </div>
      <div>
        <dt className='text-muted-foreground'>{t('Responsibility')}</dt>
        <dd className='mt-0.5 font-medium'>
          {props.probe.responsibility || t('Unknown')}
        </dd>
      </div>
    </dl>
  )
}

function ProbeUsageSummary(props: { probe: RoutingProbeResult }) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const totalTokens = props.probe.prompt_tokens + props.probe.completion_tokens
  return (
    <div className='text-xs'>
      <div className='font-medium tabular-nums'>
        {t('{{count}} tokens', { count: format.number(totalTokens) })}
      </div>
      <div className='text-muted-foreground mt-1'>
        {t('{{currency}} {{cost}}', {
          currency: 'USD',
          cost: format.cost(props.probe.cost_nano_usd / 1_000_000_000),
        })}
      </div>
      <div className='text-muted-foreground mt-1'>
        {t('{{prompt}} input · {{completion}} output', {
          prompt: format.number(props.probe.prompt_tokens),
          completion: format.number(props.probe.completion_tokens),
        })}
      </div>
    </div>
  )
}

function ProbeFinishedTime(props: { value: number }) {
  const format = useChannelRoutingFormatters()
  return (
    <span className='inline-flex items-center gap-1'>
      <HugeiconsIcon
        icon={Clock03Icon}
        className='text-muted-foreground size-3.5'
        aria-hidden='true'
      />
      {format.timestamp(props.value)}
    </span>
  )
}

function ProbeCursorPagination(props: {
  cursor: number
  nextCursor: number | string
  disabled: boolean
  onCursorChange: (cursor: number) => void
}) {
  const { t } = useTranslation()
  const nextCursor = Number(props.nextCursor || 0)
  const hasNext = Number.isFinite(nextCursor) && nextCursor > 0
  return (
    <div className='flex flex-wrap items-center justify-end gap-2 border-t pt-3'>
      <Button
        size='sm'
        variant='outline'
        className='min-h-11 lg:min-h-7'
        disabled={props.disabled || props.cursor <= 0}
        onClick={() => props.onCursorChange(0)}
      >
        <HugeiconsIcon
          icon={ArrowTurnBackwardIcon}
          data-icon='inline-start'
          aria-hidden='true'
        />
        {t('First page')}
      </Button>
      <Button
        size='sm'
        variant='outline'
        className='min-h-11 lg:min-h-7'
        disabled={props.disabled || !hasNext}
        onClick={() => props.onCursorChange(nextCursor)}
      >
        {t('Next page')}
        <HugeiconsIcon
          icon={ArrowRight01Icon}
          data-icon='inline-end'
          aria-hidden='true'
        />
      </Button>
    </div>
  )
}

export function ActiveProbesTab() {
  const { t } = useTranslation()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const cursor = search.probeCursor ?? 0
  const limit = search.probeLimit ?? 20
  const outcome = search.probeOutcome ?? 'all'
  const params = {
    limit,
    cursor: cursor || undefined,
    channel_id: search.probeChannelId,
    outcome: outcome === 'all' ? undefined : outcome,
  }
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.probes(params),
    queryFn: () => listChannelRoutingProbes(params),
    meta: { handleErrorLocally: true },
  })

  const updateSearch = (patch: Record<string, string | number | undefined>) => {
    void navigate({
      search: (previous) => ({ ...previous, ...patch }),
      replace: true,
    })
  }
  const handleChannelFilter = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const rawChannelId = String(form.get('probeChannelId') ?? '').trim()
    updateSearch({
      probeCursor: 0,
      probeChannelId: rawChannelId ? Number(rawChannelId) : undefined,
    })
  }
  const filtersActive = outcome !== 'all' || search.probeChannelId != null

  return (
    <div className='flex flex-col gap-3 pb-2'>
      <div className='flex flex-col gap-2 lg:flex-row lg:items-center lg:justify-between'>
        <div>
          <h2 className='flex items-center gap-2 text-sm font-semibold'>
            <HugeiconsIcon
              icon={TestTube01Icon}
              className='size-4'
              aria-hidden='true'
            />
            {t('Active probe records')}
          </h2>
          <p className='text-muted-foreground mt-0.5 text-xs'>
            {t(
              'Bounded serving checks with endpoint, breaker, latency, usage, and cost evidence.'
            )}
          </p>
        </div>
        <Button
          size='sm'
          variant='outline'
          className='min-h-11 self-start lg:min-h-7'
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          <HugeiconsIcon
            icon={RefreshIcon}
            data-icon='inline-start'
            className={
              query.isFetching
                ? 'animate-spin motion-reduce:animate-none'
                : undefined
            }
            aria-hidden='true'
          />
          {t('Refresh')}
        </Button>
      </div>

      <div className='flex flex-col gap-2 lg:flex-row lg:flex-wrap lg:items-center'>
        <NativeSelect
          size='sm'
          className='w-full lg:w-auto [&_[data-slot=native-select]]:min-h-11 lg:[&_[data-slot=native-select]]:min-h-7'
          value={outcome}
          aria-label={t('Probe outcome')}
          onChange={(event) =>
            updateSearch({
              probeCursor: 0,
              probeOutcome: event.target.value,
            })
          }
        >
          <NativeSelectOption value='all'>
            {t('All outcomes')}
          </NativeSelectOption>
          <NativeSelectOption value='success'>
            {t('Success')}
          </NativeSelectOption>
          <NativeSelectOption value='failure'>{t('Failed')}</NativeSelectOption>
          <NativeSelectOption value='timeout'>
            {t('Timeout')}
          </NativeSelectOption>
          <NativeSelectOption value='canceled'>
            {t('Canceled')}
          </NativeSelectOption>
          <NativeSelectOption value='local_error'>
            {t('Local error')}
          </NativeSelectOption>
        </NativeSelect>
        <form
          key={search.probeChannelId ?? 'all'}
          className='flex min-w-0 items-center gap-2 lg:max-w-xs lg:flex-1'
          onSubmit={handleChannelFilter}
        >
          <Input
            name='probeChannelId'
            type='number'
            min={1}
            step={1}
            defaultValue={search.probeChannelId}
            className='min-h-11 lg:min-h-8'
            aria-label={t('Filter probes by channel ID')}
            placeholder={t('Channel ID')}
          />
          <Button
            type='submit'
            size='icon'
            variant='outline'
            className='min-h-11 min-w-11 lg:min-h-8 lg:min-w-8'
            aria-label={t('Apply channel filter')}
          >
            <HugeiconsIcon icon={Search01Icon} aria-hidden='true' />
          </Button>
        </form>
        <NativeSelect
          size='sm'
          className='w-full lg:w-auto [&_[data-slot=native-select]]:min-h-11 lg:[&_[data-slot=native-select]]:min-h-7'
          value={String(limit)}
          aria-label={t('Rows per page')}
          onChange={(event) =>
            updateSearch({
              probeCursor: 0,
              probeLimit: Number(event.target.value),
            })
          }
        >
          {CHANNEL_ROUTING_PAGE_SIZES.map((size) => (
            <NativeSelectOption key={size} value={size}>
              {t('{{count}} per page', { count: size })}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        {filtersActive ? (
          <Button
            size='sm'
            variant='ghost'
            className='min-h-11 justify-start lg:min-h-7'
            onClick={() =>
              updateSearch({
                probeCursor: 0,
                probeOutcome: 'all',
                probeChannelId: undefined,
              })
            }
          >
            <HugeiconsIcon
              icon={Cancel01Icon}
              data-icon='inline-start'
              aria-hidden='true'
            />
            {t('Clear')}
          </Button>
        ) : null}
      </div>

      {query.isLoading ? (
        <ChannelRoutingLoadingState
          label={t('Loading active probe records')}
          rows={8}
        />
      ) : null}
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
          title={t('No active probe records')}
          description={t('No probe results match the current filters.')}
        />
      ) : null}

      {query.data && query.data.items.length > 0 ? (
        <>
          <div className='text-muted-foreground text-xs'>
            {t('Showing {{count}} probe records', {
              count: query.data.items.length,
            })}
          </div>
          <div className='hidden overflow-hidden rounded-lg border xl:block'>
            <Table
              className='min-w-[78rem]'
              scrollAreaLabel={t('Active probe records table')}
            >
              <TableHeader>
                <TableRow>
                  <TableHead className='min-w-56'>{t('Result')}</TableHead>
                  <TableHead className='min-w-56'>{t('Target')}</TableHead>
                  <TableHead className='min-w-64'>
                    {t('Endpoint and breaker')}
                  </TableHead>
                  <TableHead className='min-w-52'>{t('Evidence')}</TableHead>
                  <TableHead className='min-w-52'>
                    {t('Usage and cost')}
                  </TableHead>
                  <TableHead className='min-w-44'>{t('Finished')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {query.data.items.map((probe) => (
                  <TableRow key={probe.id}>
                    <TableCell className='align-top'>
                      <ProbeResultSummary probe={probe} />
                    </TableCell>
                    <TableCell className='align-top'>
                      <ProbeTargetSummary probe={probe} />
                    </TableCell>
                    <TableCell className='align-top'>
                      <ProbeEndpointSummary probe={probe} />
                    </TableCell>
                    <TableCell className='align-top'>
                      <ProbeEvidenceSummary probe={probe} />
                    </TableCell>
                    <TableCell className='align-top'>
                      <ProbeUsageSummary probe={probe} />
                    </TableCell>
                    <TableCell className='align-top text-xs'>
                      <ProbeFinishedTime value={probe.finished_time_ms} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          <div className='divide-y rounded-lg border xl:hidden'>
            {query.data.items.map((probe) => (
              <article key={probe.id} className='min-w-0 p-3'>
                <div className='flex min-w-0 flex-col gap-3 sm:flex-row sm:items-start sm:justify-between'>
                  <ProbeTargetSummary probe={probe} />
                  <ProbeResultSummary probe={probe} />
                </div>
                <div className='mt-3 border-y py-3'>
                  <ProbeEndpointSummary probe={probe} />
                </div>
                <div className='mt-3 grid gap-3 sm:grid-cols-2'>
                  <ProbeEvidenceSummary probe={probe} />
                  <ProbeUsageSummary probe={probe} />
                </div>
                <div className='text-muted-foreground mt-3 inline-flex items-center gap-1 text-xs'>
                  <ProbeFinishedTime value={probe.finished_time_ms} />
                </div>
              </article>
            ))}
          </div>

          <ProbeCursorPagination
            cursor={cursor}
            nextCursor={query.data.next_cursor}
            disabled={query.isRefetchError}
            onCursorChange={(nextCursor) =>
              updateSearch({ probeCursor: nextCursor })
            }
          />
        </>
      ) : null}
    </div>
  )
}
