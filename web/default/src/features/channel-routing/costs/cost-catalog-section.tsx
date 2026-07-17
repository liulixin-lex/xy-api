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
  ArrowLeft01Icon,
  ArrowRight01Icon,
  Search01Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import {
  useEffect,
  useReducer,
  useState,
  type FormEvent,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Empty, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

import {
  getChannelRoutingCostCatalogModel,
  listChannelRoutingCostCatalogMembers,
  listChannelRoutingCostCatalogModels,
  listChannelRoutingCostCatalogPools,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingIdentityText } from '../components/identity-text'
import { ChannelRoutingRefetchErrorAlert } from '../components/page-state'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import {
  initialCostCatalogNavigationState,
  reduceCostCatalogNavigation,
} from '../lib/cost-catalog-navigation'
import { routingCostDimensionLabel } from '../lib/cost-dimensions'
import { useChannelRoutingFormatters } from '../lib/format'
import type {
  RoutingCostCatalogMember,
  RoutingCostCatalogModel,
  RoutingCostCatalogPool,
} from '../types'
import { CostCatalogHierarchyLayout } from './cost-catalog-layout'
import { CostContractDetail } from './cost-contract-detail'

const catalogPageSize = 20

function CatalogPanel(props: {
  title: string
  searchLabel: string
  search: string
  onSearch: (value: string) => void
  page: number
  total: number
  loading: boolean
  error: boolean
  onRetry: () => void
  onPageChange: (page: number) => void
  children: ReactNode
}) {
  const { t } = useTranslation()
  const [searchValue, setSearchValue] = useState(props.search)
  const totalPages = Math.max(1, Math.ceil(props.total / catalogPageSize))

  useEffect(() => {
    setSearchValue(props.search)
  }, [props.search])

  const submitSearch = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const nextSearch = searchValue.trim()
    setSearchValue(nextSearch)
    props.onSearch(nextSearch)
  }
  let panelBody = props.children
  if (props.loading) {
    panelBody = (
      <div
        className='flex flex-col gap-2 p-3'
        role='status'
        aria-live='polite'
        aria-busy='true'
      >
        <span className='sr-only'>{t('Loading')}</span>
        {Array.from({ length: 5 }, (_, index) => (
          <Skeleton key={index} className='h-14 motion-reduce:animate-none' />
        ))}
      </div>
    )
  } else if (props.error) {
    panelBody = (
      <div className='p-3'>
        <Alert variant='destructive' role='alert'>
          <HugeiconsIcon
            icon={Alert02Icon}
            strokeWidth={2}
            aria-hidden='true'
          />
          <AlertTitle>{t('Cost catalog unavailable')}</AlertTitle>
          <AlertDescription className='flex flex-col items-start gap-3'>
            <span>{t('Unable to load this cost catalog level.')}</span>
            <Button size='sm' variant='outline' onClick={props.onRetry}>
              {t('Retry')}
            </Button>
          </AlertDescription>
        </Alert>
      </div>
    )
  }

  return (
    <section className='flex min-h-64 min-w-0 flex-col border-b xl:min-h-[34rem] xl:border-r xl:border-b-0'>
      <div className='flex flex-col gap-2 border-b p-3'>
        <div className='flex items-center justify-between gap-2'>
          <h3 className='text-sm font-semibold'>{props.title}</h3>
          <span className='text-muted-foreground text-xs tabular-nums'>
            {props.total}
          </span>
        </div>
        <form className='flex gap-2' onSubmit={submitSearch}>
          <Input
            name='search'
            value={searchValue}
            onChange={(event) => setSearchValue(event.target.value)}
            aria-label={props.searchLabel}
            placeholder={props.searchLabel}
            className='h-8'
          />
          <Button
            type='submit'
            size='icon-sm'
            variant='outline'
            aria-label={t('Search')}
          >
            <HugeiconsIcon
              icon={Search01Icon}
              data-icon='inline-start'
              strokeWidth={2}
            />
          </Button>
        </form>
      </div>
      <ScrollArea className='min-h-0 flex-1 lg:h-[26rem]'>
        {panelBody}
      </ScrollArea>
      <div className='flex items-center justify-between gap-2 border-t p-2'>
        <span className='text-muted-foreground text-xs'>
          {t('Page {{page}} of {{total}}', {
            page: props.page,
            total: totalPages,
          })}
        </span>
        <div className='flex gap-1'>
          <Button
            size='icon-sm'
            variant='ghost'
            aria-label={t('Previous page')}
            disabled={props.page <= 1 || props.loading}
            onClick={() => props.onPageChange(props.page - 1)}
          >
            <HugeiconsIcon icon={ArrowLeft01Icon} strokeWidth={2} />
          </Button>
          <Button
            size='icon-sm'
            variant='ghost'
            aria-label={t('Next page')}
            disabled={props.page >= totalPages || props.loading}
            onClick={() => props.onPageChange(props.page + 1)}
          >
            <HugeiconsIcon icon={ArrowRight01Icon} strokeWidth={2} />
          </Button>
        </div>
      </div>
    </section>
  )
}

function CatalogPanelEmpty(props: { description: string }) {
  return (
    <Empty className='min-h-40 rounded-none border-0 p-4'>
      <EmptyHeader>
        <EmptyDescription>{props.description}</EmptyDescription>
      </EmptyHeader>
    </Empty>
  )
}

function CatalogChoice(props: {
  selected: boolean
  onClick: () => void
  title: string
  description: ReactNode
  trailing?: ReactNode
}) {
  return (
    <button
      type='button'
      aria-pressed={props.selected}
      className={cn(
        'focus-visible:ring-ring flex min-h-14 w-full items-center gap-3 border-b px-3 py-2 text-left transition-colors outline-none last:border-b-0 focus-visible:ring-2 focus-visible:ring-inset',
        props.selected
          ? 'bg-accent text-accent-foreground'
          : 'hover:bg-muted/60'
      )}
      onClick={props.onClick}
    >
      <div className='min-w-0 flex-1'>
        <ChannelRoutingIdentityText
          text={props.title}
          className='text-sm font-medium'
          withinInteractive
        />
        <div className='text-muted-foreground mt-0.5 text-xs'>
          {props.description}
        </div>
      </div>
      {props.trailing}
    </button>
  )
}

export function CostCatalogSection() {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const [navigation, dispatchNavigation] = useReducer(
    reduceCostCatalogNavigation,
    initialCostCatalogNavigationState
  )

  const poolParams = {
    page: navigation.poolPage,
    page_size: catalogPageSize,
    search: navigation.poolSearch || undefined,
  }
  const pools = useQuery({
    queryKey: channelRoutingQueryKeys.costCatalogPools(poolParams),
    queryFn: () => listChannelRoutingCostCatalogPools(poolParams),
    meta: { handleErrorLocally: true },
  })
  const selectedPool =
    pools.data?.items.find(
      (pool) => pool.pool_id === navigation.selectedPoolId
    ) ?? pools.data?.items[0]

  const memberParams = {
    page: navigation.memberPage,
    page_size: catalogPageSize,
    search: navigation.memberSearch || undefined,
  }
  const members = useQuery({
    queryKey: channelRoutingQueryKeys.costCatalogMembers(
      selectedPool?.pool_id ?? 0,
      memberParams
    ),
    queryFn: () =>
      listChannelRoutingCostCatalogMembers(
        selectedPool?.pool_id ?? 0,
        memberParams
      ),
    enabled: Boolean(selectedPool),
    meta: { handleErrorLocally: true },
  })
  const selectedMember =
    members.data?.items.find(
      (member) => member.member_id === navigation.selectedMemberId
    ) ?? members.data?.items[0]

  const modelParams = {
    page: navigation.modelPage,
    page_size: catalogPageSize,
    search: navigation.modelSearch || undefined,
  }
  const models = useQuery({
    queryKey: channelRoutingQueryKeys.costCatalogModels(
      selectedPool?.pool_id ?? 0,
      selectedMember?.member_id ?? 0,
      modelParams
    ),
    queryFn: () =>
      listChannelRoutingCostCatalogModels(
        selectedPool?.pool_id ?? 0,
        selectedMember?.member_id ?? 0,
        modelParams
      ),
    enabled: Boolean(selectedPool && selectedMember),
    meta: { handleErrorLocally: true },
  })
  const selectedModel =
    models.data?.items.find(
      (model) => model.model_name === navigation.selectedModelName
    ) ?? models.data?.items[0]

  const detail = useQuery({
    queryKey: channelRoutingQueryKeys.costCatalogModel(
      selectedPool?.pool_id ?? 0,
      selectedMember?.member_id ?? 0,
      selectedModel?.model_name ?? ''
    ),
    queryFn: () =>
      getChannelRoutingCostCatalogModel(
        selectedPool?.pool_id ?? 0,
        selectedMember?.member_id ?? 0,
        selectedModel?.model_name ?? ''
      ),
    enabled: Boolean(selectedPool && selectedMember && selectedModel),
    meta: { handleErrorLocally: true },
  })
  const hasRefetchError =
    (pools.isRefetchError && Boolean(pools.data)) ||
    (members.isRefetchError && Boolean(members.data)) ||
    (models.isRefetchError && Boolean(models.data)) ||
    (detail.isRefetchError && Boolean(detail.data))
  const retryFailedQueries = () => {
    if (pools.isRefetchError && pools.data) void pools.refetch()
    if (members.isRefetchError && members.data) void members.refetch()
    if (models.isRefetchError && models.data) void models.refetch()
    if (detail.isRefetchError && detail.data) void detail.refetch()
  }

  const selectPool = (pool: RoutingCostCatalogPool) => {
    dispatchNavigation({ type: 'select-pool', poolId: pool.pool_id })
  }
  const selectMember = (member: RoutingCostCatalogMember) => {
    dispatchNavigation({ type: 'select-member', memberId: member.member_id })
  }
  const selectModel = (model: RoutingCostCatalogModel) => {
    dispatchNavigation({ type: 'select-model', modelName: model.model_name })
  }

  let poolContent: ReactNode = (
    <CatalogPanelEmpty
      description={t('No cost catalog pools match the current search.')}
    />
  )
  if (pools.data?.items.length) {
    poolContent = pools.data.items.map((pool) => (
      <CatalogChoice
        key={pool.pool_id}
        selected={pool.pool_id === selectedPool?.pool_id}
        onClick={() => selectPool(pool)}
        title={pool.display_name || pool.group_name}
        description={t('{{members}} channels · {{models}} models', {
          members: pool.member_count,
          models: pool.model_count,
        })}
        trailing={
          <Badge variant='outline' className='tabular-nums'>
            {pool.known_contract_count}/
            {pool.known_contract_count + pool.unknown_contract_count}
          </Badge>
        }
      />
    ))
  }

  let memberContent: ReactNode = (
    <CatalogPanelEmpty description={t('Select a pool first.')} />
  )
  if (selectedPool) {
    memberContent = members.data?.items.length ? (
      members.data.items.map((member) => (
        <CatalogChoice
          key={member.member_id}
          selected={member.member_id === selectedMember?.member_id}
          onClick={() => selectMember(member)}
          title={member.channel_name || `#${member.channel_id}`}
          description={
            <span className='flex flex-wrap gap-x-2 gap-y-0.5'>
              <span>#{member.channel_id}</span>
              <span>
                {member.model_count} {t('Models')}
              </span>
              <span>{format.cost(member.upstream_cost_multiplier)}×</span>
            </span>
          }
          trailing={
            <ChannelRoutingStatusBadge
              status={member.physical_status === 1 ? 'active' : 'disabled'}
            />
          }
        />
      ))
    ) : (
      <CatalogPanelEmpty
        description={t('No channels match the current search.')}
      />
    )
  }

  let modelContent: ReactNode = (
    <CatalogPanelEmpty description={t('Select a channel first.')} />
  )
  if (selectedMember) {
    modelContent = models.data?.items.length ? (
      models.data.items.map((model) => (
        <CatalogChoice
          key={model.model_name}
          selected={model.model_name === selectedModel?.model_name}
          onClick={() => selectModel(model)}
          title={model.model_name}
          description={
            model.configured_dimensions.length > 0
              ? model.configured_dimensions
                  .map((dimension) => t(routingCostDimensionLabel(dimension)))
                  .join(', ')
              : t('No explicit dimensions')
          }
          trailing={
            <ChannelRoutingStatusBadge
              status={model.known ? 'known' : 'unknown'}
            />
          }
        />
      ))
    ) : (
      <CatalogPanelEmpty
        description={t('No models match the current search.')}
      />
    )
  }

  return (
    <div className='flex flex-col gap-3'>
      <div className='flex flex-wrap items-center justify-between gap-2 border-y py-2 text-xs'>
        <p className='text-muted-foreground max-w-3xl'>
          {t(
            'Browse pricing by pool, channel lifecycle, and model. Missing dimensions are free; explicit zero rates are shown as free.'
          )}
        </p>
        <div className='flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 font-mono'>
          <span>
            {t('Pricing epoch')}:{' '}
            {pools.data?.pricing_epoch ?? t('Unavailable')}
          </span>
          <span className='text-muted-foreground'>{t('Pricing hash')}:</span>
          <ChannelRoutingIdentityText
            text={pools.data?.pricing_hash || t('Unavailable')}
            className='max-w-48'
            breakAll
          />
        </div>
      </div>

      {hasRefetchError ? (
        <ChannelRoutingRefetchErrorAlert
          isFetching={
            pools.isFetching ||
            members.isFetching ||
            models.isFetching ||
            detail.isFetching
          }
          onRetry={retryFailedQueries}
        />
      ) : null}

      <CostCatalogHierarchyLayout
        pools={
          <CatalogPanel
            title={t('Pools')}
            searchLabel={t('Search pools')}
            search={navigation.poolSearch}
            onSearch={(value) =>
              dispatchNavigation({ type: 'search-pools', value })
            }
            page={navigation.poolPage}
            total={pools.data?.total ?? 0}
            loading={pools.isLoading}
            error={pools.isError && !pools.data}
            onRetry={() => void pools.refetch()}
            onPageChange={(page) =>
              dispatchNavigation({ type: 'page-pools', page })
            }
          >
            {poolContent}
          </CatalogPanel>
        }
        members={
          <CatalogPanel
            title={t('Channels')}
            searchLabel={t('Search channels')}
            search={navigation.memberSearch}
            onSearch={(value) =>
              dispatchNavigation({ type: 'search-members', value })
            }
            page={navigation.memberPage}
            total={members.data?.total ?? 0}
            loading={members.isLoading && Boolean(selectedPool)}
            error={members.isError && !members.data}
            onRetry={() => void members.refetch()}
            onPageChange={(page) =>
              dispatchNavigation({ type: 'page-members', page })
            }
          >
            {memberContent}
          </CatalogPanel>
        }
        models={
          <CatalogPanel
            title={t('Models')}
            searchLabel={t('Search models')}
            search={navigation.modelSearch}
            onSearch={(value) =>
              dispatchNavigation({ type: 'search-models', value })
            }
            page={navigation.modelPage}
            total={models.data?.total ?? 0}
            loading={models.isLoading && Boolean(selectedMember)}
            error={models.isError && !models.data}
            onRetry={() => void models.refetch()}
            onPageChange={(page) =>
              dispatchNavigation({ type: 'page-models', page })
            }
          >
            {modelContent}
          </CatalogPanel>
        }
        detail={
          <section className='min-w-0'>
            <div className='border-b px-4 py-3'>
              <h3 className='text-sm font-semibold'>{t('Contract details')}</h3>
            </div>
            {detail.isError && !detail.data ? (
              <div className='p-3'>
                <Alert variant='destructive' role='alert'>
                  <HugeiconsIcon
                    icon={Alert02Icon}
                    strokeWidth={2}
                    aria-hidden='true'
                  />
                  <AlertTitle>{t('Pricing contract unavailable')}</AlertTitle>
                  <AlertDescription className='flex flex-col items-start gap-3'>
                    <span>{t('Unable to load this pricing contract.')}</span>
                    <Button
                      size='sm'
                      variant='outline'
                      onClick={() => void detail.refetch()}
                    >
                      {t('Retry')}
                    </Button>
                  </AlertDescription>
                </Alert>
              </div>
            ) : (
              <CostContractDetail
                member={selectedMember}
                model={selectedModel}
                detail={detail.data}
                loading={detail.isLoading}
              />
            )}
          </section>
        }
      />
    </div>
  )
}
