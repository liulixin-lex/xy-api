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
import {
  hasCurrentChannelCostAudit,
  routingCostUnknownReasonLabel,
} from '../lib/cost-audit'
import { useChannelRoutingFormatters } from '../lib/format'
import type { RoutingCostEstimate } from '../types'

function RoutingCostEstimateColumn(props: {
  title: string
  estimate: RoutingCostEstimate
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const estimate = props.estimate
  const currency = estimate.currency || ''
  const unit = estimate.unit || ''
  const hasChannelCostAudit = hasCurrentChannelCostAudit(estimate)
  const value = (known: boolean | undefined, amount: number | undefined) =>
    known && typeof amount === 'number' && Number.isFinite(amount)
      ? t('{{currency}} {{cost}}', {
          currency,
          cost: format.cost(amount),
        })
      : t('Unknown')

  return (
    <div className='min-w-0 space-y-3 p-3 sm:p-4'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <h4 className='text-sm font-medium'>{props.title}</h4>
        <div className='flex flex-wrap gap-1'>
          <ChannelRoutingStatusBadge
            status={estimate.known ? 'known' : 'unknown'}
          />
          {estimate.version_confidence ? (
            <ChannelRoutingStatusBadge status={estimate.version_confidence} />
          ) : null}
          {estimate.freshness ? (
            <ChannelRoutingStatusBadge status={estimate.freshness} />
          ) : null}
        </div>
      </div>

      <dl className='grid grid-cols-1 gap-x-4 gap-y-3 text-sm sm:grid-cols-3 lg:grid-cols-1 xl:grid-cols-3'>
        <div>
          <dt className='text-muted-foreground text-xs'>
            {t('Expected cost')}
          </dt>
          <dd className='mt-1 font-medium'>
            {value(estimate.known, estimate.cost)}
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground text-xs'>
            {t('Worst-case cost')}
          </dt>
          <dd className='mt-1 font-medium'>
            {value(estimate.worst_case_known, estimate.worst_case_cost)}
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground text-xs'>
            {t('Expected effective cost')}
          </dt>
          <dd className='mt-1 font-medium'>
            {value(estimate.effective_known, estimate.effective_cost)}
          </dd>
        </div>
        {hasChannelCostAudit ? (
          <>
            <div>
              <dt className='text-muted-foreground text-xs'>
                {t('1× baseline expected cost')}
              </dt>
              <dd className='mt-1 font-medium'>
                {value(
                  estimate.baseline_expected_known,
                  estimate.baseline_expected_cost
                )}
              </dd>
            </div>
            <div>
              <dt className='text-muted-foreground text-xs'>
                {t('1× baseline worst-case cost')}
              </dt>
              <dd className='mt-1 font-medium'>
                {value(
                  estimate.baseline_worst_case_known,
                  estimate.baseline_worst_case_cost
                )}
              </dd>
            </div>
          </>
        ) : null}
      </dl>

      <dl className='grid grid-cols-2 gap-x-4 gap-y-2 border-t pt-3 text-xs'>
        <div className='min-w-0'>
          <dt className='text-muted-foreground'>{t('Unit')}</dt>
          <dd className='mt-0.5 truncate font-medium' title={unit}>
            {unit || t('Unknown')}
          </dd>
        </div>
        <div className='min-w-0'>
          <dt className='text-muted-foreground'>{t('Pricing basis')}</dt>
          <dd
            className='mt-0.5 truncate font-medium'
            title={estimate.pricing_basis}
          >
            {estimate.pricing_basis || t('Unknown')}
          </dd>
        </div>
        {estimate.pricing_version !== estimate.pricing_identity ? (
          <div className='min-w-0'>
            <dt className='text-muted-foreground'>{t('Pricing version')}</dt>
            <dd
              className='mt-0.5 truncate font-medium'
              title={estimate.pricing_version}
            >
              {estimate.pricing_version || t('Unknown')}
            </dd>
          </div>
        ) : null}
        {hasChannelCostAudit ? (
          <div className='min-w-0'>
            <dt className='text-muted-foreground'>{t('Pricing identity')}</dt>
            <dd
              className='mt-0.5 truncate font-medium'
              title={estimate.pricing_identity}
            >
              {format.shortHash(estimate.pricing_identity)}
            </dd>
          </div>
        ) : null}
        <div className='min-w-0'>
          <dt className='text-muted-foreground'>{t('Pricing hash')}</dt>
          <dd
            className='mt-0.5 truncate font-medium'
            title={estimate.pricing_hash}
          >
            {format.shortHash(estimate.pricing_hash)}
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>{t('Confidence score')}</dt>
          <dd className='mt-0.5 font-medium'>
            {format.percent(estimate.confidence_score)}
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>{t('Freshness score')}</dt>
          <dd className='mt-0.5 font-medium'>
            {format.percent(estimate.freshness_score)}
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>{t('Effective')}</dt>
          <dd className='mt-0.5 font-medium'>
            {format.timestamp(estimate.effective_time)}
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>{t('Expires')}</dt>
          <dd className='mt-0.5 font-medium'>
            {format.timestamp(estimate.expires_time)}
          </dd>
        </div>
        {hasChannelCostAudit ? (
          <>
            <div>
              <dt className='text-muted-foreground'>
                {t('Configuration revision')}
              </dt>
              <dd className='mt-0.5 font-medium'>
                {(estimate.configuration_revision ?? 0) > 0
                  ? format.number(estimate.configuration_revision ?? 0)
                  : t('Unknown')}
              </dd>
            </div>
            <div>
              <dt className='text-muted-foreground'>
                {t('Channel multiplier')}
              </dt>
              <dd className='mt-0.5 font-medium'>
                {Number.isFinite(estimate.upstream_cost_multiplier)
                  ? `${format.cost(estimate.upstream_cost_multiplier)}×`
                  : t('Unknown')}
              </dd>
            </div>
          </>
        ) : null}
        {estimate.unknown_reason ? (
          <div className='col-span-full min-w-0'>
            <dt className='text-muted-foreground'>{t('Unknown reason')}</dt>
            <dd
              className='mt-0.5 font-medium break-words'
              title={estimate.unknown_reason}
            >
              {routingCostUnknownReasonLabel(estimate.unknown_reason, t)}
            </dd>
          </div>
        ) : null}
      </dl>
      {!hasChannelCostAudit ? (
        <p className='text-muted-foreground border-t pt-3 text-xs'>
          {t(
            'Channel multiplier audit was not recorded for this historical decision.'
          )}
        </p>
      ) : null}
    </div>
  )
}

export function ChannelRoutingDecisionCostSection(props: {
  actual?: RoutingCostEstimate
  observed?: RoutingCostEstimate
}) {
  const { t } = useTranslation()
  if (!props.actual && !props.observed) return null

  return (
    <section aria-labelledby='decision-cost-audit-title'>
      <div className='mb-2'>
        <h3 id='decision-cost-audit-title' className='text-sm font-semibold'>
          {t('Route cost estimates')}
        </h3>
      </div>
      <div className='divide-y rounded-lg border lg:grid lg:grid-cols-2 lg:divide-x lg:divide-y-0'>
        {props.actual ? (
          <RoutingCostEstimateColumn
            title={t('Actual route estimate')}
            estimate={props.actual}
          />
        ) : null}
        {props.observed ? (
          <RoutingCostEstimateColumn
            title={t('Selected route estimate')}
            estimate={props.observed}
          />
        ) : null}
      </div>
    </section>
  )
}
