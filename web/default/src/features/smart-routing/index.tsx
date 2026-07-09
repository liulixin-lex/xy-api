import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Activity,
  Bot,
  Check,
  DatabaseZap,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  Route,
  ShieldAlert,
  Trash2,
  X,
} from 'lucide-react'
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
import { useEffect, useMemo, useState, type ChangeEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { SectionPageLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
  FieldTitle,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { getChannels } from '@/features/channels/api'
import type { Channel } from '@/features/channels/types'
import { toIntlLocale } from '@/i18n/languages'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'
import {
  formatNumber,
  formatTimestampRelative,
  formatTimestampToDate,
} from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  approveSmartRoutingAgentRecommendation,
  createSmartRoutingBinding,
  deleteSmartRoutingBinding,
  enqueueSmartRoutingSync,
  listSmartRoutingAgentRecommendations,
  listSmartRoutingBindings,
  listSmartRoutingBreakers,
  listSmartRoutingMetrics,
  listSmartRoutingSnapshots,
  loadSmartRoutingBindingGroups,
  rejectSmartRoutingAgentRecommendation,
  resetSmartRoutingBreaker,
  testSmartRoutingBinding,
  updateSmartRoutingBinding,
  getSmartRoutingSettings,
  updateSmartRoutingSettings,
} from './api'
import type {
  RoutingAgentRecommendation,
  RoutingBinding,
  RoutingBindingActionResult,
  RoutingBindingRequest,
  RoutingBreaker,
  RoutingCostSnapshot,
  RoutingMetric,
  SmartRoutingMode,
  SmartRoutingSettings,
} from './types'

const QUERY_STALE_MS = 30 * 1000
const TABLE_INITIAL_LIMIT = 50
const TABLE_LIMIT_STEP = 50
const TABLE_MAX_LIMIT = 500

const EMPTY_BINDING_REQUEST: RoutingBindingRequest = {
  channel_id: 0,
  upstream_type: 'newapi',
  base_url: '',
  upstream_group: 'default',
  serves_claude_code: false,
  enabled: true,
  credentials: {},
}

const modeOptions: Array<{ value: SmartRoutingMode; label: string }> = [
  { value: 'observe', label: 'Observe' },
  { value: 'shadow', label: 'Shadow' },
  { value: 'balanced', label: 'Balanced' },
  { value: 'enterprise_slo', label: 'Enterprise SLO' },
]

const upstreamOptions = [
  { value: 'newapi', label: 'New API' },
  { value: 'sub2api', label: 'Sub2API' },
] as const

const enterpriseSLOWeights = {
  weight_availability: 0.55,
  weight_latency: 0.3,
  weight_throughput: 0.1,
  weight_cost: 0.05,
} as const

function requireData<T>(response: {
  success: boolean
  message?: string
  data: T
}) {
  if (!response.success) {
    throw new Error(response.message || 'Request failed')
  }
  return response.data
}

function compactJson(raw: string) {
  if (!raw) return '-'
  try {
    return JSON.stringify(JSON.parse(raw))
  } catch {
    return raw
  }
}

function averageLatency(metric: RoutingMetric) {
  if (metric.request_count <= 0) return null
  return metric.total_latency_ms / metric.request_count
}

function successRate(metric: RoutingMetric) {
  if (metric.request_count <= 0) return null
  return (metric.success_count / metric.request_count) * 100
}

function costValue(snapshot: RoutingCostSnapshot) {
  if (snapshot.model_price > 0) return snapshot.model_price
  return snapshot.group_ratio * snapshot.base_ratio
}

function confidenceLabel(confidence: string) {
  if (confidence === 'full') return 'Full'
  if (confidence === 'group_only') return 'Group only'
  if (confidence === 'unknown') return 'Unknown'
  return confidence
}

function translatedModeLabel(
  mode: SmartRoutingMode | undefined,
  t: (key: string) => string
) {
  switch (mode) {
    case 'observe':
      return t('Observe')
    case 'shadow':
      return t('Shadow')
    case 'balanced':
      return t('Balanced')
    case 'enterprise_slo':
      return t('Enterprise SLO')
    default:
      return ''
  }
}

function translatedRecommendationStatus(
  status: string | undefined,
  t: (key: string) => string
) {
  switch (status) {
    case 'pending':
      return t('Pending')
    case 'approved':
      return t('Approved')
    case 'rejected':
      return t('Rejected')
    case 'auto_applied':
      return t('Auto applied')
    default:
      return status || '-'
  }
}

function translatedSeverity(
  severity: string | undefined,
  t: (key: string) => string
) {
  switch (severity) {
    case 'critical':
      return t('Critical')
    case 'high':
      return t('High')
    case 'medium':
      return t('Medium')
    case 'low':
      return t('Low')
    default:
      return severity || '-'
  }
}

function toBindingRequest(binding: RoutingBinding): RoutingBindingRequest {
  return {
    channel_id: binding.channel_id,
    upstream_type: binding.upstream_type,
    base_url: binding.base_url,
    upstream_group: binding.upstream_group,
    serves_claude_code:
      binding.upstream_type === 'sub2api' ? binding.serves_claude_code : false,
    new_api_user_id: binding.new_api_user_id,
    enabled: binding.enabled,
    credentials: {},
  }
}

function normalizedBindingRequest(
  request: RoutingBindingRequest
): RoutingBindingRequest {
  const credentials = Object.fromEntries(
    Object.entries(request.credentials).filter(([, value]) => {
      if (typeof value !== 'string') return false
      return value.trim() !== ''
    })
  )
  return {
    ...request,
    base_url: request.base_url.trim(),
    upstream_group: request.upstream_group.trim(),
    serves_claude_code:
      request.upstream_type === 'sub2api' ? request.serves_claude_code : false,
    new_api_user_id:
      request.upstream_type === 'newapi' &&
      typeof request.new_api_user_id === 'number'
        ? request.new_api_user_id
        : undefined,
    credentials,
  }
}

function statusBadgeVariant(state: string) {
  if (state === 'open') return 'destructive'
  if (state === 'healthy') return 'default'
  return 'secondary'
}

function Panel(props: {
  title: string
  description?: string
  icon?: React.ComponentType<{ className?: string; 'aria-hidden'?: boolean }>
  action?: React.ReactNode
  children: React.ReactNode
  className?: string
}) {
  const Icon = props.icon
  return (
    <section
      className={cn(
        'bg-card overflow-hidden rounded-lg border shadow-xs',
        props.className
      )}
    >
      <div className='flex flex-col gap-3 border-b px-4 py-3 sm:flex-row sm:items-center sm:justify-between sm:px-5'>
        <div className='flex min-w-0 items-start gap-2.5'>
          {Icon != null && (
            <span className='bg-muted text-muted-foreground mt-0.5 inline-flex size-7 shrink-0 items-center justify-center rounded-md'>
              <Icon className='size-4' aria-hidden />
            </span>
          )}
          <div className='min-w-0'>
            <h3 className='text-sm font-semibold'>{props.title}</h3>
            {props.description != null && (
              <p className='text-muted-foreground mt-0.5 text-xs'>
                {props.description}
              </p>
            )}
          </div>
        </div>
        {props.action != null && (
          <div className='flex shrink-0 items-center gap-2'>{props.action}</div>
        )}
      </div>
      <div>{props.children}</div>
    </section>
  )
}

function TableEmpty(props: { title: string; description: string }) {
  return (
    <Empty className='min-h-[240px] border-0'>
      <EmptyHeader>
        <EmptyMedia variant='icon'>
          <Route className='size-4' aria-hidden />
        </EmptyMedia>
        <EmptyTitle>{props.title}</EmptyTitle>
        <EmptyDescription>{props.description}</EmptyDescription>
      </EmptyHeader>
    </Empty>
  )
}

function LoadingRows() {
  return (
    <div className='flex flex-col gap-2 p-4 sm:p-5'>
      {Array.from(
        { length: 6 },
        (_, index) => `smart-routing-skeleton-${index}`
      ).map((key) => (
        <Skeleton key={key} className='h-9 w-full rounded-md' />
      ))}
    </div>
  )
}

function LoadMoreFooter(props: {
  loadedCount: number
  limit: number
  isFetching: boolean
  onLoadMore: () => void
}) {
  const { t } = useTranslation()
  const canLoadMore =
    props.loadedCount >= props.limit && props.limit < TABLE_MAX_LIMIT

  if (props.loadedCount === 0 || !canLoadMore) return null

  return (
    <div className='flex flex-col gap-2 border-t px-4 py-3 text-xs sm:flex-row sm:items-center sm:justify-between sm:px-5'>
      <div className='text-muted-foreground'>
        <span>{t('Loaded {{count}} rows', { count: props.loadedCount })}</span>
        <span className='hidden sm:inline'> · </span>
        <span className='block sm:inline'>
          {t('Increase the limit to inspect older rows.')}
        </span>
      </div>
      <Button
        type='button'
        variant='outline'
        size='sm'
        onClick={props.onLoadMore}
        disabled={props.isFetching}
        aria-label={t('Load more')}
      >
        {props.isFetching ? t('Loading') : t('Load more')}
      </Button>
    </div>
  )
}

function NumericInput(props: {
  id: string
  value: number
  min?: number
  max?: number
  step?: number
  disabled?: boolean
  onChange: (value: number) => void
}) {
  const handleChange = (event: ChangeEvent<HTMLInputElement>) => {
    const value = Number(event.target.value)
    if (!Number.isFinite(value)) return
    props.onChange(value)
  }

  return (
    <Input
      id={props.id}
      type='number'
      value={props.value}
      min={props.min}
      max={props.max}
      step={props.step}
      disabled={props.disabled}
      onChange={handleChange}
    />
  )
}

function SettingsPanel(props: {
  settings?: SmartRoutingSettings
  isLoading: boolean
  isError: boolean
  error: unknown
  onRetry: () => void
  canWrite: boolean
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<SmartRoutingSettings | null>(null)

  useEffect(() => {
    if (props.settings) setDraft(props.settings)
  }, [props.settings])

  const updateMutation = useMutation({
    mutationFn: updateSmartRoutingSettings,
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(
          response.message || t('Failed to save smart routing settings')
        )
        return
      }
      setDraft(response.data)
      queryClient.setQueryData(['smart-routing', 'settings'], response.data)
      toast.success(t('Smart routing settings saved'))
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to save smart routing settings'))
    },
  })

  const setField = <K extends keyof SmartRoutingSettings>(
    key: K,
    value: SmartRoutingSettings[K]
  ) => {
    setDraft((current) => (current ? { ...current, [key]: value } : current))
  }

  if (props.isLoading) return <LoadingRows />
  if (props.isError) {
    return (
      <ErrorState
        title={t('We could not load smart routing settings.')}
        description={
          props.error instanceof Error ? props.error.message : undefined
        }
        onRetry={props.onRetry}
        className='min-h-[260px]'
      />
    )
  }
  if (!draft) {
    return (
      <TableEmpty
        title={t('No smart routing settings found')}
        description={t(
          'Reload the page after the backend initializes routing settings.'
        )}
      />
    )
  }
  const formDisabled = !props.canWrite || updateMutation.isPending
  const enterpriseWeightsLocked = draft.mode === 'enterprise_slo'
  const agentControlsDisabled = true

  return (
    <form
      className='flex flex-col gap-5 p-4 sm:p-5'
      onSubmit={(event) => {
        event.preventDefault()
        if (!props.canWrite) {
          toast.error(t('No permission to perform this action'))
          return
        }
        updateMutation.mutate(draft)
      }}
    >
      {!props.canWrite && (
        <div className='bg-muted/50 text-muted-foreground rounded-md px-3 py-2 text-sm'>
          {t('Smart Routing settings are read-only for your role.')}
        </div>
      )}
      <FieldSet disabled={formDisabled}>
        <FieldLegend>{t('Routing Mode')}</FieldLegend>
        <FieldGroup className='grid gap-4 lg:grid-cols-2'>
          <Field
            orientation='horizontal'
            className='items-center justify-between'
          >
            <div className='min-w-0'>
              <FieldTitle>{t('Enable Smart Routing')}</FieldTitle>
              <FieldDescription>
                {t(
                  'Routes eligible requests through ranked provider candidates.'
                )}
              </FieldDescription>
            </div>
            <Switch
              aria-label={t('Enable Smart Routing')}
              checked={draft.enabled}
              onCheckedChange={(value) => setField('enabled', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='smart-routing-mode'>{t('Mode')}</FieldLabel>
            <Select
              items={modeOptions}
              value={draft.mode}
              onValueChange={(value) => {
                if (!value) return
                setDraft((current) => {
                  if (!current) return current
                  if (value === 'enterprise_slo') {
                    return {
                      ...current,
                      mode: value as SmartRoutingMode,
                      ...enterpriseSLOWeights,
                    }
                  }
                  return { ...current, mode: value as SmartRoutingMode }
                })
              }}
            >
              <SelectTrigger id='smart-routing-mode' className='w-full'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent alignItemWithTrigger={false}>
                <SelectGroup>
                  {modeOptions.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {translatedModeLabel(option.value, t)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
          </Field>
          <Field>
            <FieldLabel htmlFor='smart-routing-agent-model'>
              {t('Agent Model')}
            </FieldLabel>
            <Input
              id='smart-routing-agent-model'
              value={draft.agent_model}
              disabled={agentControlsDisabled}
              onChange={(event) => setField('agent_model', event.target.value)}
            />
            <FieldDescription>
              {t(
                'Agent recommendations are planned for v2 and are currently read-only.'
              )}
            </FieldDescription>
          </Field>
          <Field
            orientation='horizontal'
            className='items-center justify-between'
          >
            <div className='min-w-0'>
              <FieldTitle>{t('Enable Agent Recommendations')}</FieldTitle>
              <FieldDescription>
                {t('Let the routing agent propose configuration changes.')}
              </FieldDescription>
            </div>
            <Switch
              aria-label={t('Enable Agent Recommendations')}
              checked={draft.agent_enabled}
              disabled={agentControlsDisabled}
              onCheckedChange={(value) => setField('agent_enabled', value)}
            />
          </Field>
          <Field
            orientation='horizontal'
            className='items-center justify-between'
          >
            <div className='min-w-0'>
              <FieldTitle>{t('Auto Apply Agent Changes')}</FieldTitle>
              <FieldDescription>
                {t(
                  'Apply approved routing agent changes without manual edits.'
                )}
              </FieldDescription>
            </div>
            <Switch
              aria-label={t('Auto Apply Agent Changes')}
              checked={draft.agent_auto_apply}
              disabled={agentControlsDisabled}
              onCheckedChange={(value) => setField('agent_auto_apply', value)}
            />
          </Field>
        </FieldGroup>
      </FieldSet>

      <FieldSet disabled={formDisabled}>
        <FieldLegend>{t('Scoring')}</FieldLegend>
        {enterpriseWeightsLocked && (
          <div className='text-muted-foreground flex flex-col gap-1 text-sm'>
            <FieldDescription>
              {t('Enterprise SLO uses fixed scoring weights.')}
            </FieldDescription>
            <FieldDescription>
              {t(
                'Enterprise SLO mode is enforced by the backend; scoring weights are fixed at 55% availability, 30% latency, 10% throughput, and 5% cost.'
              )}
            </FieldDescription>
          </div>
        )}
        <FieldGroup className='grid gap-4 lg:grid-cols-4'>
          <Field>
            <FieldLabel htmlFor='weight-availability'>
              {t('Availability Weight')}
            </FieldLabel>
            <NumericInput
              id='weight-availability'
              value={draft.weight_availability}
              min={0}
              step={0.01}
              disabled={enterpriseWeightsLocked}
              onChange={(value) => setField('weight_availability', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='weight-latency'>
              {t('Latency Weight')}
            </FieldLabel>
            <NumericInput
              id='weight-latency'
              value={draft.weight_latency}
              min={0}
              step={0.01}
              disabled={enterpriseWeightsLocked}
              onChange={(value) => setField('weight_latency', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='weight-throughput'>
              {t('Throughput Weight')}
            </FieldLabel>
            <NumericInput
              id='weight-throughput'
              value={draft.weight_throughput}
              min={0}
              step={0.01}
              disabled={enterpriseWeightsLocked}
              onChange={(value) => setField('weight_throughput', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='weight-cost'>{t('Cost Weight')}</FieldLabel>
            <NumericInput
              id='weight-cost'
              value={draft.weight_cost}
              min={0}
              step={0.01}
              disabled={enterpriseWeightsLocked}
              onChange={(value) => setField('weight_cost', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='availability-floor'>
              {t('Availability Floor')}
            </FieldLabel>
            <NumericInput
              id='availability-floor'
              value={draft.availability_floor}
              min={0}
              max={1}
              step={0.01}
              onChange={(value) => setField('availability_floor', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='min-volume'>{t('Minimum Volume')}</FieldLabel>
            <NumericInput
              id='min-volume'
              value={draft.min_volume}
              min={0}
              step={1}
              onChange={(value) => setField('min_volume', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='top-k'>{t('Candidate Top K')}</FieldLabel>
            <NumericInput
              id='top-k'
              value={draft.top_k}
              min={1}
              step={1}
              onChange={(value) => setField('top_k', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='balance-margin'>
              {t('Balance Margin USD')}
            </FieldLabel>
            <NumericInput
              id='balance-margin'
              value={draft.balance_margin_usd}
              min={0}
              step={0.01}
              onChange={(value) => setField('balance_margin_usd', value)}
            />
          </Field>
        </FieldGroup>
      </FieldSet>

      <FieldSet disabled={formDisabled}>
        <FieldLegend>{t('Breaker and Retry')}</FieldLegend>
        <FieldGroup className='grid gap-4 lg:grid-cols-4'>
          <Field>
            <FieldLabel htmlFor='consecutive-5xx'>
              {t('Consecutive 5xx')}
            </FieldLabel>
            <NumericInput
              id='consecutive-5xx'
              value={draft.consecutive_5xx}
              min={1}
              step={1}
              onChange={(value) => setField('consecutive_5xx', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='failure-rate'>
              {t('Failure Rate Percent')}
            </FieldLabel>
            <NumericInput
              id='failure-rate'
              value={draft.failure_rate_pct}
              min={1}
              max={100}
              step={1}
              onChange={(value) => setField('failure_rate_pct', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='base-cooldown'>
              {t('Base Cooldown Seconds')}
            </FieldLabel>
            <NumericInput
              id='base-cooldown'
              value={draft.base_cooldown_sec}
              min={1}
              step={1}
              onChange={(value) => setField('base_cooldown_sec', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='max-cooldown'>
              {t('Max Cooldown Seconds')}
            </FieldLabel>
            <NumericInput
              id='max-cooldown'
              value={draft.max_cooldown_sec}
              min={1}
              step={1}
              onChange={(value) => setField('max_cooldown_sec', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='max-ejected'>
              {t('Max Ejected Percent')}
            </FieldLabel>
            <NumericInput
              id='max-ejected'
              value={draft.max_ejected_pct}
              min={1}
              max={100}
              step={1}
              onChange={(value) => setField('max_ejected_pct', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='half-open-probes'>
              {t('Half-open Probes')}
            </FieldLabel>
            <NumericInput
              id='half-open-probes'
              value={draft.half_open_probes}
              min={1}
              step={1}
              onChange={(value) => setField('half_open_probes', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='max-switches'>{t('Max Switches')}</FieldLabel>
            <NumericInput
              id='max-switches'
              value={draft.max_switches}
              min={0}
              step={1}
              onChange={(value) => setField('max_switches', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='backoff-base-5xx'>
              {t('5xx Backoff Base Milliseconds')}
            </FieldLabel>
            <NumericInput
              id='backoff-base-5xx'
              value={draft.backoff_base_ms_5xx}
              min={1}
              step={100}
              onChange={(value) => setField('backoff_base_ms_5xx', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='backoff-base-429'>
              {t('429 Backoff Base Milliseconds')}
            </FieldLabel>
            <NumericInput
              id='backoff-base-429'
              value={draft.backoff_base_ms_429}
              min={1}
              step={100}
              onChange={(value) => setField('backoff_base_ms_429', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='backoff-cap'>
              {t('Backoff Cap Milliseconds')}
            </FieldLabel>
            <NumericInput
              id='backoff-cap'
              value={draft.backoff_cap_ms}
              min={1}
              step={100}
              onChange={(value) => setField('backoff_cap_ms', value)}
            />
          </Field>
        </FieldGroup>
      </FieldSet>

      <FieldSet disabled={formDisabled}>
        <FieldLegend>{t('Synchronization')}</FieldLegend>
        <FieldGroup className='grid gap-4 lg:grid-cols-4'>
          <Field>
            <FieldLabel htmlFor='sync-interval'>
              {t('Sync Interval Minutes')}
            </FieldLabel>
            <NumericInput
              id='sync-interval'
              value={draft.sync_interval_min}
              min={1}
              step={1}
              onChange={(value) => setField('sync_interval_min', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='hotcache-refresh'>
              {t('Hot Cache Refresh Seconds')}
            </FieldLabel>
            <NumericInput
              id='hotcache-refresh'
              value={draft.hotcache_refresh_sec}
              min={1}
              step={1}
              onChange={(value) => setField('hotcache_refresh_sec', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='metric-bucket'>
              {t('Metric Bucket Seconds')}
            </FieldLabel>
            <NumericInput
              id='metric-bucket'
              value={draft.metric_bucket_sec}
              min={1}
              step={1}
              onChange={(value) => setField('metric_bucket_sec', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='snapshot-live'>
              {t('Snapshot Live Seconds')}
            </FieldLabel>
            <NumericInput
              id='snapshot-live'
              value={draft.snapshot_live_sec}
              min={1}
              step={1}
              onChange={(value) => setField('snapshot_live_sec', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='snapshot-stale'>
              {t('Snapshot Stale Seconds')}
            </FieldLabel>
            <NumericInput
              id='snapshot-stale'
              value={draft.snapshot_stale_sec}
              min={1}
              step={1}
              onChange={(value) => setField('snapshot_stale_sec', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='flush-interval'>
              {t('Flush Interval Minutes')}
            </FieldLabel>
            <NumericInput
              id='flush-interval'
              value={draft.flush_interval_min}
              min={1}
              step={1}
              onChange={(value) => setField('flush_interval_min', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='retention-days'>
              {t('Retention Days')}
            </FieldLabel>
            <NumericInput
              id='retention-days'
              value={draft.retention_days}
              min={1}
              step={1}
              onChange={(value) => setField('retention_days', value)}
            />
          </Field>
          <Field
            orientation='horizontal'
            className='items-center justify-between lg:col-span-2'
          >
            <div className='min-w-0'>
              <FieldTitle>{t('First-byte Failover')}</FieldTitle>
              <FieldDescription>
                {t('Switch channels when streaming first byte is delayed.')}
              </FieldDescription>
            </div>
            <Switch
              aria-label={t('First-byte Failover')}
              checked={draft.first_byte_failover_enabled}
              onCheckedChange={(value) =>
                setField('first_byte_failover_enabled', value)
              }
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='first-byte-min'>
              {t('First-byte Minimum Milliseconds')}
            </FieldLabel>
            <NumericInput
              id='first-byte-min'
              value={draft.first_byte_min_ms}
              min={1}
              step={100}
              onChange={(value) => setField('first_byte_min_ms', value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='first-byte-p95-multiplier'>
              {t('First-byte P95 Multiplier')}
            </FieldLabel>
            <NumericInput
              id='first-byte-p95-multiplier'
              value={draft.first_byte_p95_multiplier}
              min={1}
              step={0.1}
              onChange={(value) =>
                setField('first_byte_p95_multiplier', value)
              }
            />
          </Field>
          <Field>
            <FieldLabel htmlFor='first-byte-cap'>
              {t('First-byte Cap Milliseconds')}
            </FieldLabel>
            <NumericInput
              id='first-byte-cap'
              value={draft.first_byte_cap_ms}
              min={1}
              step={100}
              onChange={(value) => setField('first_byte_cap_ms', value)}
            />
          </Field>
        </FieldGroup>
      </FieldSet>

      <div className='flex justify-end gap-2 border-t pt-4'>
        <Button
          type='button'
          variant='outline'
          onClick={() => props.settings && setDraft(props.settings)}
          disabled={updateMutation.isPending || !props.canWrite}
        >
          {t('Reset')}
        </Button>
        <Button
          type='submit'
          disabled={updateMutation.isPending || !props.canWrite}
        >
          <Check data-icon='inline-start' />
          {updateMutation.isPending ? t('Saving...') : t('Save Settings')}
        </Button>
      </div>
    </form>
  )
}

function BindingDialog(props: {
  open: boolean
  binding: RoutingBinding | null
  channels: Channel[]
  snapshots: RoutingCostSnapshot[]
  groupResults: Record<number, RoutingBindingActionResult>
  isLoadingGroups: boolean
  isLoadingChannels: boolean
  onOpenChange: (open: boolean) => void
  onSubmit: (request: RoutingBindingRequest, original?: RoutingBinding) => void
  onLoadGroups: (
    request: RoutingBindingRequest,
    original?: RoutingBinding
  ) => void
  isPending: boolean
  canOperate: boolean
  canSensitiveWrite: boolean
}) {
  const { i18n, t } = useTranslation()
  const [form, setForm] = useState<RoutingBindingRequest>(EMPTY_BINDING_REQUEST)
  const locale = toIntlLocale(i18n.language)

  useEffect(() => {
    if (!props.open) return
    setForm(
      props.binding ? toBindingRequest(props.binding) : EMPTY_BINDING_REQUEST
    )
  }, [props.open, props.binding])

  const setCredential = (
    key: keyof RoutingBindingRequest['credentials'],
    value: string
  ) => {
    setForm((current) => ({
      ...current,
      credentials: {
        ...current.credentials,
        [key]: value,
      },
    }))
  }
  const groupOptions = props.groupResults[form.channel_id]?.groups ?? []
  const upstreamType = form.upstream_type
  const credentialMasks = props.binding?.credential_masks
  const credentialMaskText = (key: keyof RoutingBinding['credential_masks']) =>
    credentialMasks?.[key] ? `${t('Current:')} ${credentialMasks[key]}` : ''
  const selectedSnapshot = useMemo(() => {
    return props.snapshots
      .filter((snapshot) => snapshot.channel_id === form.channel_id)
      .sort((left, right) => right.snapshot_ts - left.snapshot_ts)[0]
  }, [form.channel_id, props.snapshots])
  let channelDescription = t(
    'Only channels without a smart routing binding are shown.'
  )
  if (props.binding) {
    channelDescription = t('Channel cannot be changed after binding creation.')
  } else if (props.channels.length === 0 && !props.isLoadingChannels) {
    channelDescription = t('No unbound channels')
  }

  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className='max-h-[calc(100dvh-1rem)] w-[calc(100vw-1rem)] overflow-y-auto sm:max-h-[calc(100dvh-2rem)] sm:max-w-2xl'>
        <DialogHeader>
          <DialogTitle>
            {props.binding
              ? t('Edit Routing Binding')
              : t('Create Routing Binding')}
          </DialogTitle>
          <DialogDescription>
            {t(
              'Credentials are write-only and saved values are shown as masks.'
            )}
          </DialogDescription>
        </DialogHeader>
        <form
          className='flex flex-col gap-4'
          onSubmit={(event) => {
            event.preventDefault()
            props.onSubmit(
              normalizedBindingRequest(form),
              props.binding ?? undefined
            )
          }}
        >
          <FieldGroup className='grid gap-4 sm:grid-cols-2'>
            <Field>
              <FieldLabel htmlFor='binding-channel-id'>
                {t('Channel ID')}
              </FieldLabel>
              {props.binding ? (
                <Input
                  id='binding-channel-id'
                  type='number'
                  min={1}
                  value={form.channel_id}
                  disabled
                />
              ) : (
                <Select
                  items={props.channels.map((channel) => ({
                    value: String(channel.id),
                    label: `#${channel.id} ${channel.name}`,
                  }))}
                  value={form.channel_id > 0 ? String(form.channel_id) : ''}
                  onValueChange={(value) =>
                    setForm((current) => ({
                      ...current,
                      channel_id: value ? Number(value) : 0,
                    }))
                  }
                  disabled={
                    props.isLoadingChannels || props.channels.length === 0
                  }
                >
                  <SelectTrigger id='binding-channel-id' className='w-full'>
                    <SelectValue
                      placeholder={
                        props.isLoadingChannels
                          ? t('Loading channels')
                          : t('Select channel')
                      }
                    />
                  </SelectTrigger>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      {props.channels.map((channel) => (
                        <SelectItem key={channel.id} value={String(channel.id)}>
                          <span className='font-mono text-xs'>
                            #{channel.id}
                          </span>
                          <span className='ml-2'>{channel.name}</span>
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
              )}
              <FieldDescription>{channelDescription}</FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor='binding-upstream-type'>
                {t('Upstream Type')}
              </FieldLabel>
              <RadioGroup
                value={form.upstream_type}
                onValueChange={(value) => {
                  if (value === 'newapi' || value === 'sub2api') {
                    setForm((current) => ({
                      ...current,
                      upstream_type: value,
                      serves_claude_code:
                        value === 'sub2api'
                          ? current.serves_claude_code
                          : false,
                      new_api_user_id:
                        value === 'newapi'
                          ? current.new_api_user_id
                          : undefined,
                    }))
                  }
                }}
                className='grid gap-2 sm:grid-cols-2'
              >
                {upstreamOptions.map((option) => (
                  <label
                    key={option.value}
                    className='border-input bg-background hover:bg-accent/40 has-data-checked:border-primary has-data-checked:bg-primary/5 flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-sm'
                  >
                    <RadioGroupItem
                      value={option.value}
                      aria-label={option.label}
                    />
                    <span>{option.label}</span>
                  </label>
                ))}
              </RadioGroup>
            </Field>
            <Field className='sm:col-span-2'>
              <FieldLabel htmlFor='binding-base-url'>
                {t('Base URL')}
              </FieldLabel>
              <Input
                id='binding-base-url'
                value={form.base_url}
                placeholder='https://api.example.com'
                onChange={(event) =>
                  setForm((current) => ({
                    ...current,
                    base_url: event.target.value,
                  }))
                }
              />
            </Field>
            <Field className='sm:col-span-2'>
              <FieldLabel htmlFor='binding-upstream-group'>
                {t('Upstream Group')}
              </FieldLabel>
              <div className='flex gap-2'>
                {groupOptions.length > 0 ? (
                  <Select
                    items={groupOptions.map((group) => ({
                      value: group,
                      label: group,
                    }))}
                    value={form.upstream_group}
                    onValueChange={(value) =>
                      setForm((current) => ({
                        ...current,
                        upstream_group: value ?? '',
                      }))
                    }
                  >
                    <SelectTrigger
                      id='binding-upstream-group'
                      className='w-full'
                    >
                      <SelectValue placeholder={t('Select')} />
                    </SelectTrigger>
                    <SelectContent alignItemWithTrigger={false}>
                      <SelectGroup>
                        {groupOptions.map((group) => (
                          <SelectItem key={group} value={group}>
                            {group}
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                ) : (
                  <Input
                    id='binding-upstream-group'
                    value={form.upstream_group}
                    onChange={(event) =>
                      setForm((current) => ({
                        ...current,
                        upstream_group: event.target.value,
                      }))
                    }
                  />
                )}
                <Button
                  type='button'
                  variant='outline'
                  onClick={() =>
                    props.onLoadGroups(
                      normalizedBindingRequest(form),
                      props.binding ?? undefined
                    )
                  }
                  disabled={
                    props.isLoadingGroups ||
                    form.channel_id <= 0 ||
                    !props.canOperate
                  }
                >
                  <RefreshCw data-icon='inline-start' />
                  {props.isLoadingGroups ? t('Loading') : t('Groups')}
                </Button>
              </div>
              <FieldDescription>
                {t(
                  'Map local channels to upstream pricing and routing groups.'
                )}
              </FieldDescription>
            </Field>
            {upstreamType === 'newapi' && (
              <Field>
                <FieldLabel htmlFor='binding-new-api-user'>
                  {t('New API User ID')}
                </FieldLabel>
                <Input
                  id='binding-new-api-user'
                  type='number'
                  min={1}
                  step={1}
                  value={form.new_api_user_id ?? ''}
                  onChange={(event) => {
                    const parsed = Number(event.target.value)
                    setForm((current) => ({
                      ...current,
                      new_api_user_id:
                        event.target.value === '' ||
                        !Number.isFinite(parsed) ||
                        parsed < 1
                          ? undefined
                          : Math.trunc(parsed),
                    }))
                  }}
                />
              </Field>
            )}
            <Field
              orientation='horizontal'
              className='items-center justify-between'
            >
              <div className='min-w-0'>
                <FieldTitle>{t('Enabled')}</FieldTitle>
                <FieldDescription>
                  {t(
                    'Allow this binding to participate in cost sync and routing.'
                  )}
                </FieldDescription>
              </div>
              <Switch
                aria-label={t('Enabled')}
                checked={form.enabled}
                onCheckedChange={(value) =>
                  setForm((current) => ({ ...current, enabled: value }))
                }
              />
            </Field>
            {upstreamType === 'sub2api' && (
              <Field
                orientation='horizontal'
                className='items-center justify-between'
              >
                <div className='min-w-0'>
                  <FieldTitle>{t('Serves Claude Code')}</FieldTitle>
                  <FieldDescription>
                    {t(
                      'Marks this upstream as eligible for Claude Code traffic.'
                    )}
                  </FieldDescription>
                </div>
                <Switch
                  aria-label={t('Serves Claude Code')}
                  checked={form.serves_claude_code}
                  onCheckedChange={(value) =>
                    setForm((current) => ({
                      ...current,
                      serves_claude_code: value,
                    }))
                  }
                />
              </Field>
            )}
          </FieldGroup>

          <FieldSet>
            <FieldLegend variant='label'>
              {t('Read-only routing cost')}
            </FieldLegend>
            <FieldDescription>
              {t('Only used for routing. User billing is unchanged.')}
            </FieldDescription>
            {selectedSnapshot ? (
              <div className='grid gap-3 rounded-md border p-3 text-sm sm:grid-cols-3'>
                <div>
                  <div className='text-muted-foreground text-xs'>
                    {t('Cost Multiplier')}
                  </div>
                  <div className='font-medium'>
                    {formatNumber(costValue(selectedSnapshot), locale)}
                  </div>
                </div>
                <div>
                  <div className='text-muted-foreground text-xs'>
                    {t('Confidence')}
                  </div>
                  <Badge variant='secondary'>
                    {t(confidenceLabel(selectedSnapshot.confidence))}
                  </Badge>
                </div>
                <div>
                  <div className='text-muted-foreground text-xs'>
                    {t('Freshness')}
                  </div>
                  <div className='font-medium'>
                    {formatTimestampRelative(
                      selectedSnapshot.snapshot_ts,
                      'seconds',
                      locale
                    )}
                  </div>
                </div>
              </div>
            ) : (
              <div className='text-muted-foreground rounded-md border border-dashed px-3 py-2 text-sm'>
                {t('No pricing snapshot yet')}
              </div>
            )}
          </FieldSet>

          <FieldSet>
            <FieldLegend variant='label'>{t('Credentials')}</FieldLegend>
            <FieldGroup className='grid gap-4 sm:grid-cols-2'>
              {upstreamType === 'newapi' && (
                <Field>
                  <FieldLabel htmlFor='new-api-token'>
                    {t('New API Access Token')}
                  </FieldLabel>
                  <Input
                    id='new-api-token'
                    value={form.credentials.new_api_access_token ?? ''}
                    type='password'
                    autoComplete='new-password'
                    onChange={(event) =>
                      setCredential('new_api_access_token', event.target.value)
                    }
                  />
                  {credentialMaskText('new_api_access_token') && (
                    <FieldDescription>
                      {credentialMaskText('new_api_access_token')}
                    </FieldDescription>
                  )}
                </Field>
              )}
              <Field>
                <FieldLabel htmlFor='gateway-key'>
                  {t('Gateway API Key')}
                </FieldLabel>
                <Input
                  id='gateway-key'
                  value={form.credentials.gateway_api_key ?? ''}
                  type='password'
                  autoComplete='new-password'
                  onChange={(event) =>
                    setCredential('gateway_api_key', event.target.value)
                  }
                />
                {credentialMaskText('gateway_api_key') && (
                  <FieldDescription>
                    {credentialMaskText('gateway_api_key')}
                  </FieldDescription>
                )}
              </Field>
              {upstreamType === 'sub2api' && (
                <>
                  <Field>
                    <FieldLabel htmlFor='sub2api-email'>
                      {t('Sub2API Email')}
                    </FieldLabel>
                    <Input
                      id='sub2api-email'
                      value={form.credentials.sub2api_email ?? ''}
                      autoComplete='off'
                      onChange={(event) =>
                        setCredential('sub2api_email', event.target.value)
                      }
                    />
                    {credentialMaskText('sub2api_email') && (
                      <FieldDescription>
                        {credentialMaskText('sub2api_email')}
                      </FieldDescription>
                    )}
                  </Field>
                  <Field>
                    <FieldLabel htmlFor='sub2api-password'>
                      {t('Sub2API Password')}
                    </FieldLabel>
                    <Input
                      id='sub2api-password'
                      value={form.credentials.sub2api_password ?? ''}
                      type='password'
                      autoComplete='new-password'
                      onChange={(event) =>
                        setCredential('sub2api_password', event.target.value)
                      }
                    />
                    {credentialMaskText('sub2api_password') && (
                      <FieldDescription>
                        {credentialMaskText('sub2api_password')}
                      </FieldDescription>
                    )}
                  </Field>
                  <Field className='sm:col-span-2'>
                    <FieldLabel htmlFor='sub2api-token'>
                      {t('Sub2API Token')}
                    </FieldLabel>
                    <Input
                      id='sub2api-token'
                      value={form.credentials.sub2api_token ?? ''}
                      type='password'
                      autoComplete='new-password'
                      onChange={(event) =>
                        setCredential('sub2api_token', event.target.value)
                      }
                    />
                    {credentialMaskText('sub2api_token') && (
                      <FieldDescription>
                        {credentialMaskText('sub2api_token')}
                      </FieldDescription>
                    )}
                  </Field>
                </>
              )}
            </FieldGroup>
          </FieldSet>

          <DialogFooter>
            <DialogClose render={<Button type='button' variant='outline' />}>
              {t('Cancel')}
            </DialogClose>
            <Button
              type='submit'
              disabled={props.isPending || !props.canSensitiveWrite}
            >
              <Check data-icon='inline-start' />
              {props.isPending ? t('Saving...') : t('Save Binding')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function BindingsPanel(props: {
  bindings?: RoutingBinding[]
  snapshots?: RoutingCostSnapshot[]
  isLoading: boolean
  isError: boolean
  error: unknown
  onRetry: () => void
  canOperate: boolean
  canSensitiveWrite: boolean
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingBinding, setEditingBinding] = useState<RoutingBinding | null>(
    null
  )
  const [pendingDeleteBinding, setPendingDeleteBinding] =
    useState<RoutingBinding | null>(null)
  const [groupResults, setGroupResults] = useState<
    Record<number, RoutingBindingActionResult>
  >({})
  const bindings = useMemo(() => props.bindings ?? [], [props.bindings])
  const snapshots = props.snapshots ?? []
  const boundChannelIds = useMemo(
    () => new Set(bindings.map((binding) => binding.channel_id)),
    [bindings]
  )
  const channelsQuery = useQuery({
    queryKey: ['smart-routing', 'channels', 'unbound'],
    queryFn: async () => {
      const response = await getChannels({
        p: 1,
        page_size: 500,
        status: 'enabled',
        id_sort: true,
      })
      if (!response.success) {
        throw new Error(response.message || t('Failed to load channels'))
      }
      return response.data?.items ?? []
    },
    staleTime: QUERY_STALE_MS,
    enabled: props.canSensitiveWrite && dialogOpen && editingBinding == null,
  })
  const availableChannels = useMemo(
    () =>
      (channelsQuery.data ?? []).filter(
        (channel) => !boundChannelIds.has(channel.id)
      ),
    [boundChannelIds, channelsQuery.data]
  )

  const invalidateBindings = () =>
    queryClient.invalidateQueries({ queryKey: ['smart-routing', 'bindings'] })

  const createMutation = useMutation({
    mutationFn: createSmartRoutingBinding,
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(response.message || t('Failed to create routing binding'))
        return
      }
      toast.success(t('Routing binding created'))
      setDialogOpen(false)
      invalidateBindings()
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to create routing binding'))
    },
  })

  const updateMutation = useMutation({
    mutationFn: (payload: {
      channelId: number
      request: RoutingBindingRequest
    }) => updateSmartRoutingBinding(payload.channelId, payload.request),
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(response.message || t('Failed to update routing binding'))
        return
      }
      toast.success(t('Routing binding updated'))
      setDialogOpen(false)
      invalidateBindings()
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to update routing binding'))
    },
  })

  const deleteMutation = useMutation({
    mutationFn: deleteSmartRoutingBinding,
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(response.message || t('Failed to delete routing binding'))
        return
      }
      toast.success(t('Routing binding deleted'))
      setPendingDeleteBinding(null)
      invalidateBindings()
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to delete routing binding'))
    },
  })

  const testMutation = useMutation({
    mutationFn: (payload: {
      channelId: number | 'new'
      request?: RoutingBindingRequest
    }) => testSmartRoutingBinding(payload.channelId, payload.request),
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(response.message || t('Binding test failed'))
        return
      }
      setGroupResults((current) => ({
        ...current,
        [response.data.channel_id]: response.data,
      }))
      toast.success(
        t('Binding test returned {{count}} priced models', {
          count: response.data.model_count,
        })
      )
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Binding test failed'))
    },
  })

  const groupsMutation = useMutation({
    mutationFn: (payload: {
      channelId: number | 'new'
      request?: RoutingBindingRequest
    }) => loadSmartRoutingBindingGroups(payload.channelId, payload.request),
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(response.message || t('Failed to load upstream groups'))
        return
      }
      setGroupResults((current) => ({
        ...current,
        [response.data.channel_id]: response.data,
      }))
      toast.success(
        t('Loaded {{count}} upstream groups', {
          count: response.data.groups.length,
        })
      )
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to load upstream groups'))
    },
  })

  if (props.isLoading) return <LoadingRows />
  if (props.isError) {
    return (
      <ErrorState
        title={t('We could not load routing bindings.')}
        description={
          props.error instanceof Error ? props.error.message : undefined
        }
        onRetry={props.onRetry}
        className='min-h-[260px]'
      />
    )
  }

  return (
    <>
      <Panel
        title={t('Upstream Bindings')}
        description={t(
          'Map local channels to upstream pricing and routing groups.'
        )}
        icon={DatabaseZap}
        action={
          props.canSensitiveWrite ? (
            <Button
              type='button'
              size='sm'
              onClick={() => {
                setEditingBinding(null)
                setDialogOpen(true)
              }}
            >
              <Plus data-icon='inline-start' />
              {t('Create Binding')}
            </Button>
          ) : null
        }
      >
        {bindings.length === 0 ? (
          <TableEmpty
            title={t('No routing bindings')}
            description={t(
              'Create a binding to sync upstream pricing into smart routing.'
            )}
          />
        ) : (
          <div className='overflow-x-auto'>
            <Table className='min-w-[1100px]'>
              <TableHeader>
                <TableRow className='bg-muted/40 hover:bg-muted/40'>
                  <TableHead className='h-9 px-4 text-xs'>
                    {t('Channel')}
                  </TableHead>
                  <TableHead className='h-9 text-xs'>{t('Type')}</TableHead>
                  <TableHead className='h-9 text-xs'>{t('Upstream')}</TableHead>
                  <TableHead className='h-9 text-xs'>
                    {t('Credentials')}
                  </TableHead>
                  <TableHead className='h-9 text-xs'>
                    {t('Last Sync')}
                  </TableHead>
                  <TableHead className='h-9 text-xs'>{t('Groups')}</TableHead>
                  <TableHead className='h-9 pr-4 text-right text-xs'>
                    {t('Actions')}
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {bindings.map((binding) => {
                  const result = groupResults[binding.channel_id]
                  const credentialCount = Object.values(
                    binding.credential_masks ?? {}
                  ).filter(Boolean).length
                  return (
                    <TableRow key={binding.channel_id}>
                      <TableCell className='px-4 py-3 align-top'>
                        <div className='flex flex-col gap-1'>
                          <div className='font-medium'>
                            #{binding.channel_id}
                          </div>
                          <div className='text-muted-foreground text-xs'>
                            {binding.enabled ? t('Enabled') : t('Disabled')}
                          </div>
                        </div>
                      </TableCell>
                      <TableCell className='py-3 align-top'>
                        <div className='flex flex-col gap-1'>
                          <Badge variant='outline'>
                            {binding.upstream_type}
                          </Badge>
                          {binding.upstream_type === 'sub2api' &&
                            binding.serves_claude_code && (
                            <span className='text-muted-foreground text-xs'>
                              {t('Claude Code')}
                            </span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className='max-w-[260px] py-3 align-top'>
                        <div className='flex flex-col gap-1'>
                          <span className='truncate font-mono text-xs'>
                            {binding.base_url}
                          </span>
                          <span className='text-muted-foreground text-xs'>
                            {t('Group')}: {binding.upstream_group}
                          </span>
                        </div>
                      </TableCell>
                      <TableCell className='py-3 align-top'>
                        <div className='flex flex-col gap-1'>
                          <span className='text-xs'>
                            {credentialCount > 0
                              ? t('{{count}} saved', { count: credentialCount })
                              : t('No credentials')}
                          </span>
                          {binding.credential_error && (
                            <span className='text-destructive max-w-[220px] truncate text-xs'>
                              {binding.credential_error}
                            </span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className='text-muted-foreground py-3 align-top text-xs'>
                        <div className='flex flex-col gap-1'>
                          <span>
                            {formatTimestampToDate(binding.updated_time)}
                          </span>
                          {binding.last_sync_error && (
                            <span className='text-destructive max-w-[220px] truncate'>
                              {binding.last_sync_error}
                            </span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className='py-3 align-top'>
                        <div className='flex max-w-[220px] flex-wrap gap-1'>
                          {result?.groups?.slice(0, 4).map((group) => (
                            <Badge key={group} variant='secondary'>
                              {group}
                            </Badge>
                          ))}
                          {result && result.groups.length > 4 && (
                            <Badge variant='outline'>
                              +{result.groups.length - 4}
                            </Badge>
                          )}
                          {!result && (
                            <span className='text-muted-foreground text-xs'>
                              -
                            </span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className='py-3 pr-4 align-top'>
                        <div className='flex justify-end gap-1'>
                          <Button
                            type='button'
                            variant='ghost'
                            size='sm'
                            onClick={() =>
                              testMutation.mutate({
                                channelId: binding.channel_id,
                              })
                            }
                            disabled={
                              testMutation.isPending || !props.canOperate
                            }
                          >
                            {t('Test')}
                          </Button>
                          <Button
                            type='button'
                            variant='ghost'
                            size='sm'
                            onClick={() =>
                              groupsMutation.mutate({
                                channelId: binding.channel_id,
                              })
                            }
                            disabled={
                              groupsMutation.isPending || !props.canOperate
                            }
                          >
                            {t('Groups')}
                          </Button>
                          <Button
                            type='button'
                            variant='ghost'
                            size='icon-sm'
                            aria-label={t('Edit')}
                            onClick={() => {
                              setEditingBinding(binding)
                              setDialogOpen(true)
                            }}
                            disabled={!props.canSensitiveWrite}
                          >
                            <Pencil />
                          </Button>
                          <Button
                            type='button'
                            variant='destructive'
                            size='icon-sm'
                            aria-label={t('Delete')}
                            onClick={() => setPendingDeleteBinding(binding)}
                            disabled={
                              deleteMutation.isPending ||
                              !props.canSensitiveWrite
                            }
                          >
                            <Trash2 />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </div>
        )}
      </Panel>
      <BindingDialog
        open={dialogOpen}
        binding={editingBinding}
        channels={availableChannels}
        snapshots={snapshots}
        groupResults={groupResults}
        isLoadingGroups={groupsMutation.isPending}
        isLoadingChannels={channelsQuery.isLoading}
        canOperate={props.canOperate}
        canSensitiveWrite={props.canSensitiveWrite}
        onOpenChange={setDialogOpen}
        isPending={createMutation.isPending || updateMutation.isPending}
        onLoadGroups={(request, original) => {
          groupsMutation.mutate({
            channelId: original ? original.channel_id : 'new',
            request,
          })
        }}
        onSubmit={(request, original) => {
          if (original) {
            updateMutation.mutate({
              channelId: original.channel_id,
              request,
            })
          } else {
            createMutation.mutate(request)
          }
        }}
      />
      <ConfirmDialog
        open={pendingDeleteBinding != null}
        onOpenChange={(open) => {
          if (!open) setPendingDeleteBinding(null)
        }}
        title={t('Delete routing binding')}
        desc={t(
          'Delete binding for channel #{{channelId}}? This removes synced routing snapshots and breaker state. This cannot be undone.',
          { channelId: pendingDeleteBinding?.channel_id ?? '-' }
        )}
        confirmText={t('Delete Binding')}
        destructive
        isLoading={deleteMutation.isPending}
        handleConfirm={() => {
          if (pendingDeleteBinding) {
            deleteMutation.mutate(pendingDeleteBinding.channel_id)
          }
        }}
      />
    </>
  )
}

function MetricsPanel(props: {
  metrics?: RoutingMetric[]
  snapshots?: RoutingCostSnapshot[]
  metricsLimit: number
  snapshotsLimit: number
  isLoading: boolean
  isFetchingMetrics: boolean
  isFetchingSnapshots: boolean
  isError: boolean
  error: unknown
  onRetry: () => void
  onLoadMoreMetrics: () => void
  onLoadMoreSnapshots: () => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.language)

  if (props.isLoading) return <LoadingRows />
  if (props.isError) {
    return (
      <ErrorState
        title={t('We could not load routing telemetry.')}
        description={
          props.error instanceof Error ? props.error.message : undefined
        }
        onRetry={props.onRetry}
        className='min-h-[260px]'
      />
    )
  }

  const metrics = props.metrics ?? []
  const snapshots = props.snapshots ?? []

  return (
    <div className='flex flex-col gap-4'>
      <Panel
        title={t('Channel Metrics')}
        description={t('Recent request buckets used by the selector.')}
        icon={Activity}
      >
        {metrics.length === 0 ? (
          <TableEmpty
            title={t('No routing metrics')}
            description={t('Metrics appear after routed requests are flushed.')}
          />
        ) : (
          <>
            <div className='overflow-x-auto'>
              <Table className='min-w-[980px]'>
                <TableHeader>
                  <TableRow className='bg-muted/40 hover:bg-muted/40'>
                    <TableHead className='h-9 px-4 text-xs'>
                      {t('Channel')}
                    </TableHead>
                    <TableHead className='h-9 text-xs'>{t('Model')}</TableHead>
                    <TableHead className='h-9 text-xs'>{t('Group')}</TableHead>
                    <TableHead className='h-9 text-right text-xs'>
                      {t('Requests')}
                    </TableHead>
                    <TableHead className='h-9 text-right text-xs'>
                      {t('Success Rate')}
                    </TableHead>
                    <TableHead className='h-9 text-right text-xs'>
                      {t('Avg Latency')}
                    </TableHead>
                    <TableHead className='h-9 text-right text-xs'>
                      {t('Errors')}
                    </TableHead>
                    <TableHead className='h-9 pr-4 text-xs'>
                      {t('Bucket')}
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {metrics.map((metric) => (
                    <TableRow key={metric.id}>
                      <TableCell className='px-4 py-3'>
                        #{metric.channel_id}
                      </TableCell>
                      <TableCell className='py-3 font-mono text-xs'>
                        {metric.model_name}
                      </TableCell>
                      <TableCell className='py-3'>{metric.group}</TableCell>
                      <TableCell className='py-3 text-right'>
                        {formatNumber(metric.request_count, locale)}
                      </TableCell>
                      <TableCell className='py-3 text-right'>
                        {successRate(metric) == null
                          ? '-'
                          : `${formatNumber(successRate(metric), locale)}%`}
                      </TableCell>
                      <TableCell className='py-3 text-right'>
                        {averageLatency(metric) == null
                          ? '-'
                          : `${formatNumber(averageLatency(metric), locale)}ms`}
                      </TableCell>
                      <TableCell className='py-3 text-right'>
                        {formatNumber(
                          metric.err_4xx + metric.err_5xx + metric.err_429,
                          locale
                        )}
                      </TableCell>
                      <TableCell
                        className='text-muted-foreground py-3 pr-4 text-xs'
                        title={formatTimestampToDate(metric.bucket_ts)}
                      >
                        {formatTimestampRelative(
                          metric.bucket_ts,
                          'seconds',
                          locale
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
            <LoadMoreFooter
              loadedCount={metrics.length}
              limit={props.metricsLimit}
              isFetching={props.isFetchingMetrics}
              onLoadMore={props.onLoadMoreMetrics}
            />
          </>
        )}
      </Panel>

      <Panel
        title={t('Cost Snapshots')}
        description={t(
          'Latest upstream pricing snapshots available to routing.'
        )}
        icon={DatabaseZap}
      >
        {snapshots.length === 0 ? (
          <TableEmpty
            title={t('No cost snapshots')}
            description={t('Run sync after creating upstream bindings.')}
          />
        ) : (
          <>
            <div className='overflow-x-auto'>
              <Table className='min-w-[980px]'>
                <TableHeader>
                  <TableRow className='bg-muted/40 hover:bg-muted/40'>
                    <TableHead className='h-9 px-4 text-xs'>
                      {t('Channel')}
                    </TableHead>
                    <TableHead className='h-9 text-xs'>{t('Model')}</TableHead>
                    <TableHead className='h-9 text-right text-xs'>
                      {t('Group Ratio')}
                    </TableHead>
                    <TableHead className='h-9 text-right text-xs'>
                      {t('Base Ratio')}
                    </TableHead>
                    <TableHead className='h-9 text-right text-xs'>
                      {t('Completion Ratio')}
                    </TableHead>
                    <TableHead className='h-9 text-right text-xs'>
                      {t('Cost')}
                    </TableHead>
                    <TableHead className='h-9 text-xs'>
                      {t('Confidence')}
                    </TableHead>
                    <TableHead className='h-9 pr-4 text-xs'>
                      {t('Updated')}
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {snapshots.map((snapshot) => (
                    <TableRow
                      key={`${snapshot.channel_id}-${snapshot.model_name}`}
                    >
                      <TableCell className='px-4 py-3'>
                        #{snapshot.channel_id}
                      </TableCell>
                      <TableCell className='py-3 font-mono text-xs'>
                        {snapshot.model_name}
                      </TableCell>
                      <TableCell className='py-3 text-right'>
                        {formatNumber(snapshot.group_ratio, locale)}
                      </TableCell>
                      <TableCell className='py-3 text-right'>
                        {formatNumber(snapshot.base_ratio, locale)}
                      </TableCell>
                      <TableCell className='py-3 text-right'>
                        {formatNumber(snapshot.completion_ratio, locale)}
                      </TableCell>
                      <TableCell className='py-3 text-right'>
                        {formatNumber(costValue(snapshot), locale)}
                      </TableCell>
                      <TableCell className='py-3'>
                        <Badge variant='outline'>
                          {t(confidenceLabel(snapshot.confidence))}
                        </Badge>
                      </TableCell>
                      <TableCell
                        className='text-muted-foreground py-3 pr-4 text-xs'
                        title={formatTimestampToDate(snapshot.snapshot_ts)}
                      >
                        {formatTimestampRelative(
                          snapshot.snapshot_ts,
                          'seconds',
                          locale
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
            <LoadMoreFooter
              loadedCount={snapshots.length}
              limit={props.snapshotsLimit}
              isFetching={props.isFetchingSnapshots}
              onLoadMore={props.onLoadMoreSnapshots}
            />
          </>
        )}
      </Panel>
    </div>
  )
}

function BreakersPanel(props: {
  breakers?: RoutingBreaker[]
  limit: number
  isLoading: boolean
  isFetching: boolean
  isError: boolean
  error: unknown
  onRetry: () => void
  onLoadMore: () => void
  canOperate: boolean
}) {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const locale = toIntlLocale(i18n.language)

  const resetMutation = useMutation({
    mutationFn: resetSmartRoutingBreaker,
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(response.message || t('Failed to reset breaker'))
        return
      }
      toast.success(t('Breaker reset'))
      queryClient.invalidateQueries({ queryKey: ['smart-routing', 'breakers'] })
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to reset breaker'))
    },
  })

  if (props.isLoading) return <LoadingRows />
  if (props.isError) {
    return (
      <ErrorState
        title={t('We could not load routing breakers.')}
        description={
          props.error instanceof Error ? props.error.message : undefined
        }
        onRetry={props.onRetry}
        className='min-h-[260px]'
      />
    )
  }

  const breakers = props.breakers ?? []
  return (
    <Panel
      title={t('Breaker States')}
      description={t(
        'Circuit breaker state by channel, key, model, and group.'
      )}
      icon={ShieldAlert}
    >
      {breakers.length === 0 ? (
        <TableEmpty
          title={t('No breaker states')}
          description={t(
            'Breaker states appear after routing observes failures.'
          )}
        />
      ) : (
        <>
          <div className='overflow-x-auto'>
            <Table className='min-w-[1000px]'>
              <TableHeader>
                <TableRow className='bg-muted/40 hover:bg-muted/40'>
                  <TableHead className='h-9 px-4 text-xs'>
                    {t('Channel')}
                  </TableHead>
                  <TableHead className='h-9 text-xs'>{t('Model')}</TableHead>
                  <TableHead className='h-9 text-xs'>{t('Group')}</TableHead>
                  <TableHead className='h-9 text-xs'>{t('State')}</TableHead>
                  <TableHead className='h-9 text-right text-xs'>
                    {t('Failures')}
                  </TableHead>
                  <TableHead className='h-9 text-right text-xs'>
                    {t('Ejections')}
                  </TableHead>
                  <TableHead className='h-9 text-xs'>{t('Cooldown')}</TableHead>
                  <TableHead className='h-9 pr-4 text-right text-xs'>
                    {t('Actions')}
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {breakers.map((breaker) => (
                  <TableRow key={breaker.id}>
                    <TableCell className='px-4 py-3'>
                      <div className='flex flex-col gap-1'>
                        <span>#{breaker.channel_id}</span>
                        <span className='text-muted-foreground text-xs'>
                          {t('Key')} {breaker.api_key_index}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className='py-3 font-mono text-xs'>
                      {breaker.model_name}
                    </TableCell>
                    <TableCell className='py-3'>{breaker.group}</TableCell>
                    <TableCell className='py-3'>
                      <div className='flex flex-col gap-1'>
                        <Badge variant={statusBadgeVariant(breaker.state)}>
                          {t(breaker.state)}
                        </Badge>
                        {breaker.reason && (
                          <span className='text-muted-foreground text-xs'>
                            {breaker.reason}
                          </span>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className='py-3 text-right'>
                      {formatNumber(breaker.consecutive_failures, locale)}
                    </TableCell>
                    <TableCell className='py-3 text-right'>
                      {formatNumber(breaker.ejection_count, locale)}
                    </TableCell>
                    <TableCell
                      className='text-muted-foreground py-3 text-xs'
                      title={formatTimestampToDate(breaker.cooldown_until)}
                    >
                      {formatTimestampRelative(
                        breaker.cooldown_until,
                        'seconds',
                        locale
                      )}
                    </TableCell>
                    <TableCell className='py-3 pr-4 text-right'>
                      <Button
                        type='button'
                        variant='outline'
                        size='sm'
                        disabled={resetMutation.isPending || !props.canOperate}
                        onClick={() => resetMutation.mutate(breaker.id)}
                      >
                        <RotateCcw data-icon='inline-start' />
                        {t('Reset')}
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
          <LoadMoreFooter
            loadedCount={breakers.length}
            limit={props.limit}
            isFetching={props.isFetching}
            onLoadMore={props.onLoadMore}
          />
        </>
      )}
    </Panel>
  )
}

function AgentPanel(props: {
  recommendations?: RoutingAgentRecommendation[]
  isLoading: boolean
  isError: boolean
  error: unknown
  onRetry: () => void
  canWrite: boolean
}) {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const locale = toIntlLocale(i18n.language)

  const approveMutation = useMutation({
    mutationFn: approveSmartRoutingAgentRecommendation,
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(response.message || t('Failed to approve recommendation'))
        return
      }
      toast.success(t('Recommendation approved'))
      queryClient.invalidateQueries({
        queryKey: ['smart-routing', 'agent-recommendations'],
      })
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to approve recommendation'))
    },
  })

  const rejectMutation = useMutation({
    mutationFn: rejectSmartRoutingAgentRecommendation,
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(response.message || t('Failed to reject recommendation'))
        return
      }
      toast.success(t('Recommendation rejected'))
      queryClient.invalidateQueries({
        queryKey: ['smart-routing', 'agent-recommendations'],
      })
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to reject recommendation'))
    },
  })

  if (props.isLoading) return <LoadingRows />
  if (props.isError) {
    return (
      <ErrorState
        title={t('We could not load routing recommendations.')}
        description={
          props.error instanceof Error ? props.error.message : undefined
        }
        onRetry={props.onRetry}
        className='min-h-[260px]'
      />
    )
  }

  const recommendations = props.recommendations ?? []
  return (
    <Panel
      title={t('Agent Recommendations')}
      description={t(
        'Agent recommendations are planned for v2 and are currently read-only.'
      )}
      icon={Bot}
    >
      {recommendations.length === 0 ? (
        <TableEmpty
          title={t('No recommendations')}
          description={t(
            'Recommendations appear after the routing agent evaluates telemetry.'
          )}
        />
      ) : (
        <div className='overflow-x-auto'>
          <Table className='min-w-[1000px]'>
            <TableHeader>
              <TableRow className='bg-muted/40 hover:bg-muted/40'>
                <TableHead className='h-9 px-4 text-xs'>{t('Type')}</TableHead>
                <TableHead className='h-9 text-xs'>{t('Severity')}</TableHead>
                <TableHead className='h-9 text-xs'>{t('Rationale')}</TableHead>
                <TableHead className='h-9 text-xs'>
                  {t('Proposed Change')}
                </TableHead>
                <TableHead className='h-9 text-xs'>{t('Status')}</TableHead>
                <TableHead className='h-9 pr-4 text-right text-xs'>
                  {t('Actions')}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {recommendations.map((recommendation) => (
                <TableRow key={recommendation.id}>
                  <TableCell className='px-4 py-3'>
                    <div className='flex flex-col gap-1'>
                      <span className='font-medium'>
                        {recommendation.type || t('Read-only preview')}
                      </span>
                      <span className='text-muted-foreground text-xs'>
                        {formatTimestampRelative(
                          recommendation.created_time,
                          'seconds',
                          locale
                        )}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell className='py-3'>
                    <Badge variant='outline'>
                      {translatedSeverity(recommendation.severity, t)}
                    </Badge>
                  </TableCell>
                  <TableCell className='max-w-[260px] py-3 align-top'>
                    <p className='line-clamp-3 text-sm'>
                      {recommendation.rationale || '-'}
                    </p>
                  </TableCell>
                  <TableCell className='max-w-[280px] py-3 align-top'>
                    <code className='bg-muted block max-h-20 overflow-auto rounded-md px-2 py-1.5 text-xs'>
                      {compactJson(recommendation.proposed_json)}
                    </code>
                  </TableCell>
                  <TableCell className='py-3'>
                    <Badge variant='secondary'>
                      {translatedRecommendationStatus(
                        recommendation.status,
                        t
                      )}
                    </Badge>
                  </TableCell>
                  <TableCell className='py-3 pr-4'>
                    <div className='flex justify-end gap-1'>
                      <Button
                        type='button'
                        variant='outline'
                        size='sm'
                        disabled
                        onClick={() =>
                          approveMutation.mutate(recommendation.id)
                        }
                      >
                        <Check data-icon='inline-start' />
                        {t('Approve')}
                      </Button>
                      <Button
                        type='button'
                        variant='ghost'
                        size='sm'
                        disabled
                        onClick={() => rejectMutation.mutate(recommendation.id)}
                      >
                        <X data-icon='inline-start' />
                        {t('Reject')}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </Panel>
  )
}

export function SmartRouting() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const currentUser = useAuthStore((state) => state.auth.user)
  const canRead = hasPermission(
    currentUser,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.READ
  )
  const canWrite = hasPermission(
    currentUser,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.WRITE
  )
  const canOperate = hasPermission(
    currentUser,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )
  const canSensitiveWrite = hasPermission(
    currentUser,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
  )
  const [activeTab, setActiveTab] = useState<
    'settings' | 'bindings' | 'metrics' | 'breakers' | 'agent'
  >('settings')
  const [metricsLimit, setMetricsLimit] = useState(TABLE_INITIAL_LIMIT)
  const [snapshotsLimit, setSnapshotsLimit] = useState(TABLE_INITIAL_LIMIT)
  const [breakersLimit, setBreakersLimit] = useState(TABLE_INITIAL_LIMIT)

  const settingsQuery = useQuery({
    queryKey: ['smart-routing', 'settings'],
    queryFn: async () => requireData(await getSmartRoutingSettings()),
    staleTime: QUERY_STALE_MS,
    enabled: canRead,
  })
  const bindingsQuery = useQuery({
    queryKey: ['smart-routing', 'bindings'],
    queryFn: async () => requireData(await listSmartRoutingBindings()),
    staleTime: QUERY_STALE_MS,
    enabled: canRead && activeTab === 'bindings',
  })
  const metricsQuery = useQuery({
    queryKey: ['smart-routing', 'metrics', metricsLimit],
    queryFn: async () =>
      requireData(await listSmartRoutingMetrics(metricsLimit)),
    placeholderData: (previousData) => previousData,
    staleTime: QUERY_STALE_MS,
    enabled: canRead && activeTab === 'metrics',
  })
  const snapshotsQuery = useQuery({
    queryKey: ['smart-routing', 'snapshots', snapshotsLimit],
    queryFn: async () =>
      requireData(await listSmartRoutingSnapshots(snapshotsLimit)),
    placeholderData: (previousData) => previousData,
    staleTime: QUERY_STALE_MS,
    enabled: canRead && (activeTab === 'bindings' || activeTab === 'metrics'),
  })
  const breakersQuery = useQuery({
    queryKey: ['smart-routing', 'breakers', breakersLimit],
    queryFn: async () =>
      requireData(await listSmartRoutingBreakers(breakersLimit)),
    placeholderData: (previousData) => previousData,
    staleTime: QUERY_STALE_MS,
    enabled: canRead && activeTab === 'breakers',
  })
  const recommendationsQuery = useQuery({
    queryKey: ['smart-routing', 'agent-recommendations'],
    queryFn: async () =>
      requireData(await listSmartRoutingAgentRecommendations(TABLE_INITIAL_LIMIT)),
    staleTime: QUERY_STALE_MS,
    enabled: canRead && activeTab === 'agent',
  })

  const syncMutation = useMutation({
    mutationFn: enqueueSmartRoutingSync,
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(response.message || t('Failed to start routing sync'))
        return
      }
      toast.success(
        response.data.created
          ? t('Routing sync task started')
          : t('Routing sync task is already queued')
      )
      queryClient.invalidateQueries({ queryKey: ['smart-routing'] })
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to start routing sync'))
    },
  })

  const refreshing =
    settingsQuery.isFetching ||
    bindingsQuery.isFetching ||
    metricsQuery.isFetching ||
    snapshotsQuery.isFetching ||
    breakersQuery.isFetching ||
    recommendationsQuery.isFetching

  const activeMode = settingsQuery.data?.mode
  const modeLabel = useMemo(
    () => translatedModeLabel(activeMode, t),
    [activeMode, t]
  )

  const refreshAll = () => {
    queryClient.invalidateQueries({ queryKey: ['smart-routing'] })
  }

  if (!canRead) {
    return (
      <SectionPageLayout>
        <SectionPageLayout.Title>{t('Smart Routing')}</SectionPageLayout.Title>
        <SectionPageLayout.Content>
          <ErrorState
            title={t('No permission to perform this action')}
            className='min-h-[260px]'
          />
        </SectionPageLayout.Content>
      </SectionPageLayout>
    )
  }

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        <span className='inline-flex min-w-0 items-center gap-2'>
          <span className='truncate'>{t('Smart Routing')}</span>
          {modeLabel && (
            <Badge variant='outline' className='shrink-0'>
              {modeLabel}
            </Badge>
          )}
        </span>
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          type='button'
          variant='outline'
          onClick={refreshAll}
          disabled={refreshing}
        >
          <RefreshCw
            data-icon='inline-start'
            className={cn(refreshing && 'animate-spin')}
          />
          {t('Refresh')}
        </Button>
        <Button
          type='button'
          onClick={() => syncMutation.mutate()}
          disabled={syncMutation.isPending || !canOperate}
        >
          <DatabaseZap data-icon='inline-start' />
          {syncMutation.isPending ? t('Starting...') : t('Sync Pricing')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <Tabs
          value={activeTab}
          onValueChange={(value) =>
            setActiveTab(
              value as 'settings' | 'bindings' | 'metrics' | 'breakers' | 'agent'
            )
          }
          className='flex min-h-0 flex-col gap-4'
        >
          <TabsList className='max-w-full flex-wrap justify-start group-data-horizontal/tabs:h-auto'>
            <TabsTrigger value='settings'>{t('Settings')}</TabsTrigger>
            <TabsTrigger value='bindings'>{t('Bindings')}</TabsTrigger>
            <TabsTrigger value='metrics'>{t('Metrics')}</TabsTrigger>
            <TabsTrigger value='breakers'>{t('Breakers')}</TabsTrigger>
            <TabsTrigger value='agent'>{t('Agent')}</TabsTrigger>
          </TabsList>

          <TabsContent value='settings'>
            <Panel
              title={t('Routing Settings')}
              description={t(
                'Control selector scoring, failover, sync cadence, and agent behavior.'
              )}
              icon={Route}
            >
              <SettingsPanel
                settings={settingsQuery.data}
                isLoading={settingsQuery.isLoading}
                isError={settingsQuery.isError}
                error={settingsQuery.error}
                onRetry={() => void settingsQuery.refetch()}
                canWrite={canWrite}
              />
            </Panel>
          </TabsContent>
          <TabsContent value='bindings'>
            {activeTab === 'bindings' && (
              <BindingsPanel
                bindings={bindingsQuery.data}
                snapshots={snapshotsQuery.data}
                isLoading={bindingsQuery.isLoading}
                isError={bindingsQuery.isError}
                error={bindingsQuery.error}
                canOperate={canOperate}
                canSensitiveWrite={canSensitiveWrite}
                onRetry={() => {
                  void bindingsQuery.refetch()
                  void snapshotsQuery.refetch()
                }}
              />
            )}
          </TabsContent>
          <TabsContent value='metrics'>
            {activeTab === 'metrics' && (
              <MetricsPanel
                metrics={metricsQuery.data}
                snapshots={snapshotsQuery.data}
                metricsLimit={metricsLimit}
                snapshotsLimit={snapshotsLimit}
                isLoading={metricsQuery.isLoading || snapshotsQuery.isLoading}
                isFetchingMetrics={metricsQuery.isFetching}
                isFetchingSnapshots={snapshotsQuery.isFetching}
                isError={metricsQuery.isError || snapshotsQuery.isError}
                error={metricsQuery.error ?? snapshotsQuery.error}
                onRetry={() => {
                  void metricsQuery.refetch()
                  void snapshotsQuery.refetch()
                }}
                onLoadMoreMetrics={() =>
                  setMetricsLimit((current) =>
                    Math.min(current + TABLE_LIMIT_STEP, TABLE_MAX_LIMIT)
                  )
                }
                onLoadMoreSnapshots={() =>
                  setSnapshotsLimit((current) =>
                    Math.min(current + TABLE_LIMIT_STEP, TABLE_MAX_LIMIT)
                  )
                }
              />
            )}
          </TabsContent>
          <TabsContent value='breakers'>
            {activeTab === 'breakers' && (
              <BreakersPanel
                breakers={breakersQuery.data}
                limit={breakersLimit}
                isLoading={breakersQuery.isLoading}
                isFetching={breakersQuery.isFetching}
                isError={breakersQuery.isError}
                error={breakersQuery.error}
                canOperate={canOperate}
                onRetry={() => void breakersQuery.refetch()}
                onLoadMore={() =>
                  setBreakersLimit((current) =>
                    Math.min(current + TABLE_LIMIT_STEP, TABLE_MAX_LIMIT)
                  )
                }
              />
            )}
          </TabsContent>
          <TabsContent value='agent'>
            {activeTab === 'agent' && (
              <AgentPanel
                recommendations={recommendationsQuery.data}
                isLoading={recommendationsQuery.isLoading}
                isError={recommendationsQuery.isError}
                error={recommendationsQuery.error}
                canWrite={canWrite}
                onRetry={() => void recommendationsQuery.refetch()}
              />
            )}
          </TabsContent>
        </Tabs>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
