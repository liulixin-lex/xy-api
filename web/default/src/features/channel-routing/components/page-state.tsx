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

import { AxiosError } from 'axios'
import {
  ClockAlert,
  Inbox,
  LockKeyhole,
  RefreshCw,
  ShieldAlert,
  TriangleAlert,
  WifiOff,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'

export function ChannelRoutingLoadingState(props: {
  rows?: number
  label?: string
}) {
  const { t } = useTranslation()
  const rows = props.rows ?? 6
  return (
    <div className='space-y-2' aria-busy='true' aria-live='polite'>
      <span className='sr-only'>
        {props.label ?? t('Loading channel routing data')}
      </span>
      <Skeleton className='h-9 w-full motion-reduce:animate-none' />
      {Array.from({ length: rows }, (_, index) => (
        <Skeleton
          key={`channel-routing-row-${index}`}
          className='h-14 w-full motion-reduce:animate-none'
        />
      ))}
    </div>
  )
}

export function ChannelRoutingEmptyState(props: {
  title?: string
  description?: string
  action?: React.ReactNode
}) {
  const { t } = useTranslation()
  return (
    <Empty className='min-h-72 border'>
      <EmptyHeader>
        <EmptyMedia variant='icon'>
          <Inbox aria-hidden='true' />
        </EmptyMedia>
        <EmptyTitle>{props.title ?? t('No routing data')}</EmptyTitle>
        <EmptyDescription>
          {props.description ?? t('No records match the current filters.')}
        </EmptyDescription>
      </EmptyHeader>
      {props.action ? <EmptyContent>{props.action}</EmptyContent> : null}
    </Empty>
  )
}

export function ChannelRoutingErrorState(props: {
  error: unknown
  onRetry: () => void
  scope?: 'channel-routing' | 'billing-review'
}) {
  const { t } = useTranslation()
  const status =
    props.error instanceof AxiosError ? props.error.response?.status : undefined
  const errorCode =
    props.error instanceof AxiosError ? props.error.code?.toUpperCase() : ''
  const errorMessage =
    props.error instanceof Error ? props.error.message.toLowerCase() : ''
  const browserOffline =
    typeof navigator !== 'undefined' && navigator.onLine === false
  const offline = browserOffline
  const timedOut =
    errorCode === 'ECONNABORTED' ||
    errorCode === 'ETIMEDOUT' ||
    errorMessage.includes('timeout')
  const unauthorized = status === 401
  const permissionDenied = status === 403
  const rateLimited = status === 429
  const snapshotInitializing = status === 503
  const billingReviewScope = props.scope === 'billing-review'
  let Icon = TriangleAlert
  let title = billingReviewScope
    ? t('Billing review queue unavailable')
    : t('Could not load channel routing data')
  let description = billingReviewScope
    ? t(
        'The billing review request failed. No decision was changed. Try again.'
      )
    : t('The request failed. Check the service and try again.')
  if (offline) {
    Icon = WifiOff
    title = t('You are offline')
    description = billingReviewScope
      ? t('Reconnect to the network, then retry the billing review queue.')
      : t(
          'Reconnect to the network, then retry to refresh channel routing data.'
        )
  } else if (timedOut) {
    Icon = ClockAlert
    title = billingReviewScope
      ? t('Billing review request timed out')
      : t('Channel routing request timed out')
    description = billingReviewScope
      ? t(
          'The billing review request failed. No decision was changed. Try again.'
        )
      : t(
          'The server did not respond in time. Existing routing state was not changed.'
        )
  } else if (unauthorized) {
    Icon = LockKeyhole
    title = t('Your session has expired')
    description = billingReviewScope
      ? t('Sign in again to access billing reviews.')
      : t('Sign in again to access channel routing data.')
  } else if (permissionDenied) {
    Icon = ShieldAlert
    title = billingReviewScope
      ? t('Billing review read permission required')
      : t('Channel routing permission required')
    description = billingReviewScope
      ? t('Your role cannot view the billing review queue.')
      : t('Your role cannot access this channel routing view.')
  } else if (rateLimited) {
    Icon = ClockAlert
    title = billingReviewScope
      ? t('Too many billing review requests')
      : t('Too many channel routing requests')
    description = t('Wait briefly, then retry the request.')
  } else if (snapshotInitializing && !billingReviewScope) {
    title = t('Routing snapshot is initializing')
    description = t(
      'The control plane is available, but this node has not loaded a routing snapshot yet.'
    )
  }

  return (
    <Empty className='min-h-72 border' role='alert' aria-live='assertive'>
      <EmptyHeader>
        <EmptyMedia variant='icon'>
          <Icon aria-hidden='true' />
        </EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        <EmptyDescription>{description}</EmptyDescription>
      </EmptyHeader>
      {!permissionDenied && !unauthorized ? (
        <EmptyContent>
          <Button variant='outline' onClick={props.onRetry}>
            <RefreshCw aria-hidden='true' />
            {t('Retry')}
          </Button>
        </EmptyContent>
      ) : null}
    </Empty>
  )
}
