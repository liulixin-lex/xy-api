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
import { Skeleton } from '@/components/ui/skeleton'
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
  routingConfiguredCostRates,
  routingCostContractModeLabel,
} from '../lib/cost-dimensions'
import { useChannelRoutingFormatters } from '../lib/format'
import type {
  ChannelRoutingCostDetailResponse,
  RoutingCostCatalogMember,
  RoutingCostCatalogModel,
} from '../types'

export function CostContractDetail(props: {
  member: RoutingCostCatalogMember | undefined
  model: RoutingCostCatalogModel | undefined
  detail: ChannelRoutingCostDetailResponse | undefined
  loading: boolean
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()

  if (!props.model) {
    return (
      <Empty className='min-h-56 rounded-none border-0'>
        <EmptyHeader>
          <EmptyTitle>{t('No model selected')}</EmptyTitle>
          <EmptyDescription>
            {t('Select a model to inspect its complete pricing contract.')}
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    )
  }
  if (props.loading && !props.detail) {
    return (
      <div
        className='flex flex-col gap-3 p-4'
        role='status'
        aria-live='polite'
        aria-busy='true'
      >
        <span className='sr-only'>{t('Loading')}</span>
        <Skeleton className='h-5 w-2/3 motion-reduce:animate-none' />
        <Skeleton className='h-16 motion-reduce:animate-none' />
        <Skeleton className='h-36 motion-reduce:animate-none' />
      </div>
    )
  }

  const item = props.detail?.item
  const pricing = item?.pricing
  const multiplier = props.model.upstream_cost_multiplier
  const rates = pricing ? routingConfiguredCostRates(pricing) : []

  return (
    <div className='flex flex-col gap-4 p-4'>
      <div className='flex flex-col gap-2'>
        <div className='flex flex-wrap items-start justify-between gap-2'>
          <div className='min-w-0 flex-1'>
            <h3 className='text-sm font-semibold break-all'>
              {props.model.model_name}
            </h3>
            {props.model.upstream_model_name ? (
              <p className='text-muted-foreground text-xs break-all'>
                {props.model.upstream_model_name}
              </p>
            ) : null}
          </div>
          <ChannelRoutingStatusBadge
            status={props.model.known ? 'known' : 'unknown'}
          />
        </div>
        <div className='flex flex-wrap gap-1.5'>
          {props.model.contract_mode ? (
            <Badge variant='outline'>
              {t(routingCostContractModeLabel(props.model.contract_mode))}
            </Badge>
          ) : null}
          {props.model.billing_mode ? (
            <Badge variant='outline'>
              {format.billingMode(props.model.billing_mode)}
            </Badge>
          ) : null}
          <Badge variant='outline'>
            {t('{{value}}× channel multiplier', {
              value: format.cost(multiplier),
            })}
          </Badge>
        </div>
      </div>

      <dl className='grid gap-x-4 gap-y-3 border-y py-3 text-xs sm:grid-cols-2'>
        <div className='min-w-0'>
          <dt className='text-muted-foreground'>{t('Routing generation')}</dt>
          <dd className='mt-1 font-mono'>
            <ChannelRoutingIdentityText
              text={props.model.routing_generation}
              breakAll
            />
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>
            {t('Configuration revision')}
          </dt>
          <dd className='mt-1 font-mono'>
            {props.model.configuration_revision}
          </dd>
        </div>
        <div className='min-w-0 sm:col-span-2'>
          <dt className='text-muted-foreground'>{t('Pricing identity')}</dt>
          <dd className='mt-1 font-mono'>
            <ChannelRoutingIdentityText
              text={props.model.pricing_identity || t('Unavailable')}
              breakAll
            />
          </dd>
        </div>
      </dl>

      {!pricing ? (
        <Empty className='min-h-40 rounded-none border-0 px-0'>
          <EmptyHeader>
            <EmptyTitle>{t('Pricing contract unavailable')}</EmptyTitle>
            <EmptyDescription>
              {t('No pricing contract is available for this model.')}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <section
          className='flex flex-col gap-2'
          aria-labelledby='cost-contract-rates'
        >
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <h4 id='cost-contract-rates' className='text-sm font-semibold'>
              {t('Token, media, and business-unit rates')}
            </h4>
            <span className='text-muted-foreground text-xs'>
              {pricing.currency || props.model.currency || 'USD'}
            </span>
          </div>
          {rates.length > 0 ? (
            <div className='overflow-hidden rounded-lg border'>
              <Table
                className='min-w-[32rem]'
                scrollAreaLabel={t('Pricing contract rates')}
              >
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('Rate')}</TableHead>
                    <TableHead className='text-right'>
                      {t('1× system baseline')}
                    </TableHead>
                    <TableHead className='text-right'>
                      {t('Effective cost')}
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rates.map((rate) => (
                    <TableRow key={rate.field}>
                      <TableCell>{t(rate.label)}</TableCell>
                      <TableCell className='text-right font-mono text-xs'>
                        {rate.value === 0 ? t('Free') : format.cost(rate.value)}
                      </TableCell>
                      <TableCell className='text-right font-mono text-xs'>
                        {rate.value === 0
                          ? t('Free')
                          : format.cost(rate.value * multiplier)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          ) : (
            <p className='text-muted-foreground text-sm'>
              {t('This contract has no explicit rate dimensions.')}
            </p>
          )}
        </section>
      )}

      {pricing?.billing_expression ? (
        <section
          className='flex flex-col gap-2'
          aria-labelledby='cost-contract-expression'
        >
          <h4 id='cost-contract-expression' className='text-sm font-semibold'>
            {t('Billing expression')}
          </h4>
          <pre className='bg-muted/60 max-h-48 overflow-auto rounded-lg border p-3 font-mono text-xs break-all whitespace-pre-wrap'>
            {pricing.billing_expression}
          </pre>
        </section>
      ) : null}

      {props.member ? (
        <p className='text-muted-foreground flex min-w-0 flex-wrap gap-x-2 border-t pt-3 text-xs'>
          <span className='min-w-0 break-words'>
            {props.member.channel_name}
          </span>
          <span>#{props.member.channel_id}</span>
        </p>
      ) : null}
    </div>
  )
}
