/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/
import {
  Activity01Icon,
  Clock01Icon,
  DatabaseIcon,
  SecurityCheckIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'

import {
  getPaymentAdminErrorCode,
  getPaymentAdminErrorMessage,
} from '../../payment-admin-errors'
import { getPaymentOperationsOverview } from './api'

const CLUSTER_MESSAGE_KEYS: Record<string, string> = {
  shared_database_required:
    'Multi-node payments require a shared MySQL or PostgreSQL database.',
  shared_redis_required: 'Multi-node payments require shared Redis.',
  key_mismatch:
    'Payment encryption or session keys do not match across application nodes.',
  configuration_mismatch:
    'Payment configuration does not match across application nodes.',
  inventory_unavailable:
    'Payment cluster inventory is unavailable. New payment creation remains paused.',
}

function formatAge(seconds: number, locale: string, t: TFunction): string {
  if (seconds <= 0) return t('None')
  const formatter = new Intl.NumberFormat(locale, { maximumFractionDigits: 0 })
  if (seconds < 60) {
    return t('{{value}} sec', { value: formatter.format(seconds) })
  }
  if (seconds < 3600) {
    return t('{{value}} min', { value: formatter.format(seconds / 60) })
  }
  if (seconds < 86400) {
    return t('{{value}} hr', { value: formatter.format(seconds / 3600) })
  }
  return t('{{value}} day', { value: formatter.format(seconds / 86400) })
}

export function PaymentOverviewPanel() {
  const { t, i18n } = useTranslation()
  const overviewQuery = useQuery({
    queryKey: ['payment-operations-overview'],
    queryFn: getPaymentOperationsOverview,
    refetchInterval: 30_000,
    meta: { skipGlobalError: true },
  })

  if (overviewQuery.isLoading) {
    return (
      <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-4'>
        {Array.from({ length: 4 }, (_, index) => (
          <Skeleton key={index} className='h-32 rounded-xl' />
        ))}
      </div>
    )
  }

  if (overviewQuery.isError || !overviewQuery.data) {
    const errorCode = getPaymentAdminErrorCode(overviewQuery.error)
    const migrationRequired =
      errorCode === 'payment_operations_schema_not_ready'
    let errorTitle = t('Payment overview is unavailable')
    if (migrationRequired) {
      errorTitle = t('Payment operations need a database migration')
    } else if (errorCode === 'payment_overview_schema_invalid') {
      errorTitle = t('Payment overview needs a server update')
    }
    return (
      <Alert variant='destructive'>
        <AlertTitle>{errorTitle}</AlertTitle>
        <AlertDescription className='flex flex-wrap items-center justify-between gap-3'>
          <span className='grid gap-1'>
            <span>
              {t(
                'The payment data was not changed. Try loading the operational snapshot again.'
              )}
            </span>
            <span className='font-mono text-xs'>
              {getPaymentAdminErrorMessage(
                overviewQuery.error,
                t,
                t('Failed to load payment overview')
              )}
            </span>
          </span>
          <Button
            type='button'
            size='sm'
            variant='outline'
            onClick={() => void overviewQuery.refetch()}
          >
            {t('Retry')}
          </Button>
        </AlertDescription>
      </Alert>
    )
  }

  const data = overviewQuery.data
  const operations = data.operations
  const activeOrders =
    operations.preparing_orders +
    operations.awaiting_payment_orders +
    operations.confirming_orders
  const formatter = new Intl.NumberFormat(i18n.language)
  const cards = [
    {
      title: t('Active payment orders'),
      value: formatter.format(activeOrders),
      description: t(
        '{{preparing}} preparing, {{waiting}} waiting, {{confirming}} confirming',
        {
          preparing: formatter.format(operations.preparing_orders),
          waiting: formatter.format(operations.awaiting_payment_orders),
          confirming: formatter.format(operations.confirming_orders),
        }
      ),
      icon: Activity01Icon,
    },
    {
      title: t('Creation task backlog'),
      value: formatter.format(operations.create_task_backlog),
      description: t('Oldest task age: {{age}}', {
        age: formatAge(
          operations.oldest_create_task_age_seconds,
          i18n.language,
          t
        ),
      }),
      icon: Clock01Icon,
    },
    {
      title: t('Orders needing review'),
      value: formatter.format(operations.manual_review_orders),
      description: t('{{events}} unmatched payment events', {
        events: formatter.format(operations.unmatched_payment_events),
      }),
      icon: SecurityCheckIcon,
    },
    {
      title: t('Worker lease health'),
      value: formatter.format(operations.expired_task_leases),
      description: t('{{retrying}} tasks waiting to retry', {
        retrying: formatter.format(operations.retry_waiting_tasks),
      }),
      icon: DatabaseIcon,
    },
  ]

  return (
    <div className='grid gap-4'>
      {!data.cluster.ready && (
        <Alert variant='destructive'>
          <AlertTitle>{t('New payment creation is paused')}</AlertTitle>
          <AlertDescription className='grid gap-1'>
            <span>
              {t(
                CLUSTER_MESSAGE_KEYS[data.cluster.code] ??
                  'Payment cluster readiness could not be confirmed. New payments and automatic callback processing are paused; providers should retry after recovery.'
              )}
            </span>
            <span className='font-mono text-xs'>
              {t('Code: {{code}}', { code: data.cluster.code })}
            </span>
          </AlertDescription>
        </Alert>
      )}

      <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-4'>
        {cards.map((card) => (
          <Card key={card.title} size='sm'>
            <CardHeader className='grid-cols-[1fr_auto] border-b'>
              <div>
                <CardTitle>{card.title}</CardTitle>
                <CardDescription>{card.description}</CardDescription>
              </div>
              <HugeiconsIcon
                icon={card.icon}
                strokeWidth={2}
                className='text-muted-foreground'
                aria-hidden='true'
              />
            </CardHeader>
            <CardContent>
              <p className='text-2xl font-semibold tracking-tight tabular-nums'>
                {card.value}
              </p>
            </CardContent>
          </Card>
        ))}
      </div>

      <dl className='grid gap-x-6 gap-y-4 rounded-lg border px-4 py-4 text-sm sm:grid-cols-2 xl:grid-cols-4'>
        <div>
          <dt className='text-muted-foreground'>
            {t('Reconciliation backlog')}
          </dt>
          <dd className='font-medium tabular-nums'>
            {formatter.format(operations.reconcile_task_backlog)}
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>
            {t('Running payment tasks')}
          </dt>
          <dd className='font-medium tabular-nums'>
            {formatter.format(operations.running_tasks)}
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>
            {t('Unprocessed callbacks')}
          </dt>
          <dd className='font-medium tabular-nums'>
            {t('{{count}} pending, oldest {{age}}', {
              count: formatter.format(operations.unprocessed_payment_events),
              age: formatAge(
                operations.oldest_unprocessed_event_age_seconds,
                i18n.language,
                t
              ),
            })}
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>
            {t('Active limit reservations')}
          </dt>
          <dd className='font-medium tabular-nums'>
            {formatter.format(operations.active_limit_reservations)}
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>{t('Database')}</dt>
          <dd className='font-medium'>{data.runtime.database_type}</dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>{t('Shared Redis')}</dt>
          <dd className='font-medium'>
            {data.runtime.redis_enabled ? t('Enabled') : t('Disabled')}
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>
            {t('Payment configuration version')}
          </dt>
          <dd className='font-medium tabular-nums'>
            {formatter.format(operations.payment_configuration_version)}
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>
            {t('Expired active limit reservations')}
          </dt>
          <dd className='font-medium tabular-nums'>
            {formatter.format(operations.expired_active_limit_reservations)}
          </dd>
        </div>
      </dl>
    </div>
  )
}
