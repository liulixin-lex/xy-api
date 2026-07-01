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
import { Gift, Percent, Users } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
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
  rate: string
  link: string
  loading: boolean
}

function RuleCard(props: RuleCardProps) {
  const { t } = useTranslation()

  return (
    <Card data-card-hover='false' className='gap-0 py-0'>
      <CardHeader className='border-b p-4 !pb-4'>
        <div className='flex items-start justify-between gap-3'>
          <div className='min-w-0'>
            <CardTitle className='text-base'>{props.title}</CardTitle>
            <CardDescription className='mt-1 text-sm'>
              {props.description}
            </CardDescription>
          </div>
          <Badge variant='secondary' className='shrink-0'>
            {props.rate}
          </Badge>
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
              tooltip={t('Copy referral link')}
              aria-label={t('Copy referral link')}
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
            <CardTitle className='text-base'>{t('Reward Summary')}</CardTitle>
            <CardDescription className='mt-1 text-sm'>
              {t('Track rewards and move pending rewards to your balance.')}
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
              'Referral reward transfer is disabled until the administrator confirms compliance terms.'
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

const invitedUsersSkeletonRows = ['invite-row-1', 'invite-row-2', 'invite-row-3', 'invite-row-4']

function InvitedUsersTable(props: InvitedUsersTableProps) {
  const { t } = useTranslation()

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

  return (
    <Card data-card-hover='false' className='gap-0 py-0'>
      <CardHeader className='border-b p-4 !pb-4'>
        <CardTitle className='text-base'>{t('Invited Users')}</CardTitle>
        <CardDescription className='mt-1 text-sm'>
          {t('Users who registered through your referral links.')}
        </CardDescription>
      </CardHeader>
      <CardContent className='p-0'>
        {props.users.length === 0 ? (
          <Empty className='rounded-none border-0 py-10'>
            <EmptyHeader>
              <EmptyMedia variant='icon'>
                <Users />
              </EmptyMedia>
              <EmptyTitle>{t('No invited users yet')}</EmptyTitle>
              <EmptyDescription>
                {t('Share either referral link to start building rewards.')}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('Username')}</TableHead>
                <TableHead>{t('Display Name')}</TableHead>
                <TableHead>{t('Registered At')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {props.users.map((user) => (
                <TableRow key={user.id}>
                  <TableCell className='font-medium'>{user.username}</TableCell>
                  <TableCell>{user.display_name || '-'}</TableCell>
                  <TableCell>{formatTimestamp(user.created_at)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
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
        <SectionPageLayout.Title>{t('Invite Rewards')}</SectionPageLayout.Title>
        <SectionPageLayout.Content>
          <div className='mx-auto flex w-full max-w-7xl flex-col gap-4 sm:gap-5'>
            <Card data-card-hover='false' className='gap-0 py-0'>
              <CardHeader className='border-b p-4 !pb-4'>
                <div className='flex items-start gap-3'>
                  <div className='bg-muted flex size-9 shrink-0 items-center justify-center rounded-lg'>
                    <Percent className='text-muted-foreground size-4' />
                  </div>
                  <div className='min-w-0'>
                    <CardTitle className='text-base'>
                      {t('Referral Program')}
                    </CardTitle>
                    <CardDescription className='mt-1 text-sm'>
                      {t(
                        'Earn rewards when your referrals add funds. Transfer accumulated rewards to your balance anytime.'
                      )}
                    </CardDescription>
                  </div>
                </div>
              </CardHeader>
            </Card>

            <div className='grid gap-4 lg:grid-cols-2'>
              <RuleCard
                title={t('Continuous Referral')}
                description={t(
                  'Earn 5% from every future top-up made by users registered through this link.'
                )}
                rate='5%'
                link={continuousLink}
                loading={loading}
              />
              <RuleCard
                title={t('One-time Referral')}
                description={t(
                  'Earn 10% from the first top-up made by users registered through this link.'
                )}
                rate='10%'
                link={firstTopUpLink}
                loading={loading}
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
