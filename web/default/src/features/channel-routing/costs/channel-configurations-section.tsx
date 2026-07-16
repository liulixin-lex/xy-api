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
  Cancel01Icon,
  CheckmarkCircle02Icon,
  Database01Icon,
  Edit02Icon,
  InformationCircleIcon,
  Money03Icon,
  Search01Icon,
  ShieldKeyIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import type { TFunction } from 'i18next'
import {
  useCallback,
  useEffect,
  useState,
  type ComponentProps,
  type FormEvent,
} from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
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
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import { listChannelRoutingConfigurations } from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingIdentityText } from '../components/identity-text'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../components/page-state'
import { ChannelRoutingPagination } from '../components/pagination-bar'
import { useChannelRoutingFormatters } from '../lib/format'
import type {
  RoutingChannelConfiguration,
  RoutingChannelConfigurationCostSource,
  RoutingChannelConfigurationTrafficClass,
  RoutingChannelFailureDomainStatus,
} from '../types'
import { ChannelConfigurationSheet } from './channel-configuration-sheet'

const route = getRouteApi('/_authenticated/channel-routing/$section')

type StateTone = 'success' | 'warning' | 'info' | 'muted'
type HugeiconData = ComponentProps<typeof HugeiconsIcon>['icon']

function ConfigurationStateBadge(props: {
  label: string
  icon: HugeiconData
  tone: StateTone
}) {
  return (
    <Badge
      variant='outline'
      className={cn(
        props.tone === 'success' &&
          'border-success/40 bg-success/15 text-foreground',
        props.tone === 'warning' &&
          'border-warning/40 bg-warning/15 text-foreground',
        props.tone === 'info' && 'border-info/40 bg-info/15 text-foreground',
        props.tone === 'muted' &&
          'border-border bg-muted/50 text-muted-foreground'
      )}
    >
      <HugeiconsIcon icon={props.icon} strokeWidth={2} aria-hidden='true' />
      {props.label}
    </Badge>
  )
}

function costSourceLabel(
  source: RoutingChannelConfigurationCostSource,
  t: TFunction
): string {
  switch (source) {
    case 'manual':
      return t('Manual')
    case 'legacy_migrated':
      return t('Legacy migrated')
    case 'defaulted':
      return t('System default')
  }
}

function trafficClassLabel(
  trafficClass: RoutingChannelConfigurationTrafficClass,
  t: TFunction
): string {
  return trafficClass === 'claude_code_only'
    ? t('Claude Code only')
    : t('All eligible traffic')
}

function failureDomainLabel(
  status: RoutingChannelFailureDomainStatus,
  t: TFunction
): string {
  switch (status) {
    case 'configured':
      return t('Configured')
    case 'historical_migrated':
      return t('Historical migrated')
    case 'unconfigured':
      return t('Not configured')
  }
}

function FailureDomainBadge(props: {
  configuration: RoutingChannelConfiguration
}) {
  const { t } = useTranslation()
  if (props.configuration.failure_domain_status === 'configured') {
    return (
      <ConfigurationStateBadge
        icon={CheckmarkCircle02Icon}
        label={failureDomainLabel('configured', t)}
        tone='success'
      />
    )
  }
  if (props.configuration.failure_domain_status === 'historical_migrated') {
    return (
      <ConfigurationStateBadge
        icon={Database01Icon}
        label={failureDomainLabel('historical_migrated', t)}
        tone='info'
      />
    )
  }
  return (
    <ConfigurationStateBadge
      icon={Alert02Icon}
      label={failureDomainLabel('unconfigured', t)}
      tone='muted'
    />
  )
}

function ConfirmationBadge(props: { confirmed: boolean }) {
  const { t } = useTranslation()
  return props.confirmed ? (
    <ConfigurationStateBadge
      icon={CheckmarkCircle02Icon}
      label={t('Confirmed')}
      tone='success'
    />
  ) : (
    <ConfigurationStateBadge
      icon={Alert02Icon}
      label={t('Pending review')}
      tone='warning'
    />
  )
}

function CostBasisBadge(props: { available: boolean }) {
  const { t } = useTranslation()
  return props.available ? (
    <ConfigurationStateBadge
      icon={CheckmarkCircle02Icon}
      label={t('Available')}
      tone='success'
    />
  ) : (
    <ConfigurationStateBadge
      icon={Alert02Icon}
      label={t('Unavailable')}
      tone='warning'
    />
  )
}

function ChannelMultiplierValue(props: {
  configuration: RoutingChannelConfiguration
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  if (props.configuration.upstream_cost_multiplier === 0) {
    return (
      <span className='inline-flex items-center gap-1 font-semibold'>
        <HugeiconsIcon
          icon={Money03Icon}
          strokeWidth={2}
          className='size-3.5'
          aria-hidden='true'
        />
        {t('Free 0×')}
      </span>
    )
  }
  return (
    <span className='font-mono font-semibold'>
      {format.cost(props.configuration.upstream_cost_multiplier)}×
    </span>
  )
}

export function ChannelConfigurationsSection() {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const user = useAuthStore((state) => state.auth.user)
  const canSensitiveWrite = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
  )
  const [selectedConfiguration, setSelectedConfiguration] =
    useState<RoutingChannelConfiguration | null>(null)
  const page = search.page ?? 1
  const pageSize = search.pageSize ?? 20
  const confirmed = search.costConfirmed ?? 'all'
  const costSource = search.costSource ?? 'all'
  const trafficClass = search.trafficClass ?? 'any'
  const queryParams = {
    page,
    page_size: pageSize,
    search: search.search || undefined,
    cost_confirmed: confirmed === 'all' ? undefined : confirmed,
    cost_source:
      costSource === 'all'
        ? undefined
        : (costSource as RoutingChannelConfigurationCostSource),
    traffic_class:
      trafficClass === 'any'
        ? undefined
        : (trafficClass as RoutingChannelConfigurationTrafficClass),
  }
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.channelConfigurations(queryParams),
    queryFn: () => listChannelRoutingConfigurations(queryParams),
    meta: { handleErrorLocally: true },
  })

  const updateSearch = useCallback(
    (patch: Record<string, string | number | boolean | undefined>) => {
      void navigate({
        search: (previous) => ({ ...previous, ...patch }),
        replace: true,
      })
    },
    [navigate]
  )
  useEffect(() => {
    if (!query.data) return
    const totalPages = Math.max(1, Math.ceil(query.data.total / pageSize))
    if (page > totalPages) updateSearch({ page: totalPages })
  }, [page, pageSize, query.data, updateSearch])

  const handleSearch = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    updateSearch({
      page: 1,
      search: String(form.get('search') ?? '').trim(),
    })
  }
  const filtersActive =
    Boolean(search.search) ||
    confirmed !== 'all' ||
    costSource !== 'all' ||
    trafficClass !== 'any'

  return (
    <div className='space-y-3 pb-2'>
      <Alert role='note'>
        <HugeiconsIcon
          icon={InformationCircleIcon}
          strokeWidth={2}
          aria-hidden='true'
        />
        <AlertTitle>{t('How channel multipliers work')}</AlertTitle>
        <AlertDescription>
          {t(
            "Channel multiplier estimates this channel's upstream cost. 1× uses the system model baseline, 0.5× is half, and 2× is double. It does not change user group billing."
          )}
        </AlertDescription>
      </Alert>

      {!canSensitiveWrite ? (
        <Alert role='status'>
          <HugeiconsIcon
            icon={ShieldKeyIcon}
            strokeWidth={2}
            aria-hidden='true'
          />
          <AlertTitle>{t('Channel multipliers are read-only')}</AlertTitle>
          <AlertDescription>
            {t(
              'Sensitive write permission is required to edit channel multipliers.'
            )}
          </AlertDescription>
        </Alert>
      ) : null}

      <div className='flex flex-wrap items-center gap-2'>
        <form
          key={search.search}
          className='flex min-w-64 flex-1 items-center gap-2 sm:max-w-md'
          onSubmit={handleSearch}
        >
          <div className='relative min-w-0 flex-1'>
            <HugeiconsIcon
              icon={Search01Icon}
              strokeWidth={2}
              className='text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2'
              aria-hidden='true'
            />
            <Input
              name='search'
              defaultValue={search.search}
              className='pl-8'
              aria-label={t('Search channel multipliers')}
              placeholder={t('Search channels or IDs')}
            />
          </div>
          <Button
            type='submit'
            size='icon-sm'
            variant='outline'
            aria-label={t('Search')}
          >
            <HugeiconsIcon
              icon={Search01Icon}
              strokeWidth={2}
              aria-hidden='true'
            />
          </Button>
        </form>

        <NativeSelect
          size='sm'
          value={confirmed === 'all' ? 'all' : String(confirmed)}
          aria-label={t('Confirmation status')}
          onChange={(event) =>
            updateSearch({
              page: 1,
              costConfirmed:
                event.target.value === 'all'
                  ? 'all'
                  : event.target.value === 'true',
            })
          }
        >
          <NativeSelectOption value='all'>
            {t('All confirmation states')}
          </NativeSelectOption>
          <NativeSelectOption value='true'>{t('Confirmed')}</NativeSelectOption>
          <NativeSelectOption value='false'>
            {t('Pending review')}
          </NativeSelectOption>
        </NativeSelect>

        <NativeSelect
          size='sm'
          value={costSource}
          aria-label={t('Multiplier source')}
          onChange={(event) =>
            updateSearch({ page: 1, costSource: event.target.value })
          }
        >
          <NativeSelectOption value='all'>
            {t('All sources')}
          </NativeSelectOption>
          <NativeSelectOption value='manual'>{t('Manual')}</NativeSelectOption>
          <NativeSelectOption value='legacy_migrated'>
            {t('Legacy migrated')}
          </NativeSelectOption>
          <NativeSelectOption value='defaulted'>
            {t('System default')}
          </NativeSelectOption>
        </NativeSelect>

        <NativeSelect
          size='sm'
          value={trafficClass}
          aria-label={t('Traffic class')}
          onChange={(event) =>
            updateSearch({ page: 1, trafficClass: event.target.value })
          }
        >
          <NativeSelectOption value='any'>
            {t('All traffic classes')}
          </NativeSelectOption>
          <NativeSelectOption value='all'>
            {t('All eligible traffic')}
          </NativeSelectOption>
          <NativeSelectOption value='claude_code_only'>
            {t('Claude Code only')}
          </NativeSelectOption>
        </NativeSelect>

        {filtersActive ? (
          <Button
            size='sm'
            variant='ghost'
            onClick={() =>
              updateSearch({
                page: 1,
                search: '',
                costConfirmed: 'all',
                costSource: 'all',
                trafficClass: 'any',
              })
            }
          >
            <HugeiconsIcon
              icon={Cancel01Icon}
              data-icon='inline-start'
              strokeWidth={2}
              aria-hidden='true'
            />
            {t('Clear')}
          </Button>
        ) : null}
      </div>

      {query.isLoading ? <ChannelRoutingLoadingState rows={7} /> : null}
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
          title={
            query.data.total > 0
              ? t('This channel multiplier page is empty')
              : t('No channel multipliers')
          }
          description={
            query.data.total > 0
              ? t(
                  'Return to the first page to continue browsing channel multipliers.'
                )
              : t('No channel multipliers match the current filters.')
          }
          action={
            query.data.total > 0 ? (
              <Button
                type='button'
                variant='outline'
                onClick={() => updateSearch({ page: 1 })}
              >
                {t('First page')}
              </Button>
            ) : undefined
          }
        />
      ) : null}

      {query.data && query.data.items.length > 0 ? (
        <>
          <div className='hidden overflow-hidden rounded-lg border xl:block'>
            <Table
              className='min-w-[78rem]'
              scrollAreaLabel={t('Channel multipliers table')}
            >
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Channel')}</TableHead>
                  <TableHead className='text-right'>
                    {t('Channel multiplier')}
                  </TableHead>
                  <TableHead>{t('Confirmation / source')}</TableHead>
                  <TableHead>{t('Models / cost basis')}</TableHead>
                  <TableHead>{t('Traffic class')}</TableHead>
                  <TableHead>{t('Failure domain')}</TableHead>
                  <TableHead>{t('Updated')}</TableHead>
                  <TableHead className='w-10'>
                    <span className='sr-only'>{t('Actions')}</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {query.data.items.map((configuration) => (
                  <TableRow key={configuration.channel_id}>
                    <TableCell>
                      <div className='font-medium'>
                        {configuration.channel_name}
                      </div>
                      <div className='text-muted-foreground text-xs'>
                        #{configuration.channel_id}
                      </div>
                    </TableCell>
                    <TableCell className='text-right'>
                      <ChannelMultiplierValue configuration={configuration} />
                    </TableCell>
                    <TableCell>
                      <div className='flex flex-wrap items-center gap-1.5'>
                        <ConfirmationBadge
                          confirmed={configuration.cost_confirmed}
                        />
                        <Badge variant='outline'>
                          {costSourceLabel(configuration.cost_source, t)}
                        </Badge>
                      </div>
                    </TableCell>
                    <TableCell>
                      <div className='font-medium'>
                        {t('{{count}} models', {
                          count: configuration.effective_model_count,
                        })}
                      </div>
                      <div className='mt-1'>
                        <CostBasisBadge
                          available={configuration.cost_basis_available}
                        />
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge variant='outline'>
                        {trafficClassLabel(configuration.traffic_class, t)}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <FailureDomainBadge configuration={configuration} />
                      {configuration.failure_domain_label ? (
                        <ChannelRoutingIdentityText
                          text={configuration.failure_domain_label}
                          className='text-muted-foreground mt-1 max-w-56 text-xs'
                        />
                      ) : null}
                    </TableCell>
                    <TableCell className='text-xs'>
                      <div>
                        {configuration.updated_by > 0
                          ? t('Updated by #{{id}}', {
                              id: configuration.updated_by,
                            })
                          : t('System')}
                      </div>
                      <div className='text-muted-foreground mt-1'>
                        {format.timestamp(configuration.updated_time)}
                      </div>
                    </TableCell>
                    <TableCell>
                      <Button
                        type='button'
                        size='icon-sm'
                        variant='ghost'
                        disabled={!canSensitiveWrite}
                        aria-label={t('Edit channel multiplier')}
                        title={
                          canSensitiveWrite
                            ? t('Edit channel multiplier')
                            : t(
                                'Sensitive write permission is required to edit channel multipliers.'
                              )
                        }
                        onClick={() => setSelectedConfiguration(configuration)}
                      >
                        <HugeiconsIcon
                          icon={Edit02Icon}
                          strokeWidth={2}
                          aria-hidden='true'
                        />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          <div className='divide-y rounded-lg border xl:hidden'>
            {query.data.items.map((configuration) => (
              <article key={configuration.channel_id} className='p-3'>
                <div className='flex items-start justify-between gap-3'>
                  <div className='min-w-0'>
                    <h3 className='truncate text-sm font-medium'>
                      {configuration.channel_name}
                    </h3>
                    <div className='text-muted-foreground mt-0.5 text-xs'>
                      #{configuration.channel_id}
                    </div>
                  </div>
                  <ChannelMultiplierValue configuration={configuration} />
                </div>
                <div className='mt-3 flex flex-wrap gap-1.5'>
                  <ConfirmationBadge confirmed={configuration.cost_confirmed} />
                  <Badge variant='outline'>
                    {costSourceLabel(configuration.cost_source, t)}
                  </Badge>
                  <Badge variant='outline'>
                    {trafficClassLabel(configuration.traffic_class, t)}
                  </Badge>
                </div>
                <dl className='mt-3 grid grid-cols-2 gap-3 text-xs sm:grid-cols-4'>
                  <div>
                    <dt className='text-muted-foreground'>{t('Models')}</dt>
                    <dd className='mt-1 font-medium'>
                      {format.number(configuration.effective_model_count)}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground'>{t('Cost basis')}</dt>
                    <dd className='mt-1'>
                      <CostBasisBadge
                        available={configuration.cost_basis_available}
                      />
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground'>
                      {t('Failure domain')}
                    </dt>
                    <dd className='mt-1'>
                      <FailureDomainBadge configuration={configuration} />
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground'>{t('Updated')}</dt>
                    <dd className='mt-1 font-medium'>
                      {format.timestamp(configuration.updated_time)}
                    </dd>
                  </div>
                </dl>
                {configuration.failure_domain_label ? (
                  <ChannelRoutingIdentityText
                    text={configuration.failure_domain_label}
                    className='text-muted-foreground mt-3 text-xs'
                  />
                ) : null}
                <div className='mt-3 flex justify-end'>
                  <Button
                    type='button'
                    size='sm'
                    variant='outline'
                    disabled={!canSensitiveWrite}
                    onClick={() => setSelectedConfiguration(configuration)}
                  >
                    <HugeiconsIcon
                      icon={Edit02Icon}
                      data-icon='inline-start'
                      strokeWidth={2}
                      aria-hidden='true'
                    />
                    {t('Edit')}
                  </Button>
                </div>
              </article>
            ))}
          </div>
        </>
      ) : null}

      {query.data && query.data.total > 0 ? (
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
      ) : null}

      {selectedConfiguration ? (
        <ChannelConfigurationSheet
          key={`${selectedConfiguration.channel_id}:${selectedConfiguration.etag}`}
          open
          configuration={selectedConfiguration}
          onSaved={(updated) => setSelectedConfiguration(updated)}
          onOpenChange={(open) => {
            if (!open) setSelectedConfiguration(null)
          }}
        />
      ) : null}
    </div>
  )
}
