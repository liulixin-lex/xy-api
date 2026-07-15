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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { AxiosError } from 'axios'
import {
  Activity,
  ArrowRight,
  Clock3,
  Coins,
  Cpu,
  GitBranch,
  GitFork,
  Radar,
  RefreshCw,
  Route,
  ShieldCheck,
  TriangleAlert,
} from 'lucide-react'
import { useEffect, useMemo, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
} from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
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

import {
  createChannelRoutingIdempotencyKey,
  getChannelRoutingOperation,
  getChannelRoutingOverview,
  listChannelRoutingEndpoints,
  runChannelRoutingActiveProbe,
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
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import {
  channelRoutingOperationActiveProbeResult,
  channelRoutingOperationDisplayStatus,
  channelRoutingOperationIsActive,
} from '../lib/operations'
import type { ChannelRoutingActiveProbeResult } from '../types'
import { ChannelRoutingEndpointNetworkSection } from './endpoint-network-section'
import { ManualBillingReviewSummary } from './manual-billing-review-summary'

const overviewRefreshIntervalMs = 15_000
const overviewRefreshMaxBackoffMs = 120_000

function MetricCell(props: {
  icon: typeof Activity
  label: string
  value: ReactNode
  detail?: ReactNode
  className?: string
}) {
  const Icon = props.icon
  return (
    <div className={cn('min-w-0 px-3 py-3 sm:px-4', props.className)}>
      <div className='text-muted-foreground flex items-center gap-2 text-xs font-medium'>
        <Icon className='size-3.5' aria-hidden='true' />
        <span className='truncate'>{props.label}</span>
      </div>
      <div className='mt-2 min-w-0 text-xl font-semibold tabular-nums'>
        {props.value}
      </div>
      {props.detail ? (
        <div className='text-muted-foreground mt-1 truncate text-xs'>
          {props.detail}
        </div>
      ) : null}
    </div>
  )
}

function routingEventTitle(
  type: string,
  translate: (key: string) => string
): string {
  switch (type) {
    case 'routing.breaker.opened':
      return translate('Breaker opened')
    case 'routing.breaker.recovered':
      return translate('Breaker recovered')
    case 'routing.cost_sync.completed':
      return translate('Cost sync completed')
    case 'routing.policy.published':
      return translate('Policy published')
    case 'routing.policy.rolled_back':
      return translate('Policy rolled back')
    default:
      return type
  }
}

function routingEventDetail(
  payload: Record<string, unknown>,
  revision: number | undefined,
  translate: (key: string, options?: Record<string, unknown>) => string
): string {
  const values: string[] = []
  for (const key of [
    'group_name',
    'model_name',
    'endpoint_authority',
    'reason',
  ]) {
    const value = payload[key]
    if (typeof value === 'string' && value.trim()) values.push(value.trim())
  }
  if (typeof payload.operation_id === 'number') {
    values.push(translate('Operation #{{id}}', { id: payload.operation_id }))
  }
  if (values.length === 0 && revision) {
    values.push(translate('Revision r{{revision}}', { revision }))
  }
  return values.join(' · ') || translate('Routing state changed')
}

export function ChannelRoutingOverviewPage() {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const queryClient = useQueryClient()
  const user = useAuthStore((state) => state.auth.user)
  const canOperate = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )
  const canReadBillingReviews = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.BILLING_REVIEW,
    ADMIN_PERMISSION_ACTIONS.READ
  )
  const activeProbeKeyRef = useRef<string | null>(null)
  const notifiedActiveProbeRef = useRef<number | null>(null)
  const overviewQuery = useQuery({
    queryKey: channelRoutingQueryKeys.overview(),
    queryFn: getChannelRoutingOverview,
    refetchInterval: (query) => {
      if (
        query.state.error instanceof AxiosError &&
        [401, 403].includes(query.state.error.response?.status ?? 0)
      ) {
        return false
      }
      const failureCount = Math.min(query.state.fetchFailureCount, 3)
      return Math.min(
        overviewRefreshIntervalMs * 2 ** failureCount,
        overviewRefreshMaxBackoffMs
      )
    },
  })
  const endpointsQuery = useQuery({
    queryKey: channelRoutingQueryKeys.endpoints({ page: 1, page_size: 6 }),
    queryFn: () => listChannelRoutingEndpoints({ page: 1, page_size: 6 }),
  })
  const activeProbe = useMutation({
    mutationFn: () => {
      activeProbeKeyRef.current ??=
        createChannelRoutingIdempotencyKey('active-probe')
      return runChannelRoutingActiveProbe(activeProbeKeyRef.current)
    },
    onSuccess: (operation) => {
      queryClient.setQueryData(
        channelRoutingQueryKeys.operation(operation.id),
        operation
      )
      void queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.operationsRoot(),
      })
    },
    onError: () => {
      toast.error(t('Could not run the active probe. Try again.'))
    },
  })
  const activeProbeOperationId = activeProbe.data?.id ?? null
  const activeProbeOperationQuery = useQuery({
    queryKey: channelRoutingQueryKeys.operation(activeProbeOperationId ?? 0),
    queryFn: () =>
      getChannelRoutingOperation<ChannelRoutingActiveProbeResult>(
        activeProbeOperationId ?? 0
      ),
    enabled: activeProbeOperationId != null,
    refetchInterval: (query) =>
      channelRoutingOperationIsActive(query.state.data) ? 2_000 : false,
  })
  const trackedActiveProbe = activeProbeOperationQuery.data ?? activeProbe.data
  const activeProbeStatus = trackedActiveProbe
    ? channelRoutingOperationDisplayStatus(trackedActiveProbe)
    : ''
  const activeProbeResult = useMemo(
    () =>
      trackedActiveProbe
        ? channelRoutingOperationActiveProbeResult(trackedActiveProbe)
        : null,
    [trackedActiveProbe]
  )
  const activeProbeActive =
    activeProbe.isPending || channelRoutingOperationIsActive(trackedActiveProbe)
  const terminalActiveProbeId =
    trackedActiveProbe && !channelRoutingOperationIsActive(trackedActiveProbe)
      ? trackedActiveProbe.id
      : null

  useEffect(() => {
    if (
      terminalActiveProbeId == null ||
      notifiedActiveProbeRef.current === terminalActiveProbeId
    ) {
      return
    }
    notifiedActiveProbeRef.current = terminalActiveProbeId
    activeProbeKeyRef.current = null
    void Promise.all([
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.probesRoot(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.endpointsRoot(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.operationsRoot(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.overview(),
      }),
    ])

    if (activeProbeStatus === 'failed') {
      toast.error(t('The active probe operation failed before completion.'))
      return
    }
    if (activeProbeStatus === 'superseded') {
      toast.info(t('The active probe operation was superseded.'))
      return
    }
    if (!activeProbeResult) {
      toast.error(t('The active probe result could not be verified.'))
      return
    }
    if (!activeProbeResult.enabled) {
      toast.info(t('Active probes are disabled for the current routing mode.'))
      return
    }
    const message = t(
      'Active probe completed: {{succeeded}} succeeded, {{failed}} failed.',
      {
        succeeded: activeProbeResult.stats.succeeded,
        failed: activeProbeResult.stats.failed,
      }
    )
    if (activeProbeResult.stats.failed > 0) toast.warning(message)
    else toast.success(message)
  }, [
    activeProbeResult,
    activeProbeStatus,
    queryClient,
    t,
    terminalActiveProbeId,
  ])

  let activeProbeButtonLabel = t('Run active probe')
  if (activeProbe.isPending) {
    activeProbeButtonLabel = t('Queueing probe')
  } else if (activeProbeActive) {
    activeProbeButtonLabel = t('Probe in progress')
  }

  let activeProbeAlertTitle = t('Active probe')
  let activeProbeAlertDescription = ''
  if (activeProbeActive) {
    activeProbeAlertTitle = t('Active probe in progress')
    activeProbeAlertDescription = t(
      'The active probe operation is queued or running. Status updates automatically.'
    )
  } else if (activeProbeStatus === 'failed') {
    activeProbeAlertTitle = t('Active probe failed')
    activeProbeAlertDescription = t(
      'The active probe operation failed before completion.'
    )
  } else if (activeProbeStatus === 'superseded') {
    activeProbeAlertTitle = t('Active probe superseded')
    activeProbeAlertDescription = t(
      'The active probe operation was superseded.'
    )
  } else if (activeProbeResult?.enabled) {
    activeProbeAlertTitle = t('Active probe run completed')
    activeProbeAlertDescription = t(
      '{{executed}} targets checked: {{succeeded}} succeeded, {{failed}} failed.',
      {
        executed: activeProbeResult.stats.executed,
        succeeded: activeProbeResult.stats.succeeded,
        failed: activeProbeResult.stats.failed,
      }
    )
  } else if (activeProbeResult) {
    activeProbeAlertTitle = t('Active probes are disabled')
    activeProbeAlertDescription = t(
      'Enable active probes in a supported routing mode before running them manually.'
    )
  } else if (trackedActiveProbe) {
    activeProbeAlertTitle = t('Active probe result unavailable')
    activeProbeAlertDescription = t(
      'The active probe completed, but its result could not be verified.'
    )
  }

  if (overviewQuery.isLoading) {
    return (
      <ChannelRoutingPageFrame
        activeSection='overview'
        title={t('Channel Routing')}
      >
        <ChannelRoutingLoadingState rows={8} />
      </ChannelRoutingPageFrame>
    )
  }
  if (!overviewQuery.data) {
    return (
      <ChannelRoutingPageFrame
        activeSection='overview'
        title={t('Channel Routing')}
      >
        <ChannelRoutingErrorState
          error={overviewQuery.error}
          onRetry={() => void overviewQuery.refetch()}
        />
      </ChannelRoutingPageFrame>
    )
  }

  const overview = overviewQuery.data
  if (!overview.snapshot_available) {
    return (
      <ChannelRoutingPageFrame
        activeSection='overview'
        title={
          <span className='inline-flex min-w-0 items-center gap-2'>
            <span className='truncate'>{t('Channel Routing')}</span>
            <ChannelRoutingStatusBadge status='initializing' />
          </span>
        }
      >
        <ChannelRoutingEmptyState
          title={t('Routing snapshot is initializing')}
          description={t(
            'The control plane is available, but this node has not loaded a routing snapshot yet.'
          )}
        />
      </ChannelRoutingPageFrame>
    )
  }
  const attemptMetrics = overview.attempt_metrics
  const preCommitFailover = attemptMetrics.pre_commit_failover_success_rate
  const unitPlatformCost = attemptMetrics.unit_request_platform_cost
  const attemptMetricsReliable =
    overview.attempt_metrics_available && !overview.attempt_metrics_degraded
  const preCommitFailoverKnown =
    attemptMetricsReliable && preCommitFailover.known
  const unitPlatformCostKnown =
    attemptMetricsReliable &&
    unitPlatformCost.known &&
    unitPlatformCost.dimension_consistent
  const riskGroups = overview.risk_groups_available ? overview.risk_groups : []
  const recentEvents = overview.recent_events_available
    ? overview.recent_events
    : []
  let attemptAuditStatus = 'healthy'
  let attemptAuditStatusLabel = t('Healthy')
  if (!overview.attempt_metrics_available) {
    attemptAuditStatus = 'unavailable'
    attemptAuditStatusLabel = t('Unavailable')
  } else if (overview.attempt_metrics_degraded) {
    attemptAuditStatus = 'degraded'
    attemptAuditStatusLabel = t('Degraded')
  }

  return (
    <ChannelRoutingPageFrame
      activeSection='overview'
      title={
        <span className='flex min-w-0 flex-wrap items-center gap-2'>
          <span className='whitespace-normal'>{t('Channel Routing')}</span>
          <ChannelRoutingStatusBadge status={overview.deployment_stage} />
        </span>
      }
      actions={
        <div className='flex items-center gap-2'>
          {canOperate ? (
            <Button
              size='sm'
              aria-label={activeProbeButtonLabel}
              className='max-sm:size-11 max-sm:p-0'
              disabled={activeProbeActive}
              onClick={() => activeProbe.mutate()}
            >
              <Radar
                aria-hidden='true'
                className={
                  activeProbeActive
                    ? 'animate-pulse motion-reduce:animate-none'
                    : undefined
                }
              />
              <span className='max-sm:hidden'>{activeProbeButtonLabel}</span>
            </Button>
          ) : null}
          <Button
            size='sm'
            variant='outline'
            aria-label={t('Policies')}
            title={t('Policies')}
            className='max-sm:size-11 max-sm:p-0'
            render={
              <Link
                to='/channel-routing/$section'
                params={{ section: 'policies' }}
              />
            }
          >
            <ShieldCheck aria-hidden='true' />
            <span className='max-sm:hidden'>{t('Policies')}</span>
          </Button>
        </div>
      }
    >
      <div className='space-y-5 pb-2'>
        {trackedActiveProbe ? (
          <Alert
            role={activeProbeStatus === 'failed' ? 'alert' : 'status'}
            variant={activeProbeStatus === 'failed' ? 'destructive' : 'default'}
          >
            {activeProbeStatus === 'failed' ? (
              <TriangleAlert aria-hidden='true' />
            ) : (
              <Radar aria-hidden='true' />
            )}
            <AlertTitle className='flex flex-wrap items-center gap-2'>
              <span>{activeProbeAlertTitle}</span>
              <ChannelRoutingStatusBadge status={activeProbeStatus} />
            </AlertTitle>
            <AlertDescription>{activeProbeAlertDescription}</AlertDescription>
            {activeProbeOperationQuery.isError ? (
              <AlertAction>
                <Button
                  size='sm'
                  variant='outline'
                  onClick={() => void activeProbeOperationQuery.refetch()}
                >
                  <RefreshCw aria-hidden='true' />
                  {t('Retry status')}
                </Button>
              </AlertAction>
            ) : null}
          </Alert>
        ) : null}
        {activeProbe.isError && !trackedActiveProbe ? (
          <Alert role='alert' variant='destructive'>
            <TriangleAlert aria-hidden='true' />
            <AlertTitle>{t('Could not run the active probe')}</AlertTitle>
            <AlertDescription>
              {t(
                'The request was not confirmed. Retrying reuses the same operation key.'
              )}
            </AlertDescription>
            <AlertAction>
              <Button
                size='sm'
                variant='outline'
                disabled={activeProbe.isPending}
                onClick={() => activeProbe.mutate()}
              >
                <RefreshCw aria-hidden='true' />
                {t('Retry')}
              </Button>
            </AlertAction>
          </Alert>
        ) : null}
        {overviewQuery.isRefetchError ? (
          <ChannelRoutingRefetchErrorAlert
            title={t('Live refresh is temporarily unavailable')}
            description={t(
              'Showing the last successful routing snapshot. Automatic refresh will retry with backoff.'
            )}
            isFetching={overviewQuery.isFetching}
            onRetry={() => void overviewQuery.refetch()}
          />
        ) : null}
        <ManualBillingReviewSummary enabled={canReadBillingReviews} />
        <section
          className='grid overflow-hidden rounded-lg border sm:grid-cols-2 lg:grid-cols-3 [&>*]:border-r [&>*]:border-b [&>*:nth-child(2n)]:border-r-0 sm:[&>*:nth-child(2n)]:border-r lg:[&>*:nth-child(3n)]:border-r-0 [&>*:nth-last-child(-n+2)]:border-b-0 lg:[&>*:nth-last-child(-n+3)]:border-b-0'
          aria-label={t('Routing health summary')}
        >
          <MetricCell
            icon={GitBranch}
            label={t('Policy revision')}
            value={`r${overview.control_plane_revision || overview.snapshot_revision}`}
            detail={format.shortHash(overview.policy_hash)}
          />
          <MetricCell
            icon={Route}
            label={t('Configuration propagation')}
            value={
              <ChannelRoutingStatusBadge status={overview.propagation_status} />
            }
            detail={
              overview.revision_lag > 0
                ? t('{{count}} revisions behind', {
                    count: overview.revision_lag,
                  })
                : t('Snapshot r{{revision}}', {
                    revision: overview.snapshot_revision,
                  })
            }
          />
          <MetricCell
            icon={Activity}
            label={t('Logical success rate')}
            value={format.percent(overview.telemetry.logical_success_rate)}
            detail={t('{{count}} observed requests', {
              count: format.compact(overview.telemetry.observed_requests),
            })}
          />
          <MetricCell
            icon={Clock3}
            label={t('p95 TTFT')}
            value={format.milliseconds(overview.telemetry.p95_ttft_ms)}
            detail={
              overview.telemetry.p95_ttft_status === 'available'
                ? t('Distribution available')
                : t('Distribution coverage incomplete')
            }
          />
          <MetricCell
            icon={GitFork}
            label={t('Pre-commit failover success')}
            value={format.percent(
              preCommitFailoverKnown ? preCommitFailover.rate : undefined
            )}
            detail={
              preCommitFailoverKnown
                ? t('{{recovered}} of {{total}} recovered', {
                    recovered: preCommitFailover.numerator,
                    total: preCommitFailover.denominator,
                  })
                : t('24-hour attempt evidence incomplete')
            }
          />
          <MetricCell
            icon={Coins}
            label={t('Platform cost per request')}
            value={
              unitPlatformCostKnown
                ? t('{{currency}} {{cost}}', {
                    currency: unitPlatformCost.currency || '',
                    cost: format.cost(unitPlatformCost.value ?? Number.NaN),
                  })
                : t('Unknown')
            }
            detail={
              unitPlatformCostKnown
                ? t('{{count}} requests · {{coverage}} coverage', {
                    count: unitPlatformCost.request_count,
                    coverage: format.percent(unitPlatformCost.coverage),
                  })
                : t('Cost dimensions or attempt coverage are incomplete')
            }
          />
        </section>

        <section aria-labelledby='routing-coverage-title'>
          <div className='mb-2 flex items-center justify-between gap-3'>
            <div>
              <h2 id='routing-coverage-title' className='text-sm font-semibold'>
                {t('Snapshot coverage')}
              </h2>
              <p className='text-muted-foreground mt-0.5 text-xs'>
                {t('Built {{time}} on node {{node}}', {
                  time: format.timestamp(overview.snapshot_built_at),
                  node: overview.node_epoch_id,
                })}
              </p>
            </div>
            <span className='text-sm font-semibold tabular-nums'>
              {format.percent(overview.telemetry.coverage)}
            </span>
          </div>
          <Progress
            aria-label={t('Snapshot coverage')}
            value={overview.telemetry.coverage * 100}
          />
          <div className='text-muted-foreground mt-2 flex flex-wrap gap-x-5 gap-y-1 text-xs'>
            <span>
              {t('{{count}} groups', { count: overview.topology.pools })}
            </span>
            <span>
              {t('{{count}} members', { count: overview.topology.members })}
            </span>
            <span>
              {t('{{count}} channels', { count: overview.topology.channels })}
            </span>
            <span>
              {t('{{count}} credentials', {
                count: overview.topology.credentials,
              })}
            </span>
          </div>
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
        />

        <div className='grid gap-5 xl:grid-cols-[minmax(0,1.35fr)_minmax(320px,0.65fr)]'>
          <section className='min-w-0' aria-labelledby='risk-groups-title'>
            <div className='mb-2 flex items-center justify-between gap-3'>
              <h2 id='risk-groups-title' className='text-sm font-semibold'>
                {t('Risk groups')}
              </h2>
              <div className='flex items-center gap-2'>
                {overview.risk_groups_truncated ? (
                  <ChannelRoutingStatusBadge
                    status='warning'
                    label={t('Highest-risk groups')}
                  />
                ) : null}
                <Button
                  size='sm'
                  variant='ghost'
                  render={
                    <Link
                      to='/channel-routing/$section'
                      params={{ section: 'groups' }}
                    />
                  }
                >
                  {t('View all')}
                  <ArrowRight aria-hidden='true' />
                </Button>
              </div>
            </div>
            {!overview.risk_groups_available ? (
              <ChannelRoutingEmptyState
                title={t('Risk group summary unavailable')}
                description={t(
                  'The routing snapshot cannot provide a risk-ranked group summary yet.'
                )}
              />
            ) : null}
            {overview.risk_groups_available && riskGroups.length === 0 ? (
              <ChannelRoutingEmptyState
                title={t('No elevated-risk groups')}
                description={t(
                  'No routing groups currently meet the server risk criteria.'
                )}
              />
            ) : null}
            {overview.risk_groups_available && riskGroups.length > 0 ? (
              <>
                <div className='hidden overflow-hidden rounded-lg border md:block'>
                  <Table scrollAreaLabel={t('Risk groups')}>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{t('Group')}</TableHead>
                        <TableHead>{t('Stage')}</TableHead>
                        <TableHead className='text-right'>
                          {t('Telemetry')}
                        </TableHead>
                        <TableHead className='text-right'>
                          {t('Open')}
                        </TableHead>
                        <TableHead className='text-right'>
                          {t('Degraded')}
                        </TableHead>
                        <TableHead className='text-right'>
                          {t('Unknown cost')}
                        </TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {riskGroups.map((group) => (
                        <TableRow key={group.id}>
                          <TableCell>
                            <Link
                              className='font-medium hover:underline'
                              to='/channel-routing/groups/$id'
                              params={{ id: String(group.id) }}
                            >
                              {group.display_name || group.group_name}
                            </Link>
                            <div className='text-muted-foreground text-xs'>
                              {group.group_name}
                            </div>
                          </TableCell>
                          <TableCell>
                            <ChannelRoutingStatusBadge
                              status={group.deployment_stage}
                            />
                          </TableCell>
                          <TableCell className='text-right'>
                            {format.percent(group.telemetry_coverage)}
                          </TableCell>
                          <TableCell className='text-right'>
                            {group.open_models}
                          </TableCell>
                          <TableCell className='text-right'>
                            {group.degraded_models}
                          </TableCell>
                          <TableCell className='text-right'>
                            {group.unknown_cost_models}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
                <div className='divide-y rounded-lg border md:hidden'>
                  {riskGroups.map((group) => (
                    <Link
                      key={group.id}
                      to='/channel-routing/groups/$id'
                      params={{ id: String(group.id) }}
                      className='hover:bg-muted/50 block min-w-0 p-3 transition-colors'
                    >
                      <div className='flex min-w-0 items-start justify-between gap-3'>
                        <div className='min-w-0'>
                          <ChannelRoutingIdentityText
                            text={group.display_name || group.group_name}
                            className='text-sm font-medium'
                            withinInteractive
                          />
                          <div className='text-muted-foreground truncate text-xs'>
                            {group.group_name}
                          </div>
                        </div>
                        <ChannelRoutingStatusBadge
                          status={group.deployment_stage}
                        />
                      </div>
                      <dl className='mt-3 grid grid-cols-2 gap-3 text-xs sm:grid-cols-4'>
                        <div>
                          <dt className='text-muted-foreground'>
                            {t('Telemetry')}
                          </dt>
                          <dd className='mt-1 font-medium tabular-nums'>
                            {format.percent(group.telemetry_coverage)}
                          </dd>
                        </div>
                        <div>
                          <dt className='text-muted-foreground'>{t('Open')}</dt>
                          <dd className='mt-1 font-medium tabular-nums'>
                            {group.open_models}
                          </dd>
                        </div>
                        <div>
                          <dt className='text-muted-foreground'>
                            {t('Degraded')}
                          </dt>
                          <dd className='mt-1 font-medium tabular-nums'>
                            {group.degraded_models}
                          </dd>
                        </div>
                        <div>
                          <dt className='text-muted-foreground'>
                            {t('Unknown cost')}
                          </dt>
                          <dd className='mt-1 font-medium tabular-nums'>
                            {group.unknown_cost_models}
                          </dd>
                        </div>
                      </dl>
                    </Link>
                  ))}
                </div>
              </>
            ) : null}
          </section>

          <section className='min-w-0' aria-labelledby='recent-events-title'>
            <h2 id='recent-events-title' className='mb-2 text-sm font-semibold'>
              {t('Recent routing events')}
            </h2>
            {!overview.recent_events_available ? (
              <ChannelRoutingEmptyState
                title={t('Recent events unavailable')}
                description={t(
                  'The local event buffer cannot provide recent routing events.'
                )}
              />
            ) : null}
            {overview.recent_events_available && recentEvents.length === 0 ? (
              <ChannelRoutingEmptyState
                title={t('No recent routing events')}
                description={t(
                  'No breaker, cost, or policy events are present in the local event buffer.'
                )}
              />
            ) : null}
            {overview.recent_events_available && recentEvents.length > 0 ? (
              <div
                role='log'
                aria-labelledby='recent-events-title'
                aria-relevant='additions'
                aria-busy={overviewQuery.isFetching}
                tabIndex={0}
                className='focus-visible:ring-ring max-h-[min(28rem,55dvh)] [scrollbar-gutter:stable] overflow-y-auto overscroll-contain rounded-lg border focus-visible:ring-2 focus-visible:outline-none focus-visible:ring-inset'
              >
                <ol className='divide-y'>
                  {recentEvents.map((event) => (
                    <li
                      key={event.id}
                      className='grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-start gap-x-3 gap-y-1 px-3 py-3 sm:grid-cols-[auto_minmax(0,1fr)_auto]'
                    >
                      <Activity
                        className='text-muted-foreground mt-0.5 size-4 shrink-0'
                        aria-hidden='true'
                      />
                      <div className='min-w-0'>
                        <div className='text-sm font-medium [overflow-wrap:anywhere]'>
                          {routingEventTitle(event.type, t)}
                        </div>
                        <div className='text-muted-foreground mt-0.5 text-xs [overflow-wrap:anywhere]'>
                          {routingEventDetail(event.payload, event.revision, t)}
                        </div>
                      </div>
                      <time
                        dateTime={new Date(event.created_time_ms).toISOString()}
                        className='text-muted-foreground col-start-2 text-xs whitespace-nowrap sm:col-start-3 sm:row-start-1'
                      >
                        {format.timestamp(event.created_time_ms)}
                      </time>
                    </li>
                  ))}
                </ol>
              </div>
            ) : null}
          </section>
        </div>

        <section className='border-t pt-4' aria-labelledby='runtime-title'>
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <div>
              <h2 id='runtime-title' className='text-sm font-semibold'>
                {t('Runtime health')}
              </h2>
              <p className='text-muted-foreground mt-0.5 text-xs'>
                {overview.snapshot_stale
                  ? t('The local routing snapshot is stale.')
                  : t('The local routing snapshot is current.')}
              </p>
            </div>
            <div className='flex flex-wrap items-center gap-2'>
              <ChannelRoutingStatusBadge
                status={overview.telemetry.status}
                label={t('Telemetry: {{status}}', {
                  status: overview.telemetry.status,
                })}
              />
              <ChannelRoutingStatusBadge
                status={overview.snapshot_stale ? 'stale' : 'converged'}
                label={t('Snapshot age: {{seconds}}s', {
                  seconds: overview.snapshot_age_sec,
                })}
              />
              <ChannelRoutingStatusBadge
                status={overview.enabled ? 'enabled' : 'disabled'}
                label={overview.enabled ? t('Enabled') : t('Disabled')}
              />
              <ChannelRoutingStatusBadge
                status={attemptAuditStatus}
                label={t('Attempt audit: {{status}}', {
                  status: attemptAuditStatusLabel,
                })}
              />
            </div>
          </div>
          <div className='text-muted-foreground mt-3 flex items-center gap-2 text-xs'>
            <Cpu className='size-3.5' aria-hidden='true' />
            <span>
              {t('Runtime generation {{generation}}', {
                generation: overview.runtime_generation,
              })}
            </span>
          </div>
          <div className='text-muted-foreground mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs'>
            <span>
              {t('Attempt coverage')}:{' '}
              {format.percent(overview.attempt_metrics_coverage)}
            </span>
            <span>
              {t('Pending attempts')}:{' '}
              {format.number(overview.attempt_metrics_pipeline.entries)}
            </span>
            <span>
              {t('Persist failures')}:{' '}
              {format.number(
                overview.attempt_metrics_pipeline.persist_failures
              )}
            </span>
            <span>
              {t('Current consecutive persist failures')}:{' '}
              {format.number(
                overview.attempt_metrics_pipeline.consecutive_persist_failures
              )}
            </span>
            <span>
              {t('Rejected attempts')}:{' '}
              {format.number(overview.attempt_metrics_pipeline.rejected)}
            </span>
          </div>
        </section>
      </div>
    </ChannelRoutingPageFrame>
  )
}
