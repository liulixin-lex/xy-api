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
  Clock,
  Gift,
  Link2,
  Search,
  Users,
  XCircle,
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
import { SectionPageLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { transferAffiliateQuota } from '@/features/wallet/api'
import { TransferDialog } from '@/features/wallet/components/dialogs/transfer-dialog'
import { useTopupInfo } from '@/features/wallet/hooks'
import { formatQuota, formatTimestamp } from '@/lib/format'

import { getReferralRewards } from './api'
import type {
  InviteLinkBatch,
  InvitedUser,
  ReferralRewardDashboard,
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
  users: InvitedUser[]
  loading: boolean
}

type InvitedUsersSearchField =
  | 'all'
  | 'username'
  | 'display_name'
  | 'reward_rate'
  | 'reward_quota'

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

const invitedUsersSearchFields: Array<{
  value: InvitedUsersSearchField
  labelKey: string
}> = [
  { value: 'all', labelKey: 'All columns' },
  { value: 'username', labelKey: 'Username' },
  { value: 'display_name', labelKey: 'Display Name' },
  { value: 'reward_rate', labelKey: 'Reward Ratio' },
  { value: 'reward_quota', labelKey: 'Reward Amount' },
]

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

function RewardRatePair(props: { first: number; continuous: number }) {
  const { t } = useTranslation()

  return (
    <div className='flex flex-wrap gap-2'>
      <span className='inline-flex items-center gap-1.5 rounded-md border border-amber-500/30 bg-amber-500/10 px-2 py-1 text-xs font-medium text-amber-700 dark:text-amber-300'>
        <span className='text-[11px]'>{t('First Top-up')}</span>
        <span className='tabular-nums'>{props.first}%</span>
      </span>
      <span className='inline-flex items-center gap-1.5 rounded-md border border-emerald-500/30 bg-emerald-500/10 px-2 py-1 text-xs font-medium text-emerald-700 dark:text-emerald-300'>
        <span className='text-[11px]'>{t('Subsequent')}</span>
        <span className='tabular-nums'>{props.continuous}%</span>
      </span>
    </div>
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

function TrustedActivityHtml(props: { html: string }) {
  return (
    <div
      className='text-foreground/90 [&_a]:text-primary [&_a]:underline [&_li]:my-1 [&_ol]:list-decimal [&_ol]:ps-5 [&_p]:my-2 [&_ul]:list-disc [&_ul]:ps-5'
      dangerouslySetInnerHTML={{ __html: props.html }}
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
    activityContent = <TrustedActivityHtml html={customDescription} />
  } else if (preset.details.length > 0) {
    activityContent = (
      <div className='grid gap-2 sm:grid-cols-2'>
        {preset.details.map((detail) => (
          <div key={detail.title} className='rounded-lg border p-3'>
            <div className='text-sm font-medium'>{detail.title}</div>
            <div className='mt-2'>
              <RewardRatePair
                first={
                  detail.first_topup_reward_percent ??
                  batch.first_topup_reward_percent
                }
                continuous={
                  detail.continuous_reward_percent ??
                  batch.continuous_reward_percent
                }
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
            <div className='flex flex-wrap items-center gap-2'>
              <CardTitle className='text-base'>
                {preset.title || batch.name}
              </CardTitle>
              <Badge variant='secondary'>{t('Active')}</Badge>
            </div>
            <CardDescription className='mt-1 text-sm'>
              {preset.summary ||
                t('Invite users with your referral link and earn rewards.')}
            </CardDescription>
          </div>
          <RewardRatePair
            first={batch.first_topup_reward_percent}
            continuous={batch.continuous_reward_percent}
          />
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
        <CardDescription className='mt-1 text-sm'>
          {t('Share this link so new users are bound to the active reward activity.')}
        </CardDescription>
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
          <RewardRatePair
            first={activeBatch?.first_topup_reward_percent ?? 0}
            continuous={activeBatch?.continuous_reward_percent ?? 0}
          />
          {activeBatch ? (
            <span className='text-muted-foreground text-xs'>
              {formatTimestamp(activeBatch.start_time)} -{' '}
              {formatTimestamp(activeBatch.end_time)}
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
      label: t('Available Rewards'),
      value: formatQuota(availableQuota),
      icon: CheckCircle2,
    },
    {
      label: t('Transferred Rewards'),
      value: formatQuota(dashboard?.transferred_reward_quota ?? 0),
      icon: Gift,
    },
    {
      label: t('Canceled Rewards'),
      value: formatQuota(dashboard?.canceled_reward_quota ?? 0),
      icon: XCircle,
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
              {t('Pending rewards become available after the reward waiting period.')}
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
        <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-5'>
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

function matchesInvitedUserSearch(
  user: InvitedUser,
  field: InvitedUsersSearchField,
  search: string
) {
  const normalizedSearch = search.trim().toLowerCase()
  if (!normalizedSearch) return true

  const rewardRates = [
    String(user.first_topup_reward_percent),
    String(user.continuous_reward_percent),
    String(user.invite_reward_percent),
  ]
  const rewardAmounts = [
    String(user.contribution_quota),
    String(user.pending_reward_quota),
    String(user.available_reward_quota),
    String(user.transferred_reward_quota),
    String(user.canceled_reward_quota),
  ]
  const values: Record<InvitedUsersSearchField, string[]> = {
    all: [user.username, user.display_name, ...rewardRates, ...rewardAmounts],
    username: [user.username],
    display_name: [user.display_name],
    reward_rate: rewardRates,
    reward_quota: rewardAmounts,
  }

  return values[field].some((value) =>
    value.toLowerCase().includes(normalizedSearch)
  )
}

function InvitedUsersTable(props: InvitedUsersTableProps) {
  const { t } = useTranslation()
  const [searchField, setSearchField] = useState<InvitedUsersSearchField>('all')
  const [search, setSearch] = useState('')

  const filteredUsers = useMemo(
    () =>
      props.users.filter((user) =>
        matchesInvitedUserSearch(user, searchField, search)
      ),
    [props.users, search, searchField]
  )

  if (props.loading) {
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
          <TableHead>{t('Username')}</TableHead>
          <TableHead>{t('Registered At')}</TableHead>
          <TableHead>{t('Reward Ratio')}</TableHead>
          <TableHead>{t('Pending Rewards')}</TableHead>
          <TableHead>{t('Available Rewards')}</TableHead>
          <TableHead>{t('Transferred Rewards')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {filteredUsers.map((user) => (
          <TableRow key={user.id}>
            <TableCell>
              <div className='flex min-w-0 flex-col'>
                <span className='font-medium'>{user.username}</span>
                <span className='text-muted-foreground truncate text-xs'>
                  {user.display_name || '-'}
                </span>
              </div>
            </TableCell>
            <TableCell>{formatTimestamp(user.created_at)}</TableCell>
            <TableCell>
              <RewardRatePair
                first={user.first_topup_reward_percent}
                continuous={user.continuous_reward_percent}
              />
            </TableCell>
            <TableCell>{formatQuota(user.pending_reward_quota)}</TableCell>
            <TableCell>{formatQuota(user.available_reward_quota)}</TableCell>
            <TableCell>
              <div>{formatQuota(user.transferred_reward_quota)}</div>
              {user.canceled_reward_quota > 0 ? (
                <div className='text-muted-foreground text-xs'>
                  {t('Canceled')}: {formatQuota(user.canceled_reward_quota)}
                </div>
              ) : null}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )

  if (props.users.length === 0) {
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
  } else if (filteredUsers.length === 0) {
    tableContent = (
      <div className='text-muted-foreground flex min-h-32 items-center justify-center px-4 text-sm'>
        {t('No invited users match the current filters.')}
      </div>
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
            <NativeSelect
              value={searchField}
              onChange={(event) =>
                setSearchField(event.target.value as InvitedUsersSearchField)
              }
              className='w-full lg:w-48'
            >
              {invitedUsersSearchFields.map((field) => (
                <NativeSelectOption key={field.value} value={field.value}>
                  {t(field.labelKey)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <div className='relative min-w-0 flex-1'>
              <Search className='text-muted-foreground absolute top-1/2 left-2.5 size-4 -translate-y-1/2' />
              <Input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder={t('Search invited users...')}
                className='pl-8'
              />
            </div>
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
        <SectionPageLayout.Title>{t('Referral Rewards')}</SectionPageLayout.Title>
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
            <InvitedUsersTable
              users={dashboard?.invited_users ?? []}
              loading={loading}
            />
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
