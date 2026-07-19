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
import { RefreshIcon, Search01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { getApiErrorMessage } from '@/lib/api-error'

import { listAdminStripeInventory, syncAdminStripeInventory } from './api'
import { formatMinorAmount, formatUnixTime } from './status'
import { MappingStatusBadge, StripeStatusBadge } from './status-badges'
import { StripeInventoryDetailSheet } from './stripe-inventory-detail-sheet'
import { TablePagination } from './table-pagination'
import type { StripeInventoryFilters, StripeLegacySubscription } from './types'
import { usePaymentOperationVerification } from './verification-context'

const PAGE_SIZE = 20
const EMPTY_FILTERS: StripeInventoryFilters = {
  status: '',
  mappingStatus: '',
  userId: '',
  customerId: '',
  subscriptionId: '',
}

export function StripeInventoryPanel() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { runWithVerification, verificationOpen } =
    usePaymentOperationVerification()
  const [draftFilters, setDraftFilters] = useState(EMPTY_FILTERS)
  const [filters, setFilters] = useState(EMPTY_FILTERS)
  const [page, setPage] = useState(1)
  const [syncDialogOpen, setSyncDialogOpen] = useState(false)
  const [selectedItem, setSelectedItem] =
    useState<StripeLegacySubscription | null>(null)
  const inventoryQuery = useQuery({
    queryKey: ['stripe-legacy-inventory', 'admin', filters, page, PAGE_SIZE],
    queryFn: () => listAdminStripeInventory(filters, page, PAGE_SIZE),
  })
  const syncMutation = useMutation({
    mutationFn: () =>
      runWithVerification(
        async () => {
          const result = await syncAdminStripeInventory()
          toast.success(
            t(
              'Stripe inventory sync completed: {{seen}} seen, {{mapped}} mapped, {{unmapped}} unmapped.',
              {
                seen: result.seen,
                mapped: result.mapped,
                unmapped: result.unmapped,
              }
            )
          )
          setSyncDialogOpen(false)
          await queryClient.invalidateQueries({
            queryKey: ['stripe-legacy-inventory'],
          })
          return result
        },
        {
          preferredMethod: 'passkey',
          title: t('Verify Stripe inventory sync'),
          description: t(
            'Confirm your identity before synchronizing the legacy Stripe subscription inventory.'
          ),
        }
      ),
    onError: (error) => {
      toast.error(
        getApiErrorMessage(
          error,
          t('Failed to sync Stripe subscription inventory')
        )
      )
    },
  })
  const items = inventoryQuery.data?.items ?? []
  const total = inventoryQuery.data?.total ?? 0

  const applyFilters = () => {
    setPage(1)
    setFilters({
      status: draftFilters.status,
      mappingStatus: draftFilters.mappingStatus,
      userId: draftFilters.userId.trim(),
      customerId: draftFilters.customerId.trim(),
      subscriptionId: draftFilters.subscriptionId.trim(),
    })
  }

  const resetFilters = () => {
    setDraftFilters(EMPTY_FILTERS)
    setFilters(EMPTY_FILTERS)
    setPage(1)
  }

  return (
    <div className='grid gap-4'>
      <Alert>
        <AlertTitle>{t('Read-only Stripe inventory')}</AlertTitle>
        <AlertDescription>
          {t(
            'Inventory sync observes legacy recurring subscriptions only. It never grants, renews, cancels, or revokes local subscription entitlement.'
          )}
        </AlertDescription>
      </Alert>

      <Card>
        <CardHeader className='border-b'>
          <CardTitle>{t('Stripe Legacy Inventory')}</CardTitle>
          <CardDescription>
            {t(
              'Find mapping gaps and inspect the last observed Stripe lifecycle and invoice state.'
            )}
          </CardDescription>
          <CardAction>
            <Button
              type='button'
              variant='outline'
              size='sm'
              disabled={syncMutation.isPending}
              onClick={() => setSyncDialogOpen(true)}
            >
              <HugeiconsIcon
                icon={RefreshIcon}
                strokeWidth={2}
                data-icon='inline-start'
              />
              {t('Sync from Stripe')}
            </Button>
          </CardAction>
        </CardHeader>
        <CardContent className='grid gap-3'>
          <div className='grid gap-3 md:grid-cols-2 xl:grid-cols-4'>
            <div className='grid gap-1.5'>
              <Label htmlFor='stripe-inventory-status'>
                {t('Subscription Status')}
              </Label>
              <NativeSelect
                id='stripe-inventory-status'
                value={draftFilters.status}
                onChange={(event) =>
                  setDraftFilters((current) => ({
                    ...current,
                    status: event.target.value,
                  }))
                }
              >
                <NativeSelectOption value=''>
                  {t('All Statuses')}
                </NativeSelectOption>
                <NativeSelectOption value='active'>
                  {t('Active')}
                </NativeSelectOption>
                <NativeSelectOption value='trialing'>
                  {t('Trialing')}
                </NativeSelectOption>
                <NativeSelectOption value='past_due'>
                  {t('Past Due')}
                </NativeSelectOption>
                <NativeSelectOption value='unpaid'>
                  {t('Unpaid')}
                </NativeSelectOption>
                <NativeSelectOption value='incomplete'>
                  {t('Incomplete')}
                </NativeSelectOption>
                <NativeSelectOption value='paused'>
                  {t('Paused')}
                </NativeSelectOption>
                <NativeSelectOption value='canceled'>
                  {t('Canceled')}
                </NativeSelectOption>
              </NativeSelect>
            </div>
            <div className='grid gap-1.5'>
              <Label htmlFor='stripe-inventory-mapping'>
                {t('Mapping Status')}
              </Label>
              <NativeSelect
                id='stripe-inventory-mapping'
                value={draftFilters.mappingStatus}
                onChange={(event) =>
                  setDraftFilters((current) => ({
                    ...current,
                    mappingStatus: event.target.value,
                  }))
                }
              >
                <NativeSelectOption value=''>
                  {t('All Mapping Statuses')}
                </NativeSelectOption>
                <NativeSelectOption value='mapped'>
                  {t('Mapped')}
                </NativeSelectOption>
                <NativeSelectOption value='unmapped'>
                  {t('Unmapped')}
                </NativeSelectOption>
                <NativeSelectOption value='unmapped_user'>
                  {t('User Unmapped')}
                </NativeSelectOption>
                <NativeSelectOption value='unmapped_plan'>
                  {t('Plan Unmapped')}
                </NativeSelectOption>
                <NativeSelectOption value='ambiguous_user'>
                  {t('Ambiguous User')}
                </NativeSelectOption>
                <NativeSelectOption value='ambiguous_plan'>
                  {t('Ambiguous Plan')}
                </NativeSelectOption>
              </NativeSelect>
            </div>
            <div className='grid gap-1.5'>
              <Label htmlFor='stripe-inventory-user-id'>{t('User ID')}</Label>
              <Input
                id='stripe-inventory-user-id'
                type='number'
                min={1}
                step={1}
                value={draftFilters.userId}
                placeholder={t('Exact local user ID')}
                onChange={(event) =>
                  setDraftFilters((current) => ({
                    ...current,
                    userId: event.target.value,
                  }))
                }
                onKeyDown={(event) => {
                  if (event.key === 'Enter') applyFilters()
                }}
              />
            </div>
            <div className='grid gap-1.5'>
              <Label htmlFor='stripe-inventory-customer-id'>
                {t('Stripe Customer ID')}
              </Label>
              <Input
                id='stripe-inventory-customer-id'
                value={draftFilters.customerId}
                placeholder='cus_...'
                onChange={(event) =>
                  setDraftFilters((current) => ({
                    ...current,
                    customerId: event.target.value,
                  }))
                }
                onKeyDown={(event) => {
                  if (event.key === 'Enter') applyFilters()
                }}
              />
            </div>
          </div>
          <div className='grid gap-3 md:grid-cols-[minmax(0,1fr)_auto]'>
            <div className='grid gap-1.5'>
              <Label htmlFor='stripe-inventory-subscription-id'>
                {t('Stripe Subscription ID')}
              </Label>
              <Input
                id='stripe-inventory-subscription-id'
                value={draftFilters.subscriptionId}
                placeholder='sub_...'
                onChange={(event) =>
                  setDraftFilters((current) => ({
                    ...current,
                    subscriptionId: event.target.value,
                  }))
                }
                onKeyDown={(event) => {
                  if (event.key === 'Enter') applyFilters()
                }}
              />
            </div>
            <div className='flex items-end gap-2'>
              <Button type='button' onClick={applyFilters}>
                <HugeiconsIcon
                  icon={Search01Icon}
                  strokeWidth={2}
                  data-icon='inline-start'
                />
                {t('Search')}
              </Button>
              <Button type='button' variant='outline' onClick={resetFilters}>
                {t('Reset')}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <Card className='gap-0 py-0'>
        <CardHeader className='border-b py-4'>
          <CardTitle>{t('Observed Subscriptions')}</CardTitle>
          <CardDescription>
            {t(
              'Mapping warnings require review but do not imply a missing or active local entitlement.'
            )}
          </CardDescription>
          <CardAction>
            <Button
              type='button'
              variant='outline'
              size='icon-sm'
              disabled={inventoryQuery.isFetching}
              aria-label={t('Refresh')}
              onClick={() => inventoryQuery.refetch()}
            >
              <HugeiconsIcon icon={RefreshIcon} strokeWidth={2} />
            </Button>
          </CardAction>
        </CardHeader>
        {inventoryQuery.isLoading && (
          <div className='grid gap-2 p-4'>
            {Array.from({ length: 5 }, (_, index) => index).map((key) => (
              <Skeleton key={key} className='h-12 w-full' />
            ))}
          </div>
        )}
        {!inventoryQuery.isLoading && inventoryQuery.isError && (
          <div className='p-4'>
            <Alert variant='destructive'>
              <AlertDescription>
                {getApiErrorMessage(
                  inventoryQuery.error,
                  t('Failed to load Stripe subscription inventory')
                )}
              </AlertDescription>
            </Alert>
          </div>
        )}
        {!inventoryQuery.isLoading &&
          !inventoryQuery.isError &&
          items.length === 0 && (
            <div className='text-muted-foreground p-10 text-center text-sm'>
              {t('No Stripe subscription inventory matches these filters.')}
            </div>
          )}
        {!inventoryQuery.isLoading &&
          !inventoryQuery.isError &&
          items.length > 0 && (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Stripe Subscription')}</TableHead>
                  <TableHead>{t('Local Mapping')}</TableHead>
                  <TableHead>{t('Subscription Status')}</TableHead>
                  <TableHead>{t('Latest Invoice')}</TableHead>
                  <TableHead>{t('Current Period End')}</TableHead>
                  <TableHead>{t('Last Synced At')}</TableHead>
                  <TableHead className='text-right'>{t('Actions')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((item) => (
                  <TableRow key={item.id}>
                    <TableCell className='max-w-64 whitespace-normal'>
                      <div className='grid gap-0.5'>
                        <code className='truncate text-xs'>
                          {item.stripe_subscription_id}
                        </code>
                        <code className='text-muted-foreground truncate text-xs'>
                          {item.stripe_customer_id}
                        </code>
                      </div>
                    </TableCell>
                    <TableCell className='max-w-56 whitespace-normal'>
                      <div className='grid gap-1'>
                        <MappingStatusBadge
                          status={item.mapping_status}
                          t={t}
                        />
                        <span className='text-muted-foreground text-xs'>
                          {t('User')}: {item.user_id || '-'} · {t('Plan')}:{' '}
                          {item.subscription_plan_id || '-'}
                        </span>
                        {(item.mapping_reason || item.review_reason) && (
                          <span className='text-muted-foreground line-clamp-2 text-xs'>
                            {item.mapping_reason || item.review_reason}
                          </span>
                        )}
                      </div>
                    </TableCell>
                    <TableCell>
                      <div className='grid gap-1'>
                        <StripeStatusBadge status={item.status} t={t} />
                        {item.cancel_at_period_end && (
                          <span className='text-muted-foreground text-xs'>
                            {t('Cancels at period end')}
                          </span>
                        )}
                      </div>
                    </TableCell>
                    <TableCell>
                      <div className='grid gap-0.5'>
                        <span className='tabular-nums'>
                          {formatMinorAmount(
                            item.latest_invoice_paid
                              ? item.latest_invoice_amount_paid
                              : item.latest_invoice_amount_due,
                            item.latest_invoice_currency || item.currency,
                            'stripe'
                          )}
                        </span>
                        <div className='flex items-center gap-1'>
                          <StatusBadge
                            label={
                              item.latest_invoice_paid ? t('Paid') : t('Unpaid')
                            }
                            variant={
                              item.latest_invoice_paid ? 'success' : 'warning'
                            }
                            copyable={false}
                          />
                          {item.latest_invoice_status && (
                            <span className='text-muted-foreground text-xs'>
                              {item.latest_invoice_status}
                            </span>
                          )}
                        </div>
                      </div>
                    </TableCell>
                    <TableCell>
                      {formatUnixTime(item.current_period_end)}
                    </TableCell>
                    <TableCell>
                      <div className='grid gap-0.5'>
                        <span>{formatUnixTime(item.last_synced_at)}</span>
                        <span className='text-muted-foreground text-xs'>
                          {item.sync_source || '-'} ·{' '}
                          {item.livemode ? t('Live Mode') : t('Test Mode')}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className='text-right'>
                      <Button
                        type='button'
                        variant='outline'
                        size='sm'
                        onClick={() => setSelectedItem(item)}
                      >
                        {t('View Details')}
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        <TablePagination
          page={page}
          pageSize={PAGE_SIZE}
          total={total}
          disabled={inventoryQuery.isFetching}
          onPageChange={setPage}
        />
      </Card>

      <AlertDialog open={syncDialogOpen} onOpenChange={setSyncDialogOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('Sync Stripe inventory?')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                'This fetches recurring subscription observations from Stripe and updates the read-only inventory. It does not change local entitlements.'
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel
              disabled={syncMutation.isPending || verificationOpen}
            >
              {t('Cancel')}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={syncMutation.isPending || verificationOpen}
              onClick={() => syncMutation.mutate()}
            >
              {syncMutation.isPending ? t('Syncing...') : t('Sync from Stripe')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <StripeInventoryDetailSheet
        item={selectedItem}
        onOpenChange={(open) => {
          if (!open) setSelectedItem(null)
        }}
      />
    </div>
  )
}
