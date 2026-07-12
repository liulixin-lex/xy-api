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
import { getRouteApi, Link } from '@tanstack/react-router'
import { ArrowRight, RefreshCw, Search, X } from 'lucide-react'
import type { FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { listChannelRoutingGroups } from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingIdentityText } from '../components/identity-text'
import { ChannelRoutingPageFrame } from '../components/page-frame'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
} from '../components/page-state'
import { ChannelRoutingPagination } from '../components/pagination-bar'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'

const route = getRouteApi('/_authenticated/channel-routing/$section')

export function ChannelRoutingGroupsPage() {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const page = search.page ?? 1
  const pageSize = search.pageSize ?? 20
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.groups({
      page,
      page_size: pageSize,
      search: search.search || undefined,
    }),
    queryFn: () =>
      listChannelRoutingGroups({
        page,
        page_size: pageSize,
        search: search.search || undefined,
      }),
    placeholderData: (previous) => previous,
  })

  const updateSearch = (
    patch: Partial<{
      page: number
      pageSize: number
      search: string
    }>
  ) => {
    void navigate({
      search: (previous) => ({ ...previous, ...patch }),
      replace: true,
    })
  }

  const handleSearch = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    updateSearch({ page: 1, search: String(form.get('search') ?? '').trim() })
  }

  return (
    <ChannelRoutingPageFrame
      activeSection='groups'
      title={t('Routing groups')}
      actions={
        <Button
          size='icon-sm'
          variant='outline'
          aria-label={t('Refresh')}
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          <RefreshCw
            aria-hidden='true'
            className={
              query.isFetching
                ? 'animate-spin motion-reduce:animate-none'
                : undefined
            }
          />
        </Button>
      }
    >
      <div className='space-y-3 pb-2'>
        <form
          key={search.search}
          className='flex flex-wrap items-center gap-2'
          onSubmit={handleSearch}
        >
          <div className='relative min-w-56 flex-1 sm:max-w-md'>
            <Search
              className='text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2'
              aria-hidden='true'
            />
            <Input
              name='search'
              defaultValue={search.search}
              className='pl-8'
              aria-label={t('Search routing groups')}
              placeholder={t('Search groups')}
            />
          </div>
          <Button type='submit' size='sm' variant='outline'>
            <Search aria-hidden='true' />
            {t('Search')}
          </Button>
          {search.search ? (
            <Button
              type='button'
              size='sm'
              variant='ghost'
              onClick={() => updateSearch({ page: 1, search: '' })}
            >
              <X aria-hidden='true' />
              {t('Clear')}
            </Button>
          ) : null}
        </form>

        {query.isLoading ? <ChannelRoutingLoadingState /> : null}
        {query.isError ? (
          <ChannelRoutingErrorState
            error={query.error}
            onRetry={() => void query.refetch()}
          />
        ) : null}
        {query.data && query.data.items.length === 0 ? (
          <ChannelRoutingEmptyState
            title={t('No routing groups')}
            description={t('No routing groups match the current filters.')}
          />
        ) : null}

        {query.data && query.data.items.length > 0 ? (
          <>
            <div className='hidden overflow-hidden rounded-lg border md:block'>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('Group')}</TableHead>
                    <TableHead>{t('Stage')}</TableHead>
                    <TableHead>{t('Policy profile')}</TableHead>
                    <TableHead className='text-right'>{t('Members')}</TableHead>
                    <TableHead className='text-right'>
                      {t('Telemetry')}
                    </TableHead>
                    <TableHead className='text-right'>
                      {t('Model health')}
                    </TableHead>
                    <TableHead className='w-10'>
                      <span className='sr-only'>{t('Actions')}</span>
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {query.data.items.map((group) => (
                    <TableRow key={group.id}>
                      <TableCell>
                        <Link
                          to='/channel-routing/groups/$id'
                          params={{ id: String(group.id) }}
                          className='font-medium hover:underline'
                        >
                          {group.display_name || group.group_name}
                        </Link>
                        <div className='text-muted-foreground text-xs'>
                          {group.group_name} · {group.source}
                        </div>
                      </TableCell>
                      <TableCell>
                        <ChannelRoutingStatusBadge
                          status={group.deployment_stage}
                        />
                      </TableCell>
                      <TableCell>
                        <ChannelRoutingStatusBadge
                          status={group.policy_profile}
                          label={group.policy_profile}
                        />
                      </TableCell>
                      <TableCell className='text-right'>
                        {format.number(group.member_count)}
                      </TableCell>
                      <TableCell className='text-right'>
                        {format.percent(group.telemetry_coverage)}
                      </TableCell>
                      <TableCell className='text-right'>
                        <span className='text-destructive tabular-nums'>
                          {group.open_models}
                        </span>
                        <span className='text-muted-foreground px-1'>/</span>
                        <span className='text-amber-700 tabular-nums dark:text-amber-300'>
                          {group.degraded_models}
                        </span>
                      </TableCell>
                      <TableCell>
                        <Button
                          size='icon-sm'
                          variant='ghost'
                          aria-label={t('Open group details')}
                          render={
                            <Link
                              to='/channel-routing/groups/$id'
                              params={{ id: String(group.id) }}
                            />
                          }
                        >
                          <ArrowRight aria-hidden='true' />
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>

            <div className='divide-y rounded-lg border md:hidden'>
              {query.data.items.map((group) => (
                <Link
                  key={group.id}
                  to='/channel-routing/groups/$id'
                  params={{ id: String(group.id) }}
                  className='hover:bg-muted/50 block p-3 transition-colors'
                >
                  <div className='flex items-start justify-between gap-3'>
                    <div className='min-w-0'>
                      <ChannelRoutingIdentityText
                        text={group.display_name || group.group_name}
                        className='text-sm font-medium'
                      />
                      <div className='text-muted-foreground truncate text-xs'>
                        {group.group_name} · {group.policy_profile}
                      </div>
                    </div>
                    <ChannelRoutingStatusBadge
                      status={group.deployment_stage}
                    />
                  </div>
                  <dl className='mt-3 grid grid-cols-3 gap-3 text-xs'>
                    <div>
                      <dt className='text-muted-foreground'>{t('Members')}</dt>
                      <dd className='mt-1 font-medium'>{group.member_count}</dd>
                    </div>
                    <div>
                      <dt className='text-muted-foreground'>
                        {t('Telemetry')}
                      </dt>
                      <dd className='mt-1 font-medium'>
                        {format.percent(group.telemetry_coverage)}
                      </dd>
                    </div>
                    <div>
                      <dt className='text-muted-foreground'>
                        {t('Open / degraded')}
                      </dt>
                      <dd className='mt-1 font-medium'>
                        {group.open_models} / {group.degraded_models}
                      </dd>
                    </div>
                  </dl>
                </Link>
              ))}
            </div>

            <ChannelRoutingPagination
              page={page}
              pageSize={pageSize}
              total={query.data.total}
              onPageChange={(nextPage) => updateSearch({ page: nextPage })}
              onPageSizeChange={(nextSize) =>
                updateSearch({ page: 1, pageSize: nextSize })
              }
            />
          </>
        ) : null}
      </div>
    </ChannelRoutingPageFrame>
  )
}
