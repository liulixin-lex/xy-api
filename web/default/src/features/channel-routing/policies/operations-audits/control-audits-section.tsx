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
import { ArrowReloadHorizontalIcon } from '@hugeicons/core-free-icons'
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

import { listChannelRoutingControlAudits } from '../../api/client'
import { channelRoutingQueryKeys } from '../../api/query-keys'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../../components/page-state'
import { useChannelRoutingFormatters } from '../../lib/format'
import type {
  ChannelRoutingControlAudit,
  ChannelRoutingControlAuditSubject,
  SmartRoutingSettingField,
} from '../../types'
import {
  runtimeSettingLabels,
  runtimeSettingFields,
} from '../runtime-settings/lib/runtime-settings'
import { OperationsAuditCursorPager } from './cursor-pager'

type AuditSubjectFilter = '' | ChannelRoutingControlAuditSubject

const auditSubjectLabels: Record<ChannelRoutingControlAuditSubject, string> = {
  runtime_settings: 'Runtime settings',
  channel_configuration: 'Channel configuration',
  cost_binding: 'Retired cost binding',
}

const auditActionLabels: Record<string, string> = {
  bootstrap: 'Bootstrap',
  reconcile: 'Reconcile',
  create: 'Create',
  update: 'Update',
  delete: 'Delete',
}

function auditSummary(
  audit: ChannelRoutingControlAudit,
  translate: TFunction
): string {
  const changedKeys = audit.summary.changed_keys
  if (Array.isArray(changedKeys)) {
    return changedKeys
      .filter((key): key is string => typeof key === 'string')
      .map((key) => {
        if (!runtimeSettingFields.includes(key as SmartRoutingSettingField)) {
          return key
        }
        return String(
          translate(runtimeSettingLabels[key as SmartRoutingSettingField])
        )
      })
      .join(', ')
  }
  const source = audit.summary.source
  if (source === 'existing_options') {
    return String(translate('Bootstrapped from existing options'))
  }
  if (source === 'external_option_update') {
    return String(translate('Reconciled from an external option update'))
  }
  if (typeof source === 'string' && source.trim() !== '') return source
  const status = audit.summary.status
  if (typeof status === 'string' && status.trim() !== '') return status
  return String(translate('No summary recorded'))
}

export function ChannelRoutingControlAuditsSection() {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const [subjectType, setSubjectType] = useState<AuditSubjectFilter>('')
  const [cursor, setCursor] = useState(0)
  const [history, setHistory] = useState<number[]>([])
  const params = {
    limit: 20,
    before_id: cursor || undefined,
    subject_type: subjectType || undefined,
  }
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.controlAudits(params),
    queryFn: () => listChannelRoutingControlAudits(params),
  })

  return (
    <section
      className='space-y-3 border-t pt-5'
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
              'Immutable control-plane changes with actor, revision hashes, and normalized summaries'
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
            aria-hidden='true'
          />
        </Button>
      </div>

      <NativeSelect
        size='sm'
        value={subjectType}
        aria-label={t('Control audit subject')}
        onChange={(event) => {
          setSubjectType(event.target.value as AuditSubjectFilter)
          setCursor(0)
          setHistory([])
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
        <NativeSelectOption value='cost_binding'>
          {t('Retired cost binding')}
        </NativeSelectOption>
      </NativeSelect>

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
                  <TableHead>{t('Subject')}</TableHead>
                  <TableHead>{t('Action')}</TableHead>
                  <TableHead>{t('Actor')}</TableHead>
                  <TableHead>{t('Before')}</TableHead>
                  <TableHead>{t('After')}</TableHead>
                  <TableHead>{t('Summary')}</TableHead>
                  <TableHead>{t('Created')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {query.data.items.map((audit) => (
                  <TableRow key={audit.id}>
                    <TableCell className='font-medium'>#{audit.id}</TableCell>
                    <TableCell>
                      <div className='text-sm'>
                        {t(auditSubjectLabels[audit.subject_type])}
                      </div>
                      {audit.subject_id > 0 ? (
                        <div className='text-muted-foreground text-xs'>
                          #{audit.subject_id}
                        </div>
                      ) : null}
                    </TableCell>
                    <TableCell>
                      <Badge variant='outline'>
                        {t(auditActionLabels[audit.action] ?? audit.action)}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      {audit.actor_id > 0 ? `#${audit.actor_id}` : t('System')}
                    </TableCell>
                    <TableCell className='font-mono text-xs'>
                      {audit.before_hash
                        ? format.shortHash(audit.before_hash)
                        : '—'}
                    </TableCell>
                    <TableCell className='font-mono text-xs'>
                      {audit.after_hash
                        ? format.shortHash(audit.after_hash)
                        : '—'}
                    </TableCell>
                    <TableCell className='max-w-72 text-xs break-words'>
                      {auditSummary(audit, t)}
                    </TableCell>
                    <TableCell>
                      {format.timestamp(audit.created_time_ms)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          <div className='divide-y rounded-lg border lg:hidden'>
            {query.data.items.map((audit) => (
              <article key={audit.id} className='space-y-2 p-3'>
                <div className='flex items-start justify-between gap-3'>
                  <div>
                    <div className='font-medium'>
                      {t('Audit #{{id}}', { id: audit.id })}
                    </div>
                    <div className='text-muted-foreground text-xs'>
                      {t(auditSubjectLabels[audit.subject_type])}
                      {audit.subject_id > 0 ? ` #${audit.subject_id}` : ''}
                    </div>
                  </div>
                  <Badge variant='outline'>
                    {t(auditActionLabels[audit.action] ?? audit.action)}
                  </Badge>
                </div>
                <p className='text-sm break-words'>{auditSummary(audit, t)}</p>
                <div className='text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs'>
                  <span>
                    {audit.actor_id > 0
                      ? t('Actor #{{id}}', { id: audit.actor_id })
                      : t('System')}
                  </span>
                  <span>{format.timestamp(audit.created_time_ms)}</span>
                </div>
              </article>
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
    </section>
  )
}
