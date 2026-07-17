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
import { ArrowReloadHorizontalIcon, EyeIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import { listChannelRoutingControlAudits } from '../../api/client'
import { channelRoutingQueryKeys } from '../../api/query-keys'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../../components/page-state'
import { ChannelRoutingStatusBadge } from '../../components/status-badge'
import {
  routingControlAuditActionLabel,
  routingControlAuditHumanizeKey,
  routingControlAuditRecord,
  routingControlAuditSourceLabel,
  routingControlAuditSubjectLabel,
} from '../../lib/control-audits'
import { useChannelRoutingFormatters } from '../../lib/format'
import type {
  ChannelRoutingControlAudit,
  ChannelRoutingControlAuditSubject,
  SmartRoutingSettingField,
} from '../../types'
import {
  runtimeSettingFields,
  runtimeSettingLabels,
} from '../runtime-settings/lib/runtime-settings'
import { ChannelRoutingControlAuditSheet } from './control-audit-sheet'
import { OperationsAuditCursorPager } from './cursor-pager'

type AuditSubjectFilter = '' | ChannelRoutingControlAuditSubject
type AuditSourceFilter = '' | 'admin' | 'migration' | 'reconciler' | 'system'
type AuditResultFilter =
  | ''
  | 'succeeded'
  | 'partially_succeeded'
  | 'failed'
  | 'rejected'
type AuditAttentionFilter = 'all' | boolean

export function ChannelRoutingControlAuditsSection() {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const [subjectType, setSubjectType] = useState<AuditSubjectFilter>('')
  const [source, setSource] = useState<AuditSourceFilter>('')
  const [result, setResult] = useState<AuditResultFilter>('')
  const [attention, setAttention] = useState<AuditAttentionFilter>('all')
  const [cursor, setCursor] = useState(0)
  const [history, setHistory] = useState<number[]>([])
  const [selectedAudit, setSelectedAudit] =
    useState<ChannelRoutingControlAudit | null>(null)
  const params = {
    limit: 20,
    before_id: cursor || undefined,
    subject_type: subjectType || undefined,
    source: source || undefined,
    result: result || undefined,
    needs_attention: attention === 'all' ? undefined : attention,
  }
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.controlAudits(params),
    queryFn: () => listChannelRoutingControlAudits(params),
  })

  function resetCursor() {
    setCursor(0)
    setHistory([])
  }

  return (
    <section
      className='flex flex-col gap-3 border-t pt-5'
      aria-labelledby='routing-control-audits-heading'
    >
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <div>
          <h2
            id='routing-control-audits-heading'
            className='text-base font-semibold'
          >
            {t('Control audits')}
          </h2>
          <p className='text-muted-foreground mt-1 text-xs'>
            {t(
              'Immutable control-plane facts with stable actor and lifecycle snapshots'
            )}
          </p>
        </div>
        <Button
          size='icon-sm'
          variant='outline'
          aria-label={t('Refresh control audits')}
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          <HugeiconsIcon
            icon={ArrowReloadHorizontalIcon}
            className={
              query.isFetching
                ? 'animate-spin motion-reduce:animate-none'
                : undefined
            }
            strokeWidth={2}
            aria-hidden='true'
          />
        </Button>
      </div>

      <div className='flex flex-wrap gap-2'>
        <NativeSelect
          size='sm'
          value={subjectType}
          aria-label={t('Control audit subject')}
          onChange={(event) => {
            setSubjectType(event.target.value as AuditSubjectFilter)
            resetCursor()
          }}
        >
          <NativeSelectOption value=''>
            {t('All control subjects')}
          </NativeSelectOption>
          <NativeSelectOption value='runtime_settings'>
            {t('Runtime settings')}
          </NativeSelectOption>
          <NativeSelectOption value='channel_configuration'>
            {t('Channel configuration')}
          </NativeSelectOption>
          <NativeSelectOption value='channel_lifecycle'>
            {t('Channel lifecycle')}
          </NativeSelectOption>
          <NativeSelectOption value='cost_binding'>
            {t('Retired cost binding')}
          </NativeSelectOption>
          <NativeSelectOption value='operation'>
            {t('Operation')}
          </NativeSelectOption>
          <NativeSelectOption value='policy_draft'>
            {t('Policy draft')}
          </NativeSelectOption>
          <NativeSelectOption value='policy_revision'>
            {t('Policy revision')}
          </NativeSelectOption>
          <NativeSelectOption value='policy_activation'>
            {t('Policy activation')}
          </NativeSelectOption>
          <NativeSelectOption value='policy_risk_acceptance'>
            {t('Policy risk acceptance')}
          </NativeSelectOption>
          <NativeSelectOption value='pricing'>
            {t('Routing pricing')}
          </NativeSelectOption>
        </NativeSelect>
        <NativeSelect
          size='sm'
          value={source}
          aria-label={t('Control audit source')}
          onChange={(event) => {
            setSource(event.target.value as AuditSourceFilter)
            resetCursor()
          }}
        >
          <NativeSelectOption value=''>
            {t('All control sources')}
          </NativeSelectOption>
          <NativeSelectOption value='admin'>
            {t('Administrator')}
          </NativeSelectOption>
          <NativeSelectOption value='system'>{t('System')}</NativeSelectOption>
          <NativeSelectOption value='migration'>
            {t('Migration')}
          </NativeSelectOption>
          <NativeSelectOption value='reconciler'>
            {t('Reconciler')}
          </NativeSelectOption>
        </NativeSelect>
        <NativeSelect
          size='sm'
          value={result}
          aria-label={t('Control audit result')}
          onChange={(event) => {
            setResult(event.target.value as AuditResultFilter)
            resetCursor()
          }}
        >
          <NativeSelectOption value=''>
            {t('All control results')}
          </NativeSelectOption>
          <NativeSelectOption value='succeeded'>
            {t('Succeeded')}
          </NativeSelectOption>
          <NativeSelectOption value='partially_succeeded'>
            {t('Partially succeeded')}
          </NativeSelectOption>
          <NativeSelectOption value='failed'>{t('Failed')}</NativeSelectOption>
          <NativeSelectOption value='rejected'>
            {t('Rejected')}
          </NativeSelectOption>
        </NativeSelect>
        <NativeSelect
          size='sm'
          value={String(attention)}
          aria-label={t('Attention status')}
          onChange={(event) => {
            const value = event.target.value
            setAttention(value === 'all' ? 'all' : value === 'true')
            resetCursor()
          }}
        >
          <NativeSelectOption value='all'>
            {t('All attention states')}
          </NativeSelectOption>
          <NativeSelectOption value='true'>
            {t('Needs attention')}
          </NativeSelectOption>
          <NativeSelectOption value='false'>
            {t('No action needed')}
          </NativeSelectOption>
        </NativeSelect>
      </div>

      {query.isLoading ? <ChannelRoutingLoadingState rows={5} /> : null}
      {query.isError && !query.data ? (
        <ChannelRoutingErrorState
          error={query.error}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      {query.isRefetchError && query.data ? (
        <ChannelRoutingRefetchErrorAlert
          isFetching={query.isFetching}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      {query.data && query.data.items.length === 0 ? (
        <ChannelRoutingEmptyState
          title={t('No control audits')}
          description={t('No control-plane changes match the current filter.')}
        />
      ) : null}

      {query.data && query.data.items.length > 0 ? (
        <>
          <div className='hidden overflow-hidden rounded-lg border lg:block'>
            <Table scrollAreaLabel={t('Control audits')}>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Audit')}</TableHead>
                  <TableHead>{t('Result')}</TableHead>
                  <TableHead>{t('Object')}</TableHead>
                  <TableHead>{t('Actor snapshot')}</TableHead>
                  <TableHead>{t('Summary')}</TableHead>
                  <TableHead>{t('Attention')}</TableHead>
                  <TableHead>{t('Created')}</TableHead>
                  <TableHead className='w-14'>
                    <span className='sr-only'>{t('Actions')}</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {query.data.items.map((audit) => (
                  <TableRow key={audit.id}>
                    <TableCell>
                      <div className='font-medium'>#{audit.id}</div>
                      <Badge variant='outline' className='mt-1'>
                        {t(routingControlAuditActionLabel(audit.action))}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <ChannelRoutingStatusBadge status={audit.result} />
                    </TableCell>
                    <TableCell className='max-w-64'>
                      <div className='text-sm font-medium break-words'>
                        {audit.subject_name}
                      </div>
                      <div className='text-muted-foreground mt-1 text-xs'>
                        {t(routingControlAuditSubjectLabel(audit.subject_type))}
                      </div>
                      <div className='text-muted-foreground mt-1 line-clamp-2 font-mono text-xs break-all'>
                        {audit.subject_generation || audit.subject_identity}
                      </div>
                    </TableCell>
                    <TableCell>
                      <div className='text-sm font-medium break-words'>
                        {audit.actor_name}
                      </div>
                      <div className='text-muted-foreground mt-1 text-xs'>
                        {audit.actor_id > 0
                          ? t('Actor #{{id}}', { id: audit.actor_id })
                          : t('System')}
                      </div>
                      <div className='text-muted-foreground mt-1 text-xs'>
                        {t(routingControlAuditSourceLabel(audit.source))}
                      </div>
                    </TableCell>
                    <TableCell className='max-w-80 text-xs break-words'>
                      {auditSummary(audit, t)}
                    </TableCell>
                    <TableCell>
                      <ChannelRoutingStatusBadge
                        status={audit.needs_attention ? 'warning' : 'healthy'}
                        label={
                          audit.needs_attention
                            ? t('Needs attention')
                            : t('No action needed')
                        }
                      />
                    </TableCell>
                    <TableCell>
                      {format.timestamp(audit.created_time_ms)}
                    </TableCell>
                    <TableCell>
                      <Tooltip>
                        <TooltipTrigger
                          render={
                            <Button
                              size='icon-sm'
                              variant='ghost'
                              aria-label={t('View control audit')}
                              onClick={() => setSelectedAudit(audit)}
                            />
                          }
                        >
                          <HugeiconsIcon
                            icon={EyeIcon}
                            data-icon='inline-start'
                            strokeWidth={2}
                            aria-hidden='true'
                          />
                        </TooltipTrigger>
                        <TooltipContent>
                          {t('View control audit')}
                        </TooltipContent>
                      </Tooltip>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          <div className='divide-y rounded-lg border lg:hidden'>
            {query.data.items.map((audit) => (
              <button
                key={audit.id}
                type='button'
                className='hover:bg-muted/50 min-h-11 w-full p-3 text-left transition-colors'
                onClick={() => setSelectedAudit(audit)}
              >
                <div className='flex items-start justify-between gap-3'>
                  <div className='min-w-0'>
                    <div className='font-medium break-words'>
                      {audit.subject_name}
                    </div>
                    <div className='text-muted-foreground mt-1 text-xs'>
                      {t('Audit #{{id}}', { id: audit.id })}
                      {', '}
                      {t(routingControlAuditActionLabel(audit.action))}
                    </div>
                  </div>
                  <ChannelRoutingStatusBadge status={audit.result} />
                </div>
                <p className='mt-2 line-clamp-3 text-sm break-words'>
                  {auditSummary(audit, t)}
                </p>
                <div className='text-muted-foreground mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs'>
                  <span>{audit.actor_name}</span>
                  <span>{format.timestamp(audit.created_time_ms)}</span>
                </div>
                <div className='mt-2'>
                  <ChannelRoutingStatusBadge
                    status={audit.needs_attention ? 'warning' : 'healthy'}
                    label={
                      audit.needs_attention
                        ? t('Needs attention')
                        : t('No action needed')
                    }
                  />
                </div>
              </button>
            ))}
          </div>

          <OperationsAuditCursorPager
            hasPrevious={history.length > 0}
            hasNext={(query.data.next_before_id ?? 0) > 0}
            disabled={query.isRefetchError}
            onFirst={() => {
              setCursor(0)
              setHistory([])
            }}
            onPrevious={() => {
              const previousCursor = history.at(-1) ?? 0
              setHistory((previous) => previous.slice(0, -1))
              setCursor(previousCursor)
            }}
            onNext={() => {
              if (!query.data.next_before_id) return
              setHistory((previous) => [...previous, cursor])
              setCursor(query.data.next_before_id)
            }}
          />
        </>
      ) : null}

      <ChannelRoutingControlAuditSheet
        audit={selectedAudit}
        open={selectedAudit != null}
        onOpenChange={(open) => {
          if (!open) setSelectedAudit(null)
        }}
      />
    </section>
  )
}

function auditSummary(
  audit: ChannelRoutingControlAudit,
  translate: TFunction
): string {
  const summary = routingControlAuditRecord(audit.summary)
  if (!summary) return String(translate('No summary recorded'))
  const changedKeys = summary.changed_keys
  if (Array.isArray(changedKeys)) {
    const labels = changedKeys
      .filter((key): key is string => typeof key === 'string')
      .map((key) => {
        if (runtimeSettingFields.includes(key as SmartRoutingSettingField)) {
          return String(
            translate(runtimeSettingLabels[key as SmartRoutingSettingField])
          )
        }
        return String(translate(routingControlAuditHumanizeKey(key)))
      })
    if (labels.length > 0) return labels.join(', ')
  }
  if (typeof summary.summary === 'string' && summary.summary.trim() !== '') {
    return summary.summary
  }
  if (summary.source === 'existing_options') {
    return String(translate('Bootstrapped from existing options'))
  }
  if (summary.source === 'external_option_update') {
    return String(translate('Reconciled from an external option update'))
  }
  for (const key of ['status', 'operation_type', 'stage', 'reason']) {
    const value = summary[key]
    if (typeof value === 'string' && value.trim() !== '') {
      return String(translate(routingControlAuditHumanizeKey(value)))
    }
  }
  const primitive = Object.entries(summary).find(
    ([, value]) =>
      typeof value === 'string' ||
      typeof value === 'number' ||
      typeof value === 'boolean'
  )
  if (primitive) {
    return `${translate(routingControlAuditHumanizeKey(primitive[0]))}: ${String(primitive[1])}`
  }
  return String(translate('No summary recorded'))
}
