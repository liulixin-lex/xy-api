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
import { ChevronLeft, ChevronRight, ChevronsLeft } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { listChannelRoutingDecisionCandidates } from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
} from '../components/page-state'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'

const candidatePageSize = 20

function ChannelRoutingDecisionCandidatesContent(props: {
  decisionId: string
  title: string
  expectedEligible?: number
  expectedTotal?: number
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const [cursorHistory, setCursorHistory] = useState([0])
  const cursor = cursorHistory.at(-1) ?? 0
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.decisionCandidates(
      props.decisionId,
      cursor,
      candidatePageSize
    ),
    queryFn: () =>
      listChannelRoutingDecisionCandidates(props.decisionId, {
        cursor,
        limit: candidatePageSize,
      }),
  })
  const data = query.data

  return (
    <section aria-labelledby={`candidate-set-title-${props.decisionId}`}>
      <div className='mb-2 flex flex-wrap items-end justify-between gap-2'>
        <div>
          <h3
            id={`candidate-set-title-${props.decisionId}`}
            className='text-sm font-semibold'
          >
            {props.title}
          </h3>
          <p className='text-muted-foreground mt-0.5 text-xs'>
            {data
              ? t('{{eligible}} of {{total}} eligible', {
                  eligible: props.expectedEligible ?? data.available,
                  total: props.expectedTotal ?? data.total,
                })
              : t('Loading candidate ranking')}
          </p>
        </div>
        {data ? (
          <div className='text-muted-foreground flex flex-wrap items-center gap-x-3 gap-y-1 text-xs'>
            <span>
              {t('Snapshot r{{revision}}', {
                revision: data.snapshot_revision,
              })}
            </span>
            <span>
              {t('Source')}: {data.source || t('Unknown')}
            </span>
            <span>
              {t('Traffic sample coverage')}:{' '}
              {format.percent(data.request_count_coverage)}
            </span>
          </div>
        ) : null}
      </div>

      {query.isLoading ? <ChannelRoutingLoadingState rows={6} /> : null}
      {query.isError ? (
        <ChannelRoutingErrorState
          error={query.error}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      {data && data.items.length === 0 ? (
        <ChannelRoutingEmptyState
          title={t('No candidate samples')}
          description={t(
            'This replayable decision has no candidate metrics available.'
          )}
        />
      ) : null}

      {data && data.items.length > 0 ? (
        <>
          {!data.request_count_known ? (
            <p className='border-border bg-muted/40 text-muted-foreground mb-2 rounded-md border px-3 py-2 text-xs'>
              {t(
                'Traffic share is unavailable because the historical request-count snapshot is incomplete.'
              )}
            </p>
          ) : null}
          {!data.complete || data.truncation_reason ? (
            <p className='border-warning/30 bg-warning/5 text-warning-foreground mb-2 rounded-md border px-3 py-2 text-xs'>
              {t('Candidate history is incomplete')}
              {data.truncation_reason ? ` · ${data.truncation_reason}` : ''}
            </p>
          ) : null}
          <div className='overflow-hidden rounded-lg border'>
            <Table
              className='min-w-[86rem]'
              scrollAreaLabel={t('Candidate ranking table')}
            >
              <TableHeader>
                <TableRow>
                  <TableHead className='w-14 text-right'>{t('Rank')}</TableHead>
                  <TableHead>{t('Channel')}</TableHead>
                  <TableHead>{t('Eligibility')}</TableHead>
                  <TableHead>{t('Score breakdown')}</TableHead>
                  <TableHead>{t('SLO signals')}</TableHead>
                  <TableHead>{t('Breaker / capacity')}</TableHead>
                  <TableHead className='text-right'>
                    {t('Traffic share')}
                  </TableHead>
                  <TableHead>{t('Sample time')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.items.map((candidate) => {
                  const trafficShare =
                    data.request_count_known &&
                    data.total_request_count > 0 &&
                    candidate.request_count != null
                      ? candidate.request_count / data.total_request_count
                      : undefined
                  let breakerStatus = 'known'
                  let breakerLabel = t('Closed')
                  if (candidate.open) {
                    breakerStatus = 'open'
                    breakerLabel = t('Open')
                  } else if (candidate.degraded) {
                    breakerStatus = 'degraded'
                    breakerLabel = t('Degraded')
                  }

                  return (
                    <TableRow
                      key={`${candidate.pool_member_id}-${candidate.channel_id}-${candidate.rank}`}
                    >
                      <TableCell className='text-right font-semibold tabular-nums'>
                        {candidate.rank}
                      </TableCell>
                      <TableCell>
                        <div className='font-medium'>
                          #{candidate.channel_id}
                        </div>
                        <div className='text-muted-foreground text-xs'>
                          {t('Member')} #{candidate.pool_member_id}
                          {candidate.credential_id
                            ? ` · ${t('Credential')} #${candidate.credential_id}`
                            : ''}
                        </div>
                      </TableCell>
                      <TableCell>
                        <ChannelRoutingStatusBadge
                          status={candidate.eligible ? 'success' : 'failed'}
                          label={
                            candidate.eligible
                              ? t('Eligible')
                              : candidate.exclusion_reason || t('Excluded')
                          }
                        />
                        {candidate.exploration_eligible === false ? (
                          <div className='text-muted-foreground mt-1 text-xs'>
                            {t('Exploration excluded')}
                          </div>
                        ) : null}
                      </TableCell>
                      <TableCell>
                        <div className='font-semibold tabular-nums'>
                          {format.number(candidate.score)}
                        </div>
                        <div className='text-muted-foreground mt-1 grid grid-cols-2 gap-x-3 gap-y-0.5 text-xs tabular-nums'>
                          <span>
                            {t('Availability')}:{' '}
                            {format.number(candidate.availability)}
                          </span>
                          <span>
                            {t('Latency')}: {format.number(candidate.latency)}
                          </span>
                          <span>
                            {t('Throughput')}:{' '}
                            {format.number(candidate.throughput)}
                          </span>
                          <span>
                            {t('Cost')}:{' '}
                            {candidate.cost_known
                              ? format.number(candidate.cost_score)
                              : t('Unknown')}
                          </span>
                        </div>
                      </TableCell>
                      <TableCell>
                        <div className='grid gap-0.5 text-xs tabular-nums'>
                          <span>
                            {t('Confidence')}:{' '}
                            {format.percent(candidate.confidence)}
                          </span>
                          <span>
                            {t('p95 TTFT')}:{' '}
                            {format.milliseconds(candidate.p95_ttft_ms)}
                          </span>
                          <span>
                            {t('Token/s')}:{' '}
                            {candidate.output_tokens_per_second == null
                              ? t('Unknown')
                              : format.number(
                                  candidate.output_tokens_per_second
                                )}
                          </span>
                        </div>
                      </TableCell>
                      <TableCell>
                        <div className='flex flex-wrap items-center gap-1'>
                          <ChannelRoutingStatusBadge
                            status={breakerStatus}
                            label={breakerLabel}
                          />
                          {candidate.capacity_utilization != null ? (
                            <ChannelRoutingStatusBadge
                              status={
                                candidate.capacity_utilization >= 1
                                  ? 'failed'
                                  : 'known'
                              }
                              label={t('{{value}} capacity', {
                                value: format.percent(
                                  candidate.capacity_utilization
                                ),
                              })}
                            />
                          ) : null}
                        </div>
                        <div className='text-muted-foreground mt-1 text-xs tabular-nums'>
                          {t('Inflight')}: {format.number(candidate.inflight)} ·{' '}
                          {t('Queue')}:{' '}
                          {format.milliseconds(candidate.queue_delay_ms)}
                        </div>
                      </TableCell>
                      <TableCell className='text-right tabular-nums'>
                        <div className='font-medium'>
                          {format.percent(trafficShare)}
                        </div>
                        <div className='text-muted-foreground text-xs'>
                          {candidate.request_count == null
                            ? t('Unknown')
                            : t('{{count}} requests', {
                                count: format.number(candidate.request_count),
                              })}
                        </div>
                      </TableCell>
                      <TableCell className='text-xs'>
                        {format.timestamp(candidate.metric_updated_unix)}
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </div>
          <div className='flex flex-wrap items-center justify-between gap-2 border-t pt-3'>
            <span className='text-muted-foreground text-xs'>
              {t('{{count}} records', { count: data.total })}
            </span>
            <div className='flex items-center gap-2'>
              <Button
                type='button'
                size='icon-sm'
                variant='outline'
                aria-label={t('First page')}
                disabled={cursorHistory.length <= 1 || query.isFetching}
                onClick={() => setCursorHistory([0])}
              >
                <ChevronsLeft aria-hidden='true' />
              </Button>
              <Button
                type='button'
                size='icon-sm'
                variant='outline'
                aria-label={t('Previous page')}
                disabled={cursorHistory.length <= 1 || query.isFetching}
                onClick={() =>
                  setCursorHistory((previous) => previous.slice(0, -1))
                }
              >
                <ChevronLeft aria-hidden='true' />
              </Button>
              <Button
                type='button'
                size='icon-sm'
                variant='outline'
                aria-label={t('Next page')}
                disabled={data.next_cursor <= 0 || query.isFetching}
                onClick={() =>
                  setCursorHistory((previous) => [
                    ...previous,
                    data.next_cursor,
                  ])
                }
              >
                <ChevronRight aria-hidden='true' />
              </Button>
            </div>
          </div>
        </>
      ) : null}
    </section>
  )
}

export function ChannelRoutingDecisionCandidatesSection(props: {
  decisionId: string
  title: string
  expectedEligible?: number
  expectedTotal?: number
}) {
  return (
    <ChannelRoutingDecisionCandidatesContent
      key={props.decisionId}
      decisionId={props.decisionId}
      title={props.title}
      expectedEligible={props.expectedEligible}
      expectedTotal={props.expectedTotal}
    />
  )
}
