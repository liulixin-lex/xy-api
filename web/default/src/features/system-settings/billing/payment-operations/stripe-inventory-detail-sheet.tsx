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

import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'

import { formatMinorAmount, formatUnixTime } from './status'
import { MappingStatusBadge, StripeStatusBadge } from './status-badges'
import type { StripeLegacySubscription } from './types'

function InventoryField(props: { label: string; children: React.ReactNode }) {
  return (
    <div className='grid min-w-0 gap-1'>
      <dt className='text-muted-foreground text-xs'>{props.label}</dt>
      <dd className='min-w-0 text-sm break-words'>{props.children}</dd>
    </div>
  )
}

function InventoryGroup(props: { title: string; children: React.ReactNode }) {
  return (
    <section className='grid gap-2'>
      <h3 className='text-sm font-medium'>{props.title}</h3>
      <dl className='grid gap-3 rounded-lg border p-3 sm:grid-cols-2'>
        {props.children}
      </dl>
    </section>
  )
}

export function StripeInventoryDetailSheet(props: {
  item: StripeLegacySubscription | null
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const item = props.item

  return (
    <Sheet
      open={Boolean(item)}
      onOpenChange={(open) => {
        if (!open) props.onOpenChange(false)
      }}
    >
      <SheetContent className='w-full sm:max-w-[680px]'>
        <SheetHeader className='border-b pr-12'>
          <SheetTitle>{t('Stripe legacy subscription')}</SheetTitle>
          <SheetDescription className='truncate font-mono'>
            {item?.stripe_subscription_id}
          </SheetDescription>
        </SheetHeader>
        {item && (
          <ScrollArea className='min-h-0 flex-1'>
            <div className='grid gap-5 px-4 pb-5'>
              <Alert>
                <AlertDescription>
                  {t(
                    'This observation record never changes local access. A separately verified cancellation can only stop future Stripe renewal at the period end.'
                  )}
                </AlertDescription>
              </Alert>

              {item.mapping_status !== 'mapped' && (
                <Alert variant='destructive'>
                  <AlertDescription>
                    {item.mapping_reason ||
                      item.review_reason ||
                      t('This Stripe record could not be mapped safely.')}
                  </AlertDescription>
                </Alert>
              )}

              <InventoryGroup title={t('Stripe State')}>
                <InventoryField label={t('Subscription Status')}>
                  <StripeStatusBadge status={item.status} t={t} />
                </InventoryField>
                <InventoryField label={t('Mapping Status')}>
                  <MappingStatusBadge status={item.mapping_status} t={t} />
                </InventoryField>
                <InventoryField label={t('Stripe Subscription ID')}>
                  <code>{item.stripe_subscription_id}</code>
                </InventoryField>
                <InventoryField label={t('Stripe Customer ID')}>
                  <code>{item.stripe_customer_id}</code>
                </InventoryField>
                <InventoryField label={t('Collection Method')}>
                  {item.collection_method || '-'}
                </InventoryField>
                <InventoryField label={t('Environment')}>
                  <StatusBadge
                    label={item.livemode ? t('Live Mode') : t('Test Mode')}
                    variant={item.livemode ? 'success' : 'warning'}
                    copyable={false}
                  />
                </InventoryField>
                <InventoryField label={t('Cancel at Period End')}>
                  {item.cancel_at_period_end ? t('Yes') : t('No')}
                </InventoryField>
                <InventoryField label={t('Quantity')}>
                  {item.quantity}
                </InventoryField>
              </InventoryGroup>

              <InventoryGroup title={t('Local Mapping')}>
                <InventoryField label={t('User ID')}>
                  {item.user_id || '-'}
                </InventoryField>
                <InventoryField label={t('Subscription Plan ID')}>
                  {item.subscription_plan_id || '-'}
                </InventoryField>
                <InventoryField label={t('Mapping Source')}>
                  {item.mapping_source || '-'}
                </InventoryField>
                <InventoryField label={t('Trade Number')}>
                  <code>{item.trade_no || '-'}</code>
                </InventoryField>
                <InventoryField label={t('Mapping Reason')}>
                  {item.mapping_reason || '-'}
                </InventoryField>
                <InventoryField label={t('Review Reason')}>
                  {item.review_reason || '-'}
                </InventoryField>
                <InventoryField label={t('Checkout Session ID')}>
                  <code>{item.checkout_session_id || '-'}</code>
                </InventoryField>
                <InventoryField label={t('Sync Source')}>
                  {item.sync_source || '-'}
                </InventoryField>
              </InventoryGroup>

              <InventoryGroup title={t('Product and Invoice')}>
                <InventoryField label={t('Price IDs')}>
                  {item.price_ids.length > 0 ? (
                    <div className='flex flex-wrap gap-1'>
                      {item.price_ids.map((priceId) => (
                        <code
                          key={priceId}
                          className='rounded border px-1 py-0.5'
                        >
                          {priceId}
                        </code>
                      ))}
                    </div>
                  ) : (
                    '-'
                  )}
                </InventoryField>
                <InventoryField label={t('Product ID')}>
                  <code>{item.product_id || '-'}</code>
                </InventoryField>
                <InventoryField label={t('Latest Invoice ID')}>
                  <code>{item.latest_invoice_id || '-'}</code>
                </InventoryField>
                <InventoryField label={t('Latest Invoice Status')}>
                  {item.latest_invoice_status || '-'}
                </InventoryField>
                <InventoryField label={t('Latest Invoice Amount Due')}>
                  {formatMinorAmount(
                    item.latest_invoice_amount_due,
                    item.latest_invoice_currency || item.currency,
                    'stripe'
                  )}
                </InventoryField>
                <InventoryField label={t('Latest Invoice Amount Paid')}>
                  {formatMinorAmount(
                    item.latest_invoice_amount_paid,
                    item.latest_invoice_currency || item.currency,
                    'stripe'
                  )}
                </InventoryField>
                <InventoryField label={t('Latest Invoice Paid')}>
                  {item.latest_invoice_paid ? t('Yes') : t('No')}
                </InventoryField>
              </InventoryGroup>

              <InventoryGroup title={t('Lifecycle')}>
                <InventoryField label={t('Current Period Start')}>
                  {formatUnixTime(item.current_period_start)}
                </InventoryField>
                <InventoryField label={t('Current Period End')}>
                  {formatUnixTime(item.current_period_end)}
                </InventoryField>
                <InventoryField label={t('Trial Start')}>
                  {formatUnixTime(item.trial_start)}
                </InventoryField>
                <InventoryField label={t('Trial End')}>
                  {formatUnixTime(item.trial_end)}
                </InventoryField>
                <InventoryField label={t('Cancel At')}>
                  {formatUnixTime(item.cancel_at)}
                </InventoryField>
                <InventoryField label={t('Canceled At')}>
                  {formatUnixTime(item.canceled_at)}
                </InventoryField>
                <InventoryField label={t('Ended At')}>
                  {formatUnixTime(item.ended_at)}
                </InventoryField>
                <InventoryField label={t('Stripe Created At')}>
                  {formatUnixTime(item.stripe_created_at)}
                </InventoryField>
                <InventoryField label={t('State Observed At')}>
                  {formatUnixTime(item.state_observed_at)}
                </InventoryField>
                <InventoryField label={t('Last Synced At')}>
                  {formatUnixTime(item.last_synced_at)}
                </InventoryField>
              </InventoryGroup>
            </div>
          </ScrollArea>
        )}
      </SheetContent>
    </Sheet>
  )
}
