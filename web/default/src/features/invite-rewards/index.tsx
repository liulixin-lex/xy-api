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
  CheckCircle2,
  ChevronsUpDown,
  Clock,
  EyeOff,
  Gift,
  Link2,
  Search,
  Users,
} from 'lucide-react'
import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ComponentType,
} from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { CopyButton } from '@/components/copy-button'
import { DataTableManualViewOptions } from '@/components/data-table'
import { SectionPageLayout } from '@/components/layout'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { CompactDateTimeRangePicker } from '@/features/usage-logs/components/compact-date-time-range-picker'
import { transferAffiliateQuota } from '@/features/wallet/api'
import { TransferDialog } from '@/features/wallet/components/dialogs/transfer-dialog'
import { useTopupInfo } from '@/features/wallet/hooks'
import { formatQuota, formatTimestamp } from '@/lib/format'
import { cn } from '@/lib/utils'

import { getInvitedUsers, getReferralRewards } from './api'
import {
  formatShanghaiTimestamp,
  renderTrustedActivityDescription,
} from './lib/activity-description'
import { formatActivityDetailLabel } from './lib/activity-label'
import {
  buildInviteSequenceMap,
  formatInitialQuotaSummary,
  formatRewardRateSummary,
  getInitialQuotaActivities,
  getInitialQuotaSortValue,
  getInitialQuotaTotal,
  getPendingRewardQuotaSortValue,
  getRewardActivities,
  getRewardRateSortValue,
  type RewardRateSource,
} from './lib/reward-display'
import type {
  InviteLinkBatch,
  InvitedUser,
  ReferralRewardDashboard,
  RewardActivity,
} from './types'

type PresetActivityDescription = {
  title: string
  summary: string
  details: Array<{
    title: string
    first_topup_reward_percent?: number
    continuous_reward_percent?: number
  }>
}

type RewardMetricProps = {
  label: string
  value: string
  loading: boolean
  icon: ComponentType<{ className?: string }>
}

type ReferralSummaryCardProps = {
  dashboard: ReferralRewardDashboard | null
  loading: boolean
  complianceConfirmed: boolean
  onTransfer: () => void
}

type ActivityCardProps = {
  batch?: InviteLinkBatch
  loading: boolean
}

type ReferralLinkCardProps = {
  dashboard: ReferralRewardDashboard | null
  loading: boolean
}

type InvitedUsersTableProps = {
  refreshKey: number
}

type SortDirection = 'asc' | 'desc'
type InvitedUsersSortKey =
  | 'sequence'
  | 'registered'
  | 'ratio'
  | 'reward'
  | 'initial_quota'

const defaultPresetActivityDescription: PresetActivityDescription = {
  title: '',
  summary: '',
  details: [],
}

const invitedUsersSkeletonRows = [
  'invite-row-1',
  'invite-row-2',
  'invite-row-3',
  'invite-row-4',
]

const rewardBadgeClassMap: Record<RewardActivity['type'], string> = {
  first_topup:
    'border border-amber-200/45 bg-amber-50/35 !text-amber-700 dark:border-amber-900/40 dark:bg-amber-950/15 dark:!text-amber-300',
  continuous:
    'border border-emerald-200/40 bg-emerald-50/35 !text-emerald-700 dark:border-emerald-900/40 dark:bg-emerald-950/15 dark:!text-emerald-300',
  initial_quota:
    'border border-sky-200/45 bg-sky-50/40 !text-sky-700 dark:border-sky-900/40 dark:bg-sky-950/20 dark:!text-sky-300',
}

function parsePresetActivityDescription(
  batch?: InviteLinkBatch
): PresetActivityDescription {
  if (!batch?.preset_description) return defaultPresetActivityDescription
  try {
    const parsed = JSON.parse(batch.preset_description) as {
      title?: string
      summary?: string
      details?: Array<{
        title?: string
        first_topup_reward_percent?: number
        continuous_reward_percent?: number
      }>
    }
    return {
      title: parsed.title ?? '',
      summary: parsed.summary ?? '',
      details: (parsed.details ?? [])
        .map((detail) => ({
          title: detail.title ?? '',
          first_topup_reward_percent: detail.first_topup_reward_percent,
          continuous_reward_percent: detail.continuous_reward_percent,
        }))
        .filter((detail) => detail.title.trim()),
    }
  } catch {
    return defaultPresetActivityDescription
  }
}

function toShareableReferralLink(link: string) {
  if (!link || typeof window === 'undefined') return link
  try {
    return new URL(link, window.location.origin).toString()
  } catch {
    return link
  }
}

function rewardActivityLabel(
  activity: RewardActivity,
  t: (key: string) => string
) {
  const type = activity.type
  if (type === 'initial_quota') return t('Initial Quota')
  return type === 'first_topup' ? t('First Top-up') : t('Continuous')
}

function RewardActivityBadges(props: { activities: RewardActivity[] }) {
  const { t } = useTranslation()

  if (props.activities.length === 0) {
    return <span className='text-muted-foreground text-xs'>-</span>
  }

  return (
    <div className='flex flex-wrap gap-2'>
      {props.activities.map((activity) => (
        <StatusBadge
          key={`${activity.activity_detail}-${activity.type}-${activity.percent}`}
          label={`${rewardActivityLabel(activity, t)}${activity.percent ?? 0}%`}
          variant='success'
          size='sm'
          copyable={false}
          className={cn(
            'rounded-md font-mono',
            rewardBadgeClassMap[activity.type]
          )}
        />
      ))}
    </div>
  )
}

function InitialQuotaSummaryBadge(props: {
  source?: RewardRateSource
  showZero?: boolean
}) {
  const { t } = useTranslation()
  const total = getInitialQuotaTotal(props.source)
  if (total <= 0 && !props.showZero) return null

  return (
    <StatusBadge
      label={formatInitialQuotaSummary(props.source, formatQuota, t)}
      variant='info'
      size='sm'
      copyable={false}
      className='rounded-md border border-sky-200/45 bg-sky-50/40 font-mono !text-sky-700 dark:border-sky-900/40 dark:bg-sky-950/20 dark:!text-sky-300'
    />
  )
}

function RewardRateSummaryBadge(props: { batch?: InviteLinkBatch }) {
  const { t } = useTranslation()
  const summary = formatRewardRateSummary(props.batch, t)

  if (!summary) {
    return <span className='text-muted-foreground text-xs'>-</span>
  }

  return (
    <StatusBadge
      label={summary}
      variant='success'
      size='sm'
      copyable={false}
      className='border-primary/25 bg-primary/5 !text-primary rounded-md border font-mono'
    />
  )
}

function RewardMetric(props: RewardMetricProps) {
  const Icon = props.icon

  return (
    <div className='rounded-lg border p-3'>
      <div className='text-muted-foreground flex items-center gap-2 text-xs font-medium'>
        <Icon className='size-3.5' />
        {props.label}
      </div>
      {props.loading ? (
        <Skeleton className='mt-2 h-6 w-24' />
      ) : (
        <div className='mt-2 truncate text-lg font-semibold tabular-nums'>
          {props.value}
        </div>
      )}
    </div>
  )
}

function SortableHeader<TSort extends string>(props: {
  label: string
  sortKey: TSort
  onSort: (key: TSort, direction: SortDirection) => void
  onHide: (key: TSort) => void
}) {
  const { t } = useTranslation()

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant='ghost' size='sm' className='-ms-3 h-8' />}
      >
        <span>{props.label}</span>
        <ChevronsUpDown className='ms-2 size-3.5' />
      </DropdownMenuTrigger>
      <DropdownMenuContent align='start'>
        <DropdownMenuItem onClick={() => props.onSort(props.sortKey, 'asc')}>
          {props.label} ↑
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => props.onSort(props.sortKey, 'desc')}>
          {props.label} ↓
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={() => props.onHide(props.sortKey)}>
          <EyeOff className='text-muted-foreground size-3.5' />
          {t('Hide')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function TrustedActivityHtml(props: { content: string }) {
  const html = useMemo(
    () => renderTrustedActivityDescription(props.content),
    [props.content]
  )

  return (
    <div
      className='text-foreground/90 [&_a]:text-primary [&_a]:underline [&_li]:my-1 [&_ol]:list-decimal [&_ol]:ps-5 [&_p]:my-2 [&_ul]:list-disc [&_ul]:ps-5'
      dangerouslySetInnerHTML={{ __html: html }}
    />
  )
}

function ActivityCard(props: ActivityCardProps) {
  const { t } = useTranslation()
  const preset = useMemo(
    () => parsePresetActivityDescription(props.batch),
    [props.batch]
  )
  const customDescription = props.batch?.custom_description.trim() ?? ''
  const showCustom =
    props.batch?.description_mode === 'custom' && customDescription !== ''
  const activities = useMemo(
    () => getRewardActivities(props.batch),
    [props.batch]
  )
  const initialQuotaActivities = useMemo(
    () => getInitialQuotaActivities(props.batch),
    [props.batch]
  )

  if (props.loading) {
    return (
      <Card data-card-hover='false' className='gap-0 py-0'>
        <CardHeader className='border-b p-4 !pb-4'>
          <Skeleton className='h-5 w-40' />
          <Skeleton className='h-4 w-72' />
        </CardHeader>
        <CardContent className='space-y-2 p-4'>
          <Skeleton className='h-4 w-full' />
          <Skeleton className='h-4 w-5/6' />
          <Skeleton className='h-4 w-2/3' />
        </CardContent>
      </Card>
    )
  }

  if (!props.batch) {
    return (
      <Card data-card-hover='false' className='gap-0 py-0'>
        <CardContent className='p-0'>
          <Empty className='py-10'>
            <EmptyHeader>
              <EmptyMedia variant='icon'>
                <Link2 />
              </EmptyMedia>
              <EmptyTitle>{t('No active referral activity')}</EmptyTitle>
              <EmptyDescription>
                {t(
                  'The referral link will appear here after an administrator publishes an active activity.'
                )}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        </CardContent>
      </Card>
    )
  }
  const batch = props.batch
  let activityContent = (
    <p className='text-muted-foreground text-sm'>
      {t('No activity description has been configured.')}
    </p>
  )
  if (showCustom) {
    activityContent = (
      <div className='space-y-4'>
        <TrustedActivityHtml content={customDescription} />
        {activities.length > 0 || initialQuotaActivities.length > 0 ? (
          <div className='grid gap-2 sm:grid-cols-2'>
            {activities.map((activity) => (
              <div
                key={`${activity.activity_detail}-${activity.type}-${activity.percent}`}
                className='rounded-lg border p-3'
              >
                <div className='text-sm font-medium'>
                  {formatActivityDetailLabel(activity.activity_detail, t)}
                </div>
                <div className='mt-2'>
                  <RewardActivityBadges activities={[activity]} />
                </div>
              </div>
            ))}
            {initialQuotaActivities.map((activity) => (
              <div
                key={`${activity.activity_detail}-${activity.type}-${activity.quota}`}
                className='rounded-lg border p-3'
              >
                <div className='text-sm font-medium'>
                  {formatActivityDetailLabel(activity.activity_detail, t)}
                </div>
                <div className='mt-2'>
                  <InitialQuotaSummaryBadge
                    source={{ activity_rules: [activity] }}
                  />
                </div>
              </div>
            ))}
          </div>
        ) : null}
      </div>
    )
  } else if (activities.length > 0 || initialQuotaActivities.length > 0) {
    activityContent = (
      <div className='grid gap-2 sm:grid-cols-2'>
        {activities.map((activity) => (
          <div
            key={`${activity.activity_detail}-${activity.type}-${activity.percent}`}
            className='rounded-lg border p-3'
          >
            <div className='text-sm font-medium'>
              {formatActivityDetailLabel(activity.activity_detail, t)}
            </div>
            <div className='mt-2'>
              <RewardActivityBadges activities={[activity]} />
            </div>
          </div>
        ))}
        {initialQuotaActivities.map((activity) => (
          <div
            key={`${activity.activity_detail}-${activity.type}-${activity.quota}`}
            className='rounded-lg border p-3'
          >
            <div className='text-sm font-medium'>
              {formatActivityDetailLabel(activity.activity_detail, t)}
            </div>
            <div className='mt-2'>
              <InitialQuotaSummaryBadge
                source={{ activity_rules: [activity] }}
              />
            </div>
          </div>
        ))}
      </div>
    )
  }

  return (
    <Card data-card-hover='false' className='gap-0 py-0'>
      <CardHeader className='border-b p-4 !pb-4'>
        <div className='flex flex-wrap items-start justify-between gap-3'>
          <div className='min-w-0'>
            <CardTitle className='text-base'>
              {preset.title || batch.name}
            </CardTitle>
            <CardDescription className='mt-1 text-sm'>
              {preset.summary ||
                t('Invite users with your referral link and earn rewards.')}
            </CardDescription>
          </div>
        </div>
      </CardHeader>
      <CardContent className='p-4'>{activityContent}</CardContent>
    </Card>
  )
}

function ReferralLinkCard(props: ReferralLinkCardProps) {
  const { t } = useTranslation()
  const link = toShareableReferralLink(props.dashboard?.invite_link ?? '')
  const activeBatch = props.dashboard?.active_batch

  return (
    <Card data-card-hover='false' className='gap-0 py-0'>
      <CardHeader className='border-b p-4 !pb-4'>
        <CardTitle className='text-base'>{t('Referral Link')}</CardTitle>
      </CardHeader>
      <CardContent className='space-y-4 p-4'>
        {props.loading ? (
          <Skeleton className='h-10 rounded-lg' />
        ) : (
          <div className='flex items-center gap-2'>
            <Input
              value={link}
              readOnly
              placeholder={t('No active referral link')}
              className='min-w-0 flex-1 font-mono text-xs'
            />
            {link ? (
              <CopyButton
                value={link}
                variant='outline'
                tooltip={t('Copy referral link')}
                aria-label={t('Copy referral link')}
              />
            ) : (
              <Button
                variant='outline'
                size='icon'
                disabled
                aria-label={t('Copy referral link')}
              >
                <Link2 />
              </Button>
            )}
          </div>
        )}
        <div className='flex flex-wrap items-center gap-3'>
          <RewardRateSummaryBadge batch={activeBatch} />
          {activeBatch ? (
            <InitialQuotaSummaryBadge source={activeBatch} showZero />
          ) : null}
          {activeBatch ? (
            <span className='text-muted-foreground flex flex-wrap items-center gap-1.5 text-xs'>
              <span className='text-foreground/80 font-medium'>
                {t('Valid Period')}
              </span>
              <span>
                {formatShanghaiTimestamp(activeBatch.start_time)} -{' '}
                {formatShanghaiTimestamp(activeBatch.end_time)}
              </span>
            </span>
          ) : null}
        </div>
      </CardContent>
    </Card>
  )
}

function ReferralSummaryCard(props: ReferralSummaryCardProps) {
  const { t } = useTranslation()
  const dashboard = props.dashboard
  const availableQuota = dashboard?.available_reward_quota ?? 0
  const hasAvailableRewards = availableQuota > 0
  const stats = [
    {
      label: t('Pending Rewards'),
      value: formatQuota(dashboard?.pending_reward_quota ?? 0),
      icon: Clock,
    },
    {
      label: t('Transferable Rewards'),
      value: formatQuota(availableQuota),
      icon: CheckCircle2,
    },
    {
      label: t('Transferred Rewards'),
      value: formatQuota(dashboard?.transferred_reward_quota ?? 0),
      icon: Gift,
    },
    {
      label: t('Invited Users'),
      value: String(dashboard?.invited_user_count ?? 0),
      icon: Users,
    },
  ]

  return (
    <Card data-card-hover='false' className='gap-0 py-0'>
      <CardHeader className='border-b p-4 !pb-4'>
        <div className='flex flex-wrap items-center justify-between gap-3'>
          <div className='min-w-0'>
            <CardTitle className='text-base'>{t('Reward Summary')}</CardTitle>
            <CardDescription className='mt-1 text-sm'>
              {t(
                'Pending rewards automatically become transferable rewards 7 days after the user tops up.'
              )}
            </CardDescription>
          </div>
          <Button
            onClick={props.onTransfer}
            disabled={!hasAvailableRewards || !props.complianceConfirmed}
            size='sm'
          >
            <Gift data-icon='inline-start' />
            {t('Transfer to Balance')}
          </Button>
        </div>
      </CardHeader>
      <CardContent className='space-y-3 p-4'>
        <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-4'>
          {stats.map((stat) => (
            <RewardMetric
              key={stat.label}
              label={stat.label}
              value={stat.value}
              loading={props.loading}
              icon={stat.icon}
            />
          ))}
        </div>
        {!props.complianceConfirmed ? (
          <p className='text-muted-foreground text-sm'>
            {t(
              'Reward transfer is disabled until the administrator confirms compliance terms.'
            )}
          </p>
        ) : null}
      </CardContent>
    </Card>
  )
}

function invitedUserContributionText(
  user: InvitedUser,
  t: (key: string) => string
) {
  return [
    `${t('Pending Rewards')}: ${formatQuota(user.pending_reward_quota)}`,
    `${t('Transferable Rewards')}: ${formatQuota(user.available_reward_quota)}`,
    `${t('Transferred Rewards')}: ${formatQuota(user.transferred_reward_quota)}`,
  ].join(' / ')
}

function InvitedUsersTable(props: InvitedUsersTableProps) {
  const { t } = useTranslation()
  const [search, setSearch] = useState('')
  const [registeredRange, setRegisteredRange] = useState<{
    start?: Date
    end?: Date
  }>({})
  const [users, setUsers] = useState<InvitedUser[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState(false)
  const [sort, setSort] = useState<{
    key: InvitedUsersSortKey
    direction: SortDirection
  }>({ key: 'sequence', direction: 'desc' })
  const [hiddenColumns, setHiddenColumns] = useState<
    Partial<Record<InvitedUsersSortKey, boolean>>
  >({})
  const columns = useMemo(
    () => [
      { id: 'sequence' as const, label: t('Invitation No.') },
      { id: 'registered' as const, label: t('Registered At') },
      { id: 'ratio' as const, label: t('Reward Ratio') },
      { id: 'reward' as const, label: t('Contribution Rewards') },
      { id: 'initial_quota' as const, label: t('Initial Quota') },
    ],
    [t]
  )

  useEffect(() => {
    let ignore = false
    const fetchUsers = async () => {
      setLoading(true)
      setLoadError(false)
      const trimmedSearch = search.trim()
      const params: {
        registered_start?: number
        registered_end?: number
        reward_percent?: number
      } = {}
      if (registeredRange.start) {
        params.registered_start = Math.floor(
          registeredRange.start.getTime() / 1000
        )
      }
      if (registeredRange.end) {
        params.registered_end = Math.floor(registeredRange.end.getTime() / 1000)
      }
      if (trimmedSearch) {
        const normalizedSearch = trimmedSearch.endsWith('%')
          ? trimmedSearch.slice(0, -1).trim()
          : trimmedSearch
        const percent = Number(normalizedSearch)
        if (Number.isFinite(percent)) params.reward_percent = percent
      }
      try {
        const response = await getInvitedUsers(params)
        if (!ignore) {
          if (response.success) {
            setUsers(response.data ?? [])
          } else {
            setUsers([])
            setLoadError(true)
          }
        }
      } catch {
        if (!ignore) {
          setUsers([])
          setLoadError(true)
        }
      } finally {
        if (!ignore) setLoading(false)
      }
    }

    fetchUsers()
    return () => {
      ignore = true
    }
  }, [props.refreshKey, registeredRange.end, registeredRange.start, search])

  const inviteSequenceMap = useMemo(
    () => buildInviteSequenceMap(users),
    [users]
  )

  const sortedUsers = useMemo(() => {
    const rows = [...users]
    rows.sort((a, b) => {
      let left: string | number = inviteSequenceMap.get(a.id) ?? 0
      let right: string | number = inviteSequenceMap.get(b.id) ?? 0
      if (sort.key === 'registered') {
        left = a.created_at
        right = b.created_at
      }
      if (sort.key === 'ratio') {
        left = getRewardRateSortValue(a)
        right = getRewardRateSortValue(b)
      }
      if (sort.key === 'reward') {
        left = getPendingRewardQuotaSortValue(a)
        right = getPendingRewardQuotaSortValue(b)
      }
      if (sort.key === 'initial_quota') {
        left = getInitialQuotaSortValue(a)
        right = getInitialQuotaSortValue(b)
      }
      const direction = sort.direction === 'asc' ? 1 : -1
      if (left > right) return direction
      if (left < right) return -direction
      return 0
    })
    return rows
  }, [inviteSequenceMap, sort, users])

  if (loading) {
    return (
      <Card data-card-hover='false' className='gap-0 py-0'>
        <CardHeader className='border-b p-4 !pb-4'>
          <Skeleton className='h-5 w-32' />
          <Skeleton className='h-4 w-56' />
        </CardHeader>
        <CardContent className='space-y-2 p-4'>
          {invitedUsersSkeletonRows.map((row) => (
            <Skeleton key={row} className='h-10 rounded-lg' />
          ))}
        </CardContent>
      </Card>
    )
  }

  let tableContent = (
    <Table>
      <TableHeader>
        <TableRow>
          {!hiddenColumns.sequence && (
            <TableHead>
              <SortableHeader
                label={t('Invitation No.')}
                sortKey='sequence'
                onSort={(key, direction) => setSort({ key, direction })}
                onHide={(key) =>
                  setHiddenColumns((current) => ({ ...current, [key]: true }))
                }
              />
            </TableHead>
          )}
          {!hiddenColumns.registered && (
            <TableHead>
              <SortableHeader
                label={t('Registered At')}
                sortKey='registered'
                onSort={(key, direction) => setSort({ key, direction })}
                onHide={(key) =>
                  setHiddenColumns((current) => ({ ...current, [key]: true }))
                }
              />
            </TableHead>
          )}
          {!hiddenColumns.ratio && (
            <TableHead>
              <SortableHeader
                label={t('Reward Ratio')}
                sortKey='ratio'
                onSort={(key, direction) => setSort({ key, direction })}
                onHide={(key) =>
                  setHiddenColumns((current) => ({ ...current, [key]: true }))
                }
              />
            </TableHead>
          )}
          {!hiddenColumns.reward && (
            <TableHead>
              <SortableHeader
                label={t('Contribution Rewards')}
                sortKey='reward'
                onSort={(key, direction) => setSort({ key, direction })}
                onHide={(key) =>
                  setHiddenColumns((current) => ({ ...current, [key]: true }))
                }
              />
            </TableHead>
          )}
          {!hiddenColumns.initial_quota && (
            <TableHead>
              <SortableHeader
                label={t('Initial Quota')}
                sortKey='initial_quota'
                onSort={(key, direction) => setSort({ key, direction })}
                onHide={(key) =>
                  setHiddenColumns((current) => ({ ...current, [key]: true }))
                }
              />
            </TableHead>
          )}
        </TableRow>
      </TableHeader>
      <TableBody>
        {sortedUsers.map((user) => (
          <TableRow key={user.id}>
            {!hiddenColumns.sequence && (
              <TableCell>
                <span className='font-mono text-sm font-semibold tabular-nums'>
                  #{inviteSequenceMap.get(user.id) ?? '-'}
                </span>
              </TableCell>
            )}
            {!hiddenColumns.registered && (
              <TableCell>{formatTimestamp(user.created_at)}</TableCell>
            )}
            {!hiddenColumns.ratio && (
              <TableCell className='max-w-[360px] whitespace-normal'>
                <RewardActivityBadges activities={getRewardActivities(user)} />
              </TableCell>
            )}
            {!hiddenColumns.reward && (
              <TableCell className='max-w-[380px] whitespace-normal'>
                <div className='text-muted-foreground text-xs leading-relaxed break-words'>
                  {invitedUserContributionText(user, t)}
                </div>
              </TableCell>
            )}
            {!hiddenColumns.initial_quota && (
              <TableCell>
                <span className='font-mono text-sm tabular-nums'>
                  {formatQuota(user.initial_quota ?? 0)}
                </span>
              </TableCell>
            )}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )

  if (loadError) {
    tableContent = (
      <Empty className='rounded-none border-0 py-10'>
        <EmptyHeader>
          <EmptyMedia variant='icon'>
            <Users />
          </EmptyMedia>
          <EmptyTitle>{t('Request failed')}</EmptyTitle>
        </EmptyHeader>
      </Empty>
    )
  } else if (users.length === 0) {
    tableContent = (
      <Empty className='rounded-none border-0 py-10'>
        <EmptyHeader>
          <EmptyMedia variant='icon'>
            <Users />
          </EmptyMedia>
          <EmptyTitle>{t('No invited users yet')}</EmptyTitle>
          <EmptyDescription>
            {t('Share your referral link to start building rewards.')}
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    )
  }

  return (
    <Card data-card-hover='false' className='gap-0 py-0'>
      <CardHeader className='border-b p-4 !pb-4'>
        <div className='flex flex-col gap-3'>
          <div>
            <CardTitle className='text-base'>{t('Invited Users')}</CardTitle>
            <CardDescription className='mt-1 text-sm'>
              {t('Users who registered through your referral link.')}
            </CardDescription>
          </div>
          <div className='flex flex-col gap-2 lg:flex-row lg:items-center'>
            <div className='relative min-w-0 flex-1'>
              <Search className='text-muted-foreground absolute top-1/2 left-2.5 size-4 -translate-y-1/2' />
              <Input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder={t('Search reward ratio...')}
                className='pl-8'
              />
            </div>
            <CompactDateTimeRangePicker
              start={registeredRange.start}
              end={registeredRange.end}
              onChange={setRegisteredRange}
              className='lg:w-72'
            />
            <DataTableManualViewOptions
              columns={columns}
              hiddenColumns={hiddenColumns}
              onVisibilityChange={(column, visible) =>
                setHiddenColumns((current) => ({
                  ...current,
                  [column]: !visible,
                }))
              }
            />
          </div>
        </div>
      </CardHeader>
      <CardContent className='p-0'>{tableContent}</CardContent>
    </Card>
  )
}

export function InviteRewards() {
  const { t } = useTranslation()
  const [dashboard, setDashboard] = useState<ReferralRewardDashboard | null>(
    null
  )
  const [loading, setLoading] = useState(true)
  const [transferDialogOpen, setTransferDialogOpen] = useState(false)
  const [transferring, setTransferring] = useState(false)
  const [invitedUsersRefreshKey, setInvitedUsersRefreshKey] = useState(0)
  const { topupInfo } = useTopupInfo()

  const fetchInviteRewards = useCallback(async () => {
    try {
      setLoading(true)
      const response = await getReferralRewards()
      if (response.success && response.data) {
        setDashboard(response.data)
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to load referral rewards:', error)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    fetchInviteRewards()
  }, [fetchInviteRewards])

  const complianceConfirmed = topupInfo?.payment_compliance_confirmed !== false
  const availableQuota = dashboard?.available_reward_quota ?? 0

  const handleTransfer = async (amount: number) => {
    try {
      setTransferring(true)
      const response = await transferAffiliateQuota({ quota: amount })
      if (response.success) {
        toast.success(response.message || t('Transfer successful'))
        await fetchInviteRewards()
        setInvitedUsersRefreshKey((current) => current + 1)
        return true
      }
      toast.error(response.message || t('Transfer failed'))
      return false
    } catch {
      toast.error(t('Transfer failed'))
      return false
    } finally {
      setTransferring(false)
    }
  }

  return (
    <>
      <SectionPageLayout>
        <SectionPageLayout.Title>
          {t('Referral Rewards')}
        </SectionPageLayout.Title>
        <SectionPageLayout.Content>
          <div className='mx-auto flex w-full max-w-7xl flex-col gap-4 sm:gap-5'>
            <ActivityCard batch={dashboard?.active_batch} loading={loading} />
            <ReferralLinkCard dashboard={dashboard} loading={loading} />
            <ReferralSummaryCard
              dashboard={dashboard}
              loading={loading}
              complianceConfirmed={complianceConfirmed}
              onTransfer={() => setTransferDialogOpen(true)}
            />
            <InvitedUsersTable refreshKey={invitedUsersRefreshKey} />
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <TransferDialog
        open={transferDialogOpen}
        onOpenChange={setTransferDialogOpen}
        onConfirm={handleTransfer}
        availableQuota={availableQuota}
        transferring={transferring}
      />
    </>
  )
}
