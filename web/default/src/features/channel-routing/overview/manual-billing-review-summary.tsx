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
import { Link } from '@tanstack/react-router'
import { ArrowRight, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
} from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import { listManualBillingReviews } from '../api/billing-reviews'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingRefetchErrorAlert } from '../components/page-state'
import { useChannelRoutingFormatters } from '../lib/format'
import { getManualBillingReviewKindDisplay } from '../lib/manual-billing-review'

function manualReviewAge(
  seconds: number,
  translate: (key: string, options?: Record<string, unknown>) => string
): string {
  if (!Number.isFinite(seconds) || seconds < 0) return translate('Unknown')
  if (seconds >= 86_400) {
    return translate('{{count}} days ago', {
      count: Math.floor(seconds / 86_400),
    })
  }
  if (seconds >= 3_600) {
    return translate('{{count}} hours ago', {
      count: Math.floor(seconds / 3_600),
    })
  }
  return translate('{{count}} minutes ago', {
    count: Math.max(1, Math.floor(seconds / 60)),
  })
}

export function ManualBillingReviewSummary(props: { enabled: boolean }) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const reviewsQuery = useQuery({
    queryKey: channelRoutingQueryKeys.billingReviews({ cursor: 0, limit: 3 }),
    queryFn: ({ signal }) =>
      listManualBillingReviews({ cursor: 0, limit: 3 }, signal),
    enabled: props.enabled,
    refetchInterval: 30_000,
    meta: { handleErrorLocally: true },
  })

  if (!props.enabled || reviewsQuery.isLoading) return null

  if (reviewsQuery.isError && !reviewsQuery.data) {
    return (
      <ChannelRoutingRefetchErrorAlert
        title={t('Billing review queue unavailable')}
        description={t(
          'The routing snapshot is available, but billing reviews could not be loaded.'
        )}
        isFetching={reviewsQuery.isFetching}
        onRetry={() => void reviewsQuery.refetch()}
      />
    )
  }

  const page = reviewsQuery.data
  if (!page || page.pending_count <= 0) return null

  return (
    <Alert
      role='status'
      className='border-warning/35 bg-warning/5 [&>svg]:text-warning has-data-[slot=alert-action]:pr-2.5 sm:has-data-[slot=alert-action]:pr-18'
    >
      <TriangleAlert aria-hidden='true' />
      <AlertTitle className='flex flex-wrap items-center gap-2'>
        <span>{t('Manual billing review required')}</span>
        <Badge variant='outline' className='bg-background/80 tabular-nums'>
          {format.number(page.pending_count)}
        </Badge>
      </AlertTitle>
      <AlertDescription className='space-y-3'>
        <p>
          {t('Oldest review entered the queue {{age}}.', {
            age: manualReviewAge(page.oldest_age_seconds, t),
          })}
        </p>
        {page.items.length > 0 ? (
          <ul className='divide-border/70 divide-y border-y text-xs'>
            {page.items.map((review) => (
              <li
                key={review.reservation_id}
                className='flex min-w-0 items-center justify-between gap-3 py-2'
              >
                <span className='min-w-0 truncate font-mono'>
                  #{review.reservation_id} · {review.public_task_id}
                </span>
                <span className='text-muted-foreground max-w-[48%] text-right break-all'>
                  {getManualBillingReviewKindDisplay(review.review_kind, t)}
                </span>
              </li>
            ))}
          </ul>
        ) : null}
      </AlertDescription>
      <AlertAction className='static col-span-full mt-2 justify-self-start sm:absolute sm:col-auto sm:mt-0'>
        <Button
          size='sm'
          variant='outline'
          render={<Link to='/billing-reviews' search={{ cursor: 0 }} />}
        >
          {t('Open review queue')}
          <ArrowRight aria-hidden='true' />
        </Button>
      </AlertAction>
    </Alert>
  )
}
