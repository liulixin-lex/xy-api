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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
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

import { listBillingReservations } from './api'
import { BillingReservationDetailSheet } from './billing-reservation-detail-sheet'
import { formatInteger, formatUnixTime } from './status'
import { TablePagination } from './table-pagination'
import type { BillingReservation, BillingReservationFilters } from './types'

const PAGE_SIZE = 20
const EMPTY_FILTERS: BillingReservationFilters = {
  requestId: '',
  userId: '',
  resourceType: '',
}

function attentionReason(reservation: BillingReservation, t: TFunction) {
  if (reservation.settlement_pending) {
    return t('Settlement intent pending')
  }
  if (reservation.reconcile_note) {
    return reservation.reconcile_note
  }
  return t('Exceeded stale threshold')
}

export function BillingReservationPanel() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [draftFilters, setDraftFilters] = useState(EMPTY_FILTERS)
  const [filters, setFilters] = useState(EMPTY_FILTERS)
  const [page, setPage] = useState(1)
  const [selectedRequestId, setSelectedRequestId] = useState<string | null>(
    null
  )
  const reservationsQuery = useQuery({
    queryKey: ['billing-reservations', filters, page, PAGE_SIZE],
    queryFn: () => listBillingReservations(filters, page, PAGE_SIZE),
    meta: { skipGlobalError: true },
  })
  const reservations = reservationsQuery.data?.reservations ?? []
  const total = reservationsQuery.data?.total ?? 0
  const staleMinutes = Math.max(
    1,
    Math.round((reservationsQuery.data?.stale_after_seconds ?? 300) / 60)
  )

  const applyFilters = () => {
    setPage(1)
    setFilters({
      requestId: draftFilters.requestId.trim(),
      userId: draftFilters.userId.trim(),
      resourceType: draftFilters.resourceType,
    })
  }

  const resetFilters = () => {
    setDraftFilters(EMPTY_FILTERS)
    setFilters(EMPTY_FILTERS)
    setPage(1)
  }

  const refreshList = async () => {
    await queryClient.invalidateQueries({ queryKey: ['billing-reservations'] })
  }

  return (
    <div className='grid gap-4'>
      <Card>
        <CardHeader className='border-b'>
          <CardTitle>{t('Billing Reservations')}</CardTitle>
          <CardDescription>
            {t(
              'Inspect durable pre-consume records that need review before held wallet or subscription quota can be released.'
            )}
          </CardDescription>
          <CardAction>
            <Button
              type='button'
              variant='outline'
              size='sm'
              disabled={reservationsQuery.isFetching}
              onClick={() => refreshList()}
            >
              <HugeiconsIcon
                icon={RefreshIcon}
                strokeWidth={2}
                data-icon='inline-start'
              />
              {t('Refresh')}
            </Button>
          </CardAction>
        </CardHeader>
        <CardContent className='grid gap-3'>
          <Alert>
            <AlertDescription>
              {t(
                'The queue includes reviewed reservations, pending settlement intents, and reservations older than {{minutes}} minutes. Only reconciler-reviewed rows can be settled or refunded manually.',
                { minutes: staleMinutes }
              )}
            </AlertDescription>
          </Alert>
          <div className='grid gap-3 md:grid-cols-2 xl:grid-cols-[1fr_180px_220px_auto]'>
            <div className='grid gap-1.5'>
              <Label htmlFor='billing-reservation-request-id'>
                {t('Exact Request ID')}
              </Label>
              <Input
                id='billing-reservation-request-id'
                value={draftFilters.requestId}
                placeholder={t('Enter the complete request ID')}
                onChange={(event) =>
                  setDraftFilters((current) => ({
                    ...current,
                    requestId: event.target.value,
                  }))
                }
                onKeyDown={(event) => {
                  if (event.key === 'Enter') {
                    applyFilters()
                  }
                }}
              />
            </div>
            <div className='grid gap-1.5'>
              <Label htmlFor='billing-reservation-user-id'>
                {t('User ID')}
              </Label>
              <Input
                id='billing-reservation-user-id'
                value={draftFilters.userId}
                inputMode='numeric'
                placeholder={t('Exact user ID')}
                onChange={(event) =>
                  setDraftFilters((current) => ({
                    ...current,
                    userId: event.target.value,
                  }))
                }
                onKeyDown={(event) => {
                  if (event.key === 'Enter') {
                    applyFilters()
                  }
                }}
              />
            </div>
            <div className='grid gap-1.5'>
              <Label htmlFor='billing-reservation-resource-type'>
                {t('Resource Type')}
              </Label>
              <NativeSelect
                id='billing-reservation-resource-type'
                value={draftFilters.resourceType}
                onChange={(event) =>
                  setDraftFilters((current) => ({
                    ...current,
                    resourceType: event.target.value,
                  }))
                }
              >
                <NativeSelectOption value=''>
                  {t('All resource types')}
                </NativeSelectOption>
                <NativeSelectOption value='async_task'>
                  {t('Async task')}
                </NativeSelectOption>
                <NativeSelectOption value='midjourney_task'>
                  {t('Midjourney task')}
                </NativeSelectOption>
                <NativeSelectOption value='midjourney_submit'>
                  {t('Midjourney submission')}
                </NativeSelectOption>
              </NativeSelect>
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
          <CardTitle>{t('Reservations Requiring Attention')}</CardTitle>
          <CardDescription>
            {t(
              'Every row still holds quota. Open the detail view before choosing an exact settlement or a full refund.'
            )}
          </CardDescription>
        </CardHeader>
        {reservationsQuery.isLoading && (
          <div className='grid gap-2 p-4'>
            {Array.from({ length: 5 }, (_, index) => index).map((key) => (
              <Skeleton key={key} className='h-14 w-full' />
            ))}
          </div>
        )}
        {!reservationsQuery.isLoading && reservationsQuery.isError && (
          <div className='p-4'>
            <Alert variant='destructive'>
              <AlertDescription className='flex flex-col items-start justify-between gap-3 sm:flex-row sm:items-center'>
                <span>{t('Failed to load billing reservations')}</span>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={() => reservationsQuery.refetch()}
                >
                  {t('Retry')}
                </Button>
              </AlertDescription>
            </Alert>
          </div>
        )}
        {!reservationsQuery.isLoading &&
          !reservationsQuery.isError &&
          reservations.length === 0 && (
            <div className='text-muted-foreground p-10 text-center text-sm'>
              {t('No billing reservations match these filters.')}
            </div>
          )}
        {!reservationsQuery.isLoading &&
          !reservationsQuery.isError &&
          reservations.length > 0 && (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Request')}</TableHead>
                  <TableHead>{t('Funding and Pre-consume')}</TableHead>
                  <TableHead>{t('Settlement Target')}</TableHead>
                  <TableHead>{t('Resource Binding')}</TableHead>
                  <TableHead>{t('Attention Reason')}</TableHead>
                  <TableHead>{t('Version')}</TableHead>
                  <TableHead className='text-right'>{t('Actions')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {reservations.map((reservation) => (
                  <TableRow key={reservation.id}>
                    <TableCell className='max-w-60 whitespace-normal'>
                      <div className='grid gap-0.5'>
                        <code className='truncate text-xs'>
                          {reservation.request_id}
                        </code>
                        <span className='text-muted-foreground text-xs tabular-nums'>
                          {t('User ID')}: {reservation.user_id}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell>
                      <div className='grid gap-0.5'>
                        <span>
                          {reservation.funding_source === 'subscription'
                            ? t('Subscription quota')
                            : t('Wallet quota')}
                        </span>
                        <span className='text-muted-foreground text-xs tabular-nums'>
                          {t('Reserved')}:{' '}
                          {formatInteger(reservation.reserved_quota)} ·{' '}
                          {t('Initial')}:{' '}
                          {formatInteger(reservation.initial_quota)}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell>
                      {reservation.settlement_pending ? (
                        <div className='grid gap-0.5'>
                          <span className='font-medium tabular-nums'>
                            {formatInteger(reservation.settlement_target)}
                          </span>
                          <StatusBadge
                            label={t('Pending intent')}
                            variant='warning'
                            copyable={false}
                          />
                        </div>
                      ) : (
                        <span className='text-muted-foreground text-xs'>
                          {t('Not recorded')}
                        </span>
                      )}
                    </TableCell>
                    <TableCell className='max-w-52 whitespace-normal'>
                      {reservation.resource_type ? (
                        <div className='grid gap-0.5'>
                          <span>{reservation.resource_type}</span>
                          <code className='text-muted-foreground truncate text-xs'>
                            {reservation.resource_id || t('Unbound')}
                          </code>
                        </div>
                      ) : (
                        <StatusBadge
                          label={t('Unbound')}
                          variant='warning'
                          copyable={false}
                        />
                      )}
                    </TableCell>
                    <TableCell className='max-w-64 whitespace-normal'>
                      <div className='grid gap-0.5'>
                        <span className='line-clamp-2 text-sm'>
                          {attentionReason(reservation, t)}
                        </span>
                        <span className='text-muted-foreground text-xs'>
                          {formatUnixTime(reservation.updated_at)}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className='tabular-nums'>
                      {reservation.version}
                    </TableCell>
                    <TableCell className='text-right'>
                      <Button
                        type='button'
                        variant='outline'
                        size='sm'
                        onClick={() =>
                          setSelectedRequestId(reservation.request_id)
                        }
                      >
                        {t('Review Details')}
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
          disabled={reservationsQuery.isFetching}
          onPageChange={setPage}
        />
      </Card>

      <BillingReservationDetailSheet
        requestId={selectedRequestId}
        onOpenChange={(open) => {
          if (!open) {
            setSelectedRequestId(null)
          }
        }}
        onDataChanged={refreshList}
      />
    </div>
  )
}
