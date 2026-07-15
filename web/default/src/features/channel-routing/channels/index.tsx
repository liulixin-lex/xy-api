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
import { getRouteApi } from '@tanstack/react-router'
import {
  KeyRound,
  Layers3,
  MapPin,
  RefreshCw,
  Search,
  ShieldAlert,
  WalletCards,
  X,
} from 'lucide-react'
import type { FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  CHANNEL_STATUS_LABELS,
  CHANNEL_TYPE_OPTIONS,
} from '@/features/channels/constants'
import { getChannelTypeLabel } from '@/features/channels/lib/channel-utils'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import {
  listChannelRoutingChannels,
  listChannelRoutingEndpoints,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingIdentityText } from '../components/identity-text'
import { ChannelRoutingPageFrame } from '../components/page-frame'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../components/page-state'
import { ChannelRoutingPagination } from '../components/pagination-bar'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import { ChannelRoutingEndpointNetworkSection } from '../overview/endpoint-network-section'
import type { ChannelSnapshot } from '../types'

const route = getRouteApi('/_authenticated/channel-routing/$section')

function costConnectorPresentation(
  channel: ChannelSnapshot,
  translate: (key: string, options?: Record<string, unknown>) => string
) {
  if (!channel.cost_connector_enabled) {
    return { status: 'disabled', label: translate('Disabled') }
  }
  if (channel.cost_sync_failures > 0) {
    return {
      status: 'degraded',
      label: translate('{{count}} sync failures', {
        count: channel.cost_sync_failures,
      }),
    }
  }
  return { status: 'success', label: translate('Healthy') }
}

function ChannelCredentialSummary(props: { channel: ChannelSnapshot }) {
  const { t } = useTranslation()
  const visibleIds = props.channel.credential_ids.slice(0, 3)
  const hiddenCount = Math.max(
    0,
    props.channel.credential_count - visibleIds.length
  )
  const identityText = visibleIds.map((id) => `#${id}`).join(' · ')

  return (
    <div className='min-w-0'>
      <span className='inline-flex items-center gap-1.5'>
        <KeyRound
          className='text-muted-foreground size-3.5'
          aria-hidden='true'
        />
        {t('{{count}} credentials', {
          count: props.channel.credential_count,
        })}
      </span>
      {identityText ? (
        <ChannelRoutingIdentityText
          text={
            props.channel.credentials_truncated && hiddenCount > 0
              ? `${identityText} · ${t('{{count}} more', { count: hiddenCount })}`
              : identityText
          }
          className='text-muted-foreground mt-1 font-mono text-xs'
        />
      ) : null}
    </div>
  )
}

function ChannelModelCoverage(props: { channel: ChannelSnapshot }) {
  const { t } = useTranslation()
  const names = props.channel.models ?? []
  const modelCount = Math.max(0, props.channel.model_count ?? names.length)
  const hiddenCount = Math.max(0, modelCount - names.length)
  let detail = t('Model names unavailable')
  if (modelCount === 0) detail = t('No models')
  else if (names.length > 0) {
    detail = names.join(', ')
    if (props.channel.models_truncated && hiddenCount > 0) {
      detail = `${detail} · ${t('{{count}} more', { count: hiddenCount })}`
    }
  }

  return (
    <div className='min-w-0'>
      <span className='inline-flex items-center gap-1.5'>
        <Layers3
          className='text-muted-foreground size-3.5'
          aria-hidden='true'
        />
        {t('{{count}} models', { count: modelCount })}
      </span>
      <ChannelRoutingIdentityText
        text={detail}
        className='text-muted-foreground mt-1 text-xs'
      />
    </div>
  )
}

export function ChannelRoutingChannelsPage() {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const user = useAuthStore((state) => state.auth.user)
  const canOperate = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )
  const page = search.page ?? 1
  const pageSize = search.pageSize ?? 20
  const endpointPage = search.endpointPage ?? 1
  const endpointPageSize = search.endpointPageSize ?? 20
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.channels({
      page,
      page_size: pageSize,
      search: search.search || undefined,
      status: search.status,
      type: search.type,
    }),
    queryFn: () =>
      listChannelRoutingChannels({
        page,
        page_size: pageSize,
        search: search.search || undefined,
        status: search.status,
        type: search.type,
      }),
  })
  const endpointsQuery = useQuery({
    queryKey: channelRoutingQueryKeys.endpoints({
      page: endpointPage,
      page_size: endpointPageSize,
      search: search.search || undefined,
    }),
    queryFn: () =>
      listChannelRoutingEndpoints({
        page: endpointPage,
        page_size: endpointPageSize,
        search: search.search || undefined,
      }),
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
      endpointPage: 1,
      search: String(form.get('search') ?? '').trim(),
    })
  }

  return (
    <ChannelRoutingPageFrame
      activeSection='channels'
      title={t('Channel health')}
      actions={
        <Button
          size='icon-sm'
          variant='outline'
          aria-label={t('Refresh')}
          disabled={query.isFetching || endpointsQuery.isFetching}
          onClick={() => {
            void query.refetch()
            void endpointsQuery.refetch()
          }}
        >
          <RefreshCw
            aria-hidden='true'
            className={
              query.isFetching || endpointsQuery.isFetching
                ? 'animate-spin motion-reduce:animate-none'
                : undefined
            }
          />
        </Button>
      }
    >
      <div className='space-y-3 pb-2'>
        <div className='flex flex-wrap items-center gap-2'>
          <form
            key={search.search}
            className='flex min-w-64 flex-1 items-center gap-2 sm:max-w-md'
            onSubmit={handleSearch}
          >
            <div className='relative min-w-0 flex-1'>
              <Search
                className='text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2'
                aria-hidden='true'
              />
              <Input
                name='search'
                defaultValue={search.search}
                className='pl-8'
                aria-label={t('Search channel health')}
                placeholder={t('Search channels or endpoints')}
              />
            </div>
            <Button
              type='submit'
              size='icon-sm'
              variant='outline'
              aria-label={t('Search')}
            >
              <Search aria-hidden='true' />
            </Button>
          </form>
          <NativeSelect
            size='sm'
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
            <NativeSelectOption value='all'>
              {t('All status')}
            </NativeSelectOption>
            {Object.entries(CHANNEL_STATUS_LABELS).map(([value, label]) => (
              <NativeSelectOption key={value} value={value}>
                {t(label)}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          <NativeSelect
            size='sm'
            className='max-w-48'
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
            <NativeSelectOption value='all'>
              {t('All types')}
            </NativeSelectOption>
            {CHANNEL_TYPE_OPTIONS.map((option) => (
              <NativeSelectOption key={option.value} value={option.value}>
                {option.label}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          {search.search || search.status != null || search.type != null ? (
            <Button
              size='sm'
              variant='ghost'
              onClick={() =>
                updateSearch({
                  page: 1,
                  endpointPage: 1,
                  search: '',
                  status: undefined,
                  type: undefined,
                })
              }
            >
              <X aria-hidden='true' />
              {t('Clear')}
            </Button>
          ) : null}
        </div>

        <section
          className='space-y-3'
          aria-labelledby='physical-channels-title'
        >
          <div className='flex flex-wrap items-end justify-between gap-2'>
            <h2 id='physical-channels-title' className='text-sm font-semibold'>
              {t('Physical channels')}
            </h2>
            {query.data ? (
              <span className='text-muted-foreground text-xs'>
                {t('{{count}} channels', { count: query.data.total })}
              </span>
            ) : null}
          </div>

          {query.isLoading ? <ChannelRoutingLoadingState /> : null}
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
              title={t('No channels')}
              description={t('No channels match the current health filters.')}
            />
          ) : null}

          {query.data && query.data.items.length > 0 ? (
            <>
              <div className='hidden overflow-hidden rounded-lg border lg:block'>
                <Table scrollAreaLabel={t('Channel health table')}>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t('Channel')}</TableHead>
                      <TableHead>{t('Serving health')}</TableHead>
                      <TableHead>{t('Credentials')}</TableHead>
                      <TableHead>{t('Model coverage')}</TableHead>
                      <TableHead>{t('Balance')}</TableHead>
                      <TableHead>{t('Cost connector')}</TableHead>
                      <TableHead>{t('Last updated')}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {query.data.items.map((channel) => {
                      const statusLabel =
                        CHANNEL_STATUS_LABELS[
                          channel.status as keyof typeof CHANNEL_STATUS_LABELS
                        ] ?? 'Unknown'
                      const costConnector = costConnectorPresentation(
                        channel,
                        t
                      )
                      return (
                        <TableRow key={channel.id}>
                          <TableCell className='max-w-80'>
                            <div className='truncate font-medium'>
                              {channel.name || `#${channel.id}`}
                            </div>
                            <div className='text-muted-foreground text-xs'>
                              {getChannelTypeLabel(channel.type)}
                            </div>
                            <ChannelRoutingIdentityText
                              text={
                                channel.endpoint_authority ||
                                channel.endpoint ||
                                t('Default endpoint')
                              }
                              className='text-muted-foreground mt-1 font-mono text-xs'
                              breakAll
                            />
                            <div className='text-muted-foreground mt-1 inline-flex items-center gap-1 text-xs'>
                              <MapPin className='size-3' aria-hidden='true' />
                              {channel.region || t('Unknown region')}
                            </div>
                          </TableCell>
                          <TableCell>
                            <div className='flex flex-wrap items-center gap-2'>
                              <ChannelRoutingStatusBadge
                                status={channel.status}
                                label={t(statusLabel)}
                              />
                              {channel.auth_failure ? (
                                <ChannelRoutingStatusBadge
                                  status='failed'
                                  label={t('Credential failure')}
                                />
                              ) : null}
                              <ChannelRoutingStatusBadge
                                status={
                                  channel.endpoint_state.known
                                    ? channel.endpoint_state.state || 'unknown'
                                    : 'unknown'
                                }
                              />
                            </div>
                          </TableCell>
                          <TableCell>
                            <ChannelCredentialSummary channel={channel} />
                            {channel.multi_key ? (
                              <div className='text-muted-foreground mt-1 text-xs'>
                                {t('Multi-key')}
                              </div>
                            ) : null}
                          </TableCell>
                          <TableCell className='max-w-64'>
                            <ChannelModelCoverage channel={channel} />
                          </TableCell>
                          <TableCell>
                            <span className='inline-flex items-center gap-1.5'>
                              <WalletCards
                                className='text-muted-foreground size-3.5'
                                aria-hidden='true'
                              />
                              {channel.balance_known
                                ? format.number(channel.balance)
                                : t('Unknown')}
                            </span>
                          </TableCell>
                          <TableCell>
                            <ChannelRoutingStatusBadge
                              status={costConnector.status}
                              label={costConnector.label}
                            />
                            {channel.cost_sync_error ? (
                              <div className='text-destructive mt-1 max-w-64 truncate text-xs'>
                                {channel.cost_sync_error}
                              </div>
                            ) : null}
                          </TableCell>
                          <TableCell>
                            {format.timestamp(
                              Math.max(
                                channel.auth_failure_updated_at,
                                channel.balance_updated_at
                              )
                            )}
                          </TableCell>
                        </TableRow>
                      )
                    })}
                  </TableBody>
                </Table>
              </div>

              <div className='divide-y rounded-lg border lg:hidden'>
                {query.data.items.map((channel) => {
                  const statusLabel =
                    CHANNEL_STATUS_LABELS[
                      channel.status as keyof typeof CHANNEL_STATUS_LABELS
                    ] ?? 'Unknown'
                  return (
                    <article key={channel.id} className='p-3'>
                      <div className='flex items-start justify-between gap-3'>
                        <div className='min-w-0'>
                          <h3>
                            <ChannelRoutingIdentityText
                              text={channel.name || `#${channel.id}`}
                              className='text-sm font-medium'
                            />
                          </h3>
                          <p className='text-muted-foreground text-xs'>
                            {getChannelTypeLabel(channel.type)}
                          </p>
                          <ChannelRoutingIdentityText
                            text={
                              channel.endpoint_authority ||
                              channel.endpoint ||
                              t('Default endpoint')
                            }
                            className='text-muted-foreground font-mono text-xs'
                            breakAll
                          />
                          <div className='text-muted-foreground mt-1 inline-flex items-center gap-1 text-xs'>
                            <MapPin className='size-3' aria-hidden='true' />
                            {channel.region || t('Unknown region')}
                          </div>
                        </div>
                        <ChannelRoutingStatusBadge
                          status={channel.status}
                          label={t(statusLabel)}
                        />
                      </div>
                      <div className='mt-3 grid grid-cols-2 gap-3 text-xs sm:grid-cols-3'>
                        <div>
                          <ChannelCredentialSummary channel={channel} />
                        </div>
                        <div>
                          <ChannelModelCoverage channel={channel} />
                        </div>
                        <div>
                          <div className='text-muted-foreground'>
                            {t('Balance')}
                          </div>
                          <div className='mt-1 font-medium'>
                            {channel.balance_known
                              ? format.number(channel.balance)
                              : t('Unknown')}
                          </div>
                        </div>
                        <div>
                          <div className='text-muted-foreground'>
                            {t('Cost connector')}
                          </div>
                          <div className='mt-1 font-medium'>
                            {channel.cost_connector_enabled
                              ? t('Enabled')
                              : t('Disabled')}
                          </div>
                        </div>
                        <div>
                          <div className='text-muted-foreground'>
                            {t('Serving credential')}
                          </div>
                          <div className='mt-1 inline-flex items-center gap-1 font-medium'>
                            {channel.auth_failure ? (
                              <ShieldAlert
                                className='text-destructive size-3.5'
                                aria-hidden='true'
                              />
                            ) : null}
                            {channel.auth_failure ? t('Failed') : t('Healthy')}
                          </div>
                        </div>
                        <div>
                          <div className='text-muted-foreground'>
                            {t('Endpoint state')}
                          </div>
                          <div className='mt-1'>
                            <ChannelRoutingStatusBadge
                              status={
                                channel.endpoint_state.known
                                  ? channel.endpoint_state.state || 'unknown'
                                  : 'unknown'
                              }
                            />
                          </div>
                        </div>
                      </div>
                    </article>
                  )
                })}
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
        </section>

        <ChannelRoutingEndpointNetworkSection
          endpoints={endpointsQuery.data?.items ?? []}
          total={endpointsQuery.data?.total ?? 0}
          region={endpointsQuery.data?.region ?? ''}
          stableNodeId={endpointsQuery.data?.stable_node_id ?? ''}
          quorumEligible={
            endpointsQuery.data?.endpoint_quorum_eligible ?? false
          }
          canOperate={canOperate}
          loading={endpointsQuery.isLoading}
          fetching={endpointsQuery.isFetching}
          refetchError={
            endpointsQuery.isRefetchError && endpointsQuery.data != null
          }
          error={endpointsQuery.error}
          onRetry={() => void endpointsQuery.refetch()}
          page={endpointPage}
          pageSize={endpointPageSize}
          onPageChange={(nextPage) => updateSearch({ endpointPage: nextPage })}
          onPageSizeChange={(nextSize) =>
            updateSearch({ endpointPage: 1, endpointPageSize: nextSize })
          }
        />
      </div>
    </ChannelRoutingPageFrame>
  )
}
