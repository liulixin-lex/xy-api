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

import { getPaymentAudit } from './api'
import {
  CredentialIncidentActionDialog,
  PaymentOrderActionDialog,
  ResolveDebtDialog,
  StripeCustomerBindingRetirementDialog,
} from './payment-action-dialogs'
import {
  formatInteger,
  formatMinorAmount,
  formatUnixTime,
  getCredentialIncidentActions,
} from './status'
import {
  CredentialIncidentStatusBadge,
  EventStatusBadge,
  PaymentStatusBadge,
} from './status-badges'
import type { PaymentCustomerBinding, PaymentDebt } from './types'

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

export function PaymentAuditDetailSheet(props: {
  tradeNo: string | null
  onOpenChange: (open: boolean) => void
  onDataChanged: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const [orderAction, setOrderAction] = useState<
    'reject' | 'void' | 'external-refund' | null
  >(null)
  const [credentialIncidentAction, setCredentialIncidentAction] = useState<
    'acknowledge' | 'resolve' | null
  >(null)
  const [selectedDebt, setSelectedDebt] = useState<PaymentDebt | null>(null)
  const [selectedCustomerBinding, setSelectedCustomerBinding] =
    useState<PaymentCustomerBinding | null>(null)
  const detailQuery = useQuery({
    queryKey: ['payment-audit-detail', props.tradeNo],
    queryFn: () => getPaymentAudit(props.tradeNo || ''),
    enabled: Boolean(props.tradeNo),
    meta: { skipGlobalError: true },
  })
  const order = detailQuery.data?.order
  const events = detailQuery.data?.events ?? []
  const debts = detailQuery.data?.debts ?? []
  const ledger = detailQuery.data?.ledger ?? []
  const customerBindings = detailQuery.data?.customer_bindings ?? []
  const customerBindingRetirements =
    detailQuery.data?.customer_binding_retirements ?? []
  const operationsAudits = detailQuery.data?.operations_audits ?? []
  let orderKindLabel = order?.order_kind || '-'
  if (order?.order_kind === 'subscription') orderKindLabel = t('Subscription')
  if (order?.order_kind === 'topup') orderKindLabel = t('Top-up')
  const canConfirmExternalRefund = Boolean(
    order &&
    order.expected_amount_minor > order.refunded_amount_minor &&
    order.disputed_amount_minor === 0
  )
  const credentialIncidentActions = order
    ? getCredentialIncidentActions(
        order.credential_incident_state || '',
        order.credential_incident
      )
    : []

  const handleDataChanged = async () => {
    await detailQuery.refetch()
    await props.onDataChanged()
  }

  return (
    <>
      <Sheet
        open={Boolean(props.tradeNo)}
        onOpenChange={(open) => {
          if (!open) props.onOpenChange(false)
        }}
      >
        <SheetContent className='w-full sm:max-w-[760px]'>
          <SheetHeader className='border-b pr-12'>
            <div className='flex items-start justify-between gap-3'>
              <div className='min-w-0'>
                <SheetTitle>{t('Payment audit detail')}</SheetTitle>
                <SheetDescription className='truncate font-mono'>
                  {props.tradeNo}
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
              <Skeleton className='h-24 w-full' />
              <Skeleton className='h-44 w-full' />
              <Skeleton className='h-32 w-full' />
            </div>
          )}
          {!detailQuery.isLoading && detailQuery.isError && (
            <div className='px-4'>
              <Alert variant='destructive'>
                <AlertDescription>
                  {detailQuery.error instanceof Error
                    ? detailQuery.error.message
                    : t('Failed to load payment audit detail')}
                </AlertDescription>
              </Alert>
            </div>
          )}
          {!detailQuery.isLoading && !detailQuery.isError && order && (
            <ScrollArea className='min-h-0 flex-1'>
              <div className='grid gap-5 px-4 pb-5'>
                {(order.status === 'manual_review' ||
                  order.status === 'refund_pending' ||
                  order.status === 'disputed' ||
                  order.status === 'debt') && (
                  <Alert
                    variant={
                      order.status === 'disputed' || order.status === 'debt'
                        ? 'destructive'
                        : 'default'
                    }
                  >
                    <AlertDescription>
                      {order.status_reason ||
                        t(
                          'This order needs administrator attention before its financial state is considered closed.'
                        )}
                    </AlertDescription>
                  </Alert>
                )}

                {detailQuery.data?.legacy_review_reason ? (
                  <Alert variant='destructive'>
                    <AlertDescription className='grid gap-1'>
                      <span className='font-medium'>
                        {t('Legacy callback review evidence')}
                      </span>
                      <code className='break-all'>
                        {detailQuery.data.legacy_review_reason}
                      </code>
                    </AlertDescription>
                  </Alert>
                ) : null}

                {order.credential_incident_state && (
                  <DetailSection title={t('Payment Credential Incident')}>
                    <Alert
                      variant={
                        order.credential_incident ? 'destructive' : 'default'
                      }
                    >
                      <AlertDescription>
                        {order.credential_incident_reason ||
                          t(
                            'This order is associated with a revoked or ambiguous payment credential and requires an audited investigation.'
                          )}
                      </AlertDescription>
                    </Alert>
                    <dl className='grid gap-3 rounded-lg border p-3 sm:grid-cols-2 lg:grid-cols-3'>
                      <DetailField label={t('Incident Status')}>
                        <CredentialIncidentStatusBadge
                          status={order.credential_incident_state}
                          t={t}
                        />
                      </DetailField>
                      <DetailField label={t('Credential Generation')}>
                        {order.credential_incident_generation
                          ? formatInteger(order.credential_incident_generation)
                          : t('Unknown or legacy')}
                      </DetailField>
                      <DetailField label={t('Incident Detected At')}>
                        {formatUnixTime(order.credential_incident_at)}
                      </DetailField>
                      <DetailField label={t('Last Reviewed At')}>
                        {formatUnixTime(order.credential_incident_reviewed_at)}
                      </DetailField>
                      <DetailField label={t('Reviewed By')}>
                        {order.credential_incident_reviewed_by || '-'}
                      </DetailField>
                      <DetailField label={t('Review Note')}>
                        {order.credential_incident_review_note || '-'}
                      </DetailField>
                    </dl>
                    {credentialIncidentActions.length > 0 && (
                      <div className='flex flex-wrap justify-end gap-2'>
                        {credentialIncidentActions.includes('acknowledge') && (
                          <Button
                            type='button'
                            variant='outline'
                            disabled={detailQuery.isFetching}
                            onClick={() =>
                              setCredentialIncidentAction('acknowledge')
                            }
                          >
                            {t('Acknowledge incident')}
                          </Button>
                        )}
                        {credentialIncidentActions.includes('resolve') && (
                          <Button
                            type='button'
                            disabled={detailQuery.isFetching}
                            onClick={() =>
                              setCredentialIncidentAction('resolve')
                            }
                          >
                            {t('Resolve incident')}
                          </Button>
                        )}
                      </div>
                    )}
                  </DetailSection>
                )}

                <DetailSection title={t('Order Summary')}>
                  <dl className='grid gap-3 rounded-lg border p-3 sm:grid-cols-2 lg:grid-cols-3'>
                    <DetailField label={t('Status')}>
                      <PaymentStatusBadge status={order.status} t={t} />
                    </DetailField>
                    <DetailField label={t('User ID')}>
                      {order.user_id}
                    </DetailField>
                    <DetailField label={t('Order Type')}>
                      {orderKindLabel}
                    </DetailField>
                    <DetailField label={t('Provider')}>
                      {order.provider}
                    </DetailField>
                    <DetailField label={t('Payment Method')}>
                      {order.payment_method || '-'}
                    </DetailField>
                    <DetailField label={t('Version')}>
                      {order.version}
                    </DetailField>
                    <DetailField label={t('Created At')}>
                      {formatUnixTime(order.created_at)}
                    </DetailField>
                    <DetailField label={t('Updated At')}>
                      {formatUnixTime(order.updated_at)}
                    </DetailField>
                    <DetailField label={t('Settled At')}>
                      {formatUnixTime(order.settled_at)}
                    </DetailField>
                  </dl>
                  <dl className='grid gap-3 rounded-lg border p-3 sm:grid-cols-2'>
                    <DetailField label={t('Trade Number')}>
                      <code>{order.trade_no}</code>
                    </DetailField>
                    <DetailField label={t('Request ID')}>
                      <code>{order.request_id}</code>
                    </DetailField>
                    <DetailField label={t('Provider Order ID')}>
                      <code>{order.provider_order_key || '-'}</code>
                    </DetailField>
                    <DetailField label={t('Provider Payment ID')}>
                      <code>{order.provider_payment_key || '-'}</code>
                    </DetailField>
                  </dl>
                </DetailSection>

                <DetailSection title={t('Financial Summary')}>
                  <dl className='grid gap-3 rounded-lg border p-3 sm:grid-cols-2 lg:grid-cols-4'>
                    <DetailField label={t('Expected Amount')}>
                      <span className='tabular-nums'>
                        {formatMinorAmount(
                          order.expected_amount_minor,
                          order.currency,
                          order.provider
                        )}
                      </span>
                    </DetailField>
                    <DetailField label={t('Paid Amount')}>
                      <span className='tabular-nums'>
                        {formatMinorAmount(
                          order.paid_amount_minor,
                          order.currency,
                          order.provider
                        )}
                      </span>
                    </DetailField>
                    <DetailField label={t('Refunded Amount')}>
                      <span className='tabular-nums'>
                        {formatMinorAmount(
                          order.refunded_amount_minor,
                          order.currency,
                          order.provider
                        )}
                      </span>
                    </DetailField>
                    <DetailField label={t('Disputed Amount')}>
                      <span className='tabular-nums'>
                        {formatMinorAmount(
                          order.disputed_amount_minor,
                          order.currency,
                          order.provider
                        )}
                      </span>
                    </DetailField>
                    <DetailField label={t('Credit Quota')}>
                      <span className='tabular-nums'>
                        {formatInteger(order.credit_quota)}
                      </span>
                    </DetailField>
                    <DetailField label={t('Reversed Quota')}>
                      <span className='tabular-nums'>
                        {formatInteger(order.reversed_quota)}
                      </span>
                    </DetailField>
                  </dl>
                  {(order.status === 'manual_review' ||
                    canConfirmExternalRefund) && (
                    <div className='flex flex-wrap justify-end gap-2'>
                      {canConfirmExternalRefund && (
                        <Button
                          type='button'
                          variant='outline'
                          onClick={() => setOrderAction('external-refund')}
                        >
                          {t('Confirm external refund')}
                        </Button>
                      )}
                      {order.status === 'manual_review' && (
                        <>
                          <Button
                            type='button'
                            variant='outline'
                            onClick={() => setOrderAction('void')}
                          >
                            {t('Local void')}
                          </Button>
                          <Button
                            type='button'
                            variant='destructive'
                            onClick={() => setOrderAction('reject')}
                          >
                            {t('Reject')}
                          </Button>
                        </>
                      )}
                    </div>
                  )}
                </DetailSection>

                <DetailSection title={t('Payment Debts')} count={debts.length}>
                  {debts.length === 0 ? (
                    <div className='text-muted-foreground rounded-lg border border-dashed p-5 text-center text-sm'>
                      {t('No payment debt records')}
                    </div>
                  ) : (
                    <div className='grid gap-2'>
                      {debts.map((debt) => (
                        <div
                          key={debt.id}
                          className='grid gap-3 rounded-lg border p-3 sm:grid-cols-[1fr_auto]'
                        >
                          <div className='grid gap-2 sm:grid-cols-2'>
                            <DetailField label={t('Debt Type')}>
                              {debt.debt_kind}
                            </DetailField>
                            <DetailField label={t('Status')}>
                              <StatusBadge
                                label={
                                  debt.status === 'open'
                                    ? t('Open')
                                    : t('Resolved')
                                }
                                variant={
                                  debt.status === 'open' ? 'danger' : 'success'
                                }
                                copyable={false}
                              />
                            </DetailField>
                            <DetailField label={t('Outstanding Amount')}>
                              {formatMinorAmount(
                                debt.outstanding_amount_minor,
                                debt.currency,
                                order.provider
                              )}
                            </DetailField>
                            <DetailField label={t('Outstanding Quota')}>
                              {formatInteger(debt.outstanding_quota)}
                            </DetailField>
                            {debt.resolution && (
                              <DetailField label={t('Resolution')}>
                                {debt.resolution === 'waived'
                                  ? t('Waived')
                                  : t('Repaid')}
                              </DetailField>
                            )}
                            {debt.resolution_note && (
                              <DetailField label={t('Resolution note')}>
                                {debt.resolution_note}
                              </DetailField>
                            )}
                          </div>
                          {debt.status === 'open' && (
                            <Button
                              type='button'
                              variant='outline'
                              size='sm'
                              className='self-start'
                              onClick={() => setSelectedDebt(debt)}
                            >
                              {t('Resolve')}
                            </Button>
                          )}
                        </div>
                      ))}
                    </div>
                  )}
                </DetailSection>

                {order.provider === 'stripe' && (
                  <DetailSection
                    title={t('Stripe Customer Bindings')}
                    count={
                      customerBindings.length +
                      customerBindingRetirements.length
                    }
                  >
                    <div className='grid gap-2'>
                      <h4 className='text-muted-foreground text-xs font-medium tracking-wide uppercase'>
                        {t('Active Bindings')}
                      </h4>
                      {customerBindings.length === 0 ? (
                        <div className='text-muted-foreground rounded-lg border border-dashed p-5 text-center text-sm'>
                          {t('No active Stripe customer bindings')}
                        </div>
                      ) : (
                        customerBindings.map((binding) => (
                          <div
                            key={binding.id}
                            className='grid gap-3 rounded-lg border p-3 sm:grid-cols-[1fr_auto]'
                          >
                            <dl className='grid min-w-0 gap-3 sm:grid-cols-2 lg:grid-cols-3'>
                              <DetailField label={t('Stripe Customer ID')}>
                                <code>{binding.customer_key}</code>
                              </DetailField>
                              <DetailField label={t('Binding ID')}>
                                {binding.id}
                              </DetailField>
                              <DetailField label={t('Version')}>
                                {binding.version}
                              </DetailField>
                              <DetailField label={t('Created At')}>
                                {formatUnixTime(binding.created_at)}
                              </DetailField>
                              <DetailField label={t('Updated At')}>
                                {formatUnixTime(binding.updated_at)}
                              </DetailField>
                            </dl>
                            <Button
                              type='button'
                              variant='destructive'
                              size='sm'
                              className='self-start'
                              disabled={detailQuery.isFetching}
                              onClick={() =>
                                setSelectedCustomerBinding(binding)
                              }
                            >
                              {t('Retire binding')}
                            </Button>
                          </div>
                        ))
                      )}
                    </div>

                    {customerBindingRetirements.length > 0 && (
                      <div className='grid gap-2'>
                        <h4 className='text-muted-foreground text-xs font-medium tracking-wide uppercase'>
                          {t('Retirement History')}
                        </h4>
                        {customerBindingRetirements.map((retirement) => (
                          <dl
                            key={retirement.id}
                            className='grid gap-3 rounded-lg border p-3 sm:grid-cols-2 lg:grid-cols-3'
                          >
                            <DetailField label={t('Stripe Customer ID')}>
                              <code>{retirement.customer_key}</code>
                            </DetailField>
                            <DetailField label={t('Original Binding ID')}>
                              {retirement.original_binding_id}
                            </DetailField>
                            <DetailField label={t('Binding Version')}>
                              {retirement.binding_version}
                            </DetailField>
                            <DetailField label={t('Retired At')}>
                              {formatUnixTime(retirement.retired_at)}
                            </DetailField>
                            <DetailField label={t('Retired By')}>
                              {retirement.retired_by}
                            </DetailField>
                            <DetailField label={t('Retirement Reason')}>
                              {retirement.reason}
                            </DetailField>
                          </dl>
                        ))}
                      </div>
                    )}
                  </DetailSection>
                )}

                <DetailSection
                  title={t('Webhook Events')}
                  count={events.length}
                >
                  {events.length === 0 ? (
                    <div className='text-muted-foreground rounded-lg border border-dashed p-5 text-center text-sm'>
                      {t('No webhook events')}
                    </div>
                  ) : (
                    <div className='rounded-lg border'>
                      <Table>
                        <TableHeader>
                          <TableRow>
                            <TableHead>{t('Event')}</TableHead>
                            <TableHead>{t('Status')}</TableHead>
                            <TableHead>{t('Amount')}</TableHead>
                            <TableHead>{t('Updated At')}</TableHead>
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {events.map((event) => (
                            <TableRow key={event.id}>
                              <TableCell className='max-w-64 whitespace-normal'>
                                <div className='grid gap-0.5'>
                                  <span>{event.event_type}</span>
                                  <code className='text-muted-foreground truncate text-xs'>
                                    {event.provider} · {event.event_key}
                                  </code>
                                  {event.last_error && (
                                    <span className='text-destructive text-xs'>
                                      {event.last_error}
                                    </span>
                                  )}
                                </div>
                              </TableCell>
                              <TableCell>
                                <EventStatusBadge status={event.status} t={t} />
                              </TableCell>
                              <TableCell>
                                {formatMinorAmount(
                                  event.paid_amount_minor ||
                                    event.refunded_amount_minor ||
                                    event.disputed_amount_minor,
                                  event.currency || order.currency,
                                  event.provider || order.provider
                                )}
                              </TableCell>
                              <TableCell>
                                {formatUnixTime(event.updated_at)}
                              </TableCell>
                            </TableRow>
                          ))}
                        </TableBody>
                      </Table>
                    </div>
                  )}
                </DetailSection>

                <DetailSection
                  title={t('Payment Ledger')}
                  count={ledger.length}
                >
                  {ledger.length === 0 ? (
                    <div className='text-muted-foreground rounded-lg border border-dashed p-5 text-center text-sm'>
                      {t('No ledger entries')}
                    </div>
                  ) : (
                    <div className='rounded-lg border'>
                      <Table>
                        <TableHeader>
                          <TableRow>
                            <TableHead>{t('Entry Type')}</TableHead>
                            <TableHead>{t('Amount')}</TableHead>
                            <TableHead>{t('Quota Change')}</TableHead>
                            <TableHead>{t('Created At')}</TableHead>
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {ledger.map((entry) => (
                            <TableRow key={entry.id}>
                              <TableCell className='max-w-64 whitespace-normal'>
                                <div className='grid gap-0.5'>
                                  <span>{entry.entry_type}</span>
                                  {entry.description && (
                                    <span className='text-muted-foreground text-xs'>
                                      {entry.description}
                                    </span>
                                  )}
                                </div>
                              </TableCell>
                              <TableCell>
                                {formatMinorAmount(
                                  entry.amount_minor,
                                  entry.currency,
                                  order.provider
                                )}
                              </TableCell>
                              <TableCell className='tabular-nums'>
                                {formatInteger(entry.quota_delta)}
                              </TableCell>
                              <TableCell>
                                {formatUnixTime(entry.created_at)}
                              </TableCell>
                            </TableRow>
                          ))}
                        </TableBody>
                      </Table>
                    </div>
                  )}
                </DetailSection>

                <DetailSection
                  title={t('Privileged Operation History')}
                  count={operationsAudits.length}
                >
                  {operationsAudits.length === 0 ? (
                    <div className='text-muted-foreground rounded-lg border border-dashed p-5 text-center text-sm'>
                      {t('No privileged payment operations')}
                    </div>
                  ) : (
                    <div className='rounded-lg border'>
                      <Table>
                        <TableHeader>
                          <TableRow>
                            <TableHead>{t('Action')}</TableHead>
                            <TableHead>{t('Administrator')}</TableHead>
                            <TableHead>{t('Reason')}</TableHead>
                            <TableHead>{t('Created At')}</TableHead>
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {operationsAudits.map((audit) => (
                            <TableRow key={audit.id}>
                              <TableCell className='max-w-52 whitespace-normal'>
                                <code className='text-xs'>{audit.action}</code>
                              </TableCell>
                              <TableCell className='tabular-nums'>
                                {audit.admin_id}
                              </TableCell>
                              <TableCell className='max-w-72 whitespace-normal'>
                                {audit.reason}
                              </TableCell>
                              <TableCell>
                                {formatUnixTime(audit.created_at)}
                              </TableCell>
                            </TableRow>
                          ))}
                        </TableBody>
                      </Table>
                    </div>
                  )}
                </DetailSection>
              </div>
            </ScrollArea>
          )}
        </SheetContent>
      </Sheet>

      <PaymentOrderActionDialog
        order={order || null}
        action={orderAction}
        open={orderAction !== null}
        onOpenChange={(open) => {
          if (!open) setOrderAction(null)
        }}
        onCompleted={handleDataChanged}
      />
      <CredentialIncidentActionDialog
        order={order || null}
        action={credentialIncidentAction}
        open={credentialIncidentAction !== null}
        onOpenChange={(open) => {
          if (!open) setCredentialIncidentAction(null)
        }}
        onCompleted={handleDataChanged}
      />
      <StripeCustomerBindingRetirementDialog
        binding={selectedCustomerBinding}
        open={Boolean(selectedCustomerBinding)}
        onOpenChange={(open) => {
          if (!open) setSelectedCustomerBinding(null)
        }}
        onCompleted={handleDataChanged}
      />
      <ResolveDebtDialog
        debt={selectedDebt}
        provider={order?.provider}
        open={Boolean(selectedDebt)}
        onOpenChange={(open) => {
          if (!open) setSelectedDebt(null)
        }}
        onCompleted={handleDataChanged}
      />
    </>
  )
}
