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

import { ChevronRight } from 'lucide-react'
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

import { useChannelRoutingFormatters } from '../lib/format'
import type { HistoricalSimulationResult } from '../types'
import { ChannelRoutingStatusBadge } from './status-badge'

export function ChannelRoutingHistoricalSimulationResults(props: {
  result: HistoricalSimulationResult
  pending: boolean
  onNextBatch: (cursor: number) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const skippedCount = Math.max(
    props.result.skipped.length,
    props.result.scanned_samples - props.result.evaluated_samples
  )
  const skipReasons = Object.entries(props.result.skip_reasons).filter(
    ([, count]) => count > 0
  )
  const hasNextBatch =
    props.result.next_cursor > 0 &&
    props.result.next_cursor !== props.result.cursor

  return (
    <section
      className='space-y-4 border-t pt-4'
      aria-labelledby='simulation-batch-title'
    >
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <h3 id='simulation-batch-title' className='text-sm font-semibold'>
          {t('Simulation batch results')}
        </h3>
        <span className='text-muted-foreground text-xs tabular-nums'>
          {t('Cursor {{cursor}}', { cursor: props.result.cursor })}
        </span>
      </div>

      <div className='bg-border grid grid-cols-2 gap-px overflow-hidden rounded-lg border sm:grid-cols-3 xl:grid-cols-6'>
        {[
          [t('Scanned'), format.number(props.result.scanned_samples)],
          [t('Evaluated'), format.number(props.result.evaluated_samples)],
          [t('Skipped'), format.number(skippedCount)],
          [
            t('Actual match rate'),
            format.percent(props.result.actual_match_rate),
          ],
          [
            t('Selection change rate'),
            format.percent(props.result.selection_change_rate),
          ],
          [
            t('Average cost delta'),
            format.cost(props.result.average_expected_cost_delta ?? Number.NaN),
          ],
        ].map(([label, value]) => (
          <div key={label} className='bg-background min-w-0 p-3'>
            <div className='text-muted-foreground truncate text-xs'>
              {label}
            </div>
            <div className='mt-1 text-base font-semibold tabular-nums'>
              {value}
            </div>
          </div>
        ))}
      </div>

      {skipReasons.length > 0 ? (
        <section aria-labelledby='simulation-skip-reasons-title'>
          <h4
            id='simulation-skip-reasons-title'
            className='mb-2 text-xs font-semibold'
          >
            {t('Skip reasons')}
          </h4>
          <dl className='divide-y rounded-lg border text-xs'>
            {skipReasons.map(([reason, count]) => (
              <div
                key={reason}
                className='flex min-w-0 items-center justify-between gap-3 px-3 py-2'
              >
                <dt className='min-w-0 break-words'>{reason}</dt>
                <dd className='shrink-0 font-medium tabular-nums'>{count}</dd>
              </div>
            ))}
          </dl>
        </section>
      ) : null}

      {props.result.samples.length > 0 ? (
        <div className='overflow-hidden rounded-lg border'>
          <Table scrollAreaLabel={t('Simulation results table')}>
            <caption className='sr-only'>
              {t('{{count}} samples in this batch', {
                count: props.result.samples.length,
              })}
            </caption>
            <TableHeader>
              <TableRow>
                <TableHead>{t('Decision')}</TableHead>
                <TableHead>{t('Baseline')}</TableHead>
                <TableHead>{t('Simulated')}</TableHead>
                <TableHead>{t('Result')}</TableHead>
                <TableHead className='text-right'>{t('Cost delta')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {props.result.samples.map((sample) => (
                <TableRow key={sample.decision_id}>
                  <TableCell className='max-w-44 truncate font-mono text-xs'>
                    {sample.decision_id}
                  </TableCell>
                  <TableCell>#{sample.baseline_channel_id}</TableCell>
                  <TableCell>#{sample.simulated_channel_id}</TableCell>
                  <TableCell>
                    <ChannelRoutingStatusBadge
                      status={sample.selection_changed ? 'degraded' : 'success'}
                      label={
                        sample.selection_changed ? t('Changed') : t('Unchanged')
                      }
                    />
                  </TableCell>
                  <TableCell className='text-right tabular-nums'>
                    {sample.baseline_cost_known && sample.simulated_cost_known
                      ? format.cost(sample.expected_cost_delta)
                      : t('Unknown')}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : (
        <p className='text-muted-foreground text-sm'>
          {t('No replayable samples were available for this group.')}
        </p>
      )}

      {hasNextBatch ? (
        <div className='flex justify-end border-t pt-3'>
          <Button
            type='button'
            variant='outline'
            disabled={props.pending}
            onClick={() => props.onNextBatch(props.result.next_cursor)}
          >
            {t('Continue next batch')}
            <ChevronRight aria-hidden='true' />
          </Button>
        </div>
      ) : null}
    </section>
  )
}
