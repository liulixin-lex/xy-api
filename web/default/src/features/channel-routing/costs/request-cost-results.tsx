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
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from '@/components/ui/empty'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { ChannelRoutingIdentityText } from '../components/identity-text'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import {
  routingCostDimensionLabel,
  routingCostProfileSourceLabel,
} from '../lib/cost-dimensions'
import { useChannelRoutingFormatters } from '../lib/format'
import type {
  RoutingCostBreakdown,
  RoutingCostComparisonCandidate,
  RoutingCostComparisonResponse,
} from '../types'

const breakdownFields = [
  ['input', 'Input tokens'],
  ['output', 'Output tokens'],
  ['cache_read', 'Cache read tokens'],
  ['cache_write', 'Cache write tokens'],
  ['cache_write_1h', '1h cache write tokens'],
  ['image_input', 'Image input'],
  ['image_output', 'Image output'],
  ['image_units', 'Image units'],
  ['audio_input', 'Audio input'],
  ['audio_output', 'Audio output'],
  ['audio_seconds', 'Audio seconds'],
  ['video_seconds', 'Video seconds'],
  ['task_units', 'Task units'],
  ['per_request', 'Per request'],
  ['expression', 'Expression'],
] as const satisfies readonly [keyof RoutingCostBreakdown, string][]

function CostAmount(props: {
  known: boolean
  value: number
  currency: string
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  if (!props.known) return <>{t('Unknown')}</>
  if (props.value === 0) return <>{t('Free')}</>
  return (
    <>
      {t('{{currency}} {{cost}}', {
        currency: props.currency,
        cost: format.cost(props.value),
      })}
    </>
  )
}

function CandidateBreakdown(props: {
  candidate: RoutingCostComparisonCandidate
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const rows = breakdownFields.flatMap(([field, label]) => {
    const value = props.candidate.single_attempt.expected_breakdown[field]
    return Number.isFinite(value) && value !== 0
      ? [{ field, label, value }]
      : []
  })
  if (rows.length === 0) return null
  return (
    <details className='mt-2 text-left'>
      <summary className='text-muted-foreground cursor-pointer text-xs'>
        {t('Cost breakdown')}
      </summary>
      <dl className='mt-2 grid gap-1 text-xs'>
        {rows.map((row) => (
          <div key={row.field} className='flex justify-between gap-3'>
            <dt className='text-muted-foreground'>{t(row.label)}</dt>
            <dd className='font-mono'>{format.cost(row.value)}</dd>
          </div>
        ))}
      </dl>
    </details>
  )
}

function MissingContext(props: { values: string[] | undefined }) {
  const { t } = useTranslation()
  if (!props.values?.length) return null
  return (
    <div className='mt-2 flex max-w-64 flex-col gap-1.5'>
      <span className='text-muted-foreground text-xs'>
        {t('Missing context')}
      </span>
      <div className='flex flex-wrap gap-1'>
        {props.values.map((value) => (
          <Badge key={value} variant='outline' className='font-normal'>
            {t(routingCostDimensionLabel(value))}
          </Badge>
        ))}
      </div>
    </div>
  )
}

export function RequestCostResults(props: {
  result: RoutingCostComparisonResponse
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()

  return (
    <section
      className='flex flex-col gap-3'
      aria-labelledby='request-cost-results-title'
    >
      <div className='flex flex-wrap items-start justify-between gap-2 border-y py-2'>
        <div className='min-w-0 flex-1'>
          <h3 id='request-cost-results-title' className='text-sm font-semibold'>
            {t('Request cost comparison')}
          </h3>
          <p className='text-muted-foreground mt-0.5 flex flex-wrap gap-x-2 text-xs'>
            <span className='min-w-0 break-all'>{props.result.model_name}</span>
            <span>
              {t(routingCostProfileSourceLabel(props.result.profile_source))}
            </span>
          </p>
        </div>
        <div className='text-muted-foreground flex min-w-0 flex-col items-end gap-0.5 text-right text-xs'>
          <div>
            {t('Pricing epoch')}: {props.result.pricing_epoch}
          </div>
          <div className='flex max-w-64 min-w-0 items-start gap-1'>
            <span className='shrink-0'>{t('Pricing hash')}:</span>
            <ChannelRoutingIdentityText
              text={props.result.pricing_hash || t('Unavailable')}
              className='min-w-0 font-mono'
              breakAll
            />
          </div>
          <div>{format.timestamp(props.result.generated_at)}</div>
        </div>
      </div>

      {Object.keys(props.result.quantity_sources).length > 0 ? (
        <div className='flex flex-wrap items-center gap-1.5 text-xs'>
          <span className='text-muted-foreground'>
            {t('Quantity sources')}:
          </span>
          {Object.entries(props.result.quantity_sources).map(
            ([key, source]) => (
              <Badge key={key} variant='outline' className='font-normal'>
                {t(routingCostDimensionLabel(key))}:{' '}
                {t(routingCostProfileSourceLabel(source))}
              </Badge>
            )
          )}
        </div>
      ) : null}

      {props.result.candidates.length === 0 ? (
        <Empty className='min-h-48 border'>
          <EmptyHeader>
            <EmptyTitle>{t('No comparable candidates')}</EmptyTitle>
            <EmptyDescription>
              {t('No candidate channels match this request profile.')}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <div className='overflow-hidden rounded-lg border'>
          <Table
            className='min-w-[68rem]'
            scrollAreaLabel={t('Request cost comparison table')}
          >
            <TableHeader>
              <TableRow>
                <TableHead>{t('Channel')}</TableHead>
                <TableHead>{t('Comparison status')}</TableHead>
                <TableHead className='text-right'>
                  {t('Single attempt')}
                </TableHead>
                <TableHead className='text-right'>
                  {t('Expected execution')}
                </TableHead>
                <TableHead className='text-right'>{t('Worst case')}</TableHead>
                <TableHead className='text-right'>
                  {t('1× system baseline')}
                </TableHead>
                <TableHead className='text-right'>
                  {t('Channel multiplier')}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {props.result.candidates.map((candidate) => {
                const estimate = candidate.single_attempt
                const baseline = candidate.before_multiplier
                return (
                  <TableRow
                    key={`${candidate.member_id}:${candidate.routing_generation}`}
                  >
                    <TableCell className='align-top'>
                      <div className='font-medium'>
                        {candidate.channel_name || `#${candidate.channel_id}`}
                      </div>
                      <div className='text-muted-foreground flex flex-wrap gap-x-2 text-xs'>
                        <span>#{candidate.channel_id}</span>
                        <span>
                          {t('Member #{{member}}', {
                            member: candidate.member_id,
                          })}
                        </span>
                      </div>
                      <ChannelRoutingIdentityText
                        text={candidate.routing_generation}
                        className='text-muted-foreground mt-1 max-w-48 font-mono text-[11px]'
                        breakAll
                      />
                    </TableCell>
                    <TableCell className='align-top'>
                      <ChannelRoutingStatusBadge
                        status={candidate.comparable ? 'known' : 'unknown'}
                        label={
                          candidate.comparable
                            ? t('Comparable')
                            : t('Not comparable')
                        }
                      />
                      <MissingContext values={candidate.missing_context} />
                    </TableCell>
                    <TableCell className='text-right align-top font-medium'>
                      <CostAmount
                        known={estimate.expected_known}
                        value={estimate.expected_cost}
                        currency={estimate.currency}
                      />
                      <CandidateBreakdown candidate={candidate} />
                    </TableCell>
                    <TableCell className='text-right align-top font-medium'>
                      <CostAmount
                        known={estimate.expected_effective_known}
                        value={estimate.expected_effective_cost}
                        currency={estimate.currency}
                      />
                    </TableCell>
                    <TableCell className='text-right align-top font-medium'>
                      <CostAmount
                        known={estimate.worst_case_known}
                        value={estimate.worst_case_cost}
                        currency={estimate.currency}
                      />
                    </TableCell>
                    <TableCell className='text-right align-top font-medium'>
                      <CostAmount
                        known={baseline.expected_effective_known}
                        value={baseline.expected_effective_cost}
                        currency={baseline.currency}
                      />
                    </TableCell>
                    <TableCell className='text-right align-top font-mono'>
                      {format.cost(candidate.upstream_cost_multiplier)}×
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      )}
    </section>
  )
}
