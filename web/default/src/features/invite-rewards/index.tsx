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
  BadgePercent,
  Gift,
  Repeat2,
  Search,
  Sparkles,
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
import { getAffiliateCode, transferAffiliateQuota } from '@/features/wallet/api'
import { TransferDialog } from '@/features/wallet/components/dialogs/transfer-dialog'
import { useTopupInfo } from '@/features/wallet/hooks'
import { generateAffiliateLink } from '@/features/wallet/lib'
import type { UserWalletData } from '@/features/wallet/types'
import { getSelf } from '@/lib/api'
import { formatQuota, formatTimestamp } from '@/lib/format'

import { getInvitedUsers } from './api'
import type { InvitedUser } from './types'

type RuleCardProps = {
  title: string
  description: string
  rate: number
  link: string
  loading: boolean
  tone: 'steady' | 'prime'
  icon: ComponentType<{ className?: string }>
}

function RewardRateBadge(props: { rate: number; tone: 'steady' | 'prime' }) {
  return (
    <span
      className={`inline-flex min-w-16 items-center justify-center rounded-lg border px-3 py-1.5 text-base font-semibold tabular-nums ${
        props.tone === 'prime'
          ? 'border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300'
          : 'border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300'
      }`}
    >
      {props.rate}%
    </span>
  )
}

function RuleCard(props: RuleCardProps) {
  const { t } = useTranslation()
  const Icon = props.icon

  return (
    <Card data-card-hover='false' className='gap-0 py-0'>
      <CardHeader className='border-b p-4 !pb-4'>
        <div className='flex items-start justify-between gap-3'>
          <div className='flex min-w-0 items-start gap-3'>
            <div className='bg-muted flex size-9 shrink-0 items-center justify-center rounded-lg'>
              <Icon className='text-muted-foreground size-4' />
            </div>
            <div className='min-w-0'>
              <CardTitle className='text-base'>{props.title}</CardTitle>
              <CardDescription className='mt-1 text-sm'>
                {props.description}
              </CardDescription>
            </div>
          </div>
          <RewardRateBadge rate={props.rate} tone={props.tone} />
        </div>
      </CardHeader>
      <CardContent className='p-4'>
        {props.loading ? (
          <Skeleton className='h-10 rounded-lg' />
        ) : (
          <div className='flex items-center gap-2'>
            <Input
              value={props.link}
              readOnly
              className='min-w-0 flex-1 font-mono text-xs'
            />
            <CopyButton
              value={props.link}
              variant='outline'
              tooltip={t('Copy rebate link')}
              aria-label={t('Copy rebate link')}
            />
          </div>
        )}
      </CardContent>
    </Card>
  )
}

type SummaryCardProps = {
  user: UserWalletData | null
  loading: boolean
  complianceConfirmed: boolean
  onTransfer: () => void
}

function SummaryCard(props: SummaryCardProps) {
  const { t } = useTranslation()
  const stats = [
    {
      label: t('Pending Balance'),
      value: formatQuota(props.user?.aff_quota ?? 0),
    },
    {
      label: t('Total Income'),
      value: formatQuota(props.user?.aff_history_quota ?? 0),
    },
    {
      label: t('Invited Users'),
      value: String(props.user?.aff_count ?? 0),
    },
  ]
  const hasRewards = (props.user?.aff_quota ?? 0) > 0

  return (
    <Card data-card-hover='false' className='gap-0 py-0'>
      <CardHeader className='border-b p-4 !pb-4'>
        <div className='flex flex-wrap items-center justify-between gap-3'>
          <div className='min-w-0'>
            <CardTitle className='text-base'>{t('Rebate Summary')}</CardTitle>
            <CardDescription className='mt-1 text-sm'>
              {t('Track rebates and move pending rebates to your balance.')}
            </CardDescription>
          </div>
          <Button
            onClick={props.onTransfer}
            disabled={!hasRewards || !props.complianceConfirmed}
            size='sm'
          >
            <Gift data-icon='inline-start' />
            {t('Transfer to Balance')}
          </Button>
        </div>
      </CardHeader>
      <CardContent className='p-4'>
        <div className='grid gap-3 sm:grid-cols-3'>
          {stats.map((stat) => (
            <div key={stat.label} className='rounded-lg border p-3'>
              <div className='text-muted-foreground text-xs font-medium'>
                {stat.label}
              </div>
              {props.loading ? (
                <Skeleton className='mt-2 h-6 w-24' />
              ) : (
                <div className='mt-2 truncate text-lg font-semibold tabular-nums'>
                  {stat.value}
                </div>
              )}
            </div>
          ))}
        </div>
        {!props.complianceConfirmed ? (
          <p className='text-muted-foreground mt-3 text-sm'>
            {t(
              'Rebate transfer is disabled until the administrator confirms compliance terms.'
            )}
          </p>
        ) : null}
      </CardContent>
    </Card>
  )
}

type InvitedUsersTableProps = {
  users: InvitedUser[]
  loading: boolean
}

type InvitedUsersSearchField =
  | 'all'
  | 'username'
  | 'display_name'
  | 'invite_type'
  | 'reward_rate'
  | 'contribution_quota'

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
  { value: 'invite_type', labelKey: 'Invite Type' },
  { value: 'reward_rate', labelKey: 'Rebate Rate' },
  { value: 'contribution_quota', labelKey: 'Contribution Amount' },
]

function getInviteTypeLabel(rule: InvitedUser['invite_reward_rule']) {
  return rule === 'first_topup' ? 'First Top-up Rebate' : 'Continuous Rebate'
}

function matchesInvitedUserSearch(
  user: InvitedUser,
  field: InvitedUsersSearchField,
  search: string
) {
  const normalizedSearch = search.trim().toLowerCase()
  if (!normalizedSearch) return true

  const typeLabel = getInviteTypeLabel(user.invite_reward_rule).toLowerCase()
  const values: Record<InvitedUsersSearchField, string[]> = {
    all: [
      user.username,
      user.display_name,
      typeLabel,
      String(user.invite_reward_percent),
      String(user.contribution_quota),
    ],
    username: [user.username],
    display_name: [user.display_name],
    invite_type: [typeLabel, user.invite_reward_rule],
    reward_rate: [String(user.invite_reward_percent)],
    contribution_quota: [String(user.contribution_quota)],
  }

  return values[field].some((value) =>
    value.toLowerCase().includes(normalizedSearch)
  )
}

function InvitedUsersTable(props: InvitedUsersTableProps) {
  const { t } = useTranslation()
  const [searchField, setSearchField] = useState<InvitedUsersSearchField>('all')
  const [search, setSearch] = useState('')
  const [inviteType, setInviteType] = useState('')

  const filteredUsers = useMemo(
    () =>
      props.users.filter((user) => {
        if (inviteType && user.invite_reward_rule !== inviteType) {
          return false
        }
        return matchesInvitedUserSearch(user, searchField, search)
      }),
    [inviteType, props.users, search, searchField]
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
          <TableHead>{t('Rebate Rate')}</TableHead>
          <TableHead>{t('Invite Type')}</TableHead>
          <TableHead>{t('Contribution Amount')}</TableHead>
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
              <RewardRateBadge
                rate={user.invite_reward_percent}
                tone={
                  user.invite_reward_rule === 'first_topup' ? 'prime' : 'steady'
                }
              />
            </TableCell>
            <TableCell>
              <Badge variant='secondary'>
                {t(getInviteTypeLabel(user.invite_reward_rule))}
              </Badge>
            </TableCell>
            <TableCell>{formatQuota(user.contribution_quota)}</TableCell>
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
            {t('Share either rebate link to start earning rebates.')}
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
              {t('Users who registered through your rebate links.')}
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
            <div className='grid grid-cols-3 gap-2 lg:flex lg:shrink-0'>
              {[
                { value: '', labelKey: 'All' },
                { value: 'continuous', labelKey: 'Continuous Rebate' },
                { value: 'first_topup', labelKey: 'First Top-up Rebate' },
              ].map((option) => (
                <Button
                  key={option.value}
                  type='button'
                  variant={inviteType === option.value ? 'default' : 'outline'}
                  size='sm'
                  onClick={() => setInviteType(option.value)}
                >
                  {t(option.labelKey)}
                </Button>
              ))}
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
  const [user, setUser] = useState<UserWalletData | null>(null)
  const [affiliateCode, setAffiliateCode] = useState('')
  const [invitedUsers, setInvitedUsers] = useState<InvitedUser[]>([])
  const [loading, setLoading] = useState(true)
  const [transferDialogOpen, setTransferDialogOpen] = useState(false)
  const [transferring, setTransferring] = useState(false)
  const { topupInfo } = useTopupInfo()

  const fetchInviteRewards = useCallback(async () => {
    try {
      setLoading(true)
      const [selfResponse, codeResponse, invitedResponse] = await Promise.all([
        getSelf(),
        getAffiliateCode(),
        getInvitedUsers(),
      ])
      if (selfResponse.success && selfResponse.data) {
        setUser(selfResponse.data as UserWalletData)
      }
      if (codeResponse.success && codeResponse.data) {
        setAffiliateCode(codeResponse.data)
      }
      if (invitedResponse.success && invitedResponse.data) {
        setInvitedUsers(invitedResponse.data)
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to load invite rewards:', error)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    fetchInviteRewards()
  }, [fetchInviteRewards])

  const continuousLink = useMemo(
    () => generateAffiliateLink(affiliateCode, 'continuous'),
    [affiliateCode]
  )
  const firstTopUpLink = useMemo(
    () => generateAffiliateLink(affiliateCode, 'first_topup'),
    [affiliateCode]
  )
  const complianceConfirmed = topupInfo?.payment_compliance_confirmed !== false
  const continuousPercent = topupInfo?.affiliate_continuous_percent ?? 5
  const firstTopupPercent = topupInfo?.affiliate_first_topup_percent ?? 30

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
        <SectionPageLayout.Title>{t('Invite Rebates')}</SectionPageLayout.Title>
        <SectionPageLayout.Content>
          <div className='mx-auto flex w-full max-w-7xl flex-col gap-4 sm:gap-5'>
            <Card data-card-hover='false' className='gap-0 py-0'>
              <CardHeader className='border-b p-4 !pb-4'>
                <div className='flex items-start gap-3'>
                  <div className='bg-primary/10 text-primary flex size-9 shrink-0 items-center justify-center rounded-lg'>
                    <Sparkles className='size-4' />
                  </div>
                  <div className='min-w-0'>
                    <CardTitle className='text-base'>
                      {t('Invite Rebate Program')}
                    </CardTitle>
                    <CardDescription className='mt-1 text-sm'>
                      {t(
                        'Earn rebates when invited users add funds. Transfer accumulated rebates to your balance anytime.'
                      )}
                    </CardDescription>
                  </div>
                </div>
              </CardHeader>
            </Card>

            <div className='grid gap-4 lg:grid-cols-2'>
              <RuleCard
                title={t('Continuous Rebate')}
                description={t(
                  'Earn from every future top-up made by users registered through this link.'
                )}
                rate={continuousPercent}
                link={continuousLink}
                loading={loading}
                tone='steady'
                icon={Repeat2}
              />
              <RuleCard
                title={t('First Top-up Rebate')}
                description={t(
                  'Earn from the first successful top-up made by users registered through this link.'
                )}
                rate={firstTopupPercent}
                link={firstTopUpLink}
                loading={loading}
                tone='prime'
                icon={BadgePercent}
              />
            </div>

            <SummaryCard
              user={user}
              loading={loading}
              complianceConfirmed={complianceConfirmed}
              onTransfer={() => setTransferDialogOpen(true)}
            />

            <InvitedUsersTable users={invitedUsers} loading={loading} />
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <TransferDialog
        open={transferDialogOpen}
        onOpenChange={setTransferDialogOpen}
        onConfirm={handleTransfer}
        availableQuota={user?.aff_quota ?? 0}
        transferring={transferring}
      />
    </>
  )
}
