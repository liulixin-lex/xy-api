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
  MapPin,
  Network,
  RefreshCw,
  ShieldCheck,
  TriangleAlert,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { ChannelRoutingBreakerResetDialog } from '../components/breaker-reset-dialog'
import { ChannelRoutingIdentityText } from '../components/identity-text'
import { ChannelRoutingRefetchErrorAlert } from '../components/page-state'
import { ChannelRoutingPagination } from '../components/pagination-bar'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import type { EndpointBreaker, EndpointBreakerSource } from '../types'

function EndpointBreakerSourceSummary(props: {
  label: string
  source: EndpointBreakerSource
}) {
  const { t } = useTranslation()

  return (
    <div className='min-w-0 space-y-1'>
      <div className='text-muted-foreground text-xs'>{props.label}</div>
      <ChannelRoutingStatusBadge
        status={
          props.source.known ? props.source.state || 'unknown' : 'unknown'
        }
      />
      {props.source.reason ? (
        <div className='text-muted-foreground max-w-56 text-xs [overflow-wrap:anywhere] break-words'>
          {props.source.reason}
        </div>
      ) : null}
      {!props.source.known ? (
        <div className='text-muted-foreground text-xs'>{t('No evidence')}</div>
      ) : null}
    </div>
  )
}

export function ChannelRoutingEndpointNetworkSection(props: {
  endpoints: EndpointBreaker[]
  total: number
  region: string
  stableNodeId: string
  quorumEligible: boolean
  canOperate: boolean
  loading: boolean
  fetching?: boolean
  refetchError?: boolean
  error: unknown
  onRetry: () => void
  page?: number
  pageSize?: number
  onPageChange?: (page: number) => void
  onPageSizeChange?: (pageSize: number) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const blockingError = props.error != null && !props.refetchError

  return (
    <section aria-labelledby='endpoint-network-title'>
      <div className='mb-2 flex flex-wrap items-end justify-between gap-2'>
        <div>
          <h2
            id='endpoint-network-title'
            className='flex items-center gap-2 text-sm font-semibold'
          >
            <Network className='size-4' aria-hidden='true' />
            {t('Endpoint network breakers')}
          </h2>
          <p className='text-muted-foreground mt-0.5 text-xs'>
            {t('Endpoint × gateway region failure domains')}
          </p>
        </div>
        <div className='flex flex-wrap items-center justify-end gap-2'>
          <ChannelRoutingStatusBadge
            status={props.quorumEligible ? 'ready' : 'unavailable'}
            label={
              props.quorumEligible
                ? t('Shared quorum eligible')
                : t('Local evidence only')
            }
          />
          <span className='text-muted-foreground text-xs'>
            {t('{{count}} endpoints', { count: props.total })}
          </span>
        </div>
      </div>

      {props.loading ? (
        <div className='space-y-2' aria-busy='true' aria-live='polite'>
          {Array.from({ length: 3 }, (_, index) => (
            <Skeleton
              key={`endpoint-network-probe-${index}`}
              className='h-14 w-full motion-reduce:animate-none'
            />
          ))}
        </div>
      ) : null}

      {props.refetchError ? (
        <ChannelRoutingRefetchErrorAlert
          isFetching={props.fetching ?? false}
          onRetry={props.onRetry}
        />
      ) : null}

      {blockingError ? (
        <div
          className='border-destructive/30 bg-destructive/5 text-destructive flex flex-wrap items-center gap-3 rounded-lg border p-3 text-sm'
          role='alert'
        >
          <TriangleAlert className='size-4 shrink-0' aria-hidden='true' />
          <span className='min-w-0 flex-1'>
            {t('Could not load endpoint network health.')}
          </span>
          <Button size='sm' variant='outline' onClick={props.onRetry}>
            <RefreshCw aria-hidden='true' />
            {t('Retry')}
          </Button>
        </div>
      ) : null}

      {!props.loading && !blockingError && props.endpoints.length === 0 ? (
        <div className='text-muted-foreground rounded-lg border px-4 py-8 text-center text-sm'>
          {t('No endpoint breaker evidence is available yet.')}
        </div>
      ) : null}

      {!props.loading && !blockingError && props.endpoints.length > 0 ? (
        <>
          <div className='hidden overflow-hidden rounded-lg border xl:block'>
            <Table
              className='min-w-[64rem]'
              scrollAreaLabel={t('Endpoint network table')}
            >
              <TableHeader>
                <TableRow>
                  <TableHead className='min-w-56'>
                    {t('Endpoint / region')}
                  </TableHead>
                  <TableHead className='min-w-48'>
                    {t('Effective state')}
                  </TableHead>
                  <TableHead className='min-w-56'>
                    {t('Local / shared')}
                  </TableHead>
                  <TableHead className='min-w-40'>{t('Evidence')}</TableHead>
                  <TableHead className='min-w-36'>{t('Updated')}</TableHead>
                  <TableHead className='w-10'>
                    <span className='sr-only'>{t('Actions')}</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {props.endpoints.map((endpoint) => (
                  <TableRow
                    key={`${endpoint.endpoint_authority}-${endpoint.region}`}
                  >
                    <TableCell className='max-w-96'>
                      <ChannelRoutingIdentityText
                        text={endpoint.endpoint_authority}
                        className='font-mono text-xs'
                        breakAll
                      />
                      <div className='text-muted-foreground mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs'>
                        <span className='inline-flex items-center gap-1'>
                          <MapPin className='size-3' aria-hidden='true' />
                          {endpoint.region || t('Unknown region')}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className='max-w-56 align-top whitespace-normal'>
                      <div className='flex flex-wrap items-center gap-2'>
                        <ChannelRoutingStatusBadge
                          status={
                            endpoint.effective.known
                              ? endpoint.effective.state || 'unknown'
                              : 'unknown'
                          }
                        />
                      </div>
                      {endpoint.effective.reason ? (
                        <div className='text-muted-foreground mt-1 max-w-56 text-xs [overflow-wrap:anywhere] break-words whitespace-normal'>
                          {endpoint.effective.reason}
                        </div>
                      ) : null}
                      {endpoint.effective.cooldown_until > 0 ? (
                        <div className='text-muted-foreground mt-1 max-w-56 text-xs [overflow-wrap:anywhere] break-words whitespace-normal'>
                          {t('Cooldown until {{time}}', {
                            time: format.timestamp(
                              endpoint.effective.cooldown_until
                            ),
                          })}
                        </div>
                      ) : null}
                    </TableCell>
                    <TableCell className='min-w-56 align-top whitespace-normal'>
                      <div className='grid grid-cols-2 gap-3'>
                        <EndpointBreakerSourceSummary
                          label={t('Local')}
                          source={endpoint.local}
                        />
                        <EndpointBreakerSourceSummary
                          label={t('Shared')}
                          source={endpoint.shared}
                        />
                      </div>
                    </TableCell>
                    <TableCell className='max-w-52 align-top text-xs whitespace-normal'>
                      <div>
                        {typeof endpoint.effective.evidence_count === 'number'
                          ? t('{{count}} observations', {
                              count: endpoint.effective.evidence_count,
                            })
                          : t('Unknown')}
                      </div>
                      <div className='text-muted-foreground mt-1 break-words whitespace-normal'>
                        {typeof endpoint.effective.node_count === 'number'
                          ? t('{{count}} nodes', {
                              count: endpoint.effective.node_count,
                            })
                          : t('Unknown')}
                      </div>
                      {typeof endpoint.effective.network_failure_count ===
                      'number' ? (
                        <div className='text-muted-foreground mt-1 break-words whitespace-normal'>
                          {t('{{count}} network failures', {
                            count: endpoint.effective.network_failure_count,
                          })}
                        </div>
                      ) : null}
                    </TableCell>
                    <TableCell className='text-xs'>
                      {format.timestamp(endpoint.effective.updated_at)}
                    </TableCell>
                    <TableCell>
                      {props.canOperate && !props.refetchError ? (
                        <ChannelRoutingBreakerResetDialog
                          compact
                          request={{
                            scope: 'endpoint',
                            endpoint_authority: endpoint.endpoint_authority,
                            region: endpoint.region,
                          }}
                          targetLabel={`${endpoint.endpoint_authority} · ${endpoint.region}`}
                        />
                      ) : null}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          <div className='divide-y rounded-lg border xl:hidden'>
            {props.endpoints.map((endpoint) => (
              <article
                key={`${endpoint.endpoint_authority}-${endpoint.region}`}
                className='min-w-0 p-3'
              >
                <div className='flex min-w-0 items-start justify-between gap-3'>
                  <div className='min-w-0 flex-1'>
                    <ChannelRoutingIdentityText
                      text={endpoint.endpoint_authority}
                      className='font-mono text-xs font-medium'
                      breakAll
                    />
                    <div className='text-muted-foreground mt-1 inline-flex items-center gap-1 text-xs'>
                      <MapPin className='size-3' aria-hidden='true' />
                      {endpoint.region || t('Unknown region')}
                    </div>
                  </div>
                  <ChannelRoutingStatusBadge
                    status={
                      endpoint.effective.known
                        ? endpoint.effective.state || 'unknown'
                        : 'unknown'
                    }
                  />
                </div>
                {endpoint.effective.reason ? (
                  <p className='text-muted-foreground mt-2 text-xs break-words'>
                    {endpoint.effective.reason}
                  </p>
                ) : null}
                <div className='mt-3 grid grid-cols-2 gap-3 border-y py-3'>
                  <EndpointBreakerSourceSummary
                    label={t('Local')}
                    source={endpoint.local}
                  />
                  <EndpointBreakerSourceSummary
                    label={t('Shared')}
                    source={endpoint.shared}
                  />
                </div>
                <dl className='mt-3 grid grid-cols-2 gap-3 text-xs sm:grid-cols-3'>
                  <div>
                    <dt className='text-muted-foreground'>{t('Evidence')}</dt>
                    <dd className='mt-1 font-medium'>
                      {typeof endpoint.effective.evidence_count === 'number'
                        ? endpoint.effective.evidence_count
                        : t('Unknown')}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground'>{t('Nodes')}</dt>
                    <dd className='mt-1 font-medium'>
                      {typeof endpoint.effective.node_count === 'number'
                        ? endpoint.effective.node_count
                        : t('Unknown')}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground'>{t('Updated')}</dt>
                    <dd className='mt-1 font-medium'>
                      {format.timestamp(endpoint.effective.updated_at)}
                    </dd>
                  </div>
                </dl>
                {props.canOperate && !props.refetchError ? (
                  <div className='mt-3 flex justify-end'>
                    <ChannelRoutingBreakerResetDialog
                      request={{
                        scope: 'endpoint',
                        endpoint_authority: endpoint.endpoint_authority,
                        region: endpoint.region,
                      }}
                      targetLabel={`${endpoint.endpoint_authority} · ${endpoint.region}`}
                    />
                  </div>
                ) : null}
              </article>
            ))}
          </div>

          {props.page != null &&
          props.pageSize != null &&
          props.onPageChange &&
          props.onPageSizeChange ? (
            <ChannelRoutingPagination
              page={props.page}
              pageSize={props.pageSize}
              total={props.total}
              disabled={props.refetchError}
              onPageChange={props.onPageChange}
              onPageSizeChange={props.onPageSizeChange}
            />
          ) : null}

          <div className='text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 text-xs'>
            <span className='inline-flex items-center gap-1'>
              <MapPin className='size-3' aria-hidden='true' />
              {t('Gateway region')}: {props.region || t('Unknown region')}
            </span>
            <span className='inline-flex min-w-0 items-center gap-1'>
              <ShieldCheck className='size-3 shrink-0' aria-hidden='true' />
              <span className='truncate'>
                {t('Stable node')}: {props.stableNodeId || t('Unavailable')}
              </span>
            </span>
          </div>
        </>
      ) : null}
    </section>
  )
}
