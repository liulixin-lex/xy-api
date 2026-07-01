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
import { BadgeDollarSign, Percent, Repeat2, Search, Users } from 'lucide-react'
import * as React from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
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
import { formatQuota, formatTimestamp } from '@/lib/format'
import { cn } from '@/lib/utils'

import { getAffiliateRewardSummary } from '../api'
import { useUpdateOption } from '../hooks/use-update-option'
import type { AffiliateRewardRelation } from '../types'

type AffiliateRewardSettingsSectionProps = {
  defaultValues: {
    continuousPercent: number
    firstTopupPercent: number
  }
}

type SearchField =
  | ''
  | 'inviter_username'
  | 'invitee_username'
  | 'invitee_display_name'
  | 'invite_type'
  | 'invite_percent'
  | 'reward_quota'

const searchFields: Array<{ value: SearchField; labelKey: string }> = [
  { value: '', labelKey: 'All columns' },
  { value: 'inviter_username', labelKey: 'Inviter' },
  { value: 'invitee_username', labelKey: 'Invited user' },
  { value: 'invitee_display_name', labelKey: 'Display Name' },
  { value: 'invite_type', labelKey: 'Invite Type' },
  { value: 'invite_percent', labelKey: 'Rebate Rate' },
  { value: 'reward_quota', labelKey: 'Rebate Amount' },
]

function normalizePercent(value: string) {
  const percent = Number(value)
  if (!Number.isInteger(percent) || percent < 1 || percent > 100) {
    return null
  }
  return percent
}

function getInviteTypeLabel(
  rule: AffiliateRewardRelation['invite_reward_rule']
) {
  return rule === 'first_topup' ? 'First Top-up Rebate' : 'Continuous Rebate'
}

function RateBadge(props: { percent: number; tone: 'steady' | 'prime' }) {
  return (
    <span
      className={cn(
        'inline-flex min-w-14 items-center justify-center rounded-lg border px-2.5 py-1 text-sm font-semibold tabular-nums',
        props.tone === 'prime'
          ? 'border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300'
          : 'border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300'
      )}
    >
      {props.percent}%
    </span>
  )
}

function StatTile(props: {
  label: string
  value: string
  loading: boolean
  icon: React.ComponentType<{ className?: string }>
}) {
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

function RelationsTable(props: {
  relations: AffiliateRewardRelation[]
  loading: boolean
}) {
  const { t } = useTranslation()

  if (props.loading) {
    return (
      <div className='space-y-2 p-4'>
        {['affiliate-row-1', 'affiliate-row-2', 'affiliate-row-3'].map(
          (row) => (
            <Skeleton key={row} className='h-10 rounded-lg' />
          )
        )}
      </div>
    )
  }

  if (props.relations.length === 0) {
    return (
      <div className='text-muted-foreground flex min-h-32 items-center justify-center px-4 text-sm'>
        {t('No affiliate relations found.')}
      </div>
    )
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t('Inviter')}</TableHead>
          <TableHead>{t('Invited user')}</TableHead>
          <TableHead>{t('Invite Type')}</TableHead>
          <TableHead>{t('Rebate Rate')}</TableHead>
          <TableHead>{t('Rebate Amount')}</TableHead>
          <TableHead>{t('Registered At')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {props.relations.map((relation) => (
          <TableRow key={`${relation.inviter_id}-${relation.invitee_id}`}>
            <TableCell className='font-medium'>
              {relation.inviter_username}
            </TableCell>
            <TableCell>
              <div className='flex min-w-0 flex-col'>
                <span className='font-medium'>{relation.invitee_username}</span>
                <span className='text-muted-foreground truncate text-xs'>
                  {relation.invitee_display_name || '-'}
                </span>
              </div>
            </TableCell>
            <TableCell>
              <Badge variant='secondary'>
                {t(getInviteTypeLabel(relation.invite_reward_rule))}
              </Badge>
            </TableCell>
            <TableCell>
              <RateBadge
                percent={relation.invite_reward_percent}
                tone={
                  relation.invite_reward_rule === 'first_topup'
                    ? 'prime'
                    : 'steady'
                }
              />
            </TableCell>
            <TableCell>{formatQuota(relation.reward_quota)}</TableCell>
            <TableCell>{formatTimestamp(relation.registered_at)}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

export function AffiliateRewardSettingsSection(
  props: AffiliateRewardSettingsSectionProps
) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const [continuousPercent, setContinuousPercent] = React.useState(
    String(props.defaultValues.continuousPercent)
  )
  const [firstTopupPercent, setFirstTopupPercent] = React.useState(
    String(props.defaultValues.firstTopupPercent)
  )
  const [searchField, setSearchField] = React.useState<SearchField>('')
  const [search, setSearch] = React.useState('')
  const [inviteType, setInviteType] = React.useState('')

  React.useEffect(() => {
    setContinuousPercent(String(props.defaultValues.continuousPercent))
    setFirstTopupPercent(String(props.defaultValues.firstTopupPercent))
  }, [
    props.defaultValues.continuousPercent,
    props.defaultValues.firstTopupPercent,
  ])

  const summaryQuery = useQuery({
    queryKey: ['affiliate-reward-summary', searchField, search, inviteType],
    queryFn: () =>
      getAffiliateRewardSummary({
        search_field: searchField || undefined,
        search: search.trim() || undefined,
        invite_type: inviteType || undefined,
      }),
  })

  const summary = summaryQuery.data?.data
  const relations = summary?.relations ?? []
  const isSaving = updateOption.isPending

  const handleSave = async () => {
    const nextContinuousPercent = normalizePercent(continuousPercent)
    const nextFirstTopupPercent = normalizePercent(firstTopupPercent)
    if (nextContinuousPercent === null || nextFirstTopupPercent === null) {
      toast.error(t('Rebate rates must be integers from 1 to 100.'))
      return
    }

    const updates: Array<{ key: string; value: number }> = []
    if (nextContinuousPercent !== props.defaultValues.continuousPercent) {
      updates.push({
        key: 'payment_setting.affiliate_continuous_percent',
        value: nextContinuousPercent,
      })
    }
    if (nextFirstTopupPercent !== props.defaultValues.firstTopupPercent) {
      updates.push({
        key: 'payment_setting.affiliate_first_topup_percent',
        value: nextFirstTopupPercent,
      })
    }

    if (updates.length === 0) {
      toast.info(t('No changes to save.'))
      return
    }

    for (const update of updates) {
      const response = await updateOption.mutateAsync(update)
      if (!response.success) return
    }
  }

  return (
    <div className='flex flex-col gap-4'>
      <Card data-card-hover='false' className='gap-0 py-0'>
        <CardHeader className='border-b p-4 !pb-4'>
          <div className='flex flex-wrap items-start justify-between gap-3'>
            <div className='min-w-0'>
              <CardTitle className='text-base'>
                {t('Affiliate Rebate Rates')}
              </CardTitle>
              <CardDescription className='mt-1 text-sm'>
                {t(
                  'New invitation bindings snapshot these rates at registration. Existing bindings keep their original rates.'
                )}
              </CardDescription>
            </div>
            <Button onClick={handleSave} disabled={isSaving} size='sm'>
              {t('Save')}
            </Button>
          </div>
        </CardHeader>
        <CardContent className='p-4'>
          <div className='grid gap-4 md:grid-cols-2'>
            <label className='flex flex-col gap-2'>
              <span className='text-sm font-medium'>
                {t('Continuous Rebate')}
              </span>
              <div className='flex items-center gap-2'>
                <Input
                  type='number'
                  min={1}
                  max={100}
                  value={continuousPercent}
                  onChange={(event) => setContinuousPercent(event.target.value)}
                />
                <RateBadge
                  percent={normalizePercent(continuousPercent) ?? 0}
                  tone='steady'
                />
              </div>
            </label>
            <label className='flex flex-col gap-2'>
              <span className='text-sm font-medium'>
                {t('First Top-up Rebate')}
              </span>
              <div className='flex items-center gap-2'>
                <Input
                  type='number'
                  min={1}
                  max={100}
                  value={firstTopupPercent}
                  onChange={(event) => setFirstTopupPercent(event.target.value)}
                />
                <RateBadge
                  percent={normalizePercent(firstTopupPercent) ?? 0}
                  tone='prime'
                />
              </div>
            </label>
          </div>
        </CardContent>
      </Card>

      <Card data-card-hover='false' className='gap-0 py-0'>
        <CardHeader className='border-b p-4 !pb-4'>
          <div className='flex items-start gap-3'>
            <div className='bg-muted flex size-9 shrink-0 items-center justify-center rounded-lg'>
              <Percent className='text-muted-foreground size-4' />
            </div>
            <div className='min-w-0'>
              <CardTitle className='text-base'>
                {t('Affiliate Rebate Overview')}
              </CardTitle>
              <CardDescription className='mt-1 text-sm'>
                {t(
                  'Counts come from invitation bindings; total rebates come from affiliate rebate records.'
                )}
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent className='space-y-4 p-4'>
          <div className='grid gap-3 md:grid-cols-3'>
            <StatTile
              icon={Users}
              label={t('Inviters')}
              value={String(summary?.inviter_count ?? 0)}
              loading={summaryQuery.isLoading}
            />
            <StatTile
              icon={Repeat2}
              label={t('Invited users')}
              value={String(summary?.invitee_count ?? 0)}
              loading={summaryQuery.isLoading}
            />
            <StatTile
              icon={BadgeDollarSign}
              label={t('Total Rebates')}
              value={formatQuota(summary?.total_reward_quota ?? 0)}
              loading={summaryQuery.isLoading}
            />
          </div>

          <div className='flex flex-col gap-2 lg:flex-row lg:items-center'>
            <NativeSelect
              value={searchField}
              onChange={(event) =>
                setSearchField(event.target.value as SearchField)
              }
              className='w-full lg:w-52'
            >
              {searchFields.map((field) => (
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
                placeholder={t('Search affiliate relations...')}
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
        </CardContent>
        <CardContent className='border-t p-0'>
          <RelationsTable
            relations={relations}
            loading={summaryQuery.isLoading || summaryQuery.isFetching}
          />
        </CardContent>
      </Card>
    </div>
  )
}
