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
import {
  BadgeDollarSign,
  CheckCircle2,
  ChevronsUpDown,
  Clock,
  EyeOff,
  Link2,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
  Users,
} from 'lucide-react'
import * as React from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { CopyButton } from '@/components/copy-button'
import { DataTableManualViewOptions } from '@/components/data-table'
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
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import {
  Sheet,
  SheetContent,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'
import { formatShanghaiTimestamp } from '@/features/invite-rewards/lib/activity-description'
import {
  getRewardActivities,
  getRewardRateSortValue,
} from '@/features/invite-rewards/lib/reward-display'
import { formatQuota, formatTimestamp } from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  activateInviteLinkBatch,
  createInviteLinkBatch,
  generateInviteLinkBatchRandomLink,
  getAffiliateRewardSummary,
  getInviteLinkBatches,
  updateInviteLinkBatch,
} from '../api'
import type {
  AffiliateRewardRelation,
  InviteLinkBatch,
  RewardActivity,
} from '../types'

type AffiliateRewardSettingsSectionProps = {
  defaultValues: {
    continuousPercent: number
    firstTopupPercent: number
  }
}

type SortDirection = 'asc' | 'desc'
type BatchSortKey = 'id' | 'name' | 'ratio' | 'link' | 'usage' | 'period'
type RelationSortKey = 'inviter' | 'invitee' | 'registered' | 'ratio' | 'reward'
type ActivityRuleForm = {
  key: string
  activity_detail: string
  type: RewardActivity['type']
  percent: number
}
type BatchForm = {
  id: number
  name: string
  code: string
  base_link: string
  start_time: string
  end_time: string
  description_mode: 'preset' | 'custom'
  preset_title: string
  preset_summary: string
  activity_rules: ActivityRuleForm[]
  custom_description: string
  is_active: boolean
}

const shanghaiDateTimeFormatter = new Intl.DateTimeFormat('sv-SE', {
  timeZone: 'Asia/Shanghai',
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
})

const defaultBatchForm: BatchForm = {
  id: 0,
  name: '',
  code: '',
  base_link: '',
  start_time: '',
  end_time: '',
  description_mode: 'preset',
  preset_title: '',
  preset_summary: '',
  activity_rules: [
    {
      key: 'default-first-topup',
      activity_detail: 'One-time Referral',
      type: 'first_topup',
      percent: 30,
    },
    {
      key: 'default-continuous',
      activity_detail: 'Continuous Referral',
      type: 'continuous',
      percent: 5,
    },
  ],
  custom_description: '',
  is_active: false,
}

const rewardBadgeClassMap: Record<RewardActivity['type'], string> = {
  first_topup:
    'border border-amber-200/45 bg-amber-50/35 !text-amber-700 dark:border-amber-900/40 dark:bg-amber-950/15 dark:!text-amber-300',
  continuous:
    'border border-emerald-200/40 bg-emerald-50/35 !text-emerald-700 dark:border-emerald-900/40 dark:bg-emerald-950/15 dark:!text-emerald-300',
}

function formatShanghaiDateTimeInput(timestamp: number) {
  if (!timestamp) return ''
  return shanghaiDateTimeFormatter.format(timestamp * 1000).replace(' ', 'T')
}

function parseShanghaiDateTimeInput(value: string) {
  if (!value.trim()) return 0
  const normalized =
    value.length === 16 ? `${value}:00+08:00` : `${value}+08:00`
  const parsed = Date.parse(normalized)
  if (Number.isNaN(parsed)) return 0
  return Math.floor(parsed / 1000)
}

function createActivityRuleForm(
  activityDetail: string,
  type: RewardActivity['type'],
  percent: number
): ActivityRuleForm {
  return {
    key:
      globalThis.crypto?.randomUUID?.() ??
      `${Date.now()}-${Math.random().toString(36).slice(2)}`,
    activity_detail: activityDetail,
    type,
    percent,
  }
}

function buildPresetDescription(form: BatchForm) {
  return JSON.stringify({
    title: form.preset_title,
    summary: form.preset_summary,
    details: form.activity_rules
      .map((rule) => ({
        title: rule.activity_detail.trim(),
        type: rule.type,
        percent: Number(rule.percent),
      }))
      .filter((rule) => rule.title),
  })
}

function parsePresetDescription(batch?: InviteLinkBatch) {
  if (!batch?.preset_description) {
    return { title: '', summary: '' }
  }
  try {
    const parsed = JSON.parse(batch.preset_description) as {
      title?: string
      summary?: string
    }
    return {
      title: parsed.title ?? '',
      summary: parsed.summary ?? '',
    }
  } catch {
    return { title: '', summary: '' }
  }
}

function rewardActivityLabel(
  activity: RewardActivity,
  t: (key: string) => string
) {
  const type = activity.type
  return type === 'first_topup' ? t('First Top-up') : t('Continuous')
}

function RewardActivityBadges(props: { activities: RewardActivity[] }) {
  const { t } = useTranslation()
  if (props.activities.length === 0) {
    return <span className='text-muted-foreground text-xs'>-</span>
  }
  return (
    <div className='flex flex-wrap gap-1.5'>
      {props.activities.map((activity) => (
        <StatusBadge
          key={`${activity.activity_detail}-${activity.type}-${activity.percent}`}
          label={`${rewardActivityLabel(activity, t)}${activity.percent}%`}
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

function SortableHeader<TSort extends string>(props: {
  label: string
  sortKey: TSort
  sort?: { key: TSort; direction: SortDirection }
  hidden?: boolean
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

function contributionText(
  relation: AffiliateRewardRelation,
  t: (key: string) => string
) {
  return [
    `${t('Pending Rewards')}: ${formatQuota(relation.pending_reward_quota)}`,
    `${t('Transferable Rewards')}: ${formatQuota(relation.available_reward_quota)}`,
    `${t('Transferred Rewards')}: ${formatQuota(relation.transferred_reward_quota)}`,
    `${t('Canceled Rewards')}: ${formatQuota(relation.canceled_reward_quota)}`,
  ].join(' / ')
}

export function AffiliateRewardSettingsSection(
  _props: AffiliateRewardSettingsSectionProps
) {
  const { t } = useTranslation()
  const [batchSort, setBatchSort] = React.useState<{
    key: BatchSortKey
    direction: SortDirection
  }>({ key: 'id', direction: 'desc' })
  const [relationSort, setRelationSort] = React.useState<{
    key: RelationSortKey
    direction: SortDirection
  }>({ key: 'registered', direction: 'desc' })
  const [hiddenBatchColumns, setHiddenBatchColumns] = React.useState<
    Partial<Record<BatchSortKey, boolean>>
  >({})
  const [hiddenRelationColumns, setHiddenRelationColumns] = React.useState<
    Partial<Record<RelationSortKey, boolean>>
  >({})
  const [drawerOpen, setDrawerOpen] = React.useState(false)
  const [form, setForm] = React.useState<BatchForm>(defaultBatchForm)
  const [saving, setSaving] = React.useState(false)
  const batchColumns = React.useMemo(
    () => [
      { id: 'id' as const, label: t('ID') },
      { id: 'name' as const, label: t('Name') },
      { id: 'ratio' as const, label: t('Reward Ratio') },
      { id: 'link' as const, label: t('Link') },
      { id: 'usage' as const, label: t('Using Users') },
      { id: 'period' as const, label: t('Valid Period') },
    ],
    [t]
  )
  const relationColumns = React.useMemo(
    () => [
      { id: 'inviter' as const, label: t('Inviter') },
      { id: 'invitee' as const, label: t('Invited user') },
      { id: 'registered' as const, label: t('Registered At') },
      { id: 'ratio' as const, label: t('Reward Overview') },
      { id: 'reward' as const, label: t('Contribution Rewards') },
    ],
    [t]
  )

  const batchesQuery = useQuery({
    queryKey: ['invite-link-batches'],
    queryFn: getInviteLinkBatches,
  })
  const summaryQuery = useQuery({
    queryKey: ['affiliate-reward-summary'],
    queryFn: () => getAffiliateRewardSummary(),
  })

  const batches = React.useMemo(() => {
    const rows = [...(batchesQuery.data?.data ?? [])]
    rows.sort((a, b) => {
      if (a.is_active !== b.is_active) return a.is_active ? -1 : 1
      let left: string | number = a.id
      let right: string | number = b.id
      if (batchSort.key === 'name') {
        left = a.name
        right = b.name
      }
      if (batchSort.key === 'ratio') {
        left = getRewardRateSortValue(a)
        right = getRewardRateSortValue(b)
      }
      if (batchSort.key === 'link') {
        left = a.base_link
        right = b.base_link
      }
      if (batchSort.key === 'usage') {
        left = a.usage_count ?? 0
        right = b.usage_count ?? 0
      }
      if (batchSort.key === 'period') {
        left = a.start_time
        right = b.start_time
      }
      const direction = batchSort.direction === 'asc' ? 1 : -1
      if (left > right) return direction
      if (left < right) return -direction
      return 0
    })
    return rows
  }, [batchesQuery.data?.data, batchSort])

  const relations = React.useMemo(() => {
    const rows = [...(summaryQuery.data?.data?.relations ?? [])]
    rows.sort((a, b) => {
      let left: string | number = a.registered_at
      let right: string | number = b.registered_at
      if (relationSort.key === 'inviter') {
        left = a.inviter_username
        right = b.inviter_username
      }
      if (relationSort.key === 'invitee') {
        left = a.invitee_username
        right = b.invitee_username
      }
      if (relationSort.key === 'ratio') {
        left = getRewardRateSortValue(a)
        right = getRewardRateSortValue(b)
      }
      if (relationSort.key === 'reward') {
        left = a.reward_quota
        right = b.reward_quota
      }
      const direction = relationSort.direction === 'asc' ? 1 : -1
      if (left > right) return direction
      if (left < right) return -direction
      return 0
    })
    return rows
  }, [summaryQuery.data?.data?.relations, relationSort])

  const summary = summaryQuery.data?.data

  function openCreateDrawer() {
    setForm(defaultBatchForm)
    setDrawerOpen(true)
  }

  function openEditDrawer(batch: InviteLinkBatch) {
    const preset = parsePresetDescription(batch)
    setForm({
      id: batch.id,
      name: batch.name,
      code: batch.code,
      base_link: batch.base_link,
      start_time: formatShanghaiDateTimeInput(batch.start_time),
      end_time: formatShanghaiDateTimeInput(batch.end_time),
      description_mode: batch.description_mode,
      preset_title: preset.title,
      preset_summary: preset.summary,
      activity_rules: getRewardActivities(batch).map((activity) =>
        createActivityRuleForm(
          activity.activity_detail,
          activity.type,
          activity.percent
        )
      ),
      custom_description: batch.custom_description,
      is_active: batch.is_active,
    })
    setDrawerOpen(true)
  }

  async function handleRandomLink() {
    const response = await generateInviteLinkBatchRandomLink()
    if (response.success && response.data) {
      setForm((current) => ({
        ...current,
        code: response.data?.code ?? current.code,
        base_link: response.data?.base_link ?? current.base_link,
      }))
    }
  }

  function addActivityRule() {
    setForm((current) => ({
      ...current,
      activity_rules: [
        ...current.activity_rules,
        createActivityRuleForm('', 'continuous', 5),
      ],
    }))
  }

  function updateActivityRule(index: number, patch: Partial<ActivityRuleForm>) {
    setForm((current) => ({
      ...current,
      activity_rules: current.activity_rules.map((detail, detailIndex) =>
        detailIndex === index ? { ...detail, ...patch } : detail
      ),
    }))
  }

  function removeActivityRule(index: number) {
    setForm((current) => ({
      ...current,
      activity_rules: current.activity_rules.filter(
        (_detail, detailIndex) => detailIndex !== index
      ),
    }))
  }

  async function handleSave() {
    if (!form.name.trim()) {
      toast.error(t('Please enter a name'))
      return
    }
    for (const rule of form.activity_rules) {
      if (!rule.activity_detail.trim()) {
        toast.error(t('Activity detail is required'))
        return
      }
      if (rule.type !== 'first_topup' && rule.type !== 'continuous') {
        toast.error(t('Activity type is required'))
        return
      }
      if (
        !Number.isFinite(Number(rule.percent)) ||
        Number(rule.percent) < 0 ||
        Number(rule.percent) > 100
      ) {
        toast.error(t('Reward ratio must be between 0 and 100'))
        return
      }
    }
    setSaving(true)
    try {
      const payload = {
        name: form.name,
        code: form.code,
        base_link: form.base_link,
        activity_rules: form.activity_rules.map((rule) => ({
          activity_detail: rule.activity_detail.trim(),
          type: rule.type,
          percent: Number(rule.percent),
        })),
        start_time: parseShanghaiDateTimeInput(form.start_time),
        end_time: parseShanghaiDateTimeInput(form.end_time),
        description_mode: form.description_mode,
        preset_description: buildPresetDescription(form),
        custom_description: form.custom_description,
        is_active: form.is_active,
      }
      const response =
        form.id > 0
          ? await updateInviteLinkBatch(form.id, payload)
          : await createInviteLinkBatch(payload)
      if (response.success) {
        toast.success(t('Saved successfully'))
        setDrawerOpen(false)
        await Promise.all([batchesQuery.refetch(), summaryQuery.refetch()])
      }
    } finally {
      setSaving(false)
    }
  }

  async function handleActivate(batch: InviteLinkBatch) {
    const response = await activateInviteLinkBatch(batch.id)
    if (response.success) {
      toast.success(t('Saved successfully'))
      await batchesQuery.refetch()
    }
  }

  return (
    <div className='flex flex-col gap-4'>
      <Card data-card-hover='false' className='gap-0 py-0'>
        <CardHeader className='border-b p-4 !pb-4'>
          <div className='flex flex-wrap items-center justify-between gap-3'>
            <div>
              <CardTitle className='text-base'>
                {t('Invitation Links')}
              </CardTitle>
              <CardDescription className='mt-1 text-sm'>
                {t('Create referral link batches and choose one active link.')}
              </CardDescription>
            </div>
            <div className='flex flex-wrap items-center justify-end gap-2'>
              <DataTableManualViewOptions
                columns={batchColumns}
                hiddenColumns={hiddenBatchColumns}
                onVisibilityChange={(column, visible) =>
                  setHiddenBatchColumns((current) => ({
                    ...current,
                    [column]: !visible,
                  }))
                }
              />
              <Button size='sm' onClick={openCreateDrawer}>
                <Plus data-icon='inline-start' />
                {t('Create')}
              </Button>
            </div>
          </div>
        </CardHeader>
        <CardContent className='p-0'>
          {batchesQuery.isLoading ? (
            <div className='space-y-2 p-4'>
              {['batch-1', 'batch-2', 'batch-3'].map((row) => (
                <Skeleton key={row} className='h-10 rounded-lg' />
              ))}
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  {!hiddenBatchColumns.id && (
                    <TableHead>
                      <SortableHeader
                        label={t('ID')}
                        sortKey='id'
                        sort={batchSort}
                        onSort={(key, direction) =>
                          setBatchSort({ key, direction })
                        }
                        onHide={(key) =>
                          setHiddenBatchColumns((current) => ({
                            ...current,
                            [key]: true,
                          }))
                        }
                      />
                    </TableHead>
                  )}
                  {!hiddenBatchColumns.name && (
                    <TableHead>
                      <SortableHeader
                        label={t('Name')}
                        sortKey='name'
                        sort={batchSort}
                        onSort={(key, direction) =>
                          setBatchSort({ key, direction })
                        }
                        onHide={(key) =>
                          setHiddenBatchColumns((current) => ({
                            ...current,
                            [key]: true,
                          }))
                        }
                      />
                    </TableHead>
                  )}
                  {!hiddenBatchColumns.ratio && (
                    <TableHead>
                      <SortableHeader
                        label={t('Reward Ratio')}
                        sortKey='ratio'
                        sort={batchSort}
                        onSort={(key, direction) =>
                          setBatchSort({ key, direction })
                        }
                        onHide={(key) =>
                          setHiddenBatchColumns((current) => ({
                            ...current,
                            [key]: true,
                          }))
                        }
                      />
                    </TableHead>
                  )}
                  {!hiddenBatchColumns.link && (
                    <TableHead>
                      <SortableHeader
                        label={t('Link')}
                        sortKey='link'
                        sort={batchSort}
                        onSort={(key, direction) =>
                          setBatchSort({ key, direction })
                        }
                        onHide={(key) =>
                          setHiddenBatchColumns((current) => ({
                            ...current,
                            [key]: true,
                          }))
                        }
                      />
                    </TableHead>
                  )}
                  {!hiddenBatchColumns.usage && (
                    <TableHead>
                      <SortableHeader
                        label={t('Using Users')}
                        sortKey='usage'
                        sort={batchSort}
                        onSort={(key, direction) =>
                          setBatchSort({ key, direction })
                        }
                        onHide={(key) =>
                          setHiddenBatchColumns((current) => ({
                            ...current,
                            [key]: true,
                          }))
                        }
                      />
                    </TableHead>
                  )}
                  {!hiddenBatchColumns.period && (
                    <TableHead>
                      <SortableHeader
                        label={t('Valid Period')}
                        sortKey='period'
                        sort={batchSort}
                        onSort={(key, direction) =>
                          setBatchSort({ key, direction })
                        }
                        onHide={(key) =>
                          setHiddenBatchColumns((current) => ({
                            ...current,
                            [key]: true,
                          }))
                        }
                      />
                    </TableHead>
                  )}
                  <TableHead>{t('Edit')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {batches.map((batch) => (
                  <TableRow
                    key={batch.id}
                    className={cn(batch.is_active && 'bg-primary/5')}
                  >
                    {!hiddenBatchColumns.id && (
                      <TableCell>{batch.id}</TableCell>
                    )}
                    {!hiddenBatchColumns.name && (
                      <TableCell>
                        <div className='flex min-w-0 flex-col gap-1'>
                          <span className='font-medium'>{batch.name}</span>
                          <span className='text-muted-foreground text-xs'>
                            {batch.is_active ? t('Active') : t('Inactive')}
                          </span>
                        </div>
                      </TableCell>
                    )}
                    {!hiddenBatchColumns.ratio && (
                      <TableCell className='max-w-[360px] whitespace-normal'>
                        <RewardActivityBadges
                          activities={getRewardActivities(batch)}
                        />
                      </TableCell>
                    )}
                    {!hiddenBatchColumns.link && (
                      <TableCell>
                        <div className='flex max-w-[240px] items-center gap-2'>
                          <span className='truncate font-mono text-xs'>
                            {batch.base_link}
                          </span>
                          <CopyButton
                            value={batch.base_link}
                            variant='ghost'
                            tooltip={t('Copy link')}
                            aria-label={t('Copy link')}
                          />
                        </div>
                      </TableCell>
                    )}
                    {!hiddenBatchColumns.usage && (
                      <TableCell>{batch.usage_count ?? 0}</TableCell>
                    )}
                    {!hiddenBatchColumns.period && (
                      <TableCell className='min-w-[180px] text-xs'>
                        <div>{formatShanghaiTimestamp(batch.start_time)}</div>
                        <div className='text-muted-foreground'>
                          {formatShanghaiTimestamp(batch.end_time)}
                        </div>
                      </TableCell>
                    )}
                    <TableCell>
                      <div className='flex items-center gap-1.5'>
                        {!batch.is_active && (
                          <Button
                            variant='ghost'
                            size='icon-sm'
                            onClick={() => handleActivate(batch)}
                            aria-label={t('Set as active')}
                          >
                            <CheckCircle2 />
                          </Button>
                        )}
                        <Button
                          variant='ghost'
                          size='icon-sm'
                          onClick={() => openEditDrawer(batch)}
                          aria-label={t('Edit')}
                        >
                          <Pencil />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card data-card-hover='false' className='gap-0 py-0'>
        <CardHeader className='border-b p-4 !pb-4'>
          <div className='flex flex-wrap items-center justify-between gap-3'>
            <CardTitle className='text-base'>
              {t('Invitation Reward Overview')}
            </CardTitle>
            <DataTableManualViewOptions
              columns={relationColumns}
              hiddenColumns={hiddenRelationColumns}
              onVisibilityChange={(column, visible) =>
                setHiddenRelationColumns((current) => ({
                  ...current,
                  [column]: !visible,
                }))
              }
            />
          </div>
        </CardHeader>
        <CardContent className='space-y-4 p-4'>
          <div className='grid gap-3 md:grid-cols-3'>
            <StatTile
              label={t('Inviters')}
              value={String(summary?.inviter_count ?? 0)}
              loading={summaryQuery.isLoading}
              icon={Users}
            />
            <StatTile
              label={t('Invited users')}
              value={String(summary?.invitee_count ?? 0)}
              loading={summaryQuery.isLoading}
              icon={Link2}
            />
            <StatTile
              label={t('Total Rewards')}
              value={formatQuota(summary?.total_reward_quota ?? 0)}
              loading={summaryQuery.isLoading}
              icon={BadgeDollarSign}
            />
          </div>
          <div className='grid gap-3 md:grid-cols-4'>
            <StatTile
              label={t('Pending Rewards')}
              value={formatQuota(summary?.pending_reward_quota ?? 0)}
              loading={summaryQuery.isLoading}
              icon={Clock}
            />
            <StatTile
              label={t('Transferable Rewards')}
              value={formatQuota(summary?.available_reward_quota ?? 0)}
              loading={summaryQuery.isLoading}
              icon={CheckCircle2}
            />
            <StatTile
              label={t('Transferred Rewards')}
              value={formatQuota(summary?.transferred_reward_quota ?? 0)}
              loading={summaryQuery.isLoading}
              icon={RefreshCw}
            />
            <StatTile
              label={t('Canceled Rewards')}
              value={formatQuota(summary?.canceled_reward_quota ?? 0)}
              loading={summaryQuery.isLoading}
              icon={EyeOff}
            />
          </div>
        </CardContent>
        <CardContent className='border-t p-0'>
          <Table>
            <TableHeader>
              <TableRow>
                {!hiddenRelationColumns.inviter && (
                  <TableHead>
                    <SortableHeader
                      label={t('Inviter')}
                      sortKey='inviter'
                      sort={relationSort}
                      onSort={(key, direction) =>
                        setRelationSort({ key, direction })
                      }
                      onHide={(key) =>
                        setHiddenRelationColumns((current) => ({
                          ...current,
                          [key]: true,
                        }))
                      }
                    />
                  </TableHead>
                )}
                {!hiddenRelationColumns.invitee && (
                  <TableHead>
                    <SortableHeader
                      label={t('Invited user')}
                      sortKey='invitee'
                      sort={relationSort}
                      onSort={(key, direction) =>
                        setRelationSort({ key, direction })
                      }
                      onHide={(key) =>
                        setHiddenRelationColumns((current) => ({
                          ...current,
                          [key]: true,
                        }))
                      }
                    />
                  </TableHead>
                )}
                {!hiddenRelationColumns.registered && (
                  <TableHead>
                    <SortableHeader
                      label={t('Registered At')}
                      sortKey='registered'
                      sort={relationSort}
                      onSort={(key, direction) =>
                        setRelationSort({ key, direction })
                      }
                      onHide={(key) =>
                        setHiddenRelationColumns((current) => ({
                          ...current,
                          [key]: true,
                        }))
                      }
                    />
                  </TableHead>
                )}
                {!hiddenRelationColumns.ratio && (
                  <TableHead>
                    <SortableHeader
                      label={t('Reward Overview')}
                      sortKey='ratio'
                      sort={relationSort}
                      onSort={(key, direction) =>
                        setRelationSort({ key, direction })
                      }
                      onHide={(key) =>
                        setHiddenRelationColumns((current) => ({
                          ...current,
                          [key]: true,
                        }))
                      }
                    />
                  </TableHead>
                )}
                {!hiddenRelationColumns.reward && (
                  <TableHead>
                    <SortableHeader
                      label={t('Contribution Rewards')}
                      sortKey='reward'
                      sort={relationSort}
                      onSort={(key, direction) =>
                        setRelationSort({ key, direction })
                      }
                      onHide={(key) =>
                        setHiddenRelationColumns((current) => ({
                          ...current,
                          [key]: true,
                        }))
                      }
                    />
                  </TableHead>
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {relations.map((relation) => (
                <TableRow key={`${relation.inviter_id}-${relation.invitee_id}`}>
                  {!hiddenRelationColumns.inviter && (
                    <TableCell className='font-medium'>
                      {relation.inviter_username}
                    </TableCell>
                  )}
                  {!hiddenRelationColumns.invitee && (
                    <TableCell className='font-medium'>
                      {relation.invitee_username}
                    </TableCell>
                  )}
                  {!hiddenRelationColumns.registered && (
                    <TableCell>
                      {formatTimestamp(relation.registered_at)}
                    </TableCell>
                  )}
                  {!hiddenRelationColumns.ratio && (
                    <TableCell className='max-w-[360px] whitespace-normal'>
                      <RewardActivityBadges
                        activities={getRewardActivities(relation)}
                      />
                    </TableCell>
                  )}
                  {!hiddenRelationColumns.reward && (
                    <TableCell className='max-w-[380px] whitespace-normal'>
                      <div className='text-muted-foreground text-xs leading-relaxed break-words'>
                        {contributionText(relation, t)}
                      </div>
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Sheet open={drawerOpen} onOpenChange={setDrawerOpen}>
        <SheetContent className='sm:max-w-[640px]'>
          <SheetHeader>
            <SheetTitle>
              {form.id > 0
                ? t('Edit Invitation Link')
                : t('Create Invitation Link')}
            </SheetTitle>
          </SheetHeader>
          <div className='flex-1 space-y-4 overflow-y-auto px-4'>
            <label className='flex flex-col gap-2'>
              <span className='text-sm font-medium'>{t('Name')}</span>
              <Input
                value={form.name}
                onChange={(event) =>
                  setForm((current) => ({
                    ...current,
                    name: event.target.value,
                  }))
                }
              />
            </label>
            <label className='flex flex-col gap-2'>
              <span className='text-sm font-medium'>
                {t('Base Invite Link')}
              </span>
              <div className='flex gap-2'>
                <Input
                  value={form.base_link}
                  onChange={(event) =>
                    setForm((current) => ({
                      ...current,
                      base_link: event.target.value,
                    }))
                  }
                />
                <Button
                  type='button'
                  variant='outline'
                  onClick={handleRandomLink}
                >
                  <RefreshCw data-icon='inline-start' />
                  {t('Random')}
                </Button>
              </div>
            </label>
            <div className='grid gap-3 md:grid-cols-2'>
              <label className='flex flex-col gap-2'>
                <span className='text-sm font-medium'>
                  {t('Start Time (Shanghai)')}
                </span>
                <Input
                  type='datetime-local'
                  value={form.start_time}
                  onChange={(event) =>
                    setForm((current) => ({
                      ...current,
                      start_time: event.target.value,
                    }))
                  }
                />
              </label>
              <label className='flex flex-col gap-2'>
                <span className='text-sm font-medium'>
                  {t('End Time (Shanghai)')}
                </span>
                <Input
                  type='datetime-local'
                  value={form.end_time}
                  onChange={(event) =>
                    setForm((current) => ({
                      ...current,
                      end_time: event.target.value,
                    }))
                  }
                />
              </label>
            </div>
            <label className='flex flex-col gap-2'>
              <span className='text-sm font-medium'>
                {t('Activity Description Mode')}
              </span>
              <NativeSelect
                value={form.description_mode}
                onChange={(event) =>
                  setForm((current) => ({
                    ...current,
                    description_mode: event.target.value as 'preset' | 'custom',
                  }))
                }
              >
                <NativeSelectOption value='preset'>
                  {t('Preset')}
                </NativeSelectOption>
                <NativeSelectOption value='custom'>
                  {t('Custom')}
                </NativeSelectOption>
              </NativeSelect>
            </label>
            {form.description_mode === 'preset' ? (
              <div className='space-y-3'>
                <Input
                  value={form.preset_title}
                  placeholder={t('Activity title')}
                  onChange={(event) =>
                    setForm((current) => ({
                      ...current,
                      preset_title: event.target.value,
                    }))
                  }
                />
                <Textarea
                  value={form.preset_summary}
                  placeholder={t('Activity overview')}
                  onChange={(event) =>
                    setForm((current) => ({
                      ...current,
                      preset_summary: event.target.value,
                    }))
                  }
                />
              </div>
            ) : (
              <Textarea
                value={form.custom_description}
                placeholder={t('Markdown and HTML are supported')}
                className='min-h-40'
                onChange={(event) =>
                  setForm((current) => ({
                    ...current,
                    custom_description: event.target.value,
                  }))
                }
              />
            )}
            <div className='space-y-2'>
              <div className='flex items-center justify-between gap-2'>
                <span className='text-sm font-medium'>
                  {t('Activity details')}
                </span>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={addActivityRule}
                >
                  <Plus data-icon='inline-start' />
                  {t('Add detail')}
                </Button>
              </div>
              <div className='space-y-2'>
                {form.activity_rules.map((detail, index) => (
                  <div
                    key={detail.key}
                    className='grid gap-2 rounded-lg border p-3 md:grid-cols-[minmax(0,1fr)_120px_100px_32px]'
                  >
                    <Input
                      value={detail.activity_detail}
                      placeholder={t('Activity detail')}
                      aria-label={t('Activity detail')}
                      maxLength={255}
                      onChange={(event) =>
                        updateActivityRule(index, {
                          activity_detail: event.target.value,
                        })
                      }
                    />
                    <NativeSelect
                      value={detail.type}
                      aria-label={t('Type')}
                      onChange={(event) =>
                        updateActivityRule(index, {
                          type: event.target.value as RewardActivity['type'],
                        })
                      }
                    >
                      <NativeSelectOption value='first_topup'>
                        {t('First Top-up')}
                      </NativeSelectOption>
                      <NativeSelectOption value='continuous'>
                        {t('Continuous')}
                      </NativeSelectOption>
                    </NativeSelect>
                    <Input
                      type='number'
                      min={0}
                      max={100}
                      value={detail.percent}
                      aria-label={t('Reward Ratio')}
                      onChange={(event) =>
                        updateActivityRule(index, {
                          percent: Number(event.target.value),
                        })
                      }
                    />
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon-sm'
                      onClick={() => removeActivityRule(index)}
                      aria-label={t('Remove detail')}
                    >
                      <Trash2 />
                    </Button>
                  </div>
                ))}
              </div>
            </div>
            <label className='flex items-center justify-between rounded-lg border p-3'>
              <span className='text-sm font-medium'>
                {t('Set as active invite link')}
              </span>
              <Switch
                checked={form.is_active}
                onCheckedChange={(checked) =>
                  setForm((current) => ({ ...current, is_active: checked }))
                }
              />
            </label>
          </div>
          <SheetFooter>
            <Button onClick={handleSave} disabled={saving}>
              {t('Save')}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </div>
  )
}
