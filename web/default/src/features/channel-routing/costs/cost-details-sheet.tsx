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
import { useTranslation } from 'react-i18next'

import {
  sideDrawerContentClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { getChannelRoutingCostDetail } from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingIdentityText } from '../components/identity-text'
import {
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
} from '../components/page-state'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import type { CostSnapshotSummary, RoutingNormalizedPricing } from '../types'

const normalizedPricingFields: Array<{
  key: keyof RoutingNormalizedPricing
  label: string
}> = [
  { key: 'model_price', label: 'Model price' },
  { key: 'input_cost_per_million', label: 'Input / million' },
  { key: 'output_cost_per_million', label: 'Output / million' },
  { key: 'cache_read_cost_per_million', label: 'Cache read / million' },
  { key: 'cache_write_cost_per_million', label: 'Cache write / million' },
  { key: 'cache_write_1h_cost_per_million', label: '1h cache write / million' },
  { key: 'image_input_cost_per_million', label: 'Image input / million' },
  { key: 'image_output_cost_per_million', label: 'Image output / million' },
  { key: 'image_cost', label: 'Image cost' },
  { key: 'per_image_cost', label: 'Per image' },
  { key: 'audio_input_cost_per_million', label: 'Audio input / million' },
  { key: 'audio_output_cost_per_million', label: 'Audio output / million' },
  { key: 'per_request_cost', label: 'Per request' },
  { key: 'group_ratio', label: 'Group ratio' },
  { key: 'base_ratio', label: 'Base ratio' },
  { key: 'completion_ratio', label: 'Completion ratio' },
]

export function ChannelRoutingCostDetailsSheet(props: {
  summary: CostSnapshotSummary | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const summary = props.summary
  const detailQuery = useQuery({
    queryKey: channelRoutingQueryKeys.costDetail(
      summary?.pool_id ?? 0,
      summary?.member_id ?? 0,
      summary?.model_name ?? ''
    ),
    queryFn: () =>
      getChannelRoutingCostDetail(
        summary?.pool_id ?? 0,
        summary?.member_id ?? 0,
        summary?.model_name ?? ''
      ),
    enabled: props.open && summary != null,
  })
  const cost = detailQuery.data?.item
  const pricing = cost?.pricing
  const pricingRows = pricing
    ? normalizedPricingFields.flatMap((field) => {
        const value = pricing[field.key]
        return typeof value === 'number' && Number.isFinite(value)
          ? [{ ...field, value }]
          : []
      })
    : []

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent
        className={sideDrawerContentClassName(
          'channel-routing-touch-surface max-w-none max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11 sm:!max-w-3xl'
        )}
      >
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle>{t('Cost snapshot details')}</SheetTitle>
          <SheetDescription className='min-w-0'>
            {summary ? (
              <ChannelRoutingIdentityText
                text={`${summary.group_name} · ${summary.channel_name || `#${summary.channel_id}`} · ${summary.model_name}`}
                className='text-xs'
              />
            ) : null}
          </SheetDescription>
        </SheetHeader>

        {detailQuery.isLoading ? (
          <div className='min-h-0 flex-1 overflow-auto px-4 pb-4'>
            <ChannelRoutingLoadingState rows={8} />
          </div>
        ) : null}

        {detailQuery.isError ? (
          <div className='min-h-0 flex-1 overflow-auto px-4 pb-4'>
            <ChannelRoutingErrorState
              error={detailQuery.error}
              onRetry={() => void detailQuery.refetch()}
            />
          </div>
        ) : null}

        {cost && detailQuery.data ? (
          <div className='min-h-0 flex-1 space-y-5 overflow-auto px-4 pb-4'>
            <p className='text-muted-foreground text-xs'>
              {t('Snapshot r{{revision}} · built {{time}}', {
                revision: detailQuery.data.snapshot_revision,
                time: format.timestamp(detailQuery.data.snapshot_built_at),
              })}
            </p>
            <div className='flex flex-wrap items-center gap-2'>
              <ChannelRoutingStatusBadge
                status={cost.known ? 'known' : 'unknown'}
              />
              <ChannelRoutingStatusBadge
                status={cost.confidence || 'unknown'}
              />
              <ChannelRoutingStatusBadge status={cost.freshness || 'unknown'} />
              <ChannelRoutingStatusBadge
                status={cost.source_sync_status || 'unknown'}
              />
            </div>

            <section aria-labelledby='cost-source-title'>
              <h3 id='cost-source-title' className='mb-2 text-sm font-semibold'>
                {t('Pricing source')}
              </h3>
              <dl className='bg-border grid grid-cols-2 gap-px overflow-hidden rounded-lg border sm:grid-cols-3'>
                {[
                  [t('Local model'), cost.model_name],
                  [t('Upstream group'), cost.upstream_group || t('Unknown')],
                  [t('Upstream model'), cost.upstream_model || t('Unknown')],
                  [
                    t('Billing Mode'),
                    format.billingMode(pricing?.billing_mode),
                  ],
                  [
                    t('Currency'),
                    pricing?.currency || cost.currency || t('Unknown'),
                  ],
                  [t('Unit'), pricing?.unit || cost.unit || t('Unknown')],
                  [t('Pricing version'), cost.pricing_version || t('Unknown')],
                  [t('Pricing hash'), format.shortHash(cost.version)],
                  [t('Snapshot time'), format.timestamp(cost.snapshot_time)],
                ].map(([label, value]) => (
                  <div key={label} className='bg-background min-w-0 p-3'>
                    <dt className='text-muted-foreground text-xs'>{label}</dt>
                    <dd
                      className='mt-1 text-sm font-medium break-words'
                      title={String(value)}
                    >
                      {value}
                    </dd>
                  </div>
                ))}
              </dl>
            </section>

            <section aria-labelledby='cost-validity-title'>
              <h3
                id='cost-validity-title'
                className='mb-2 text-sm font-semibold'
              >
                {t('Confidence and validity')}
              </h3>
              <dl className='grid gap-x-5 gap-y-3 text-sm sm:grid-cols-2'>
                <div>
                  <dt className='text-muted-foreground text-xs'>
                    {t('Confidence score')}
                  </dt>
                  <dd className='mt-1 font-medium'>
                    {format.percent(cost.confidence_score)}
                  </dd>
                </div>
                <div>
                  <dt className='text-muted-foreground text-xs'>
                    {t('Freshness score')}
                  </dt>
                  <dd className='mt-1 font-medium'>
                    {format.percent(cost.freshness_score)}
                  </dd>
                </div>
                <div>
                  <dt className='text-muted-foreground text-xs'>
                    {t('Observed')}
                  </dt>
                  <dd className='mt-1 font-medium'>
                    {format.timestamp(cost.observed_time)}
                  </dd>
                </div>
                <div>
                  <dt className='text-muted-foreground text-xs'>
                    {t('Effective')}
                  </dt>
                  <dd className='mt-1 font-medium'>
                    {format.timestamp(cost.effective_time)}
                  </dd>
                </div>
                <div>
                  <dt className='text-muted-foreground text-xs'>
                    {t('Expires')}
                  </dt>
                  <dd className='mt-1 font-medium'>
                    {format.timestamp(cost.expires_time)}
                  </dd>
                </div>
                <div>
                  <dt className='text-muted-foreground text-xs'>
                    {t('Source sync')}
                  </dt>
                  <dd className='mt-1 font-medium break-words'>
                    {cost.source_sync_status || t('Unknown')}
                  </dd>
                </div>
              </dl>
              {cost.source_sync_error ? (
                <p className='border-destructive/30 bg-destructive/5 text-destructive mt-3 rounded-lg border p-3 text-sm break-words'>
                  {cost.source_sync_error}
                </p>
              ) : null}
            </section>

            <section aria-labelledby='normalized-pricing-title'>
              <h3
                id='normalized-pricing-title'
                className='mb-2 text-sm font-semibold'
              >
                {t('Normalized pricing')}
              </h3>
              {pricingRows.length > 0 ? (
                <div className='overflow-hidden rounded-lg border'>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{t('Rate')}</TableHead>
                        <TableHead className='text-right'>
                          {t('Value')}
                        </TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {pricingRows.map((row) => (
                        <TableRow key={row.key}>
                          <TableCell>{t(row.label)}</TableCell>
                          <TableCell className='text-right font-mono text-xs'>
                            {format.cost(row.value)}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              ) : (
                <p className='text-muted-foreground text-sm'>
                  {pricing?.billing_expression
                    ? t('Pricing is calculated by a billing expression.')
                    : t('No normalized rate fields are available.')}
                </p>
              )}
            </section>

            <section
              className='border-t pt-4'
              aria-labelledby='cost-account-title'
            >
              <h3
                id='cost-account-title'
                className='mb-2 text-sm font-semibold'
              >
                {t('Upstream account')}
              </h3>
              {cost.account ? (
                <dl className='grid gap-x-5 gap-y-3 text-sm sm:grid-cols-2'>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Account')}
                    </dt>
                    <dd className='mt-1 font-mono text-xs break-all'>
                      {cost.account.masked_identity || `#${cost.account.id}`}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Source type')}
                    </dt>
                    <dd className='mt-1 font-medium'>
                      {cost.account.source_type || t('Unknown')}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Account status')}
                    </dt>
                    <dd className='mt-1'>
                      <ChannelRoutingStatusBadge
                        status={cost.account.status || 'unknown'}
                      />
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Balance')}
                    </dt>
                    <dd className='mt-1 font-medium'>
                      {cost.account.balance_known &&
                      typeof cost.account.balance === 'number'
                        ? t('{{currency}} {{amount}}', {
                            currency: pricing?.currency || cost.currency || '',
                            amount: format.cost(cost.account.balance),
                          })
                        : t('Unknown')}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Balance updated')}
                    </dt>
                    <dd className='mt-1 font-medium'>
                      {format.timestamp(cost.account.balance_updated_at)}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Last sync')}
                    </dt>
                    <dd className='mt-1 font-medium break-words'>
                      {cost.account.last_sync_status || t('Unknown')}
                    </dd>
                  </div>
                  {cost.account.last_sync_error ? (
                    <div className='sm:col-span-2'>
                      <dt className='text-muted-foreground text-xs'>
                        {t('Last sync error')}
                      </dt>
                      <dd className='text-destructive mt-1 break-words'>
                        {cost.account.last_sync_error}
                      </dd>
                    </div>
                  ) : null}
                </dl>
              ) : (
                <p className='text-muted-foreground text-sm'>
                  {t('No upstream account metadata is available.')}
                </p>
              )}
            </section>
          </div>
        ) : null}
      </SheetContent>
    </Sheet>
  )
}
