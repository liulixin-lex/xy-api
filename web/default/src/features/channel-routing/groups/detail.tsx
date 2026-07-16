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
  FlaskConicalIcon,
  Key01Icon,
  Layers01Icon,
  RefreshIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import { getRouteApi, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@/components/ui/breadcrumb'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { CHANNEL_STATUS_LABELS } from '@/features/channels/constants'
import { getChannelTypeLabel } from '@/features/channels/lib/channel-utils'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import { getChannelRoutingGroup } from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingBreakerResetDialog } from '../components/breaker-reset-dialog'
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
import type { ModelSnapshot, PoolMemberSnapshot } from '../types'
import { ChannelRoutingErrorBudgetSection } from './error-budget-section'
import { ChannelRoutingGroupPolicySummary } from './policy-summary'
import { ChannelRoutingGroupReplayRankingSection } from './replay-ranking-section'
import { ChannelRoutingSimulationSheet } from './simulation-sheet'

const route = getRouteApi('/_authenticated/channel-routing/groups/$id')

type PoolMemberRoutingWeight = PoolMemberSnapshot & {
  automatic_traffic_paused?: boolean
  normalized_weight?: number
}

function ChannelRoutingMemberWeight(props: {
  member: PoolMemberSnapshot
  align?: 'start' | 'end'
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const routingWeight = props.member as PoolMemberRoutingWeight
  const automaticTrafficPaused =
    routingWeight.automatic_traffic_paused || props.member.legacy_weight === 0

  return (
    <div className={props.align === 'end' ? 'text-right' : undefined}>
      <div className='font-medium tabular-nums'>
        {props.member.legacy_priority} / {props.member.legacy_weight}
      </div>
      {automaticTrafficPaused ? (
        <div
          className={
            props.align === 'end'
              ? 'mt-1 flex justify-end'
              : 'mt-1 flex justify-start'
          }
        >
          <ChannelRoutingStatusBadge
            status='paused'
            label={t('Paused for automatic traffic')}
          />
        </div>
      ) : null}
      {!automaticTrafficPaused && routingWeight.normalized_weight != null ? (
        <div className='text-muted-foreground mt-1 text-xs'>
          {t('Normalized {{value}}', {
            value: format.percent(routingWeight.normalized_weight),
          })}
        </div>
      ) : null}
    </div>
  )
}

function ChannelRoutingModelMetrics(props: {
  poolId: number
  memberId: number
  models: ModelSnapshot[]
  canOperate: boolean
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()

  if (props.models.length === 0) return null

  return (
    <details className='mt-2'>
      <summary className='text-primary flex min-h-11 cursor-pointer items-center text-xs font-medium md:min-h-0'>
        {t('View model metrics')}
      </summary>
      <div className='mt-2 space-y-2 whitespace-normal'>
        {props.models.map((model) => {
          const successRate =
            model.request_count > 0
              ? model.success_count / model.request_count
              : undefined
          return (
            <div key={model.model_name} className='border-t pt-2 text-xs'>
              <div className='flex flex-wrap items-center gap-2'>
                <span className='font-medium break-words'>
                  {model.model_name}
                </span>
                <span className='text-muted-foreground'>
                  {t('Provider reliability')}
                </span>
                <ChannelRoutingStatusBadge
                  status={
                    model.breaker_known
                      ? model.breaker_state || 'unknown'
                      : 'unknown'
                  }
                />
                {props.canOperate && model.breaker_known ? (
                  <ChannelRoutingBreakerResetDialog
                    compact
                    request={{
                      scope: 'member',
                      pool_id: props.poolId,
                      member_id: props.memberId,
                      model_name: model.model_name,
                    }}
                    targetLabel={`${model.model_name} · #${props.memberId}`}
                  />
                ) : null}
              </div>
              <div className='text-muted-foreground mt-1 flex flex-wrap gap-x-3 gap-y-1'>
                <span>
                  {t('Success')}: {format.percent(successRate)}
                </span>
                <span>
                  {t('p95 TTFT')}:{' '}
                  {format.milliseconds(
                    model.p95_ttft_known ? model.p95_ttft_ms : undefined
                  )}
                </span>
                <span>
                  {t('Token/s')}:{' '}
                  {format.number(model.output_tokens_per_second)}
                </span>
                <span>
                  {t('Inflight')}: {model.inflight}
                </span>
              </div>
              {model.breaker_reason || model.breaker_cooldown_until > 0 ? (
                <div className='text-muted-foreground mt-1 flex flex-wrap gap-x-3 gap-y-1'>
                  {model.breaker_reason ? (
                    <span className='break-words'>{model.breaker_reason}</span>
                  ) : null}
                  {model.breaker_cooldown_until > 0 ? (
                    <span>
                      {t('Cooldown until {{time}}', {
                        time: format.timestamp(model.breaker_cooldown_until),
                      })}
                    </span>
                  ) : null}
                </div>
              ) : null}
            </div>
          )
        })}
      </div>
    </details>
  )
}

export function ChannelRoutingGroupDetailPage() {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const params = route.useParams()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const user = useAuthStore((state) => state.auth.user)
  const canOperate = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )
  const [simulationOpen, setSimulationOpen] = useState(false)
  const poolId = Number(params.id)
  const page = search.page ?? 1
  const pageSize = search.pageSize ?? 20
  const modelLimit = search.modelLimit ?? 20
  const credentialLimit = search.credentialLimit ?? 20
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.group(poolId, {
      page,
      page_size: pageSize,
      model_limit: modelLimit,
      credential_limit: credentialLimit,
    }),
    queryFn: () =>
      getChannelRoutingGroup(poolId, {
        page,
        page_size: pageSize,
        model_limit: modelLimit,
        credential_limit: credentialLimit,
      }),
  })

  const breadcrumb = (
    <Breadcrumb>
      <BreadcrumbList>
        <BreadcrumbItem>
          <BreadcrumbLink
            render={
              <Link
                to='/channel-routing/$section'
                params={{ section: 'groups' }}
              />
            }
          >
            {t('Routing groups')}
          </BreadcrumbLink>
        </BreadcrumbItem>
        <BreadcrumbSeparator />
        <BreadcrumbItem>
          <BreadcrumbPage>
            {query.data?.summary.display_name || `#${poolId}`}
          </BreadcrumbPage>
        </BreadcrumbItem>
      </BreadcrumbList>
    </Breadcrumb>
  )

  if (query.isLoading) {
    return (
      <ChannelRoutingPageFrame
        activeSection='groups'
        title={t('Routing group')}
        breadcrumb={breadcrumb}
      >
        <ChannelRoutingLoadingState rows={8} />
      </ChannelRoutingPageFrame>
    )
  }
  if (!query.data) {
    return (
      <ChannelRoutingPageFrame
        activeSection='groups'
        title={t('Routing group')}
        breadcrumb={breadcrumb}
      >
        <ChannelRoutingErrorState
          error={query.error}
          onRetry={() => void query.refetch()}
        />
      </ChannelRoutingPageFrame>
    )
  }

  const data = query.data
  const group = data.group
  const summary = data.summary
  const operationsAvailable = canOperate && !query.isRefetchError
  const policyProfile =
    summary.policy_profile === 'enterprise_slo'
      ? t('Enterprise SLO')
      : summary.policy_profile

  return (
    <>
      <ChannelRoutingPageFrame
        activeSection='groups'
        breadcrumb={breadcrumb}
        title={
          <span className='flex min-w-0 flex-wrap items-center gap-2'>
            <span className='min-w-0 break-words'>
              {summary.display_name || summary.group_name}
            </span>
            <ChannelRoutingStatusBadge status={summary.deployment_stage} />
          </span>
        }
        actions={
          <div className='flex items-center gap-2'>
            <Button
              size='icon-sm'
              variant='outline'
              className='max-sm:size-11'
              aria-label={t('Refresh')}
              disabled={query.isFetching}
              onClick={() => void query.refetch()}
            >
              <HugeiconsIcon
                icon={RefreshIcon}
                aria-hidden='true'
                className={
                  query.isFetching
                    ? 'animate-spin motion-reduce:animate-none'
                    : undefined
                }
              />
            </Button>
            {operationsAvailable ? (
              <Button
                size='sm'
                className='max-sm:size-11 max-sm:p-0'
                aria-label={t('Simulate')}
                title={t('Simulate')}
                onClick={() => setSimulationOpen(true)}
              >
                <HugeiconsIcon
                  icon={FlaskConicalIcon}
                  data-icon='inline-start'
                  aria-hidden='true'
                />
                <span className='max-sm:hidden'>{t('Simulate')}</span>
              </Button>
            ) : null}
          </div>
        }
      >
        <div className='space-y-4 pb-2'>
          {query.isRefetchError ? (
            <ChannelRoutingRefetchErrorAlert
              isFetching={query.isFetching}
              onRetry={() => void query.refetch()}
            />
          ) : null}

          <section className='bg-border grid grid-cols-2 gap-px overflow-hidden rounded-lg border lg:grid-cols-6'>
            {[
              [t('Policy profile'), policyProfile],
              [t('Members'), format.number(summary.member_count)],
              [t('Enabled channels'), format.number(summary.enabled_channels)],
              [t('Telemetry'), format.percent(summary.telemetry_coverage)],
              [t('Open models'), format.number(summary.open_models)],
              [t('Degraded models'), format.number(summary.degraded_models)],
            ].map(([label, value]) => (
              <div key={label} className='bg-background min-w-0 p-3'>
                <div className='text-muted-foreground truncate text-xs'>
                  {label}
                </div>
                <div className='mt-1 text-base font-semibold break-words'>
                  {value}
                </div>
              </div>
            ))}
          </section>

          <ChannelRoutingGroupPolicySummary group={group} />

          {summary.policy_profile === 'enterprise_slo' ? (
            <ChannelRoutingErrorBudgetSection
              poolId={poolId}
              snapshotRevision={data.snapshot_revision}
            />
          ) : null}

          <ChannelRoutingGroupReplayRankingSection
            poolId={poolId}
            canOperate={operationsAvailable}
            onSimulate={() => setSimulationOpen(true)}
          />

          {group.members.length === 0 ? (
            <ChannelRoutingEmptyState
              title={t('No channels')}
              description={t(
                'This routing group has no members in the current snapshot.'
              )}
            />
          ) : (
            <>
              <div className='hidden overflow-hidden rounded-lg border md:block'>
                <Table scrollAreaLabel={t('Routing group channels table')}>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t('Channel')}</TableHead>
                      <TableHead>{t('Physical status')}</TableHead>
                      <TableHead className='text-right'>
                        {t('Priority / weight')}
                      </TableHead>
                      <TableHead className='text-right'>
                        {t('Credentials')}
                      </TableHead>
                      <TableHead className='text-right'>
                        {t('Models')}
                      </TableHead>
                      <TableHead>{t('Telemetry')}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {group.members.map((member) => {
                      const statusLabel =
                        CHANNEL_STATUS_LABELS[
                          member.physical_status as keyof typeof CHANNEL_STATUS_LABELS
                        ] ?? 'Unknown'
                      return (
                        <TableRow key={member.id}>
                          <TableCell className='max-w-72 align-top'>
                            <div className='truncate font-medium'>
                              {member.channel_name || `#${member.channel_id}`}
                            </div>
                            <div className='text-muted-foreground text-xs'>
                              #{member.channel_id} ·{' '}
                              {getChannelTypeLabel(member.channel_type)}
                            </div>
                            <ChannelRoutingModelMetrics
                              poolId={poolId}
                              memberId={member.id}
                              models={member.models}
                              canOperate={operationsAvailable}
                            />
                          </TableCell>
                          <TableCell className='align-top'>
                            <ChannelRoutingStatusBadge
                              status={member.physical_status}
                              label={t(statusLabel)}
                            />
                          </TableCell>
                          <TableCell className='text-right align-top'>
                            <ChannelRoutingMemberWeight
                              member={member}
                              align='end'
                            />
                          </TableCell>
                          <TableCell className='text-right align-top'>
                            <span className='inline-flex items-center gap-1'>
                              <HugeiconsIcon
                                icon={Key01Icon}
                                className='text-muted-foreground size-3.5'
                                aria-hidden='true'
                              />
                              {member.credential_count}
                            </span>
                          </TableCell>
                          <TableCell className='text-right align-top'>
                            <span className='inline-flex items-center gap-1'>
                              <HugeiconsIcon
                                icon={Layers01Icon}
                                className='text-muted-foreground size-3.5'
                                aria-hidden='true'
                              />
                              {member.model_count}
                            </span>
                          </TableCell>
                          <TableCell className='align-top'>
                            <ChannelRoutingStatusBadge
                              status={
                                member.telemetry_known ? 'known' : 'unknown'
                              }
                            />
                          </TableCell>
                        </TableRow>
                      )
                    })}
                  </TableBody>
                </Table>
              </div>

              <div className='divide-y rounded-lg border md:hidden'>
                {group.members.map((member) => (
                  <div key={member.id} className='p-3'>
                    <div className='flex items-start justify-between gap-3'>
                      <div className='min-w-0'>
                        <ChannelRoutingIdentityText
                          text={member.channel_name || `#${member.channel_id}`}
                          className='text-sm font-medium'
                        />
                        <div className='text-muted-foreground truncate text-xs'>
                          {getChannelTypeLabel(member.channel_type)} · #
                          {member.channel_id}
                        </div>
                      </div>
                      <ChannelRoutingStatusBadge
                        status={member.telemetry_known ? 'known' : 'unknown'}
                      />
                    </div>
                    <dl className='mt-3 grid grid-cols-3 gap-3 text-xs'>
                      <div>
                        <dt className='text-muted-foreground'>
                          {t('Priority / weight')}
                        </dt>
                        <dd className='mt-1'>
                          <ChannelRoutingMemberWeight member={member} />
                        </dd>
                      </div>
                      <div>
                        <dt className='text-muted-foreground'>
                          {t('Credentials')}
                        </dt>
                        <dd className='mt-1 font-medium'>
                          {member.credential_count}
                        </dd>
                      </div>
                      <div>
                        <dt className='text-muted-foreground'>{t('Models')}</dt>
                        <dd className='mt-1 font-medium'>
                          {member.model_count}
                        </dd>
                      </div>
                    </dl>
                    <ChannelRoutingModelMetrics
                      poolId={poolId}
                      memberId={member.id}
                      models={member.models}
                      canOperate={operationsAvailable}
                    />
                  </div>
                ))}
              </div>
            </>
          )}

          <ChannelRoutingPagination
            page={page}
            pageSize={pageSize}
            total={group.member_count}
            disabled={query.isRefetchError}
            onPageChange={(nextPage) =>
              void navigate({
                search: (previous) => ({ ...previous, page: nextPage }),
                replace: true,
              })
            }
            onPageSizeChange={(nextSize) =>
              void navigate({
                search: (previous) => ({
                  ...previous,
                  page: 1,
                  pageSize: nextSize,
                }),
                replace: true,
              })
            }
          />
          <div className='text-muted-foreground text-xs'>
            {t('Snapshot r{{revision}} · built {{time}}', {
              revision: data.snapshot_revision,
              time: format.timestamp(data.snapshot_built_at),
            })}
          </div>
        </div>
      </ChannelRoutingPageFrame>

      <ChannelRoutingSimulationSheet
        poolId={poolId}
        open={simulationOpen}
        onOpenChange={setSimulationOpen}
      />
    </>
  )
}
