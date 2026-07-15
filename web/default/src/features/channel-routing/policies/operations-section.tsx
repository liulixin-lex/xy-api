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
import { Eye, RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

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

import { listChannelRoutingOperations } from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingIdentityText } from '../components/identity-text'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../components/page-state'
import { ChannelRoutingCursorPagination } from '../components/pagination-bar'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import {
  channelRoutingOperationDisplayStatus,
  channelRoutingOperationTypeLabel,
} from '../lib/operations'
import { ChannelRoutingOperationSheet } from './operation-sheet'

type RoutingOperationTypeFilter =
  | ''
  | 'canary_auto_rollback'
  | 'policy_simulation'
  | 'historical_simulation'
  | 'policy_publish'
  | 'policy_manual_rollback'
  | 'cost_sync'
  | 'active_probe'
  | 'audit_export'
  | 'breaker_reset'

type RoutingOperationStatusFilter =
  | ''
  | 'pending'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'superseded'

export function ChannelRoutingOperationsSection(props: {
  cursor: number
  operationType: RoutingOperationTypeFilter
  operationStatus: RoutingOperationStatusFilter
  onSearchChange: (patch: {
    operationCursor?: number
    operationType?: RoutingOperationTypeFilter
    operationStatus?: RoutingOperationStatusFilter
  }) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const [selectedOperationId, setSelectedOperationId] = useState<number | null>(
    null
  )
  const params = {
    cursor: props.cursor || undefined,
    limit: 10,
    type: props.operationType || undefined,
    status: props.operationStatus || undefined,
  }
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.operations(params),
    queryFn: () => listChannelRoutingOperations(params),
  })

  return (
    <section
      className='space-y-3 border-t pt-5'
      aria-labelledby='routing-operations-heading'
    >
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <div>
          <h2
            id='routing-operations-heading'
            className='text-base font-semibold'
          >
            {t('Recent operations')}
          </h2>
          <p className='text-muted-foreground mt-1 text-xs'>
            {t(
              'Persistent simulation, control, deployment, and export records'
            )}
          </p>
        </div>
        <Button
          size='icon-sm'
          variant='outline'
          aria-label={t('Refresh operations')}
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
      </div>

      <div className='flex flex-wrap gap-2'>
        <NativeSelect
          size='sm'
          value={props.operationType}
          aria-label={t('Operation type')}
          onChange={(event) =>
            props.onSearchChange({
              operationCursor: 0,
              operationType: event.target.value as RoutingOperationTypeFilter,
            })
          }
        >
          <NativeSelectOption value=''>
            {t('All operation types')}
          </NativeSelectOption>
          <NativeSelectOption value='policy_simulation'>
            {t('Policy simulation')}
          </NativeSelectOption>
          <NativeSelectOption value='historical_simulation'>
            {t('Historical simulation')}
          </NativeSelectOption>
          <NativeSelectOption value='policy_publish'>
            {t('Policy publish')}
          </NativeSelectOption>
          <NativeSelectOption value='policy_manual_rollback'>
            {t('Manual rollback')}
          </NativeSelectOption>
          <NativeSelectOption value='canary_auto_rollback'>
            {t('Automatic Canary rollback')}
          </NativeSelectOption>
          <NativeSelectOption value='cost_sync'>
            {t('Cost sync')}
          </NativeSelectOption>
          <NativeSelectOption value='active_probe'>
            {t('Active probe')}
          </NativeSelectOption>
          <NativeSelectOption value='audit_export'>
            {t('Audit export')}
          </NativeSelectOption>
          <NativeSelectOption value='breaker_reset'>
            {t('Breaker reset')}
          </NativeSelectOption>
        </NativeSelect>
        <NativeSelect
          size='sm'
          value={props.operationStatus}
          aria-label={t('Operation status')}
          onChange={(event) =>
            props.onSearchChange({
              operationCursor: 0,
              operationStatus: event.target
                .value as RoutingOperationStatusFilter,
            })
          }
        >
          <NativeSelectOption value=''>
            {t('All operation statuses')}
          </NativeSelectOption>
          <NativeSelectOption value='pending'>
            {t('Pending')}
          </NativeSelectOption>
          <NativeSelectOption value='running'>
            {t('Running')}
          </NativeSelectOption>
          <NativeSelectOption value='succeeded'>
            {t('Succeeded')}
          </NativeSelectOption>
          <NativeSelectOption value='failed'>{t('Failed')}</NativeSelectOption>
          <NativeSelectOption value='superseded'>
            {t('Superseded')}
          </NativeSelectOption>
        </NativeSelect>
      </div>

      {query.isLoading ? <ChannelRoutingLoadingState rows={4} /> : null}
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
          title={t('No routing operations')}
          description={t('No persistent operations match the current filters.')}
        />
      ) : null}

      {query.data && query.data.items.length > 0 ? (
        <>
          <div className='hidden overflow-hidden rounded-lg border md:block'>
            <Table scrollAreaLabel={t('Recent operations')}>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Operation')}</TableHead>
                  <TableHead>{t('Type')}</TableHead>
                  <TableHead>{t('Subject')}</TableHead>
                  <TableHead>{t('Status')}</TableHead>
                  <TableHead>{t('Actor')}</TableHead>
                  <TableHead>{t('Started')}</TableHead>
                  <TableHead className='w-14'>
                    <span className='sr-only'>{t('Actions')}</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {query.data.items.map((operation) => (
                  <TableRow key={operation.id}>
                    <TableCell className='font-medium'>
                      #{operation.id}
                    </TableCell>
                    <TableCell>
                      <ChannelRoutingIdentityText
                        text={t(
                          channelRoutingOperationTypeLabel(operation.type)
                        )}
                        className='max-w-44 text-xs'
                      />
                    </TableCell>
                    <TableCell>
                      {operation.subject_type} #{operation.subject_id}
                    </TableCell>
                    <TableCell>
                      <ChannelRoutingStatusBadge
                        status={channelRoutingOperationDisplayStatus(operation)}
                      />
                    </TableCell>
                    <TableCell>#{operation.actor_id}</TableCell>
                    <TableCell>
                      {format.timestamp(operation.created_time_ms)}
                    </TableCell>
                    <TableCell>
                      <Tooltip>
                        <TooltipTrigger
                          render={
                            <Button
                              size='icon-sm'
                              variant='ghost'
                              aria-label={t('View operation')}
                              onClick={() =>
                                setSelectedOperationId(operation.id)
                              }
                            />
                          }
                        >
                          <Eye aria-hidden='true' />
                        </TooltipTrigger>
                        <TooltipContent>{t('View operation')}</TooltipContent>
                      </Tooltip>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          <div className='divide-y rounded-lg border md:hidden'>
            {query.data.items.map((operation) => (
              <button
                key={operation.id}
                type='button'
                className='hover:bg-muted/50 min-h-11 w-full p-3 text-left transition-colors'
                onClick={() => setSelectedOperationId(operation.id)}
              >
                <div className='flex items-start justify-between gap-3'>
                  <div className='min-w-0'>
                    <div className='text-sm font-medium'>
                      {t('Operation #{{id}}', { id: operation.id })}
                    </div>
                    <ChannelRoutingIdentityText
                      text={t(channelRoutingOperationTypeLabel(operation.type))}
                      className='text-muted-foreground text-xs'
                      withinInteractive
                    />
                  </div>
                  <ChannelRoutingStatusBadge
                    status={channelRoutingOperationDisplayStatus(operation)}
                  />
                </div>
                <div className='text-muted-foreground mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs'>
                  <span>
                    {operation.subject_type} #{operation.subject_id}
                  </span>
                  <span>{format.timestamp(operation.created_time_ms)}</span>
                </div>
              </button>
            ))}
          </div>

          <ChannelRoutingCursorPagination
            cursor={props.cursor}
            nextCursor={query.data.next_cursor}
            disabled={query.isRefetchError}
            onCursorChange={(operationCursor) =>
              props.onSearchChange({ operationCursor })
            }
          />
        </>
      ) : null}

      <ChannelRoutingOperationSheet
        operationId={selectedOperationId}
        open={selectedOperationId != null}
        onOpenChange={(open) => {
          if (!open) setSelectedOperationId(null)
        }}
      />
    </section>
  )
}
