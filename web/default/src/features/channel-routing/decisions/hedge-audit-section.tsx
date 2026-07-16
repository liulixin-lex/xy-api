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
  Award01Icon,
  Clock3Icon,
  Coins01Icon,
  GitForkIcon,
  InternetIcon,
  MapPinIcon,
  ServerStack01Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon, type IconSvgElement } from '@hugeicons/react'
import { useTranslation } from 'react-i18next'

import { ChannelRoutingStatusBadge } from '../components/status-badge'
import {
  hasCurrentRoutingAttemptCostAudit,
  routingCostUnknownReasonLabel,
} from '../lib/cost-audit'
import { useChannelRoutingFormatters } from '../lib/format'
import type { RoutingAttempt, RoutingAttemptTimeline } from '../types'

function attemptRoleLabel(
  attempt: RoutingAttempt,
  translate: (key: string, options?: Record<string, unknown>) => string
): string {
  if (attempt.role === 'primary') return translate('Primary attempt')
  if (attempt.role === 'secondary') return translate('Secondary attempt')
  return translate('Attempt {{index}}', { index: attempt.attempt_index + 1 })
}

function attemptExecutionModeLabel(
  mode: string,
  translate: (key: string) => string
): string {
  if (mode === 'serial') return translate('Serial')
  const labels: Record<string, string> = {
    failover: 'Failover',
    hedge: 'Hedge',
    primary: 'Primary',
    retry: 'Retry',
    sequential: 'Sequential',
  }
  return translate(labels[mode] ?? mode)
}

function attemptContinuationLabel(
  attempt: RoutingAttempt,
  translate: (key: string) => string
): string {
  if (attempt.will_retry) return translate('Retry scheduled')
  if (attempt.final_attempt) return translate('Final attempt')
  return translate('No retry')
}

function attemptResultLabel(
  result: string,
  translate: (key: string) => string
): string {
  const labels: Record<string, string> = {
    client_canceled: 'Client canceled',
    hedge_lost: 'Hedge lost',
    internal_error: 'Internal error',
    pending: 'Pending',
    response_too_large: 'Response too large',
    success: 'Success',
    upstream_error: 'Upstream error',
  }
  return translate(labels[result] ?? result)
}

function attemptResultStatus(result: string): string {
  if (result === 'success') return 'success'
  if (result === 'pending') return 'pending'
  if (result === 'hedge_lost' || result === 'client_canceled') return 'warning'
  return 'failed'
}

function attemptStateLabel(
  state: string,
  translate: (key: string) => string
): string {
  if (state === 'started') return translate('Started')
  if (state === 'completed') return translate('Completed')
  return state
}

function AttemptTimelineRow(props: {
  attempt: RoutingAttempt
  index: number
  currency: string
  unit: string
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const attempt = props.attempt
  const currency = attempt.cost_currency || props.currency
  const unit = attempt.cost_unit || props.unit
  const cost = (known: boolean, value: number | undefined) => {
    if (!known || value == null || !Number.isFinite(value)) return t('Unknown')
    const formatted = format.cost(value)
    return currency
      ? t('{{currency}} {{cost}}', { currency, cost: formatted })
      : formatted
  }
  const hasTokenAudit =
    attempt.actual_total_tokens != null ||
    attempt.actual_prompt_tokens != null ||
    attempt.actual_completion_tokens != null ||
    attempt.actual_cache_read_tokens != null ||
    attempt.actual_cache_write_tokens != null ||
    attempt.actual_cache_write_1h_tokens != null
  const hasOutcomeAudit =
    (attempt.http_status ?? 0) > 0 ||
    Boolean(attempt.error_classification) ||
    Boolean(attempt.error_responsibility) ||
    Boolean(attempt.error_retryability) ||
    Boolean(attempt.error_code)
  const hasChannelCostAudit = hasCurrentRoutingAttemptCostAudit(attempt)

  return (
    <li className='grid min-w-0 grid-cols-[2rem_minmax(0,1fr)] gap-3 p-3 sm:p-4'>
      <span
        className='bg-muted text-muted-foreground flex size-7 items-center justify-center rounded-full text-xs font-semibold tabular-nums'
        aria-hidden='true'
      >
        {props.index + 1}
      </span>
      <div className='min-w-0 space-y-3'>
        <div className='flex flex-wrap items-start justify-between gap-2'>
          <div className='min-w-0'>
            <h4 className='text-sm font-semibold'>
              {attemptRoleLabel(attempt, t)}
            </h4>
            <p className='text-muted-foreground mt-0.5 flex flex-wrap gap-x-2 gap-y-1 text-xs'>
              {t('Member #{{member}} · channel #{{channel}} · {{region}}', {
                member: attempt.member_id,
                channel: attempt.channel_id,
                region: attempt.region || t('Unknown region'),
              })}
            </p>
            <div className='text-muted-foreground mt-1 flex min-w-0 flex-wrap gap-x-3 gap-y-1 text-xs'>
              <span className='inline-flex min-w-0 items-center gap-1'>
                <HugeiconsIcon
                  icon={InternetIcon}
                  className='size-3 shrink-0'
                  aria-hidden='true'
                />
                <span className='break-all'>
                  {attempt.endpoint_authority || t('Endpoint unavailable')}
                </span>
              </span>
              <span className='inline-flex min-w-0 items-center gap-1'>
                <HugeiconsIcon
                  icon={ServerStack01Icon}
                  className='size-3 shrink-0'
                  aria-hidden='true'
                />
                <span className='break-all'>
                  {attempt.stable_node_known
                    ? attempt.stable_node_id || attempt.node_epoch_id
                    : attempt.node_epoch_id || t('Unknown node')}
                </span>
              </span>
            </div>
          </div>
          <div className='flex flex-wrap gap-1'>
            <ChannelRoutingStatusBadge
              status={attempt.execution_mode || 'unknown'}
              label={attemptExecutionModeLabel(attempt.execution_mode, t)}
            />
            <ChannelRoutingStatusBadge
              status={attempt.state === 'started' ? 'running' : attempt.state}
              label={attemptStateLabel(attempt.state, t)}
            />
            <ChannelRoutingStatusBadge
              status={attemptResultStatus(attempt.result)}
              label={attemptResultLabel(attempt.result, t)}
            />
            {attempt.winner ? (
              <ChannelRoutingStatusBadge status='success' label={t('Winner')} />
            ) : null}
            <ChannelRoutingStatusBadge
              status={attempt.client_committed ? 'warning' : 'ready'}
              label={
                attempt.client_committed
                  ? t('Client committed')
                  : t('Before client commit')
              }
            />
            <ChannelRoutingStatusBadge
              status={attempt.will_retry ? 'pending' : 'completed'}
              label={attemptContinuationLabel(attempt, t)}
            />
          </div>
        </div>

        <dl className='grid grid-cols-2 gap-x-4 gap-y-2 text-xs sm:grid-cols-4'>
          <div>
            <dt className='text-muted-foreground'>{t('Started')}</dt>
            <dd className='mt-0.5 font-medium'>
              {format.timestamp(attempt.started_time_ms)}
            </dd>
          </div>
          <div>
            <dt className='text-muted-foreground'>{t('First byte')}</dt>
            <dd className='mt-0.5 font-medium'>
              {format.timestamp(attempt.first_byte_time_ms)}
            </dd>
          </div>
          <div>
            <dt className='text-muted-foreground'>{t('Duration')}</dt>
            <dd className='mt-0.5 font-medium'>
              {format.milliseconds(attempt.duration_ms)}
            </dd>
          </div>
          <div>
            <dt className='text-muted-foreground'>{t('Expected cost')}</dt>
            <dd className='mt-0.5 font-medium'>
              {cost(attempt.cost_known, attempt.expected_cost)}
            </dd>
          </div>
          <div>
            <dt className='text-muted-foreground'>{t('Actual cost')}</dt>
            <dd className='mt-0.5 font-medium'>
              {cost(attempt.actual_cost_known, attempt.actual_cost)}
            </dd>
          </div>
          <div>
            <dt className='text-muted-foreground'>{t('Worst-case cost')}</dt>
            <dd className='mt-0.5 font-medium'>
              {cost(attempt.cost_known, attempt.worst_case_cost)}
            </dd>
          </div>
          <div>
            <dt className='text-muted-foreground'>{t('Effective cost')}</dt>
            <dd className='mt-0.5 font-medium'>
              {cost(attempt.cost_known, attempt.effective_cost)}
            </dd>
          </div>
          <div className='min-w-0'>
            <dt className='text-muted-foreground'>{t('Cost unit')}</dt>
            <dd className='mt-0.5 truncate font-medium' title={unit}>
              {unit || t('Unknown')}
            </dd>
          </div>
          <div>
            <dt className='text-muted-foreground'>{t('Completed')}</dt>
            <dd className='mt-0.5 font-medium'>
              {format.timestamp(attempt.completed_time_ms)}
            </dd>
          </div>
          <div>
            <dt className='text-muted-foreground'>{t('Upstream request')}</dt>
            <dd className='mt-0.5 font-medium'>
              {attempt.upstream_sent ? t('Sent') : t('Not sent')}
            </dd>
          </div>
        </dl>

        {hasChannelCostAudit ? (
          <dl className='grid grid-cols-2 gap-x-4 gap-y-2 border-t pt-3 text-xs sm:grid-cols-3 lg:grid-cols-6'>
            <div>
              <dt className='text-muted-foreground'>
                {t('1× baseline expected cost')}
              </dt>
              <dd className='mt-0.5 font-medium'>
                {cost(
                  attempt.baseline_expected_known === true,
                  attempt.baseline_expected_cost
                )}
              </dd>
            </div>
            <div>
              <dt className='text-muted-foreground'>
                {t('1× baseline worst-case cost')}
              </dt>
              <dd className='mt-0.5 font-medium'>
                {cost(
                  attempt.baseline_worst_case_known === true,
                  attempt.baseline_worst_case_cost
                )}
              </dd>
            </div>
            <div>
              <dt className='text-muted-foreground'>
                {t('Channel multiplier')}
              </dt>
              <dd className='mt-0.5 font-medium'>
                {typeof attempt.upstream_cost_multiplier === 'number' &&
                Number.isFinite(attempt.upstream_cost_multiplier)
                  ? `${format.cost(attempt.upstream_cost_multiplier)}×`
                  : t('Unknown')}
              </dd>
            </div>
            <div>
              <dt className='text-muted-foreground'>
                {t('Configuration revision')}
              </dt>
              <dd className='mt-0.5 font-medium'>
                {(attempt.configuration_revision ?? 0) > 0
                  ? format.number(attempt.configuration_revision ?? 0)
                  : t('Unknown')}
              </dd>
            </div>
            <div className='min-w-0'>
              <dt className='text-muted-foreground'>{t('Pricing basis')}</dt>
              <dd
                className='mt-0.5 truncate font-medium'
                title={attempt.pricing_basis}
              >
                {attempt.pricing_basis || t('Unknown')}
              </dd>
            </div>
            <div className='min-w-0'>
              <dt className='text-muted-foreground'>{t('Pricing identity')}</dt>
              <dd
                className='mt-0.5 truncate font-medium'
                title={attempt.pricing_identity}
              >
                {format.shortHash(attempt.pricing_identity)}
              </dd>
            </div>
            {attempt.unknown_reason ? (
              <div className='col-span-full min-w-0'>
                <dt className='text-muted-foreground'>{t('Unknown reason')}</dt>
                <dd
                  className='mt-0.5 font-medium break-words'
                  title={attempt.unknown_reason}
                >
                  {routingCostUnknownReasonLabel(attempt.unknown_reason, t)}
                </dd>
              </div>
            ) : null}
          </dl>
        ) : (
          <p className='text-muted-foreground border-t pt-3 text-xs'>
            {t(
              'Channel multiplier audit was not recorded for this historical decision.'
            )}
          </p>
        )}

        {hasTokenAudit ? (
          <dl className='grid grid-cols-2 gap-x-4 gap-y-2 border-t pt-3 text-xs sm:grid-cols-3 lg:grid-cols-6'>
            {[
              [t('Total tokens'), attempt.actual_total_tokens],
              [t('Prompt tokens'), attempt.actual_prompt_tokens],
              [t('Output tokens'), attempt.actual_completion_tokens],
              [t('Cache read'), attempt.actual_cache_read_tokens],
              [t('Cache write'), attempt.actual_cache_write_tokens],
              [t('Cache write 1h'), attempt.actual_cache_write_1h_tokens],
            ].map(([label, value]) => (
              <div key={String(label)}>
                <dt className='text-muted-foreground'>{label}</dt>
                <dd className='mt-0.5 font-medium tabular-nums'>
                  {format.number(Number(value ?? 0))}
                </dd>
              </div>
            ))}
          </dl>
        ) : null}

        {hasOutcomeAudit ? (
          <dl className='grid grid-cols-2 gap-x-4 gap-y-2 border-t pt-3 text-xs sm:grid-cols-5'>
            <div>
              <dt className='text-muted-foreground'>{t('HTTP status')}</dt>
              <dd className='mt-0.5 font-medium tabular-nums'>
                {attempt.http_status || t('Unknown')}
              </dd>
            </div>
            <div className='min-w-0'>
              <dt className='text-muted-foreground'>{t('Classification')}</dt>
              <dd
                className='mt-0.5 truncate font-mono font-medium'
                title={attempt.error_classification}
              >
                {attempt.error_classification || t('None')}
              </dd>
            </div>
            <div className='min-w-0'>
              <dt className='text-muted-foreground'>{t('Responsibility')}</dt>
              <dd
                className='mt-0.5 truncate font-mono font-medium'
                title={attempt.error_responsibility}
              >
                {attempt.error_responsibility || t('None')}
              </dd>
            </div>
            <div className='min-w-0'>
              <dt className='text-muted-foreground'>{t('Retryability')}</dt>
              <dd
                className='mt-0.5 truncate font-mono font-medium'
                title={attempt.error_retryability}
              >
                {attempt.error_retryability || t('None')}
              </dd>
            </div>
            <div className='min-w-0'>
              <dt className='text-muted-foreground'>{t('Error code')}</dt>
              <dd
                className='mt-0.5 truncate font-mono font-medium'
                title={attempt.error_code}
              >
                {attempt.error_code || t('None')}
              </dd>
            </div>
          </dl>
        ) : null}
      </div>
    </li>
  )
}

export function ChannelRoutingAttemptTimelineSection(props: {
  timeline: RoutingAttemptTimeline
  title?: string
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const timeline = props.timeline
  const currency = timeline.cost_currency || ''
  const unit = timeline.cost_unit || ''
  const hasHedge = timeline.attempts.some(
    (attempt) =>
      attempt.execution_mode === 'hedge' || attempt.role === 'secondary'
  )
  const finalNode = timeline.final_stable_node_known
    ? timeline.final_stable_node_id || timeline.final_node_epoch_id
    : timeline.final_node_epoch_id
  const cost = (known: boolean, value: number | undefined) => {
    if (!known || value == null || !Number.isFinite(value)) return t('Unknown')
    const formatted = format.cost(value)
    return currency
      ? t('{{currency}} {{cost}}', { currency, cost: formatted })
      : formatted
  }
  const summary: Array<{
    icon: IconSvgElement
    label: string
    value: string
  }> = [
    {
      icon: Coins01Icon,
      label: t('Estimated total'),
      value: cost(
        timeline.estimated_total_cost_known,
        timeline.estimated_total_cost
      ),
    },
    {
      icon: Coins01Icon,
      label: t('Worst-case total'),
      value: cost(
        timeline.worst_case_total_cost_known,
        timeline.worst_case_total_cost
      ),
    },
    {
      icon: Award01Icon,
      label: t('Actual total'),
      value: cost(timeline.actual_total_cost_known, timeline.actual_total_cost),
    },
  ]
  if (
    hasHedge ||
    timeline.duplicate_expected_cost_known ||
    timeline.duplicate_worst_case_cost_known ||
    timeline.duplicate_actual_cost_known
  ) {
    summary.push({
      icon: GitForkIcon,
      label: t('Duplicate expected'),
      value: cost(
        timeline.duplicate_expected_cost_known,
        timeline.duplicate_expected_cost
      ),
    })
    summary.push({
      icon: GitForkIcon,
      label: t('Duplicate worst-case'),
      value: cost(
        timeline.duplicate_worst_case_cost_known,
        timeline.duplicate_worst_case_cost
      ),
    })
    summary.push({
      icon: Award01Icon,
      label: t('Duplicate actual'),
      value: cost(
        timeline.duplicate_actual_cost_known,
        timeline.duplicate_actual_cost
      ),
    })
  }

  return (
    <section aria-labelledby='attempt-timeline-title'>
      <div className='mb-2 flex flex-wrap items-start justify-between gap-2'>
        <div>
          <h3
            id='attempt-timeline-title'
            className='flex items-center gap-2 text-sm font-semibold'
          >
            <HugeiconsIcon
              icon={GitForkIcon}
              className='size-4'
              aria-hidden='true'
            />
            {props.title || t('Attempt timeline')}
          </h3>
          <p className='text-muted-foreground mt-0.5 text-xs'>
            {unit
              ? t('Attempt costs use {{currency}} · {{unit}}.', {
                  currency: currency || t('Unknown currency'),
                  unit,
                })
              : t('Request attempts, commit boundaries, and error ownership.')}
          </p>
          {timeline.final_channel_id ? (
            <p className='text-muted-foreground mt-1 inline-flex items-center gap-1 text-xs'>
              <HugeiconsIcon
                icon={MapPinIcon}
                className='size-3'
                aria-hidden='true'
              />
              {t(
                'Final route: member #{{member}} · channel #{{channel}} · {{region}}',
                {
                  member: timeline.final_member_id,
                  channel: timeline.final_channel_id,
                  region: timeline.final_region || t('Unknown region'),
                }
              )}
            </p>
          ) : null}
          {finalNode ? (
            <p className='text-muted-foreground mt-1 inline-flex min-w-0 items-center gap-1 text-xs'>
              <HugeiconsIcon
                icon={ServerStack01Icon}
                className='size-3 shrink-0'
                aria-hidden='true'
              />
              <span className='break-all'>
                {t('Final node: {{node}}', { node: finalNode })}
              </span>
            </p>
          ) : null}
        </div>
        <div className='flex flex-wrap gap-1'>
          {hasHedge ? (
            <ChannelRoutingStatusBadge
              status='known'
              label={t('Hedge execution audit')}
            />
          ) : null}
          {timeline.final_result ? (
            <ChannelRoutingStatusBadge
              status={attemptResultStatus(timeline.final_result)}
              label={attemptResultLabel(timeline.final_result, t)}
            />
          ) : null}
          <ChannelRoutingStatusBadge
            status={timeline.all_attempts_completed ? 'completed' : 'running'}
            label={
              timeline.all_attempts_completed
                ? t('All attempts completed')
                : t('Attempts still running')
            }
          />
          <ChannelRoutingStatusBadge
            status={timeline.attempts_truncated ? 'warning' : 'full'}
            label={t('{{count}} attempts', { count: timeline.attempt_count })}
          />
        </div>
      </div>

      <dl className='bg-border grid grid-cols-2 gap-px overflow-hidden rounded-lg border lg:grid-cols-3'>
        {summary.map((item) => {
          return (
            <div key={item.label} className='bg-background min-w-0 p-3'>
              <dt className='text-muted-foreground flex items-center gap-1.5 text-xs'>
                <HugeiconsIcon
                  icon={item.icon}
                  className='size-3.5'
                  aria-hidden='true'
                />
                <span>{item.label}</span>
              </dt>
              <dd className='mt-1 font-medium break-words'>{item.value}</dd>
            </div>
          )
        })}
      </dl>

      {timeline.attempts.length > 0 ? (
        <ol className='mt-3 divide-y rounded-lg border'>
          {timeline.attempts.map((attempt, index) => (
            <AttemptTimelineRow
              key={`${attempt.attempt_index}-${attempt.role}-${attempt.member_id}-${attempt.started_time_ms}`}
              attempt={attempt}
              index={index}
              currency={currency}
              unit={unit}
            />
          ))}
        </ol>
      ) : (
        <div className='text-muted-foreground mt-3 flex items-center gap-2 rounded-lg border p-3 text-sm'>
          <HugeiconsIcon
            icon={Clock3Icon}
            className='size-4'
            aria-hidden='true'
          />
          {t('No retained attempts are available.')}
        </div>
      )}

      {timeline.attempts_truncated ? (
        <p className='text-muted-foreground mt-2 text-xs'>
          {t(
            'The retained attempt timeline is truncated; aggregate totals include all recorded attempts.'
          )}
        </p>
      ) : null}
    </section>
  )
}
