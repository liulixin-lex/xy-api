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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ChevronRight,
  Clock3,
  LockKeyhole,
  RefreshCw,
  TriangleAlert,
} from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { listManualBillingReviews } from '../api/billing-reviews'
import { channelRoutingQueryKeys } from '../api/query-keys'
import type { ManualBillingReviewItem } from '../billing-review-types'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
} from '../components/page-state'
import { ChannelRoutingCursorPagination } from '../components/pagination-bar'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import { getManualBillingReviewKindLabelKey } from '../lib/manual-billing-review'
import { ManualBillingReviewSheet } from './manual-billing-review-sheet'

const reviewPageLimit = 10

function reviewAge(
  sinceMs: number,
  translate: (key: string, options?: Record<string, unknown>) => string
): string {
  const seconds = Math.max(0, Math.floor((Date.now() - sinceMs) / 1000))
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

export function ManualBillingReviewsSection(props: {
  cursor: number
  canResolve: boolean
  onCursorChange: (cursor: number) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const queryClient = useQueryClient()
  const cursor = props.cursor
  const onCursorChange = props.onCursorChange
  const [selectedReview, setSelectedReview] =
    useState<ManualBillingReviewItem | null>(null)
  const [sheetOpen, setSheetOpen] = useState(false)
  const reviewsQuery = useQuery({
    queryKey: channelRoutingQueryKeys.billingReviews({
      cursor,
      limit: reviewPageLimit,
    }),
    queryFn: ({ signal }) =>
      listManualBillingReviews(
        {
          cursor: cursor || undefined,
          limit: reviewPageLimit,
        },
        signal
      ),
    placeholderData: (previous) => previous,
    refetchInterval: sheetOpen ? false : 30_000,
    meta: { handleErrorLocally: true },
  })

  useEffect(() => {
    if (
      cursor > 0 &&
      reviewsQuery.data &&
      !reviewsQuery.isPlaceholderData &&
      reviewsQuery.data.items.length === 0
    ) {
      onCursorChange(0)
    }
  }, [
    cursor,
    onCursorChange,
    reviewsQuery.data,
    reviewsQuery.isPlaceholderData,
  ])

  const openReview = (review: ManualBillingReviewItem) => {
    setSelectedReview(review)
    setSheetOpen(true)
  }

  const refreshReview = async (
    reservationId: number
  ): Promise<ManualBillingReviewItem | null> => {
    const result = await reviewsQuery.refetch()
    if (result.isError) throw result.error
    return (
      result.data?.items.find(
        (review) => review.reservation_id === reservationId
      ) ?? null
    )
  }

  const refreshQueue = async () => {
    await queryClient.invalidateQueries({
      queryKey: channelRoutingQueryKeys.billingReviewsRoot(),
    })
  }

  const page = reviewsQuery.data
  const serverCanResolve = page?.capabilities.can_resolve === true
  const canResolve = props.canResolve && serverCanResolve

  return (
    <>
      <section
        id='manual-billing-reviews'
        className='scroll-mt-20 space-y-3 border-t pt-5'
        aria-labelledby='manual-billing-reviews-heading'
      >
        <div className='flex flex-wrap items-start justify-between gap-3'>
          <div className='min-w-0'>
            <div className='flex flex-wrap items-center gap-2'>
              <h2
                id='manual-billing-reviews-heading'
                className='text-base font-semibold'
              >
                {t('Manual billing reviews')}
              </h2>
              {page ? (
                <Badge variant='outline' className='tabular-nums'>
                  {format.number(page.pending_count)}
                </Badge>
              ) : null}
              {page && !serverCanResolve ? (
                <Badge variant='outline'>
                  <LockKeyhole aria-hidden='true' />
                  {t('Read only')}
                </Badge>
              ) : null}
            </div>
            <p className='text-muted-foreground mt-1 text-xs leading-5'>
              {t(
                'Resolve ambiguous asynchronous billing outcomes using provider evidence and server-calculated financial consequences.'
              )}
            </p>
          </div>
          <Button
            size='icon-sm'
            variant='outline'
            aria-label={t('Refresh billing reviews')}
            disabled={reviewsQuery.isFetching}
            onClick={() => void reviewsQuery.refetch()}
          >
            <RefreshCw
              aria-hidden='true'
              className={
                reviewsQuery.isFetching
                  ? 'animate-spin motion-reduce:animate-none'
                  : undefined
              }
            />
          </Button>
        </div>

        {page && page.pending_count > 0 ? (
          <div className='text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 text-xs'>
            <span className='inline-flex items-center gap-1.5'>
              <Clock3 className='size-3.5' aria-hidden='true' />
              {t('Oldest review: {{age}}', {
                age: reviewAge(Date.now() - page.oldest_age_seconds * 1000, t),
              })}
            </span>
            <span>
              {t('Decisions are applied only after server confirmation.')}
            </span>
          </div>
        ) : null}

        {reviewsQuery.isRefetchError && page ? (
          <Alert role='status' className='border-amber-500/30 bg-amber-500/5'>
            <TriangleAlert
              className='text-amber-700 dark:text-amber-300'
              aria-hidden='true'
            />
            <AlertTitle>{t('Billing review refresh failed')}</AlertTitle>
            <AlertDescription>
              {t(
                'Showing the last confirmed queue page. Retry before making a decision.'
              )}
            </AlertDescription>
          </Alert>
        ) : null}
        {reviewsQuery.isLoading ? (
          <ChannelRoutingLoadingState rows={4} />
        ) : null}
        {reviewsQuery.isError && !page ? (
          <ChannelRoutingErrorState
            error={reviewsQuery.error}
            onRetry={() => void reviewsQuery.refetch()}
          />
        ) : null}
        {page && page.items.length === 0 ? (
          <ChannelRoutingEmptyState
            title={t('No billing reviews pending')}
            description={t(
              'Ambiguous asynchronous billing cases will appear here when manual evidence is required.'
            )}
          />
        ) : null}

        {page && page.items.length > 0 ? (
          <>
            <div className='hidden overflow-hidden rounded-lg border md:block'>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('Reservation')}</TableHead>
                    <TableHead>{t('Review type')}</TableHead>
                    <TableHead>{t('Waiting since')}</TableHead>
                    <TableHead>{t('Financial outcome')}</TableHead>
                    <TableHead>{t('Status')}</TableHead>
                    <TableHead className='w-12'>
                      <span className='sr-only'>{t('Open')}</span>
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {page.items.map((review) => (
                    <TableRow key={review.reservation_id}>
                      <TableCell>
                        <button
                          type='button'
                          className='focus-visible:ring-ring block max-w-64 text-left hover:underline focus-visible:rounded-sm focus-visible:ring-2 focus-visible:outline-none'
                          onClick={() => openReview(review)}
                        >
                          <span className='block font-mono text-xs font-medium break-all'>
                            #{review.reservation_id}
                          </span>
                          <span className='text-muted-foreground block truncate text-xs'>
                            {review.public_task_id}
                          </span>
                        </button>
                      </TableCell>
                      <TableCell>
                        <div className='text-sm font-medium'>
                          {t(
                            getManualBillingReviewKindLabelKey(
                              review.review_kind
                            )
                          )}
                        </div>
                        <div className='text-muted-foreground text-xs'>
                          {review.kind}
                        </div>
                      </TableCell>
                      <TableCell className='text-xs'>
                        <div>{reviewAge(review.manual_review_since_ms, t)}</div>
                        <div className='text-muted-foreground'>
                          {format.timestamp(review.manual_review_since_ms)}
                        </div>
                      </TableCell>
                      <TableCell className='text-xs tabular-nums'>
                        <div>
                          {t('Accept')}:{' '}
                          {format.number(
                            review.financial_consequences.accept_final_charge
                          )}
                        </div>
                        <div className='text-muted-foreground'>
                          {t('Reject')}:{' '}
                          {format.number(
                            review.financial_consequences.reject_final_charge
                          )}
                        </div>
                      </TableCell>
                      <TableCell>
                        {review.blockers.length > 0 ? (
                          <Badge variant='outline'>
                            {t('{{count}} blockers', {
                              count: review.blockers.length,
                            })}
                          </Badge>
                        ) : (
                          <ChannelRoutingStatusBadge status='ready' />
                        )}
                      </TableCell>
                      <TableCell>
                        <Button
                          size='icon-sm'
                          variant='ghost'
                          aria-label={t('Open billing review #{{id}}', {
                            id: review.reservation_id,
                          })}
                          onClick={() => openReview(review)}
                        >
                          <ChevronRight aria-hidden='true' />
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>

            <div className='divide-y overflow-hidden rounded-lg border md:hidden'>
              {page.items.map((review) => (
                <button
                  key={review.reservation_id}
                  type='button'
                  className='hover:bg-muted/40 focus-visible:bg-muted/40 flex w-full min-w-0 items-start justify-between gap-3 p-3 text-left focus-visible:outline-none'
                  onClick={() => openReview(review)}
                >
                  <span className='min-w-0 flex-1'>
                    <span className='flex flex-wrap items-center gap-2'>
                      <span className='font-mono text-xs font-semibold break-all'>
                        #{review.reservation_id}
                      </span>
                      <Badge variant='outline'>
                        {t(
                          getManualBillingReviewKindLabelKey(review.review_kind)
                        )}
                      </Badge>
                    </span>
                    <span className='text-muted-foreground mt-1 block truncate text-xs'>
                      {review.public_task_id}
                    </span>
                    <span className='text-muted-foreground mt-2 flex flex-wrap gap-x-3 gap-y-1 text-xs'>
                      <span>{reviewAge(review.manual_review_since_ms, t)}</span>
                      <span>
                        {t('Accept')}:{' '}
                        {format.number(
                          review.financial_consequences.accept_final_charge
                        )}
                      </span>
                      <span>
                        {t('Reject')}:{' '}
                        {format.number(
                          review.financial_consequences.reject_final_charge
                        )}
                      </span>
                    </span>
                  </span>
                  <ChevronRight
                    className='text-muted-foreground mt-1 size-4 shrink-0'
                    aria-hidden='true'
                  />
                </button>
              ))}
            </div>

            <ChannelRoutingCursorPagination
              cursor={cursor}
              nextCursor={page.has_more ? page.next_cursor : 0}
              onCursorChange={props.onCursorChange}
            />
          </>
        ) : null}
      </section>

      <ManualBillingReviewSheet
        review={selectedReview}
        open={sheetOpen}
        canResolve={canResolve}
        onOpenChange={(open) => {
          setSheetOpen(open)
          if (!open) setSelectedReview(null)
        }}
        onRefreshReview={refreshReview}
        onResolved={refreshQueue}
      />
    </>
  )
}
