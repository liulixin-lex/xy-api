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
import { RefreshIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { getBillingReservation } from './api'
import { isBillingReservationReviewable } from './billing-reservation'
import { BillingReservationResolutionDialog } from './billing-reservation-resolution-dialog'
import { formatInteger, formatUnixTime } from './status'

type Resolution = 'settle' | 'refund'

function DetailField(props: { label: string; children: React.ReactNode }) {
  return (
    <div className='grid min-w-0 gap-1'>
      <dt className='text-muted-foreground text-xs'>{props.label}</dt>
      <dd className='min-w-0 text-sm break-words'>{props.children}</dd>
    </div>
  )
}

function DetailSection(props: {
  title: string
  count?: number
  children: React.ReactNode
}) {
  return (
    <section className='grid gap-2'>
      <div className='flex items-center justify-between gap-2'>
        <h3 className='text-sm font-medium'>{props.title}</h3>
        {props.count !== undefined && (
          <span className='text-muted-foreground text-xs tabular-nums'>
            {props.count}
          </span>
        )}
      </div>
      {props.children}
    </section>
  )
}

function reservationStatus(status: string, t: TFunction) {
  switch (status) {
    case 'reserved':
      return { label: t('Reserved'), variant: 'warning' as const }
    case 'settled':
      return { label: t('Settled'), variant: 'success' as const }
    case 'refunded':
      return { label: t('Refunded'), variant: 'neutral' as const }
    default:
      return { label: status || t('Unknown'), variant: 'neutral' as const }
  }
}

function ledgerPhase(phase: string, t: TFunction): string {
  switch (phase) {
    case 'reserve':
      return t('Reserve')
    case 'legacy_adopt':
      return t('Legacy adoption')
    case 'bind':
      return t('Resource binding')
    case 'settle_intent':
      return t('Settlement intent')
    case 'settle':
      return t('Settlement')
    case 'refund':
      return t('Refund')
    case 'reconcile_review':
      return t('Reconciliation review')
    case 'admin_resolution':
      return t('Administrator resolution')
    default:
      return phase
  }
}

function signedQuota(value: number): string {
  if (!value) {
    return '0'
  }
  return `${value > 0 ? '+' : ''}${formatInteger(value)}`
}

export function BillingReservationDetailSheet(props: {
  requestId: string | null
  onOpenChange: (open: boolean) => void
  onDataChanged: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const [resolution, setResolution] = useState<Resolution | null>(null)
  const detailQuery = useQuery({
    queryKey: ['billing-reservation-detail', props.requestId],
    queryFn: () => getBillingReservation(props.requestId || ''),
    enabled: Boolean(props.requestId),
    meta: { skipGlobalError: true },
  })
  const reservation = detailQuery.data?.reservation
  const ledger = detailQuery.data?.ledger ?? []
  const adminResolutions = detailQuery.data?.admin_resolutions ?? []
  const status = reservationStatus(reservation?.status || '', t)
  const reviewable = reservation
    ? isBillingReservationReviewable(reservation)
    : false

  const handleDataChanged = async () => {
    await detailQuery.refetch()
    await props.onDataChanged()
  }

  return (
    <>
      <Sheet
        open={Boolean(props.requestId)}
        onOpenChange={(open) => {
          if (!open) {
            props.onOpenChange(false)
          }
        }}
      >
        <SheetContent className='w-full sm:max-w-[840px]'>
          <SheetHeader className='border-b pr-12'>
            <div className='flex items-start justify-between gap-3'>
              <div className='min-w-0'>
                <SheetTitle>{t('Billing reservation detail')}</SheetTitle>
                <SheetDescription className='truncate font-mono'>
                  {props.requestId}
                </SheetDescription>
              </div>
              <Button
                type='button'
                variant='outline'
                size='icon-sm'
                disabled={detailQuery.isFetching}
                aria-label={t('Refresh')}
                onClick={() => detailQuery.refetch()}
              >
                <HugeiconsIcon icon={RefreshIcon} strokeWidth={2} />
              </Button>
            </div>
          </SheetHeader>

          {detailQuery.isLoading && (
            <div className='grid gap-3 px-4'>
              <Skeleton className='h-32 w-full' />
              <Skeleton className='h-52 w-full' />
              <Skeleton className='h-40 w-full' />
            </div>
          )}
          {!detailQuery.isLoading && detailQuery.isError && (
            <div className='px-4'>
              <Alert variant='destructive'>
                <AlertDescription className='flex flex-col items-start justify-between gap-3 sm:flex-row sm:items-center'>
                  <span>{t('Failed to load billing reservation detail')}</span>
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    onClick={() => detailQuery.refetch()}
                  >
                    {t('Retry')}
                  </Button>
                </AlertDescription>
              </Alert>
            </div>
          )}
          {!detailQuery.isLoading && !detailQuery.isError && reservation && (
            <ScrollArea className='min-h-0 flex-1'>
              <div className='grid gap-5 px-4 pb-5'>
                <Alert variant={reviewable ? 'destructive' : 'default'}>
                  <AlertDescription>
                    {reservation.reconcile_note ||
                      t(
                        'This reservation is visible because it exceeded the stale threshold or has a pending settlement intent.'
                      )}
                  </AlertDescription>
                </Alert>

                <DetailSection title={t('Reservation Summary')}>
                  <dl className='grid gap-3 rounded-lg border p-3 sm:grid-cols-2 lg:grid-cols-4'>
                    <DetailField label={t('Status')}>
                      <StatusBadge
                        label={status.label}
                        variant={status.variant}
                        copyable={false}
                      />
                    </DetailField>
                    <DetailField label={t('Version')}>
                      {reservation.version}
                    </DetailField>
                    <DetailField label={t('User ID')}>
                      {reservation.user_id}
                    </DetailField>
                    <DetailField label={t('Token ID')}>
                      {reservation.token_id || '-'}
                    </DetailField>
                    <DetailField label={t('Created At')}>
                      {formatUnixTime(reservation.created_at)}
                    </DetailField>
                    <DetailField label={t('Updated At')}>
                      {formatUnixTime(reservation.updated_at)}
                    </DetailField>
                    <DetailField label={t('Last Reconciled At')}>
                      {formatUnixTime(reservation.last_reconciled_at)}
                    </DetailField>
                    <DetailField label={t('Legacy Adopted')}>
                      {reservation.legacy_adopted ? t('Yes') : t('No')}
                    </DetailField>
                  </dl>
                  <dl className='grid gap-3 rounded-lg border p-3 sm:grid-cols-2'>
                    <DetailField label={t('Request ID')}>
                      <code>{reservation.request_id}</code>
                    </DetailField>
                    <DetailField label={t('Review Reason')}>
                      {reservation.reconcile_note || '-'}
                    </DetailField>
                  </dl>
                </DetailSection>

                <DetailSection title={t('Financial Reservation')}>
                  <dl className='grid gap-3 rounded-lg border p-3 sm:grid-cols-2 lg:grid-cols-4'>
                    <DetailField label={t('Funding Source')}>
                      {reservation.funding_source === 'subscription'
                        ? t('Subscription quota')
                        : t('Wallet quota')}
                    </DetailField>
                    <DetailField label={t('Subscription ID')}>
                      {reservation.subscription_id || '-'}
                    </DetailField>
                    <DetailField label={t('Initial Pre-consume')}>
                      <span className='tabular-nums'>
                        {formatInteger(reservation.initial_quota)}
                      </span>
                    </DetailField>
                    <DetailField label={t('Current Reserved Quota')}>
                      <span className='tabular-nums'>
                        {formatInteger(reservation.reserved_quota)}
                      </span>
                    </DetailField>
                    <DetailField label={t('Token Reserved Quota')}>
                      <span className='tabular-nums'>
                        {formatInteger(reservation.token_reserved)}
                      </span>
                    </DetailField>
                    <DetailField label={t('Settlement Pending')}>
                      {reservation.settlement_pending ? t('Yes') : t('No')}
                    </DetailField>
                    <DetailField label={t('Exact Settlement Target')}>
                      {reservation.settlement_pending
                        ? formatInteger(reservation.settlement_target)
                        : t('Not recorded')}
                    </DetailField>
                    <DetailField label={t('Settled Quota')}>
                      {reservation.status === 'settled'
                        ? formatInteger(reservation.settled_quota)
                        : '-'}
                    </DetailField>
                  </dl>
                </DetailSection>

                <DetailSection title={t('Resource Binding')}>
                  <dl className='grid gap-3 rounded-lg border p-3 sm:grid-cols-2'>
                    <DetailField label={t('Resource Type')}>
                      {reservation.resource_type || t('Unbound')}
                    </DetailField>
                    <DetailField label={t('Resource ID')}>
                      {reservation.resource_id ? (
                        <code>{reservation.resource_id}</code>
                      ) : (
                        t('Unbound')
                      )}
                    </DetailField>
                  </dl>
                </DetailSection>

                {reviewable && (
                  <DetailSection title={t('Administrator Resolution')}>
                    <Alert variant='destructive'>
                      <AlertDescription>
                        {t(
                          'Choose settlement only with an exact final quota. Choose refund only when the entire pre-consume must return to its original source.'
                        )}
                      </AlertDescription>
                    </Alert>
                    <div className='flex flex-col justify-end gap-2 sm:flex-row'>
                      <Button
                        type='button'
                        variant='outline'
                        onClick={() => setResolution('settle')}
                      >
                        {t('Settle exact quota')}
                      </Button>
                      <Button
                        type='button'
                        variant='destructive'
                        onClick={() => setResolution('refund')}
                      >
                        {t('Refund full reservation')}
                      </Button>
                    </div>
                  </DetailSection>
                )}

                <DetailSection title={t('Quota Ledger')} count={ledger.length}>
                  {ledger.length === 0 ? (
                    <div className='text-muted-foreground rounded-lg border border-dashed p-5 text-center text-sm'>
                      {t('No quota ledger entries')}
                    </div>
                  ) : (
                    <div className='rounded-lg border'>
                      <Table>
                        <TableHeader>
                          <TableRow>
                            <TableHead>{t('Revision')}</TableHead>
                            <TableHead>{t('Phase')}</TableHead>
                            <TableHead>{t('Funding Delta')}</TableHead>
                            <TableHead>{t('Token Delta')}</TableHead>
                            <TableHead>{t('Note')}</TableHead>
                            <TableHead>{t('Created At')}</TableHead>
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {ledger.map((entry) => {
                            const fundingDelta =
                              entry.funding_source === 'subscription'
                                ? entry.subscription_total_used_delta
                                : entry.user_quota_delta
                            return (
                              <TableRow key={entry.id}>
                                <TableCell className='tabular-nums'>
                                  {entry.revision}
                                </TableCell>
                                <TableCell>
                                  {ledgerPhase(entry.phase, t)}
                                </TableCell>
                                <TableCell className='tabular-nums'>
                                  {signedQuota(fundingDelta)}
                                </TableCell>
                                <TableCell className='tabular-nums'>
                                  {signedQuota(entry.token_used_quota_delta)}
                                </TableCell>
                                <TableCell className='max-w-64 whitespace-normal'>
                                  {entry.note || '-'}
                                </TableCell>
                                <TableCell>
                                  {formatUnixTime(entry.created_at)}
                                </TableCell>
                              </TableRow>
                            )
                          })}
                        </TableBody>
                      </Table>
                    </div>
                  )}
                </DetailSection>

                {adminResolutions.length > 0 && (
                  <DetailSection
                    title={t('Administrator Resolution Ledger')}
                    count={adminResolutions.length}
                  >
                    <div className='grid gap-2'>
                      {adminResolutions.map((entry) => (
                        <dl
                          key={entry.id}
                          className='grid gap-3 rounded-lg border p-3 sm:grid-cols-2 lg:grid-cols-4'
                        >
                          <DetailField label={t('Resolution')}>
                            {entry.resolution === 'settle'
                              ? t('Settlement')
                              : t('Refund')}
                          </DetailField>
                          <DetailField label={t('Administrator ID')}>
                            {entry.admin_id}
                          </DetailField>
                          <DetailField label={t('Expected Version')}>
                            {entry.expected_version}
                          </DetailField>
                          <DetailField label={t('Actual Quota')}>
                            {entry.actual_quota === undefined
                              ? t('Not applicable')
                              : formatInteger(entry.actual_quota)}
                          </DetailField>
                          <div className='grid gap-1 sm:col-span-2 lg:col-span-4'>
                            <dt className='text-muted-foreground text-xs'>
                              {t('Reason')}
                            </dt>
                            <dd className='text-sm break-words'>
                              {entry.reason}
                            </dd>
                          </div>
                        </dl>
                      ))}
                    </div>
                  </DetailSection>
                )}
              </div>
            </ScrollArea>
          )}
        </SheetContent>
      </Sheet>

      <BillingReservationResolutionDialog
        reservation={reservation ?? null}
        resolution={resolution}
        open={resolution !== null}
        onOpenChange={(open) => {
          if (!open) {
            setResolution(null)
          }
        }}
        onCompleted={handleDataChanged}
      />
    </>
  )
}
