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
  Cancel01Icon,
  RefreshIcon,
  Search01Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import type { FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import {
  Table,
  TableBody,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  CHANNEL_STATUS_LABELS,
  CHANNEL_TYPE_OPTIONS,
} from '@/features/channels/constants'

import { listChannelRoutingChannels } from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../components/page-state'
import { ChannelRoutingPagination } from '../components/pagination-bar'
import {
  PhysicalChannelCard,
  PhysicalChannelTableRow,
} from './physical-channel-item'

const route = getRouteApi('/_authenticated/channel-routing/$section')

export function PhysicalChannelsTab() {
  const { t } = useTranslation()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const page = search.page ?? 1
  const pageSize = search.pageSize ?? 20
  const params = {
    page,
    page_size: pageSize,
    search: search.search || undefined,
    status: search.status,
    type: search.type,
  }
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.channels(params),
    queryFn: () => listChannelRoutingChannels(params),
    meta: { handleErrorLocally: true },
  })

  const updateSearch = (patch: Record<string, string | number | undefined>) => {
    void navigate({
      search: (previous) => ({ ...previous, ...patch }),
      replace: true,
    })
  }
  const handleSearch = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    updateSearch({
      page: 1,
      search: String(form.get('channelSearch') ?? '').trim(),
    })
  }
  const filtersActive =
    Boolean(search.search) || search.status != null || search.type != null

  return (
    <div className='flex flex-col gap-3 pb-2'>
      <div className='flex flex-col gap-2 lg:flex-row lg:items-center lg:justify-between'>
        <div>
          <h2 className='text-sm font-semibold'>{t('Physical channels')}</h2>
          <p className='text-muted-foreground mt-0.5 text-xs'>
            {t(
              'Serving health, credentials, cost plan, traffic scope, and failure-domain placement.'
            )}
          </p>
        </div>
        <Button
          size='sm'
          variant='outline'
          className='min-h-11 self-start lg:min-h-7'
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          <HugeiconsIcon
            icon={RefreshIcon}
            data-icon='inline-start'
            className={
              query.isFetching
                ? 'animate-spin motion-reduce:animate-none'
                : undefined
            }
            aria-hidden='true'
          />
          {t('Refresh')}
        </Button>
      </div>

      <div className='flex flex-col gap-2 lg:flex-row lg:flex-wrap lg:items-center'>
        <form
          key={search.search}
          className='flex min-w-0 items-center gap-2 lg:max-w-md lg:flex-1'
          onSubmit={handleSearch}
        >
          <div className='relative min-w-0 flex-1'>
            <HugeiconsIcon
              icon={Search01Icon}
              className='text-muted-foreground pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2'
              aria-hidden='true'
            />
            <Input
              name='channelSearch'
              defaultValue={search.search}
              className='min-h-11 pl-9 lg:min-h-8'
              aria-label={t('Search physical channels')}
              placeholder={t('Search channel name or endpoint')}
            />
          </div>
          <Button
            type='submit'
            size='icon'
            variant='outline'
            className='min-h-11 min-w-11 lg:min-h-8 lg:min-w-8'
            aria-label={t('Search')}
          >
            <HugeiconsIcon icon={Search01Icon} aria-hidden='true' />
          </Button>
        </form>
        <NativeSelect
          size='sm'
          className='w-full lg:w-auto [&_[data-slot=native-select]]:min-h-11 lg:[&_[data-slot=native-select]]:min-h-7'
          value={search.status == null ? 'all' : String(search.status)}
          aria-label={t('Channel status')}
          onChange={(event) =>
            updateSearch({
              page: 1,
              status:
                event.target.value === 'all'
                  ? undefined
                  : Number(event.target.value),
            })
          }
        >
          <NativeSelectOption value='all'>{t('All status')}</NativeSelectOption>
          {Object.entries(CHANNEL_STATUS_LABELS).map(([value, label]) => (
            <NativeSelectOption key={value} value={value}>
              {t(label)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        <NativeSelect
          size='sm'
          className='w-full lg:max-w-52 [&_[data-slot=native-select]]:min-h-11 lg:[&_[data-slot=native-select]]:min-h-7'
          value={search.type == null ? 'all' : String(search.type)}
          aria-label={t('Channel type')}
          onChange={(event) =>
            updateSearch({
              page: 1,
              type:
                event.target.value === 'all'
                  ? undefined
                  : Number(event.target.value),
            })
          }
        >
          <NativeSelectOption value='all'>{t('All types')}</NativeSelectOption>
          {CHANNEL_TYPE_OPTIONS.map((option) => (
            <NativeSelectOption key={option.value} value={option.value}>
              {t(option.label)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        {filtersActive ? (
          <Button
            size='sm'
            variant='ghost'
            className='min-h-11 justify-start lg:min-h-7'
            onClick={() =>
              updateSearch({
                page: 1,
                search: '',
                status: undefined,
                type: undefined,
              })
            }
          >
            <HugeiconsIcon
              icon={Cancel01Icon}
              data-icon='inline-start'
              aria-hidden='true'
            />
            {t('Clear')}
          </Button>
        ) : null}
      </div>

      {query.data ? (
        <div className='text-muted-foreground text-xs'>
          {t('{{count}} channels', { count: query.data.total })}
        </div>
      ) : null}
      {query.isLoading ? (
        <ChannelRoutingLoadingState
          label={t('Loading physical channels')}
          rows={8}
        />
      ) : null}
      {query.isError && !query.data ? (
        <ChannelRoutingErrorState
          error={query.error}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      {query.isRefetchError && query.data ? (
        <ChannelRoutingRefetchErrorAlert
          isFetching={query.isFetching}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      {query.data && query.data.items.length === 0 ? (
        <ChannelRoutingEmptyState
          title={t('No physical channels')}
          description={t(
            'No channels match the current physical channel filters.'
          )}
        />
      ) : null}

      {query.data && query.data.items.length > 0 ? (
        <>
          <div className='hidden overflow-hidden rounded-lg border xl:block'>
            <Table
              className='min-w-[76rem]'
              scrollAreaLabel={t('Physical channels table')}
            >
              <TableHeader>
                <TableRow>
                  <TableHead className='min-w-64'>{t('Channel')}</TableHead>
                  <TableHead className='min-w-52'>
                    {t('Serving health')}
                  </TableHead>
                  <TableHead className='min-w-60'>
                    {t('Coverage and balance')}
                  </TableHead>
                  <TableHead className='min-w-64'>{t('Cost plan')}</TableHead>
                  <TableHead className='min-w-52'>
                    {t('Traffic and failure domain')}
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {query.data.items.map((channel) => (
                  <PhysicalChannelTableRow key={channel.id} channel={channel} />
                ))}
              </TableBody>
            </Table>
          </div>

          <div className='divide-y rounded-lg border xl:hidden'>
            {query.data.items.map((channel) => (
              <PhysicalChannelCard key={channel.id} channel={channel} />
            ))}
          </div>

          <ChannelRoutingPagination
            page={page}
            pageSize={pageSize}
            total={query.data.total}
            disabled={query.isRefetchError}
            onPageChange={(nextPage) => updateSearch({ page: nextPage })}
            onPageSizeChange={(nextSize) =>
              updateSearch({ page: 1, pageSize: nextSize })
            }
          />
        </>
      ) : null}
    </div>
  )
}
