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
import { DatabaseRestoreIcon, ShieldKeyIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useChannelRoutingFormatters } from '@/features/channel-routing/lib/format'

import { getBillingProjectionCodeDisplay } from '../lib/projection-operations'
import type {
  BillingLogSinkConflict,
  FailedBillingProjection,
} from '../projection-types'

const kindLabels: Record<string, string> = {
  accepted: 'Accepted reservation',
  task_terminal: 'Task terminal settlement',
  midjourney_terminal: 'Midjourney terminal settlement',
}

const statusLabels: Record<string, string> = {
  failed: 'Failed',
  open: 'Open',
  pending: 'Pending',
  invalid_payload: 'Invalid payload',
  not_required: 'Not required',
  applied: 'Applied',
  applied_split: 'Applied across periods',
  repaired_split: 'Repaired across periods',
  saturated: 'Saturated',
  skipped_deleted: 'Skipped deleted record',
  skipped_invalid: 'Skipped invalid record',
  skipped_missing: 'Skipped missing record',
  skipped_overflow: 'Skipped overflow',
  written: 'Written',
}

const failureLabels: Record<string, string> = {
  invalid_frozen_payload: 'Invalid frozen payload',
  invalid_frozen_spec: 'Invalid frozen specification',
  retry_exhausted: 'Retry exhausted',
  sink_receipt_conflict: 'Sink receipt conflict',
  sink_receipt_conflict_late: 'Late sink receipt conflict',
}

const outcomeScopeLabels: Record<string, string> = {
  channel: 'Channel outcome',
  data_export: 'Data export outcome',
  log: 'Log outcome',
  user: 'User outcome',
}

function OperationHash(props: { value: string }) {
  const { t } = useTranslation()
  return (
    <div className='min-w-0'>
      <div className='text-muted-foreground text-xs'>{t('Operation hash')}</div>
      <div className='mt-0.5 max-w-72 font-mono text-xs break-all whitespace-normal'>
        {props.value || t('Not available')}
      </div>
    </div>
  )
}

function ProjectionOutcomes(props: { projection: FailedBillingProjection }) {
  const { t } = useTranslation()
  const outcomes = Object.entries(props.projection.outcome).filter(
    (entry): entry is [string, string] => Boolean(entry[1])
  )
  if (outcomes.length === 0) {
    return <span className='text-muted-foreground'>{t('Not available')}</span>
  }
  return (
    <div className='flex min-w-0 flex-col gap-1'>
      {outcomes.map(([scope, outcome]) => (
        <div key={scope} className='flex min-w-0 items-center gap-2 text-xs'>
          <span className='text-muted-foreground min-w-16 capitalize'>
            {t(outcomeScopeLabels[scope] ?? 'Outcome')}
          </span>
          <Badge variant='outline' className='h-auto min-w-0 whitespace-normal'>
            {getBillingProjectionCodeDisplay(outcome, statusLabels, t)}
          </Badge>
        </div>
      ))}
    </div>
  )
}

export function FailedProjectionTable(props: {
  items: FailedBillingProjection[]
  canRequeue: boolean
  onRequeue: (projection: FailedBillingProjection) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()

  return (
    <>
      <div className='hidden rounded-lg border md:block'>
        <Table
          className='min-w-[880px]'
          scrollAreaLabel={t('Projection failures')}
        >
          <TableHeader>
            <TableRow>
              <TableHead>{t('Projection')}</TableHead>
              <TableHead>{t('Failure')}</TableHead>
              <TableHead>{t('Outcomes')}</TableHead>
              <TableHead>{t('Attempts')}</TableHead>
              <TableHead>{t('Last updated')}</TableHead>
              <TableHead className='w-32 text-right'>{t('Action')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {props.items.map((projection) => (
              <TableRow key={projection.id}>
                <TableCell className='align-top'>
                  <div className='min-w-48'>
                    <div className='font-mono text-xs font-semibold'>
                      #{projection.id}
                    </div>
                    <div className='mt-1 text-sm font-medium break-words'>
                      {getBillingProjectionCodeDisplay(
                        projection.kind,
                        kindLabels,
                        t
                      )}
                    </div>
                    <Badge
                      variant='outline'
                      className='mt-1 h-auto max-w-full break-all whitespace-normal'
                    >
                      {getBillingProjectionCodeDisplay(
                        projection.state,
                        statusLabels,
                        t
                      )}
                    </Badge>
                    <div className='text-muted-foreground mt-0.5 text-xs'>
                      {t('Reference #{{id}}', { id: projection.reference_id })}
                    </div>
                    {projection.user_id || projection.channel_id ? (
                      <div className='text-muted-foreground mt-0.5 flex flex-wrap gap-x-3 gap-y-1 text-xs'>
                        {projection.user_id ? (
                          <span>
                            {t('User')} #{projection.user_id}
                          </span>
                        ) : null}
                        {projection.channel_id ? (
                          <span>
                            {t('Channel')} #{projection.channel_id}
                          </span>
                        ) : null}
                      </div>
                    ) : null}
                    <OperationHash value={projection.operation_key_hash} />
                  </div>
                </TableCell>
                <TableCell className='max-w-72 align-top'>
                  <Badge
                    variant='destructive'
                    className='border-destructive/30 bg-destructive/10 text-foreground dark:bg-destructive/15 h-auto max-w-full break-all whitespace-normal'
                  >
                    {getBillingProjectionCodeDisplay(
                      projection.failure_code,
                      failureLabels,
                      t
                    )}
                  </Badge>
                  {projection.disposition ? (
                    <div className='text-muted-foreground mt-1 text-xs'>
                      {getBillingProjectionCodeDisplay(
                        projection.disposition,
                        statusLabels,
                        t
                      )}
                    </div>
                  ) : null}
                  <p
                    className='text-muted-foreground focus-visible:ring-ring/50 mt-2 max-h-24 overflow-auto rounded-sm text-xs leading-5 break-words whitespace-pre-wrap focus-visible:ring-2 focus-visible:outline-none'
                    tabIndex={0}
                  >
                    {projection.error || t('No sanitized error was recorded.')}
                  </p>
                </TableCell>
                <TableCell className='align-top'>
                  <ProjectionOutcomes projection={projection} />
                </TableCell>
                <TableCell className='align-top font-mono text-xs tabular-nums'>
                  {format.number(projection.attempts)}
                </TableCell>
                <TableCell className='align-top text-xs'>
                  {format.timestamp(projection.updated_time_ms)}
                </TableCell>
                <TableCell className='text-right align-top'>
                  {props.canRequeue && projection.requeueable ? (
                    <Button
                      size='sm'
                      variant='outline'
                      onClick={() => props.onRequeue(projection)}
                    >
                      <HugeiconsIcon
                        icon={DatabaseRestoreIcon}
                        data-icon='inline-start'
                      />
                      {t('Requeue')}
                    </Button>
                  ) : (
                    <Badge variant='outline'>
                      <HugeiconsIcon icon={ShieldKeyIcon} />
                      {projection.requeueable
                        ? t('Read only')
                        : t('Alternate remediation')}
                    </Badge>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      <div className='divide-y overflow-hidden rounded-lg border md:hidden'>
        {props.items.map((projection) => (
          <article
            key={projection.id}
            className='flex min-w-0 flex-col gap-3 p-3'
          >
            <div className='flex min-w-0 items-start justify-between gap-3'>
              <div className='min-w-0'>
                <div className='font-mono text-xs font-semibold'>
                  #{projection.id}
                </div>
                <div className='mt-1 text-sm font-medium break-words'>
                  {getBillingProjectionCodeDisplay(
                    projection.kind,
                    kindLabels,
                    t
                  )}
                </div>
                <Badge
                  variant='outline'
                  className='mt-1 h-auto max-w-full break-all whitespace-normal'
                >
                  {getBillingProjectionCodeDisplay(
                    projection.state,
                    statusLabels,
                    t
                  )}
                </Badge>
              </div>
              <Badge
                variant='destructive'
                className='border-destructive/30 bg-destructive/10 text-foreground dark:bg-destructive/15 h-auto max-w-full break-all whitespace-normal'
              >
                {getBillingProjectionCodeDisplay(
                  projection.failure_code,
                  failureLabels,
                  t
                )}
              </Badge>
            </div>
            <div className='text-muted-foreground text-xs leading-5 break-words whitespace-pre-wrap'>
              {projection.error || t('No sanitized error was recorded.')}
            </div>
            <ProjectionOutcomes projection={projection} />
            <OperationHash value={projection.operation_key_hash} />
            <div className='text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs'>
              <span>
                {t('Reference #{{id}}', { id: projection.reference_id })}
              </span>
              {projection.user_id ? (
                <span>
                  {t('User')} #{projection.user_id}
                </span>
              ) : null}
              {projection.channel_id ? (
                <span>
                  {t('Channel')} #{projection.channel_id}
                </span>
              ) : null}
              <span>
                {t('{{count}} attempts', { count: projection.attempts })}
              </span>
              <span>{format.timestamp(projection.updated_time_ms)}</span>
            </div>
            {props.canRequeue && projection.requeueable ? (
              <Button
                size='sm'
                variant='outline'
                className='self-start'
                onClick={() => props.onRequeue(projection)}
              >
                <HugeiconsIcon
                  icon={DatabaseRestoreIcon}
                  data-icon='inline-start'
                />
                {t('Requeue projection')}
              </Button>
            ) : null}
          </article>
        ))}
      </div>
    </>
  )
}

export function BillingConflictTable(props: {
  items: BillingLogSinkConflict[]
  canResolve: boolean
  onResolve: (conflict: BillingLogSinkConflict) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()

  return (
    <div className='divide-y overflow-hidden rounded-lg border'>
      {props.items.map((conflict) => (
        <article
          key={conflict.id}
          className='grid min-w-0 gap-3 p-3 md:grid-cols-[minmax(0,1fr)_auto] md:items-center'
        >
          <div className='min-w-0'>
            <div className='flex flex-wrap items-center gap-2'>
              <span className='font-mono text-xs font-semibold'>
                {t('Conflict #{{id}}', { id: conflict.id })}
              </span>
              <Badge
                variant='destructive'
                className='border-destructive/30 bg-destructive/10 text-foreground dark:bg-destructive/15 h-auto max-w-full break-all whitespace-normal'
              >
                {getBillingProjectionCodeDisplay(
                  conflict.state,
                  statusLabels,
                  t
                )}
              </Badge>
              <Badge variant='outline'>v{conflict.version}</Badge>
            </div>
            <div className='text-muted-foreground mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs'>
              <span>
                {t('Projection #{{id}}', { id: conflict.projection_id })}
              </span>
              <span>
                {t('{{count}} distinct receipts', {
                  count: conflict.distinct_receipts,
                })}
              </span>
              <span>
                {t('{{count}} physical rows', {
                  count: conflict.physical_rows,
                })}
              </span>
            </div>
            <div className='text-muted-foreground mt-1 text-xs'>
              {t('Detected {{first}} · Last seen {{last}}', {
                first: format.timestamp(conflict.first_detected_ms),
                last: format.timestamp(conflict.last_detected_ms),
              })}
            </div>
            <OperationHash value={conflict.operation_key_hash} />
          </div>
          {props.canResolve ? (
            <Button
              size='sm'
              variant='outline'
              className='justify-self-start md:justify-self-end'
              onClick={() => props.onResolve(conflict)}
            >
              <HugeiconsIcon
                icon={DatabaseRestoreIcon}
                data-icon='inline-start'
              />
              {t('Resolve and requeue')}
            </Button>
          ) : (
            <Badge
              variant='outline'
              className='justify-self-start md:justify-self-end'
            >
              <HugeiconsIcon icon={ShieldKeyIcon} />
              {t('Read only')}
            </Badge>
          )}
        </article>
      ))}
    </div>
  )
}
