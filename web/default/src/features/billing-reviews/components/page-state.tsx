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
import {
  Alert02Icon,
  ArrowReloadHorizontalIcon,
  Clock01Icon,
  InboxIcon,
  ShieldKeyIcon,
  WifiDisconnected01Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { AxiosError } from 'axios'
import { useTranslation } from 'react-i18next'

import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
} from '@/components/ui/alert'
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

export function BillingOperationsLoadingState(props: {
  rows?: number
  label?: string
}) {
  const { t } = useTranslation()
  return (
    <div className='space-y-2' aria-busy='true' aria-live='polite'>
      <span className='sr-only'>
        {props.label ?? t('Loading billing operations')}
      </span>
      <Skeleton className='h-9 w-full motion-reduce:animate-none' />
      {Array.from({ length: props.rows ?? 6 }, (_, index) => (
        <Skeleton
          key={index}
          className='h-14 w-full motion-reduce:animate-none'
        />
      ))}
    </div>
  )
}

export function BillingOperationsEmptyState(props: {
  title: string
  description: string
}) {
  return (
    <Empty className='min-h-72 border'>
      <EmptyHeader>
        <EmptyMedia variant='icon'>
          <HugeiconsIcon icon={InboxIcon} aria-hidden='true' />
        </EmptyMedia>
        <EmptyTitle>{props.title}</EmptyTitle>
        <EmptyDescription>{props.description}</EmptyDescription>
      </EmptyHeader>
    </Empty>
  )
}

export function BillingOperationsRefetchErrorAlert(props: {
  isFetching: boolean
  onRetry: () => void
  title?: string
  description?: string
}) {
  const { t } = useTranslation()
  return (
    <Alert
      role='status'
      className='has-data-[slot=alert-action]:pr-2.5 sm:has-data-[slot=alert-action]:pr-18'
    >
      <HugeiconsIcon icon={Alert02Icon} aria-hidden='true' />
      <AlertTitle>{props.title ?? t('Refresh failed')}</AlertTitle>
      <AlertDescription>
        {props.description ??
          t(
            'Showing the last confirmed page. Refresh before starting a new operation.'
          )}
      </AlertDescription>
      <AlertAction className='static col-span-full mt-2 justify-self-start sm:absolute sm:col-auto sm:mt-0'>
        <Button
          size='sm'
          variant='outline'
          disabled={props.isFetching}
          onClick={props.onRetry}
        >
          <HugeiconsIcon
            icon={ArrowReloadHorizontalIcon}
            data-icon='inline-start'
            aria-hidden='true'
          />
          {t('Retry')}
        </Button>
      </AlertAction>
    </Alert>
  )
}

export function BillingOperationsErrorState(props: {
  error: unknown
  onRetry: () => void
}) {
  const { t } = useTranslation()
  const status =
    props.error instanceof AxiosError ? props.error.response?.status : undefined
  const offline = typeof navigator !== 'undefined' && navigator.onLine === false
  const timedOut =
    props.error instanceof AxiosError &&
    ['ECONNABORTED', 'ETIMEDOUT'].includes(
      props.error.code?.toUpperCase() ?? ''
    )
  const permissionDenied = status === 403
  let icon = Alert02Icon
  let title = t('Billing operations unavailable')
  let description = t(
    'The request failed without changing billing state. Try again.'
  )
  if (offline) {
    icon = WifiDisconnected01Icon
    title = t('You are offline')
    description = t('Reconnect to the network, then retry billing operations.')
  } else if (timedOut) {
    icon = Clock01Icon
    title = t('Billing operations request timed out')
  } else if (permissionDenied) {
    icon = ShieldKeyIcon
    title = t('Billing operations permission required')
    description = t(
      'Your current role cannot view this billing operations queue.'
    )
  }

  return (
    <Empty className='min-h-72 border' role='alert'>
      <EmptyHeader>
        <EmptyMedia variant='icon'>
          <HugeiconsIcon icon={icon} aria-hidden='true' />
        </EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        <EmptyDescription>{description}</EmptyDescription>
      </EmptyHeader>
      {!permissionDenied ? (
        <EmptyContent>
          <Button variant='outline' onClick={props.onRetry}>
            <HugeiconsIcon
              icon={ArrowReloadHorizontalIcon}
              data-icon='inline-start'
              aria-hidden='true'
            />
            {t('Retry')}
          </Button>
        </EmptyContent>
      ) : null}
    </Empty>
  )
}
