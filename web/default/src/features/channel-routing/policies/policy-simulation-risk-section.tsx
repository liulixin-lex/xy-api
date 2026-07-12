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

import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import type { PolicySimulationRiskAssessment } from '../types'

function simulationRiskStateLabel(
  state: string | undefined,
  translate: (key: string) => string
): string {
  if (state === 'pass') return translate('Passed')
  if (state === 'fail') return translate('Blocked')
  return translate('Needs review')
}

function simulationRiskReasonLabel(
  reason: string,
  translate: (key: string) => string
): string {
  switch (reason) {
    case 'slo_degradation_detected':
      return translate('SLO degradation detected')
    case 'slo_tradeoff_requires_review':
      return translate('SLO tradeoff requires review')
    case 'slo_evidence_incomplete':
      return translate('SLO evidence is incomplete')
    case 'capacity_insufficient':
      return translate('Capacity is insufficient')
    case 'capacity_evidence_incomplete':
      return translate('Capacity evidence is incomplete')
    case 'traffic_change_rate_limit_unconfigured':
      return translate('Traffic change-rate limit is not configured')
    case 'affected_model_scope_incomplete':
      return translate('Affected model scope is incomplete')
    case 'changed_pools_not_simulated':
      return translate('Some changed routing groups were not simulated')
    default:
      return reason
  }
}

export function ChannelRoutingPolicySimulationRiskSection(props: {
  risk?: PolicySimulationRiskAssessment | null
  compact?: boolean
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const risk = props.risk
  const state = risk?.state || 'unknown'
  const structuralChanges = risk
    ? [
        [t('Added groups'), risk.changes.added_pools],
        [t('Removed groups'), risk.changes.removed_pools],
        [t('Policy changes'), risk.changes.policy_changes],
        [t('Added members'), risk.changes.added_members],
        [t('Removed members'), risk.changes.removed_members],
        [t('Changed members'), risk.changes.changed_members],
      ].filter((item) => Number(item[1]) > 0)
    : []
  let description = t(
    'Evidence is incomplete. Review the unknown items before publishing.'
  )
  if (state === 'fail') {
    description = t('Publishing is blocked by confirmed SLO or capacity risk.')
  } else if (state === 'pass') {
    description = t('The simulated change passed all available risk checks.')
  }

  return (
    <section
      className='space-y-3 border-t pt-4'
      aria-labelledby='policy-risk-title'
    >
      <div className='flex flex-wrap items-start justify-between gap-2'>
        <div>
          <h3 id='policy-risk-title' className='text-sm font-semibold'>
            {t('Simulation risk')}
          </h3>
          <p className='text-muted-foreground mt-0.5 text-xs text-pretty'>
            {description}
          </p>
        </div>
        <ChannelRoutingStatusBadge
          status={state}
          label={simulationRiskStateLabel(state, t)}
        />
      </div>

      {!risk ? (
        <p className='border-border bg-muted/40 text-muted-foreground rounded-md border px-3 py-2 text-sm'>
          {t('No simulation risk result is available for this draft.')}
        </p>
      ) : (
        <>
          <dl className='bg-border grid grid-cols-1 gap-px overflow-hidden rounded-lg border sm:grid-cols-3'>
            <div className='bg-background p-3'>
              <dt className='flex items-center justify-between gap-2 text-xs font-medium'>
                <span>{t('SLO impact')}</span>
                <ChannelRoutingStatusBadge
                  status={risk.slo.state}
                  label={simulationRiskStateLabel(risk.slo.state, t)}
                />
              </dt>
              <dd className='text-muted-foreground mt-2 grid gap-1 text-xs'>
                <span>
                  {t('Evidence')}: {risk.slo.known_samples} /{' '}
                  {risk.slo.total_samples}
                </span>
                <span>
                  {t('Success-rate delta')}:{' '}
                  {risk.slo.average_success_rate_delta == null
                    ? t('Unknown')
                    : `${format.number(risk.slo.average_success_rate_delta * 100)}%`}
                </span>
                <span>
                  {t('Latency delta')}:{' '}
                  {format.milliseconds(risk.slo.average_latency_delta_ms)}
                </span>
              </dd>
            </div>
            <div className='bg-background p-3'>
              <dt className='flex items-center justify-between gap-2 text-xs font-medium'>
                <span>{t('Capacity')}</span>
                <ChannelRoutingStatusBadge
                  status={risk.capacity.state}
                  label={simulationRiskStateLabel(risk.capacity.state, t)}
                />
              </dt>
              <dd className='text-muted-foreground mt-2 grid gap-1 text-xs'>
                <span>
                  {t('Evidence')}: {risk.capacity.known_samples} /{' '}
                  {risk.capacity.total_samples}
                </span>
                <span>
                  {t('Exceeded samples')}:{' '}
                  {format.number(risk.capacity.exceeded_samples)}
                </span>
                <span>
                  {t('Max utilization')}:{' '}
                  {format.percent(risk.capacity.max_observed_utilization)}
                </span>
              </dd>
            </div>
            <div className='bg-background p-3'>
              <dt className='flex items-center justify-between gap-2 text-xs font-medium'>
                <span>{t('Traffic change')}</span>
                <ChannelRoutingStatusBadge
                  status={risk.traffic_change_rate.state}
                  label={simulationRiskStateLabel(
                    risk.traffic_change_rate.state,
                    t
                  )}
                />
              </dt>
              <dd className='text-muted-foreground mt-2 grid gap-1 text-xs'>
                <span>
                  {t('Estimated selection change')}:{' '}
                  {format.percent(
                    risk.traffic_change_rate.estimated_selection_change_rate
                  )}
                </span>
                <span>
                  {t('Configured limit')}:{' '}
                  {format.percent(
                    risk.traffic_change_rate.configured_rate_limit
                  )}
                </span>
              </dd>
            </div>
          </dl>

          {risk.reasons.length > 0 ? (
            <div>
              <h4 className='text-xs font-semibold'>{t('Review items')}</h4>
              <ul className='text-muted-foreground mt-1 grid gap-1 text-xs sm:grid-cols-2'>
                {risk.reasons.map((reason) => (
                  <li key={reason}>
                    <span aria-hidden='true'>- </span>
                    {simulationRiskReasonLabel(reason, t)}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}

          {!props.compact ? (
            <div className='grid gap-3 text-xs sm:grid-cols-2'>
              <div>
                <h4 className='font-semibold'>{t('Affected scope')}</h4>
                <dl className='text-muted-foreground mt-1 grid gap-1'>
                  <div>
                    <dt className='inline'>{t('Groups')}: </dt>
                    <dd className='inline break-words'>
                      {risk.scope.affected_pool_ids.length > 0
                        ? risk.scope.affected_pool_ids
                            .map((id) => `#${id}`)
                            .join(', ')
                        : t('None')}
                    </dd>
                  </div>
                  <div>
                    <dt className='inline'>{t('Channels')}: </dt>
                    <dd className='inline break-words'>
                      {risk.scope.affected_channel_ids.length > 0
                        ? risk.scope.affected_channel_ids
                            .map((id) => `#${id}`)
                            .join(', ')
                        : t('None')}
                    </dd>
                  </div>
                  <div>
                    <dt className='inline'>{t('Models')}: </dt>
                    <dd className='inline break-words'>
                      {risk.scope.affected_models.length > 0
                        ? risk.scope.affected_models.join(', ')
                        : t('Unknown')}
                    </dd>
                  </div>
                  <div>
                    <dt className='inline'>{t('Unsimulated groups')}: </dt>
                    <dd className='inline break-words'>
                      {risk.scope.unsimulated_pool_ids.length > 0
                        ? risk.scope.unsimulated_pool_ids
                            .map((id) => `#${id}`)
                            .join(', ')
                        : t('None')}
                    </dd>
                  </div>
                </dl>
              </div>
              <div>
                <h4 className='font-semibold'>{t('Structural changes')}</h4>
                {structuralChanges.length > 0 ? (
                  <dl className='text-muted-foreground mt-1 grid grid-cols-2 gap-x-3 gap-y-1'>
                    {structuralChanges.map(([label, count]) => (
                      <div
                        key={String(label)}
                        className='flex justify-between gap-2'
                      >
                        <dt>{label}</dt>
                        <dd className='font-medium tabular-nums'>{count}</dd>
                      </div>
                    ))}
                  </dl>
                ) : (
                  <p className='text-muted-foreground mt-1'>
                    {t('No traffic-affecting structural changes')}
                  </p>
                )}
              </div>
            </div>
          ) : null}
        </>
      )}
    </section>
  )
}
