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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { formatPaymentMinorAmount } from '@/features/wallet/lib/billing'

import { getSelfStripeLegacyInventory } from '../../api'
import type { StripeLegacySubscription } from '../../types'

const PAGE_SIZE = 20

function formatTime(value: number): string {
  if (!value || !Number.isFinite(value)) return '-'
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(new Date(value * 1000))
}

function getStripeStatus(status: string, t: (key: string) => string) {
  switch (status) {
    case 'active':
      return { label: t('Active'), variant: 'success' as const }
    case 'trialing':
      return { label: t('Trialing'), variant: 'info' as const }
    case 'past_due':
      return { label: t('Past Due'), variant: 'warning' as const }
    case 'unpaid':
      return { label: t('Unpaid'), variant: 'danger' as const }
    case 'canceled':
      return { label: t('Canceled'), variant: 'neutral' as const }
    default:
      return { label: status || t('Unknown'), variant: 'neutral' as const }
  }
}

function InventoryCard(props: { item: StripeLegacySubscription }) {
  const { t } = useTranslation()
  const status = getStripeStatus(props.item.status, t)
  return (
    <article className='grid gap-3 rounded-xl border p-3'>
      <div className='flex flex-wrap items-start justify-between gap-2'>
        <div className='grid min-w-0 gap-1'>
          <code className='truncate text-xs'>
            {props.item.stripe_subscription_id}
          </code>
          <span className='text-muted-foreground text-xs'>
            {t('Stripe subscription')}
          </span>
        </div>
        <StatusBadge
          label={status.label}
          variant={status.variant}
          copyable={false}
        />
      </div>
      <dl className='grid gap-2 text-sm sm:grid-cols-2'>
        <div>
          <dt className='text-muted-foreground text-xs'>
            {t('Current Period End')}
          </dt>
          <dd>{formatTime(props.item.current_period_end)}</dd>
        </div>
        <div>
          <dt className='text-muted-foreground text-xs'>
            {t('Latest Invoice')}
          </dt>
          <dd className='flex flex-wrap items-center gap-1'>
            {formatPaymentMinorAmount(
              props.item.latest_invoice_paid
                ? props.item.latest_invoice_amount_paid
                : props.item.latest_invoice_amount_due,
              props.item.latest_invoice_currency || props.item.currency,
              'stripe'
            )}
            <StatusBadge
              label={props.item.latest_invoice_paid ? t('Paid') : t('Unpaid')}
              variant={props.item.latest_invoice_paid ? 'success' : 'warning'}
              copyable={false}
            />
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground text-xs'>
            {t('Mapping Status')}
          </dt>
          <dd>{props.item.mapping_status || '-'}</dd>
        </div>
        <div>
          <dt className='text-muted-foreground text-xs'>
            {t('Last Synced At')}
          </dt>
          <dd>{formatTime(props.item.last_synced_at)}</dd>
        </div>
      </dl>
      {props.item.mapping_reason && (
        <p className='text-muted-foreground text-xs'>
          {props.item.mapping_reason}
        </p>
      )}
    </article>
  )
}

export function StripeLegacyInventoryDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const inventoryQuery = useQuery({
    queryKey: ['stripe-legacy-inventory', 'self', page, PAGE_SIZE],
    queryFn: async () => {
      const response = await getSelfStripeLegacyInventory(page, PAGE_SIZE)
      if (!response.success || !response.data) {
        throw new Error(
          response.message || t('Failed to load Stripe inventory')
        )
      }
      return response.data
    },
    enabled: props.open,
  })
  const items = inventoryQuery.data?.items ?? []
  const total = inventoryQuery.data?.total ?? 0
  const effectivePageSize = inventoryQuery.data?.page_size || PAGE_SIZE
  const totalPages = Math.max(1, Math.ceil(total / effectivePageSize))

  useEffect(() => {
    if (!props.open) setPage(1)
  }, [props.open])

  useEffect(() => {
    if (page > totalPages) setPage(totalPages)
  }, [page, totalPages])

  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className='w-full sm:max-w-2xl'>
        <DialogHeader>
          <DialogTitle>{t('Legacy Stripe subscriptions')}</DialogTitle>
          <DialogDescription>
            {t(
              'This is a read-only provider inventory. It does not grant, renew, cancel, or revoke your local subscription access.'
            )}
          </DialogDescription>
        </DialogHeader>
        {inventoryQuery.isLoading && (
          <div className='grid gap-3'>
            <Skeleton className='h-28 w-full' />
            <Skeleton className='h-28 w-full' />
          </div>
        )}
        {!inventoryQuery.isLoading && inventoryQuery.isError && (
          <Alert variant='destructive'>
            <AlertDescription className='flex flex-wrap items-center justify-between gap-3'>
              <span>{t('Failed to load Stripe inventory')}</span>
              <Button
                type='button'
                size='sm'
                variant='outline'
                onClick={() => void inventoryQuery.refetch()}
              >
                {t('Retry')}
              </Button>
            </AlertDescription>
          </Alert>
        )}
        {!inventoryQuery.isLoading &&
          !inventoryQuery.isError &&
          items.length === 0 && (
            <div className='text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm'>
              {t('No legacy Stripe subscriptions were found.')}
            </div>
          )}
        {!inventoryQuery.isLoading &&
          !inventoryQuery.isError &&
          items.length > 0 && (
            <ScrollArea className='max-h-[min(60vh,560px)] pr-2'>
              <div className='grid gap-3'>
                {items.map((item) => (
                  <InventoryCard key={item.id} item={item} />
                ))}
              </div>
            </ScrollArea>
          )}
        {!inventoryQuery.isLoading && !inventoryQuery.isError && total > 0 && (
          <div className='flex items-center justify-between gap-3 border-t pt-3 text-sm'>
            <span className='text-muted-foreground tabular-nums'>
              {t('Page {{page}} of {{total}}', { page, total: totalPages })}
            </span>
            <div className='flex gap-2'>
              <Button
                type='button'
                size='sm'
                variant='outline'
                disabled={page <= 1 || inventoryQuery.isFetching}
                onClick={() => setPage((current) => Math.max(1, current - 1))}
              >
                {t('Previous')}
              </Button>
              <Button
                type='button'
                size='sm'
                variant='outline'
                disabled={page >= totalPages || inventoryQuery.isFetching}
                onClick={() =>
                  setPage((current) => Math.min(totalPages, current + 1))
                }
              >
                {t('Next')}
              </Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
