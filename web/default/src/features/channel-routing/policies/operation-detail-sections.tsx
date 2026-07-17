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
import { ArrowDown01Icon, SourceCodeIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

import { getChannelRoutingOperationTechnical } from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingErrorState } from '../components/page-state'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import {
  channelRoutingOperationResultRows,
  type ChannelRoutingOperationResultRow,
} from '../lib/operations'
import type { RoutingOperation } from '../types'

type OperationFormatters = ReturnType<typeof useChannelRoutingFormatters>

export function ChannelRoutingOperationResultSection(props: {
  operation: RoutingOperation
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const rows = channelRoutingOperationResultRows(props.operation)
  if (rows.length === 0) return null

  return (
    <section className='flex flex-col gap-3 border-t pt-4'>
      <h3 className='text-sm font-semibold'>{t('Operation result')}</h3>
      <dl className='grid grid-cols-2 gap-x-6 gap-y-4 text-sm sm:grid-cols-3'>
        {rows.map((row) => (
          <div key={row.label} className='min-w-0'>
            <dt className='text-muted-foreground text-xs'>{t(row.label)}</dt>
            <dd className='mt-1 font-medium break-words'>
              {formatOperationResultValue(row, format, t)}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  )
}

export function ChannelRoutingOperationTechnicalSection(props: {
  operationId: number
  sheetOpen: boolean
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const [expanded, setExpanded] = useState(false)
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.operationTechnical(props.operationId),
    queryFn: ({ signal }) =>
      getChannelRoutingOperationTechnical(props.operationId, signal),
    enabled: props.sheetOpen && expanded,
  })
  const technical = query.data?.technical

  return (
    <Collapsible
      className='border-t pt-4'
      open={expanded}
      onOpenChange={setExpanded}
    >
      <CollapsibleTrigger className='hover:bg-muted/50 focus-visible:ring-ring flex min-h-10 w-full items-center justify-between rounded-md px-2 text-left text-sm font-semibold outline-none focus-visible:ring-2'>
        <span className='flex items-center gap-2'>
          <HugeiconsIcon
            icon={SourceCodeIcon}
            className='size-4'
            strokeWidth={2}
            aria-hidden='true'
          />
          {t('Technical details')}
        </span>
        <HugeiconsIcon
          icon={ArrowDown01Icon}
          className={cn(
            'size-4 transition-transform duration-150 motion-reduce:transition-none',
            expanded ? 'rotate-180' : undefined
          )}
          strokeWidth={2}
          aria-hidden='true'
        />
      </CollapsibleTrigger>
      <CollapsibleContent className='flex flex-col gap-4 px-2 pt-3'>
        {query.isLoading ? (
          <div
            className='flex flex-col gap-2'
            role='status'
            aria-live='polite'
            aria-busy='true'
          >
            <span className='sr-only'>{t('Loading')}</span>
            <Skeleton className='h-4 w-2/3' />
            <Skeleton className='h-4 w-1/2' />
            <Skeleton className='h-24 w-full' />
          </div>
        ) : null}
        {query.isError ? (
          <ChannelRoutingErrorState
            error={query.error}
            onRetry={() => void query.refetch()}
          />
        ) : null}
        {technical ? (
          <>
            <dl className='grid grid-cols-1 gap-x-6 gap-y-3 text-sm sm:grid-cols-2'>
              {[
                [t('Idempotency hash'), technical.idempotency_hash],
                [t('Request key hash'), technical.request_key_hash],
                [t('Request payload hash'), technical.request_payload_hash],
                [t('Evaluation hash'), technical.evaluation_hash],
                [t('Result payload hash'), technical.result_payload_hash],
                [t('System task'), technical.system_task_id],
                [
                  t('Claim expires'),
                  technical.claim_until_ms
                    ? format.timestamp(technical.claim_until_ms)
                    : undefined,
                ],
                [
                  t('Outbox'),
                  technical.result_outbox_id
                    ? `#${technical.result_outbox_id}`
                    : undefined,
                ],
              ]
                .filter((entry): entry is [string, string] => Boolean(entry[1]))
                .map(([label, value]) => (
                  <div key={label} className='min-w-0'>
                    <dt className='text-muted-foreground text-xs'>{label}</dt>
                    <dd className='mt-1 font-mono text-xs break-all'>
                      {value}
                    </dd>
                  </div>
                ))}
            </dl>
            {technical.result !== undefined ? (
              <div className='flex flex-col gap-2'>
                <h4 className='text-muted-foreground text-xs font-medium'>
                  {t('Sanitized raw result')}
                </h4>
                <pre className='bg-muted/40 max-h-80 overflow-auto rounded-lg border p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap'>
                  {JSON.stringify(technical.result, null, 2)}
                </pre>
              </div>
            ) : null}
          </>
        ) : null}
      </CollapsibleContent>
    </Collapsible>
  )
}

function formatOperationResultValue(
  row: ChannelRoutingOperationResultRow,
  format: OperationFormatters,
  translate: TFunction
): ReactNode {
  if (row.format === 'status') {
    return <ChannelRoutingStatusBadge status={String(row.value)} />
  }
  if (row.format === 'boolean') {
    return row.value === true ? translate('Yes') : translate('No')
  }
  if (typeof row.value !== 'number') return row.value
  if (row.format === 'basis_points') return format.percent(row.value / 10_000)
  if (row.format === 'bytes') {
    return translate('{{value}} bytes', { value: format.number(row.value) })
  }
  if (row.format === 'ratio') return format.percent(row.value)
  if (row.format === 'timestamp') return format.timestamp(row.value)
  if (row.format === 'usd') {
    return translate('USD {{value}}', { value: format.cost(row.value) })
  }
  return format.number(row.value)
}
