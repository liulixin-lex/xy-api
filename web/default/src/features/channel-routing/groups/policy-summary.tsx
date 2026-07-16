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
import { Progress, ProgressLabel } from '@/components/ui/progress'

import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import type { PoolSnapshot } from '../types'

function recordValue(
  value: Record<string, unknown>,
  key: string
): Record<string, unknown> {
  const nested = value[key]
  if (nested == null || typeof nested !== 'object' || Array.isArray(nested)) {
    return {}
  }
  return nested as Record<string, unknown>
}

function finiteNumber(
  value: Record<string, unknown>,
  key: string,
  fallback = 0
): number {
  const candidate = value[key]
  return typeof candidate === 'number' && Number.isFinite(candidate)
    ? candidate
    : fallback
}

function booleanValue(
  value: Record<string, unknown>,
  key: string,
  fallback = false
): boolean {
  const candidate = value[key]
  return typeof candidate === 'boolean' ? candidate : fallback
}

export function ChannelRoutingGroupPolicySummary(props: {
  group: PoolSnapshot
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const balanced = props.group.balanced_policy
  const selector = props.group.selector_policy
  const canary = props.group.canary_policy
  const slowStart = recordValue(canary, 'slow_start')
  const weights = [
    {
      key: 'availability',
      label: t('Availability'),
      value: finiteNumber(balanced, 'weight_availability'),
    },
    {
      key: 'latency',
      label: t('Latency'),
      value: finiteNumber(balanced, 'weight_latency'),
    },
    {
      key: 'throughput',
      label: t('Throughput'),
      value: finiteNumber(balanced, 'weight_throughput'),
    },
    {
      key: 'cost',
      label: t('Cost'),
      value: finiteNumber(balanced, 'weight_cost'),
    },
  ]
  const weightTotal = weights.reduce(
    (total, item) => total + Math.max(0, item.value),
    0
  )
  const normalizedWeights = weights.map((item) => ({
    ...item,
    normalized: weightTotal > 0 ? Math.max(0, item.value) / weightTotal : 0,
  }))
  const exploration =
    finiteNumber(balanced, 'exploration_basis_points') / 10_000
  const minimumSlowStart = finiteNumber(slowStart, 'minimum_factor')
  const slowStartSeconds = finiteNumber(slowStart, 'ramp_seconds')
  const requireKnownCost = booleanValue(balanced, 'require_known_cost')
  const hedgingEnabled = booleanValue(canary, 'hedging_enabled')

  return (
    <section
      className='overflow-hidden rounded-lg border'
      aria-labelledby='routing-group-policy-summary-title'
    >
      <div className='flex flex-wrap items-start justify-between gap-3 border-b px-3 py-3 sm:px-4'>
        <div className='min-w-0'>
          <h2
            id='routing-group-policy-summary-title'
            className='text-sm font-semibold'
          >
            {t('Current policy')}
          </h2>
          <p className='text-muted-foreground mt-0.5 text-xs'>
            {t(
              'Resolved scoring, cost preference, exploration, and safety controls for this routing group.'
            )}
          </p>
        </div>
        <div className='flex flex-wrap gap-1.5'>
          <ChannelRoutingStatusBadge
            status={props.group.policy_profile}
            label={props.group.policy_profile}
          />
          <ChannelRoutingStatusBadge status={props.group.deployment_stage} />
        </div>
      </div>

      <div className='lg:grid lg:grid-cols-[minmax(0,1.25fr)_minmax(18rem,0.75fr)] lg:divide-x'>
        <div className='space-y-3 p-3 sm:p-4'>
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <h3 className='text-sm font-medium'>
              {t('Normalized routing weights')}
            </h3>
            <span className='text-muted-foreground text-xs'>
              {t('Total {{value}}', { value: format.number(weightTotal) })}
            </span>
          </div>
          <div className='grid gap-3 sm:grid-cols-2'>
            {normalizedWeights.map((item) => (
              <Progress
                key={item.key}
                value={item.normalized * 100}
                aria-label={t('{{label}} routing weight {{value}}', {
                  label: item.label,
                  value: format.percent(item.normalized),
                })}
                className='gap-1.5'
              >
                <ProgressLabel className='text-xs'>{item.label}</ProgressLabel>
                <span className='text-muted-foreground ml-auto text-xs tabular-nums'>
                  {format.percent(item.normalized)}
                </span>
              </Progress>
            ))}
          </div>
          <div className='flex flex-wrap gap-1.5 pt-1'>
            <Badge variant='outline'>
              {t('Cost bias')}:{' '}
              {format.percent(normalizedWeights[3].normalized)}
            </Badge>
            <Badge variant='outline'>
              {t('Availability floor')}:{' '}
              {format.percent(
                finiteNumber(balanced, 'availability_floor') ||
                  finiteNumber(selector, 'availability_floor')
              )}
            </Badge>
            <Badge variant='outline'>
              {t('Maximum capacity utilization')}:{' '}
              {format.percent(
                finiteNumber(balanced, 'max_capacity_utilization')
              )}
            </Badge>
          </div>
        </div>

        <div className='border-t p-3 sm:p-4 lg:border-t-0'>
          <h3 className='text-sm font-medium'>{t('Policy controls')}</h3>
          <dl className='mt-3 grid grid-cols-2 gap-x-4 gap-y-3 text-xs'>
            <div>
              <dt className='text-muted-foreground'>{t('Exploration')}</dt>
              <dd className='mt-1 font-medium'>
                {format.percent(exploration)}
              </dd>
            </div>
            <div>
              <dt className='text-muted-foreground'>{t('Slow start')}</dt>
              <dd className='mt-1 font-medium'>
                {t('{{factor}} minimum · {{seconds}} seconds', {
                  factor: format.percent(minimumSlowStart),
                  seconds: format.number(slowStartSeconds),
                })}
              </dd>
            </div>
            <div>
              <dt className='text-muted-foreground'>{t('Known costs')}</dt>
              <dd className='mt-1'>
                <ChannelRoutingStatusBadge
                  status={requireKnownCost ? 'required' : 'optional'}
                  label={requireKnownCost ? t('Required') : t('Optional')}
                />
              </dd>
            </div>
            <div>
              <dt className='text-muted-foreground'>{t('Hedge')}</dt>
              <dd className='mt-1'>
                <ChannelRoutingStatusBadge
                  status={hedgingEnabled ? 'enabled' : 'disabled'}
                  label={hedgingEnabled ? t('Enabled') : t('Disabled')}
                />
              </dd>
            </div>
            <div>
              <dt className='text-muted-foreground'>{t('Candidate limit')}</dt>
              <dd className='mt-1 font-medium'>
                {format.number(finiteNumber(selector, 'top_k'))}
              </dd>
            </div>
            <div>
              <dt className='text-muted-foreground'>{t('Maximum ejected')}</dt>
              <dd className='mt-1 font-medium'>
                {format.percent(
                  finiteNumber(selector, 'max_ejected_pct') / 100
                )}
              </dd>
            </div>
          </dl>
        </div>
      </div>
    </section>
  )
}
