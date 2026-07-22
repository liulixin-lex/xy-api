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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

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

import { listPaymentAudit } from './api'
import { LegacySubscriptionResolutionDialog } from './legacy-subscription-resolution-dialog'
import { LegacyTopUpResolutionDialog } from './legacy-topup-resolution-dialog'
import { UnmatchedPaymentEventActionDialog } from './payment-action-dialogs'
import { PaymentAuditDetailSheet } from './payment-audit-detail-sheet'
import {
  formatMinorAmount,
  formatUnixTime,
  isPaymentEventActionAvailable,
} from './status'
import {
  CredentialIncidentStatusBadge,
  EventStatusBadge,
  PaymentStatusBadge,
} from './status-badges'
import { TablePagination } from './table-pagination'
import type { PaymentAuditFilters, PaymentEvent } from './types'

const PAGE_SIZE = 20
const UNMATCHED_PAGE_SIZE = 20
const EMPTY_FILTERS: PaymentAuditFilters = {
  status: '',
  provider: '',
  tradeNo: '',
}

export function PaymentAuditPanel() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [draftFilters, setDraftFilters] = useState(EMPTY_FILTERS)
  const [filters, setFilters] = useState(EMPTY_FILTERS)
  const [page, setPage] = useState(1)
  const [unmatchedPage, setUnmatchedPage] = useState(1)
  const [selectedTradeNo, setSelectedTradeNo] = useState<string | null>(null)
  const [unmatchedAction, setUnmatchedAction] = useState<{
    event: PaymentEvent
    action: 'dismiss' | 'link' | 'retry_legacy'
  } | null>(null)
  const [legacyTopUpResolutionEvent, setLegacyTopUpResolutionEvent] =
    useState<PaymentEvent | null>(null)
  const [
    legacySubscriptionResolutionEvent,
    setLegacySubscriptionResolutionEvent,
  ] = useState<PaymentEvent | null>(null)
  const auditQuery = useQuery({
    queryKey: [
      'payment-audit',
      filters,
      page,
      PAGE_SIZE,
      unmatchedPage,
      UNMATCHED_PAGE_SIZE,
    ],
    queryFn: () =>
      listPaymentAudit(
        filters,
        page,
        PAGE_SIZE,
        unmatchedPage,
        UNMATCHED_PAGE_SIZE
      ),
    placeholderData: (previousData) => previousData,
    meta: { skipGlobalError: true },
  })
  const orders = auditQuery.data?.orders ?? []
  const unmatchedEvents = auditQuery.data?.unmatched_events ?? []
  const total = auditQuery.data?.total ?? 0
  const unmatchedTotal = auditQuery.data?.unmatched_total ?? 0
  const auditDataStale = auditQuery.isFetching || auditQuery.isPlaceholderData

  useEffect(() => {
    if (!auditQuery.data) return
    const lastPage = Math.max(1, Math.ceil(total / PAGE_SIZE))
    if (page > lastPage) setPage(lastPage)
  }, [auditQuery.data, page, total])

  useEffect(() => {
    if (!auditQuery.data) return
    const lastPage = Math.max(
      1,
      Math.ceil(unmatchedTotal / UNMATCHED_PAGE_SIZE)
    )
    if (unmatchedPage > lastPage) setUnmatchedPage(lastPage)
  }, [auditQuery.data, unmatchedPage, unmatchedTotal])

  const applyFilters = () => {
    setPage(1)
    setUnmatchedPage(1)
    setFilters({
      status: draftFilters.status,
      provider: draftFilters.provider.trim(),
      tradeNo: draftFilters.tradeNo.trim(),
    })
  }

  const resetFilters = () => {
    setDraftFilters(EMPTY_FILTERS)
    setFilters(EMPTY_FILTERS)
    setPage(1)
    setUnmatchedPage(1)
  }

  const refreshList = async () => {
    await queryClient.invalidateQueries({ queryKey: ['payment-audit'] })
  }

  return (
    <div className='grid gap-4'>
      <Card>
        <CardHeader className='border-b'>
          <CardTitle>{t('Payment Audit')}</CardTitle>
          <CardDescription>
            {t(
              'Review payment exceptions, provider callbacks, ledger activity, and unresolved debt from one authoritative order record.'
            )}
          </CardDescription>
          <CardAction>
            <Button
              type='button'
              variant='outline'
              size='sm'
              disabled={auditQuery.isFetching}
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
          <div className='grid gap-3 md:grid-cols-2 xl:grid-cols-[180px_1fr_1fr_auto]'>
            <div className='grid gap-1.5'>
              <Label htmlFor='payment-audit-status'>{t('Status')}</Label>
              <NativeSelect
                id='payment-audit-status'
                value={draftFilters.status}
                onChange={(event) =>
                  setDraftFilters((current) => ({
                    ...current,
                    status: event.target.value,
                  }))
                }
              >
                <NativeSelectOption value=''>
                  {t('Needs Attention')}
                </NativeSelectOption>
                <NativeSelectOption value='manual_review'>
                  {t('Manual Review')}
                </NativeSelectOption>
                <NativeSelectOption value='refund_pending'>
                  {t('Refund Pending')}
                </NativeSelectOption>
                <NativeSelectOption value='refunded'>
                  {t('Refunded')}
                </NativeSelectOption>
                <NativeSelectOption value='disputed'>
                  {t('Disputed')}
                </NativeSelectOption>
                <NativeSelectOption value='debt'>
                  {t('Payment Debt')}
                </NativeSelectOption>
                <NativeSelectOption value='pending'>
                  {t('Pending')}
                </NativeSelectOption>
                <NativeSelectOption value='processing'>
                  {t('Processing')}
                </NativeSelectOption>
                <NativeSelectOption value='paid'>
                  {t('Paid')}
                </NativeSelectOption>
                <NativeSelectOption value='fulfilled'>
                  {t('Fulfilled')}
                </NativeSelectOption>
                <NativeSelectOption value='failed'>
                  {t('Failed')}
                </NativeSelectOption>
                <NativeSelectOption value='expired'>
                  {t('Expired')}
                </NativeSelectOption>
              </NativeSelect>
            </div>
            <div className='grid gap-1.5'>
              <Label htmlFor='payment-audit-provider'>{t('Provider')}</Label>
              <Input
                id='payment-audit-provider'
                value={draftFilters.provider}
                placeholder={t('stripe, epay, xorpay...')}
                onChange={(event) =>
                  setDraftFilters((current) => ({
                    ...current,
                    provider: event.target.value,
                  }))
                }
                onKeyDown={(event) => {
                  if (event.key === 'Enter') applyFilters()
                }}
              />
            </div>
            <div className='grid gap-1.5'>
              <Label htmlFor='payment-audit-trade-no'>
                {t('Exact Trade Number')}
              </Label>
              <Input
                id='payment-audit-trade-no'
                value={draftFilters.tradeNo}
                placeholder={t('Enter the complete trade number')}
                onChange={(event) =>
                  setDraftFilters((current) => ({
                    ...current,
                    tradeNo: event.target.value,
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
          <CardTitle>{t('Orders Requiring Attention')}</CardTitle>
          <CardDescription>
            {filters.status
              ? t('Showing the selected payment state.')
              : t(
                  'The default queue includes credential incidents, manual review, payment debt, disputes, and pending refunds.'
                )}
          </CardDescription>
        </CardHeader>
        {auditQuery.isLoading && (
          <div className='grid gap-2 p-4'>
            {Array.from({ length: 5 }, (_, index) => index).map((key) => (
              <Skeleton key={key} className='h-12 w-full' />
            ))}
          </div>
        )}
        {!auditQuery.isLoading && auditQuery.isError && (
          <div className='p-4'>
            <Alert variant='destructive'>
              <AlertDescription>
                {auditQuery.error instanceof Error
                  ? auditQuery.error.message
                  : t('Failed to load payment audit')}
              </AlertDescription>
            </Alert>
          </div>
        )}
        {!auditQuery.isLoading &&
          !auditQuery.isError &&
          orders.length === 0 && (
            <div className='text-muted-foreground p-10 text-center text-sm'>
              {t('No payment orders match these filters.')}
            </div>
          )}
        {!auditQuery.isLoading && !auditQuery.isError && orders.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('Order')}</TableHead>
                <TableHead>{t('User')}</TableHead>
                <TableHead>{t('Provider')}</TableHead>
                <TableHead>{t('Amount')}</TableHead>
                <TableHead>{t('Status')}</TableHead>
                <TableHead>{t('Updated At')}</TableHead>
                <TableHead className='text-right'>{t('Actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {orders.map((order) => (
                <TableRow key={order.id}>
                  <TableCell className='max-w-64 whitespace-normal'>
                    <div className='grid gap-0.5'>
                      <code className='truncate text-xs'>{order.trade_no}</code>
                      <span className='text-muted-foreground text-xs'>
                        {order.order_kind === 'subscription'
                          ? t('Subscription')
                          : t('Top-up')}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell className='tabular-nums'>
                    {order.user_id}
                  </TableCell>
                  <TableCell>
                    <div className='grid gap-0.5'>
                      <span>{order.provider}</span>
                      <span className='text-muted-foreground text-xs'>
                        {order.payment_method || '-'}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className='grid gap-0.5 tabular-nums'>
                      <span>
                        {formatMinorAmount(
                          order.expected_amount_minor,
                          order.currency,
                          order.provider
                        )}
                      </span>
                      <span className='text-muted-foreground text-xs'>
                        {t('Paid')}:{' '}
                        {formatMinorAmount(
                          order.paid_amount_minor,
                          order.currency,
                          order.provider
                        )}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell className='max-w-56 whitespace-normal'>
                    <div className='grid gap-1'>
                      <PaymentStatusBadge status={order.status} t={t} />
                      {order.credential_incident_state && (
                        <CredentialIncidentStatusBadge
                          status={order.credential_incident_state}
                          t={t}
                        />
                      )}
                      {order.status_reason && (
                        <span className='text-muted-foreground line-clamp-2 text-xs'>
                          {order.status_reason}
                        </span>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>{formatUnixTime(order.updated_at)}</TableCell>
                  <TableCell className='text-right'>
                    <Button
                      type='button'
                      variant='outline'
                      size='sm'
                      disabled={auditDataStale}
                      onClick={() => setSelectedTradeNo(order.trade_no)}
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
          disabled={auditQuery.isFetching}
          onPageChange={setPage}
        />
      </Card>

      <Card className='gap-0 py-0'>
        <CardHeader className='border-b py-4'>
          <CardTitle>{t('Unmatched Callback Events')}</CardTitle>
          <CardDescription>
            {t(
              'Only actions authorized by the server are shown. Legacy Epay top-ups without a quota snapshot require an explicit quota or confirmed refund; classified legacy subscriptions can only record a full refund already completed by the payment provider.'
            )}
          </CardDescription>
          <CardAction>
            <span className='text-muted-foreground text-xs tabular-nums'>
              {unmatchedTotal}
            </span>
          </CardAction>
        </CardHeader>
        {unmatchedEvents.length === 0 ? (
          <div className='text-muted-foreground p-8 text-center text-sm'>
            {t('No unmatched callback events.')}
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('Provider Event')}</TableHead>
                <TableHead>{t('Trade Number')}</TableHead>
                <TableHead>{t('Status')}</TableHead>
                <TableHead>{t('Amount')}</TableHead>
                <TableHead>{t('Last Error')}</TableHead>
                <TableHead>{t('Updated At')}</TableHead>
                <TableHead className='text-right'>{t('Actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {unmatchedEvents.map((event) => {
                const canRetryLegacy = isPaymentEventActionAvailable(
                  event,
                  'retry_legacy'
                )
                const canResolveLegacyTopUp = isPaymentEventActionAvailable(
                  event,
                  'resolve_legacy_topup'
                )
                const canResolveLegacySubscription =
                  isPaymentEventActionAvailable(
                    event,
                    'resolve_legacy_subscription'
                  )
                const canLink = isPaymentEventActionAvailable(event, 'link')
                const canDismiss = isPaymentEventActionAvailable(
                  event,
                  'dismiss'
                )
                const hasAvailableAction =
                  canRetryLegacy ||
                  canResolveLegacyTopUp ||
                  canResolveLegacySubscription ||
                  canLink ||
                  canDismiss
                return (
                  <TableRow key={event.id}>
                    <TableCell className='max-w-64 whitespace-normal'>
                      <div className='grid gap-0.5'>
                        <span>{event.event_type}</span>
                        <code className='text-muted-foreground truncate text-xs'>
                          {event.provider} · {event.event_key}
                        </code>
                      </div>
                    </TableCell>
                    <TableCell>
                      <code className='text-xs'>{event.trade_no || '-'}</code>
                    </TableCell>
                    <TableCell>
                      <EventStatusBadge status={event.status} t={t} />
                    </TableCell>
                    <TableCell>
                      {formatMinorAmount(
                        event.paid_amount_minor ||
                          event.refunded_amount_minor ||
                          event.disputed_amount_minor,
                        event.currency,
                        event.provider
                      )}
                    </TableCell>
                    <TableCell className='text-destructive max-w-64 whitespace-normal'>
                      {event.last_error || '-'}
                    </TableCell>
                    <TableCell>{formatUnixTime(event.updated_at)}</TableCell>
                    <TableCell className='text-right'>
                      {hasAvailableAction ? (
                        <div className='flex flex-wrap justify-end gap-2'>
                          {canRetryLegacy && (
                            <Button
                              type='button'
                              variant='outline'
                              size='sm'
                              disabled={auditDataStale}
                              onClick={() =>
                                setUnmatchedAction({
                                  event,
                                  action: 'retry_legacy',
                                })
                              }
                            >
                              {t('Safe retry legacy order')}
                            </Button>
                          )}
                          {canResolveLegacyTopUp && (
                            <Button
                              type='button'
                              variant='outline'
                              size='sm'
                              disabled={auditDataStale}
                              onClick={() =>
                                setLegacyTopUpResolutionEvent(event)
                              }
                            >
                              {t('Resolve legacy top-up')}
                            </Button>
                          )}
                          {canResolveLegacySubscription && (
                            <Button
                              type='button'
                              variant='outline'
                              size='sm'
                              disabled={auditDataStale}
                              onClick={() =>
                                setLegacySubscriptionResolutionEvent(event)
                              }
                            >
                              {t('Record completed refund')}
                            </Button>
                          )}
                          {canLink && (
                            <Button
                              type='button'
                              variant='outline'
                              size='sm'
                              disabled={auditDataStale}
                              onClick={() =>
                                setUnmatchedAction({ event, action: 'link' })
                              }
                            >
                              {t('Link')}
                            </Button>
                          )}
                          {canDismiss && (
                            <Button
                              type='button'
                              variant='destructive'
                              size='sm'
                              disabled={auditDataStale}
                              onClick={() =>
                                setUnmatchedAction({
                                  event,
                                  action: 'dismiss',
                                })
                              }
                            >
                              {t('Dismiss')}
                            </Button>
                          )}
                        </div>
                      ) : (
                        <span className='text-muted-foreground text-xs'>
                          {t('Read-only history')}
                        </span>
                      )}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
        <TablePagination
          page={unmatchedPage}
          pageSize={UNMATCHED_PAGE_SIZE}
          total={unmatchedTotal}
          disabled={auditQuery.isFetching}
          onPageChange={setUnmatchedPage}
        />
      </Card>

      <PaymentAuditDetailSheet
        tradeNo={selectedTradeNo}
        onOpenChange={(open) => {
          if (!open) setSelectedTradeNo(null)
        }}
        onDataChanged={refreshList}
      />
      <UnmatchedPaymentEventActionDialog
        event={unmatchedAction?.event ?? null}
        action={unmatchedAction?.action ?? null}
        open={unmatchedAction !== null}
        onOpenChange={(open) => {
          if (!open) setUnmatchedAction(null)
        }}
        onCompleted={refreshList}
      />
      <LegacyTopUpResolutionDialog
        event={legacyTopUpResolutionEvent}
        open={legacyTopUpResolutionEvent !== null}
        onOpenChange={(open) => {
          if (!open) setLegacyTopUpResolutionEvent(null)
        }}
        onCompleted={refreshList}
      />
      <LegacySubscriptionResolutionDialog
        event={legacySubscriptionResolutionEvent}
        open={legacySubscriptionResolutionEvent !== null}
        onOpenChange={(open) => {
          if (!open) setLegacySubscriptionResolutionEvent(null)
        }}
        onCompleted={refreshList}
      />
    </div>
  )
}
