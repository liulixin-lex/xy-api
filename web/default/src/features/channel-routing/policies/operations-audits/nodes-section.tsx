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
  ArrowReloadHorizontalIcon,
  ServerStack02Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { listChannelRoutingNodes } from '../../api/client'
import { channelRoutingQueryKeys } from '../../api/query-keys'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../../components/page-state'
import { ChannelRoutingStatusBadge } from '../../components/status-badge'
import { useChannelRoutingFormatters } from '../../lib/format'
import type { ChannelRoutingNode } from '../../types'
import { OperationsAuditCursorPager } from './cursor-pager'

function nodeRevisionDistance(
  node: ChannelRoutingNode,
  translate: TFunction,
  convergedLabel: string
): string {
  if (node.revision_lag > 0) {
    return translate('{{count}} behind', { count: node.revision_lag })
  }
  if (node.revision_ahead > 0) {
    return translate('{{count}} ahead', { count: node.revision_ahead })
  }
  return convergedLabel
}

export function ChannelRoutingNodesSection() {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const [cursor, setCursor] = useState('')
  const [history, setHistory] = useState<string[]>([])
  const params = { limit: 20, cursor: cursor || undefined }
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.nodes(params),
    queryFn: () => listChannelRoutingNodes(params),
  })

  return (
    <section
      className='space-y-3 border-t pt-5'
      aria-labelledby='routing-nodes-heading'
    >
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <div>
          <h2 id='routing-nodes-heading' className='text-base font-semibold'>
            {t('Node propagation')}
          </h2>
          <p className='text-muted-foreground mt-1 text-xs'>
            {t('Per-node policy revision, freshness, lag, and conflict state')}
          </p>
        </div>
        <Button
          size='icon-sm'
          variant='outline'
          aria-label={t('Refresh nodes')}
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

      {query.data && !query.data.control_plane_available ? (
        <Alert>
          <HugeiconsIcon icon={ServerStack02Icon} aria-hidden='true' />
          <AlertTitle>{t('Control-plane revision unavailable')}</AlertTitle>
          <AlertDescription>
            {t(
              'Node heartbeats are visible, but convergence cannot be evaluated until a policy head is available.'
            )}
          </AlertDescription>
        </Alert>
      ) : null}

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
          title={t('No active routing nodes')}
          description={t(
            'No unexpired node checkpoints are currently available.'
          )}
        />
      ) : null}

      {query.data && query.data.items.length > 0 ? (
        <>
          <div className='flex flex-wrap items-center gap-2 text-xs'>
            <Badge variant='outline'>
              {t('Control-plane revision r{{revision}}', {
                revision: query.data.control_plane_revision,
              })}
            </Badge>
            <span className='text-muted-foreground'>
              {t('{{count}} nodes on this page', {
                count: query.data.items.length,
              })}
            </span>
          </div>

          <div className='hidden overflow-hidden rounded-lg border md:block'>
            <Table scrollAreaLabel={t('Node propagation')}>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Node')}</TableHead>
                  <TableHead>{t('Status')}</TableHead>
                  <TableHead>{t('Policy revision')}</TableHead>
                  <TableHead>{t('Lag')}</TableHead>
                  <TableHead>{t('Policy hash')}</TableHead>
                  <TableHead>{t('Last observed')}</TableHead>
                  <TableHead>{t('Expires')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {query.data.items.map((node) => (
                  <TableRow key={node.node_id}>
                    <TableCell>
                      <div className='flex items-center gap-2'>
                        <span
                          className='max-w-44 truncate font-mono text-xs'
                          title={node.node_id}
                        >
                          {node.node_id}
                        </span>
                        {node.current ? (
                          <Badge variant='secondary'>{t('This node')}</Badge>
                        ) : null}
                      </div>
                    </TableCell>
                    <TableCell>
                      <ChannelRoutingStatusBadge status={node.status} />
                    </TableCell>
                    <TableCell>r{node.policy_revision}</TableCell>
                    <TableCell>{nodeRevisionDistance(node, t, '—')}</TableCell>
                    <TableCell className='font-mono text-xs'>
                      {node.policy_hash
                        ? format.shortHash(node.policy_hash)
                        : '—'}
                    </TableCell>
                    <TableCell>
                      {format.timestamp(node.observed_time)}
                    </TableCell>
                    <TableCell>{format.timestamp(node.expires_time)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          <div className='divide-y rounded-lg border md:hidden'>
            {query.data.items.map((node) => (
              <article key={node.node_id} className='space-y-3 p-3'>
                <div className='flex items-start justify-between gap-3'>
                  <div className='min-w-0'>
                    <div
                      className='truncate font-mono text-xs'
                      title={node.node_id}
                    >
                      {node.node_id}
                    </div>
                    <div className='text-muted-foreground mt-1 text-xs'>
                      r{node.policy_revision}
                    </div>
                  </div>
                  <ChannelRoutingStatusBadge status={node.status} />
                </div>
                <dl className='grid grid-cols-2 gap-2 text-xs'>
                  <div>
                    <dt className='text-muted-foreground'>{t('Lag')}</dt>
                    <dd className='mt-0.5'>
                      {nodeRevisionDistance(node, t, t('Converged'))}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground'>
                      {t('Last observed')}
                    </dt>
                    <dd className='mt-0.5'>
                      {format.timestamp(node.observed_time)}
                    </dd>
                  </div>
                </dl>
                {node.current ? (
                  <Badge variant='secondary'>{t('This node')}</Badge>
                ) : null}
              </article>
            ))}
          </div>

          <OperationsAuditCursorPager
            hasPrevious={history.length > 0}
            hasNext={Boolean(query.data.next_cursor)}
            disabled={query.isRefetchError}
            onFirst={() => {
              setCursor('')
              setHistory([])
            }}
            onPrevious={() => {
              const previousCursor = history.at(-1) ?? ''
              setHistory((previous) => previous.slice(0, -1))
              setCursor(previousCursor)
            }}
            onNext={() => {
              if (!query.data.next_cursor) return
              setHistory((previous) => [...previous, cursor])
              setCursor(query.data.next_cursor)
            }}
          />
        </>
      ) : null}
    </section>
  )
}
