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
  RefreshIcon,
  ShieldKeyIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import {
  keepPreviousData,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { channelRoutingQueryKeys } from '@/features/channel-routing/api/query-keys'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingLoadingState,
} from '@/features/channel-routing/components/page-state'
import { ChannelRoutingCursorPagination } from '@/features/channel-routing/components/pagination-bar'
import { useChannelRoutingFormatters } from '@/features/channel-routing/lib/format'

import {
  getBillingProjectionOperationApiError,
  listFailedBillingProjections,
  listOpenBillingLogSinkConflicts,
} from '../api/projection-operations'
import {
  canMutateBillingProjectionPage,
  getBillingProjectionNextCursor,
} from '../lib/projection-operations'
import type {
  BillingLogSinkConflict,
  BillingProjectionDataset,
  FailedBillingProjection,
  FailedBillingProjectionDataset,
} from '../projection-types'
import {
  BillingConflictTable,
  FailedProjectionTable,
} from './projection-failures-table'
import {
  ProjectionOperationDialogs,
  type BillingProjectionRequeueTarget,
} from './projection-operation-dialogs'

const projectionPageLimit = 25

function ProjectionQueryError(props: { error: unknown; onRetry: () => void }) {
  const { t } = useTranslation()
  const apiError = getBillingProjectionOperationApiError(props.error)
  const permissionDenied = apiError.status === 403
  return (
    <Empty className='min-h-72 border' role='alert'>
      <EmptyHeader>
        <EmptyMedia variant='icon'>
          <HugeiconsIcon
            icon={permissionDenied ? ShieldKeyIcon : Alert02Icon}
            aria-hidden='true'
          />
        </EmptyMedia>
        <EmptyTitle>
          {permissionDenied
            ? t('Billing projection read permission required')
            : t('Billing projection data is unavailable')}
        </EmptyTitle>
        <EmptyDescription>
          {permissionDenied
            ? t('Your current role cannot view projection operations.')
            : t(
                'The request failed without changing projection state. Try again.'
              )}
        </EmptyDescription>
      </EmptyHeader>
      {!permissionDenied ? (
        <EmptyContent>
          <Button variant='outline' onClick={props.onRetry}>
            <HugeiconsIcon
              icon={RefreshIcon}
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

function ProjectionSectionHeader(props: {
  count?: number
  canOperate: boolean
  isFetching: boolean
  onRefresh: () => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  return (
    <div className='flex flex-wrap items-start justify-between gap-3'>
      <div className='min-w-0'>
        <div className='flex flex-wrap items-center gap-2'>
          <h2 className='text-base font-semibold'>
            {t('Projection failures')}
          </h2>
          {props.count != null ? (
            <Badge variant='outline' className='tabular-nums'>
              {format.number(props.count)}
            </Badge>
          ) : null}
          {!props.canOperate ? (
            <Badge variant='outline'>
              <HugeiconsIcon icon={ShieldKeyIcon} aria-hidden='true' />
              {t('Read only')}
            </Badge>
          ) : null}
        </div>
        <p className='text-muted-foreground mt-1 max-w-3xl text-xs leading-5'>
          {t(
            'Inspect failed billing projections and quarantined sink conflicts without exposing payloads, receipts, credentials, or raw operation keys.'
          )}
        </p>
      </div>
      <Button
        size='icon-sm'
        variant='outline'
        aria-label={t('Refresh projection operations')}
        disabled={props.isFetching}
        onClick={props.onRefresh}
      >
        <HugeiconsIcon
          icon={RefreshIcon}
          aria-hidden='true'
          className={
            props.isFetching
              ? 'animate-spin motion-reduce:animate-none'
              : undefined
          }
        />
      </Button>
    </div>
  )
}

function StaleProjectionAlert() {
  const { t } = useTranslation()
  return (
    <Alert role='status'>
      <HugeiconsIcon icon={Alert02Icon} aria-hidden='true' />
      <AlertTitle>{t('Projection refresh failed')}</AlertTitle>
      <AlertDescription>
        {t(
          'Showing the last confirmed page. Refresh before starting a new operation.'
        )}
      </AlertDescription>
    </Alert>
  )
}

function InvalidCursorAlert() {
  const { t } = useTranslation()
  return (
    <Alert role='status'>
      <HugeiconsIcon icon={Alert02Icon} aria-hidden='true' />
      <AlertTitle>{t('Projection pagination is unavailable')}</AlertTitle>
      <AlertDescription>
        {t(
          'The server returned an invalid or non-advancing cursor. Refresh before continuing.'
        )}
      </AlertDescription>
    </Alert>
  )
}

function FailedProjectionList(props: {
  dataset: FailedBillingProjectionDataset
  cursor: number
  canRequeue: boolean
  onCursorChange: (cursor: number) => void
  onRequeue: (projection: FailedBillingProjection) => void
  onPermissionRevoked: () => Promise<void>
}) {
  const { t } = useTranslation()
  const cursor = props.cursor
  const onCursorChange = props.onCursorChange
  const onPermissionRevoked = props.onPermissionRevoked
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.billingProjections(props.dataset, {
      cursor,
      limit: projectionPageLimit,
    }),
    queryFn: ({ signal }) =>
      listFailedBillingProjections(
        props.dataset,
        { cursor: cursor || undefined, limit: projectionPageLimit },
        signal
      ),
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
    meta: { handleErrorLocally: true },
  })
  const page = query.data
  const nextCursor = page ? getBillingProjectionNextCursor(page, cursor) : 0
  const invalidCursor = page?.has_more === true && nextCursor === 0
  const canOperate = canMutateBillingProjectionPage({
    hasPermission: props.canRequeue,
    isError: query.isError,
    isRefetchError: query.isRefetchError,
    isPlaceholderData: query.isPlaceholderData,
  })

  useEffect(() => {
    if (
      cursor > 0 &&
      page &&
      !query.isPlaceholderData &&
      page.items.length === 0
    ) {
      onCursorChange(0)
    }
  }, [cursor, onCursorChange, page, query.isPlaceholderData])

  useEffect(() => {
    if (getBillingProjectionOperationApiError(query.error).status === 403) {
      void onPermissionRevoked()
    }
  }, [onPermissionRevoked, query.error])

  return (
    <>
      <ProjectionSectionHeader
        count={page?.count}
        canOperate={canOperate}
        isFetching={query.isFetching}
        onRefresh={() => void query.refetch()}
      />
      {query.isRefetchError && page ? <StaleProjectionAlert /> : null}
      {query.isLoading ? (
        <ChannelRoutingLoadingState
          rows={5}
          label={t('Loading failed billing projections')}
        />
      ) : null}
      {query.isError && !page ? (
        <ProjectionQueryError
          error={query.error}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      {page && page.items.length === 0 ? (
        <ChannelRoutingEmptyState
          title={t('No failed projections')}
          description={t(
            'Failed projections will appear here after automated retries are exhausted.'
          )}
        />
      ) : null}
      {page && page.items.length > 0 ? (
        <>
          <FailedProjectionTable
            items={page.items}
            canRequeue={canOperate}
            onRequeue={props.onRequeue}
          />
          {invalidCursor ? <InvalidCursorAlert /> : null}
          <ChannelRoutingCursorPagination
            cursor={cursor}
            nextCursor={nextCursor}
            onCursorChange={props.onCursorChange}
          />
        </>
      ) : null}
    </>
  )
}

function BillingConflictList(props: {
  cursor: number
  canResolve: boolean
  onCursorChange: (cursor: number) => void
  onResolve: (conflict: BillingLogSinkConflict) => void
  onPermissionRevoked: () => Promise<void>
}) {
  const { t } = useTranslation()
  const cursor = props.cursor
  const onCursorChange = props.onCursorChange
  const onPermissionRevoked = props.onPermissionRevoked
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.billingProjections('conflicts', {
      cursor,
      limit: projectionPageLimit,
    }),
    queryFn: ({ signal }) =>
      listOpenBillingLogSinkConflicts(
        { cursor: cursor || undefined, limit: projectionPageLimit },
        signal
      ),
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
    meta: { handleErrorLocally: true },
  })
  const page = query.data
  const nextCursor = page ? getBillingProjectionNextCursor(page, cursor) : 0
  const invalidCursor = page?.has_more === true && nextCursor === 0
  const canOperate = canMutateBillingProjectionPage({
    hasPermission: props.canResolve,
    isError: query.isError,
    isRefetchError: query.isRefetchError,
    isPlaceholderData: query.isPlaceholderData,
  })

  useEffect(() => {
    if (
      cursor > 0 &&
      page &&
      !query.isPlaceholderData &&
      page.items.length === 0
    ) {
      onCursorChange(0)
    }
  }, [cursor, onCursorChange, page, query.isPlaceholderData])

  useEffect(() => {
    if (getBillingProjectionOperationApiError(query.error).status === 403) {
      void onPermissionRevoked()
    }
  }, [onPermissionRevoked, query.error])

  return (
    <>
      <ProjectionSectionHeader
        count={page?.count}
        canOperate={canOperate}
        isFetching={query.isFetching}
        onRefresh={() => void query.refetch()}
      />
      {query.isRefetchError && page ? <StaleProjectionAlert /> : null}
      {query.isLoading ? (
        <ChannelRoutingLoadingState
          rows={5}
          label={t('Loading billing log conflicts')}
        />
      ) : null}
      {query.isError && !page ? (
        <ProjectionQueryError
          error={query.error}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      {page && page.items.length === 0 ? (
        <ChannelRoutingEmptyState
          title={t('No open sink conflicts')}
          description={t(
            'Quarantined receipt conflicts will appear here until verified and resolved.'
          )}
        />
      ) : null}
      {page && page.items.length > 0 ? (
        <>
          <BillingConflictTable
            items={page.items}
            canResolve={canOperate}
            onResolve={props.onResolve}
          />
          {invalidCursor ? <InvalidCursorAlert /> : null}
          <ChannelRoutingCursorPagination
            cursor={cursor}
            nextCursor={nextCursor}
            onCursorChange={props.onCursorChange}
          />
        </>
      ) : null}
    </>
  )
}

export function ProjectionFailuresSection(props: {
  dataset: BillingProjectionDataset
  cursor: number
  canRequeue: boolean
  canResolve: boolean
  onCursorChange: (cursor: number) => void
  onPermissionRevoked: () => Promise<void>
}) {
  const queryClient = useQueryClient()
  const [requeueTarget, setRequeueTarget] =
    useState<BillingProjectionRequeueTarget | null>(null)
  const [conflictTarget, setConflictTarget] =
    useState<BillingLogSinkConflict | null>(null)
  const requeueFinalFocus = useRef<HTMLElement | null>(null)
  const conflictFinalFocus = useRef<HTMLElement | null>(null)
  const failedDataset = props.dataset === 'conflicts' ? null : props.dataset

  return (
    <section className='flex min-w-0 flex-col gap-3'>
      {failedDataset == null ? (
        <BillingConflictList
          cursor={props.cursor}
          canResolve={props.canResolve}
          onCursorChange={props.onCursorChange}
          onResolve={(conflict) => {
            if (document.activeElement instanceof HTMLElement) {
              conflictFinalFocus.current = document.activeElement
            }
            setConflictTarget(conflict)
          }}
          onPermissionRevoked={props.onPermissionRevoked}
        />
      ) : (
        <FailedProjectionList
          key={failedDataset}
          dataset={failedDataset}
          cursor={props.cursor}
          canRequeue={props.canRequeue}
          onCursorChange={props.onCursorChange}
          onRequeue={(projection) => {
            if (document.activeElement instanceof HTMLElement) {
              requeueFinalFocus.current = document.activeElement
            }
            setRequeueTarget({ dataset: failedDataset, projection })
          }}
          onPermissionRevoked={props.onPermissionRevoked}
        />
      )}

      <ProjectionOperationDialogs
        requeueTarget={requeueTarget}
        conflictTarget={conflictTarget}
        requeueFinalFocus={requeueFinalFocus}
        conflictFinalFocus={conflictFinalFocus}
        onRequeueTargetChange={setRequeueTarget}
        onConflictTargetChange={setConflictTarget}
        onRefresh={async () => {
          await queryClient.invalidateQueries({
            queryKey: channelRoutingQueryKeys.billingProjectionsRoot(),
          })
        }}
        onPermissionRevoked={props.onPermissionRevoked}
      />
    </section>
  )
}
