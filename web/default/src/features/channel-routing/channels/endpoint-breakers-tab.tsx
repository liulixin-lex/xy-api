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
  Location01Icon,
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
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import { listChannelRoutingEndpoints } from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingEndpointNetworkSection } from '../overview/endpoint-network-section'

const route = getRouteApi('/_authenticated/channel-routing/$section')

export function EndpointBreakersTab() {
  const { t } = useTranslation()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const user = useAuthStore((state) => state.auth.user)
  const canOperate = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )
  const page = search.endpointPage ?? 1
  const pageSize = search.endpointPageSize ?? 20
  const params = {
    page,
    page_size: pageSize,
    search: search.endpointSearch || undefined,
    region: search.endpointRegion || undefined,
  }
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.endpoints(params),
    queryFn: () => listChannelRoutingEndpoints(params),
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
      endpointPage: 1,
      endpointSearch: String(form.get('endpointSearch') ?? '').trim(),
      endpointRegion: String(form.get('endpointRegion') ?? '').trim(),
    })
  }
  const filtersActive =
    Boolean(search.endpointSearch) || Boolean(search.endpointRegion)

  return (
    <div className='flex flex-col gap-3 pb-2'>
      <div className='flex flex-col gap-2 lg:flex-row lg:items-center lg:justify-between'>
        <form
          key={[search.endpointSearch, search.endpointRegion].join(':')}
          className='grid min-w-0 gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(10rem,0.45fr)_auto] lg:max-w-3xl lg:flex-1'
          onSubmit={handleSearch}
        >
          <div className='relative min-w-0'>
            <HugeiconsIcon
              icon={Search01Icon}
              className='text-muted-foreground pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2'
              aria-hidden='true'
            />
            <Input
              name='endpointSearch'
              defaultValue={search.endpointSearch}
              className='min-h-11 pl-9 lg:min-h-8'
              aria-label={t('Search endpoint breakers')}
              placeholder={t('Search endpoint authority')}
            />
          </div>
          <div className='relative min-w-0'>
            <HugeiconsIcon
              icon={Location01Icon}
              className='text-muted-foreground pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2'
              aria-hidden='true'
            />
            <Input
              name='endpointRegion'
              defaultValue={search.endpointRegion}
              className='min-h-11 pl-9 lg:min-h-8'
              aria-label={t('Filter by gateway region')}
              placeholder={t('Gateway region')}
            />
          </div>
          <Button
            type='submit'
            size='sm'
            variant='outline'
            className='min-h-11 lg:min-h-7'
          >
            <HugeiconsIcon
              icon={Search01Icon}
              data-icon='inline-start'
              aria-hidden='true'
            />
            {t('Apply filters')}
          </Button>
        </form>
        <div className='flex flex-wrap items-center gap-2'>
          {filtersActive ? (
            <Button
              size='sm'
              variant='ghost'
              className='min-h-11 lg:min-h-7'
              onClick={() =>
                updateSearch({
                  endpointPage: 1,
                  endpointSearch: '',
                  endpointRegion: '',
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
          <Button
            size='sm'
            variant='outline'
            className='min-h-11 lg:min-h-7'
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
      </div>

      <ChannelRoutingEndpointNetworkSection
        endpoints={query.data?.items ?? []}
        total={query.data?.total ?? 0}
        region={query.data?.region ?? ''}
        stableNodeId={query.data?.stable_node_id ?? ''}
        quorumEligible={query.data?.endpoint_quorum_eligible ?? false}
        canOperate={canOperate}
        loading={query.isLoading}
        fetching={query.isFetching}
        refetchError={query.isRefetchError && query.data != null}
        error={query.error}
        onRetry={() => void query.refetch()}
        page={page}
        pageSize={pageSize}
        onPageChange={(nextPage) => updateSearch({ endpointPage: nextPage })}
        onPageSizeChange={(nextSize) =>
          updateSearch({
            endpointPage: 1,
            endpointPageSize: nextSize,
          })
        }
      />
    </div>
  )
}
