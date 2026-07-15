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
import { getRouteApi } from '@tanstack/react-router'
import { Eye, FileJson, RefreshCw, Search, X } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
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
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import { listChannelRoutingDecisions } from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingIdentityText } from '../components/identity-text'
import { ChannelRoutingPageFrame } from '../components/page-frame'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../components/page-state'
import { ChannelRoutingCursorPagination } from '../components/pagination-bar'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import { ChannelRoutingAuditExportSheet } from './audit-export-sheet'
import { ChannelRoutingDecisionSheet } from './decision-sheet'

const route = getRouteApi('/_authenticated/channel-routing/$section')
const decisionAuditMaxRangeSeconds = 31 * 24 * 60 * 60

function localDateTimeValue(timestamp?: number): string {
  if (timestamp == null || !Number.isFinite(timestamp) || timestamp <= 0) {
    return ''
  }
  const date = new Date(timestamp * 1_000)
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000)
  return local.toISOString().slice(0, 19)
}

function localDateTimeSeconds(value: FormDataEntryValue | null) {
  const text = String(value ?? '').trim()
  if (!text) return undefined
  const milliseconds = new Date(text).getTime()
  if (!Number.isFinite(milliseconds) || milliseconds <= 0) return Number.NaN
  return Math.floor(milliseconds / 1_000)
}

export function ChannelRoutingDecisionsPage() {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const user = useAuthStore((state) => state.auth.user)
  const canAuditExport = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.AUDIT_EXPORT
  )
  const [selectedDecision, setSelectedDecision] = useState<string | null>(null)
  const [auditExportOpen, setAuditExportOpen] = useState(false)
  const [timeRangeError, setTimeRangeError] = useState('')
  const cursor = search.cursor ?? 0
  const limit = search.limit ?? 20
  const matched = search.matched ?? 'all'
  const cohort = search.cohort ?? 'all'
  const queryParams = {
    limit,
    cursor: cursor || undefined,
    group: search.group || undefined,
    model: search.model || undefined,
    request_id: search.requestId || undefined,
    matched: matched === 'all' ? undefined : matched,
    activation_id: search.activationId,
    cohort: cohort === 'all' ? undefined : cohort,
    from_time: search.fromTime,
    to_time: search.toTime,
  }
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.decisions(queryParams),
    queryFn: () => listChannelRoutingDecisions(queryParams),
  })

  const updateSearch = (
    patch: Record<string, string | number | boolean | undefined>
  ) => {
    void navigate({
      search: (previous) => ({ ...previous, ...patch }),
      replace: true,
    })
  }
  const handleFilters = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const fromTime = localDateTimeSeconds(form.get('fromTime'))
    const toTime = localDateTimeSeconds(form.get('toTime'))
    const oneBoundaryMissing = (fromTime == null) !== (toTime == null)
    const invalidBoundary = Number.isNaN(fromTime) || Number.isNaN(toTime)
    const invalidOrder = fromTime != null && toTime != null && toTime < fromTime
    const rangeTooLong =
      fromTime != null &&
      toTime != null &&
      toTime - fromTime > decisionAuditMaxRangeSeconds
    if (oneBoundaryMissing || invalidBoundary || invalidOrder) {
      setTimeRangeError(t('Select a valid time range'))
      return
    }
    if (rangeTooLong) {
      setTimeRangeError(t('Decision audit ranges can cover up to 31 days'))
      return
    }
    setTimeRangeError('')
    updateSearch({
      cursor: 0,
      group: String(form.get('group') ?? '').trim(),
      model: String(form.get('model') ?? '').trim(),
      requestId: String(form.get('requestId') ?? '').trim(),
      fromTime,
      toTime,
    })
  }

  return (
    <>
      <ChannelRoutingPageFrame
        activeSection='decisions'
        title={t('Decision audit')}
        actions={
          <div className='flex items-center gap-2'>
            <Button
              size='icon-sm'
              variant='outline'
              aria-label={t('Refresh')}
              disabled={query.isFetching}
              onClick={() => void query.refetch()}
            >
              <RefreshCw
                aria-hidden='true'
                className={
                  query.isFetching
                    ? 'animate-spin motion-reduce:animate-none'
                    : undefined
                }
              />
            </Button>
            {canAuditExport ? (
              <Button size='sm' onClick={() => setAuditExportOpen(true)}>
                <FileJson aria-hidden='true' />
                {t('Export audit')}
              </Button>
            ) : null}
          </div>
        }
      >
        <div className='space-y-3 pb-2'>
          <form
            key={`${search.group}-${search.model}-${search.requestId}-${search.fromTime}-${search.toTime}`}
            className='grid items-end gap-2 sm:grid-cols-2 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(170px,.85fr)_minmax(170px,.85fr)_auto]'
            onSubmit={handleFilters}
          >
            <div className='space-y-1'>
              <label
                className='text-muted-foreground text-xs'
                htmlFor='decision-audit-group'
              >
                {t('Group')}
              </label>
              <Input
                id='decision-audit-group'
                name='group'
                defaultValue={search.group}
                placeholder={t('Group')}
              />
            </div>
            <div className='space-y-1'>
              <label
                className='text-muted-foreground text-xs'
                htmlFor='decision-audit-model'
              >
                {t('Model')}
              </label>
              <Input
                id='decision-audit-model'
                name='model'
                defaultValue={search.model}
                placeholder={t('Model')}
              />
            </div>
            <div className='space-y-1'>
              <label
                className='text-muted-foreground text-xs'
                htmlFor='decision-audit-request'
              >
                {t('Request ID')}
              </label>
              <Input
                id='decision-audit-request'
                name='requestId'
                defaultValue={search.requestId}
                placeholder={t('Request ID')}
              />
            </div>
            <div className='space-y-1'>
              <label
                className='text-muted-foreground text-xs'
                htmlFor='decision-audit-from-time'
              >
                {t('Start time')}
              </label>
              <Input
                id='decision-audit-from-time'
                name='fromTime'
                type='datetime-local'
                step={1}
                defaultValue={localDateTimeValue(search.fromTime)}
              />
            </div>
            <div className='space-y-1'>
              <label
                className='text-muted-foreground text-xs'
                htmlFor='decision-audit-to-time'
              >
                {t('End time')}
              </label>
              <Input
                id='decision-audit-to-time'
                name='toTime'
                type='datetime-local'
                step={1}
                defaultValue={localDateTimeValue(search.toTime)}
              />
            </div>
            <Button type='submit' size='sm' variant='outline'>
              <Search aria-hidden='true' />
              {t('Apply filters')}
            </Button>
          </form>
          {timeRangeError ? (
            <p className='text-destructive text-xs' role='alert'>
              {timeRangeError}
            </p>
          ) : null}
          <div className='flex flex-wrap items-center gap-2'>
            <NativeSelect
              size='sm'
              value={matched === 'all' ? 'all' : String(matched)}
              aria-label={t('Selection match')}
              onChange={(event) =>
                updateSearch({
                  cursor: 0,
                  matched:
                    event.target.value === 'all'
                      ? 'all'
                      : event.target.value === 'true',
                })
              }
            >
              <NativeSelectOption value='all'>
                {t('All decisions')}
              </NativeSelectOption>
              <NativeSelectOption value='true'>
                {t('Matched')}
              </NativeSelectOption>
              <NativeSelectOption value='false'>
                {t('Different')}
              </NativeSelectOption>
            </NativeSelect>
            <NativeSelect
              size='sm'
              value={cohort}
              aria-label={t('Cohort')}
              onChange={(event) =>
                updateSearch({ cursor: 0, cohort: event.target.value })
              }
            >
              <NativeSelectOption value='all'>
                {t('All cohorts')}
              </NativeSelectOption>
              <NativeSelectOption value='control'>
                {t('Control')}
              </NativeSelectOption>
              <NativeSelectOption value='canary'>
                {t('Canary')}
              </NativeSelectOption>
            </NativeSelect>
            {search.group ||
            search.model ||
            search.requestId ||
            matched !== 'all' ||
            cohort !== 'all' ||
            search.activationId != null ||
            search.fromTime != null ||
            search.toTime != null ? (
              <Button
                size='sm'
                variant='ghost'
                onClick={() =>
                  updateSearch({
                    cursor: 0,
                    group: '',
                    model: '',
                    requestId: '',
                    matched: 'all',
                    cohort: 'all',
                    activationId: undefined,
                    fromTime: undefined,
                    toTime: undefined,
                  })
                }
              >
                <X aria-hidden='true' />
                {t('Clear')}
              </Button>
            ) : null}
          </div>

          {query.isLoading ? <ChannelRoutingLoadingState /> : null}
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
              title={t('No routing decisions')}
              description={t('No decision audits match the current filters.')}
            />
          ) : null}

          {query.data && query.data.items.length > 0 ? (
            <>
              <div className='hidden overflow-hidden rounded-lg border lg:block'>
                <Table scrollAreaLabel={t('Decision audit')}>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t('Request')}</TableHead>
                      <TableHead>{t('Group / model')}</TableHead>
                      <TableHead>{t('Algorithm')}</TableHead>
                      <TableHead>{t('Route')}</TableHead>
                      <TableHead>{t('Candidates')}</TableHead>
                      <TableHead>{t('Created')}</TableHead>
                      <TableHead className='w-10'>
                        <span className='sr-only'>{t('Actions')}</span>
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {query.data.items.map((decision) => (
                      <TableRow key={decision.decision_id}>
                        <TableCell className='max-w-56'>
                          <div className='truncate font-mono text-xs'>
                            {decision.request_id}
                          </div>
                          <div className='text-muted-foreground truncate font-mono text-xs'>
                            {decision.decision_id}
                          </div>
                        </TableCell>
                        <TableCell>
                          <div className='font-medium'>
                            {decision.group_name}
                          </div>
                          <div className='text-muted-foreground text-xs'>
                            {decision.model_name}
                          </div>
                        </TableCell>
                        <TableCell>
                          <div className='text-xs'>
                            {decision.algorithm_version}
                          </div>
                          <div className='text-muted-foreground text-xs'>
                            r{decision.snapshot_revision}
                          </div>
                        </TableCell>
                        <TableCell>
                          <ChannelRoutingStatusBadge
                            status={
                              decision.observed_matches_actual
                                ? 'success'
                                : 'degraded'
                            }
                            label={`${decision.actual_channel_id} → ${decision.observed_channel_id}`}
                          />
                        </TableCell>
                        <TableCell>
                          {t('{{eligible}} / {{total}} eligible', {
                            eligible: decision.eligible_count,
                            total: decision.candidate_count,
                          })}
                        </TableCell>
                        <TableCell>
                          {format.timestamp(decision.created_time)}
                        </TableCell>
                        <TableCell>
                          <Button
                            size='icon-sm'
                            variant='ghost'
                            aria-label={t('View decision')}
                            onClick={() =>
                              setSelectedDecision(decision.decision_id)
                            }
                          >
                            <Eye aria-hidden='true' />
                          </Button>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>

              <div className='divide-y rounded-lg border lg:hidden'>
                {query.data.items.map((decision) => (
                  <button
                    key={decision.decision_id}
                    type='button'
                    className='hover:bg-muted/50 w-full p-3 text-left transition-colors'
                    onClick={() => setSelectedDecision(decision.decision_id)}
                  >
                    <div className='flex items-start justify-between gap-3'>
                      <div className='min-w-0'>
                        <ChannelRoutingIdentityText
                          text={`${decision.group_name} · ${decision.model_name}`}
                          className='text-sm font-medium'
                          withinInteractive
                        />
                        <ChannelRoutingIdentityText
                          text={decision.request_id}
                          className='text-muted-foreground font-mono text-xs'
                          breakAll
                          withinInteractive
                        />
                      </div>
                      <ChannelRoutingStatusBadge
                        status={
                          decision.observed_matches_actual
                            ? 'success'
                            : 'degraded'
                        }
                        label={
                          decision.observed_matches_actual
                            ? t('Matched')
                            : t('Different')
                        }
                      />
                    </div>
                    <div className='text-muted-foreground mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs'>
                      <span>{decision.algorithm_version}</span>
                      <span>r{decision.snapshot_revision}</span>
                      <span>{format.timestamp(decision.created_time)}</span>
                    </div>
                  </button>
                ))}
              </div>

              <ChannelRoutingCursorPagination
                cursor={cursor}
                nextCursor={query.data.next_cursor}
                disabled={query.isRefetchError}
                onCursorChange={(nextCursor) =>
                  updateSearch({ cursor: nextCursor })
                }
              />
            </>
          ) : null}
        </div>
      </ChannelRoutingPageFrame>

      <ChannelRoutingDecisionSheet
        decisionId={selectedDecision}
        open={Boolean(selectedDecision)}
        onOpenChange={(open) => {
          if (!open) setSelectedDecision(null)
        }}
      />
      <ChannelRoutingAuditExportSheet
        open={auditExportOpen}
        canAuditExport={canAuditExport}
        initialFromTime={search.fromTime}
        initialToTime={search.toTime}
        onOpenChange={setAuditExportOpen}
      />
    </>
  )
}
