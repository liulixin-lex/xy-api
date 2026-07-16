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
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Input } from '@/components/ui/input'

import { ChannelRoutingPagination } from '../../components/pagination-bar'
import { ChannelRoutingStatusBadge } from '../../components/status-badge'
import type { PolicyPoolDocument } from '../../types'

export function PolicyPoolNavigation(props: {
  pools: PolicyPoolDocument[]
  selectedPoolId: number
  onSelectPool: (poolId: number) => void
}) {
  const { t } = useTranslation()
  const [poolSearch, setPoolSearch] = useState('')
  const [poolPage, setPoolPage] = useState(1)
  const [poolPageSize, setPoolPageSize] = useState(20)

  const filteredPools = useMemo(() => {
    const needle = poolSearch.trim().toLowerCase()
    return props.pools.filter((pool) => {
      if (!needle) return true
      return `${pool.pool_id} ${pool.group_name} ${pool.display_name}`
        .toLowerCase()
        .includes(needle)
    })
  }, [poolSearch, props.pools])
  const poolStart = Math.min(
    (poolPage - 1) * poolPageSize,
    filteredPools.length
  )
  const visiblePools = filteredPools.slice(poolStart, poolStart + poolPageSize)

  useEffect(() => {
    const totalPages = Math.max(
      1,
      Math.ceil(filteredPools.length / poolPageSize)
    )
    if (poolPage > totalPages) setPoolPage(totalPages)
  }, [filteredPools.length, poolPage, poolPageSize])

  return (
    <aside className='min-w-0 space-y-3 lg:border-r lg:pr-4'>
      <div>
        <h3 className='text-sm font-semibold'>{t('Pools')}</h3>
        <p className='text-muted-foreground mt-0.5 text-xs'>
          {t('{{count}} pools', { count: props.pools.length })}
        </p>
      </div>
      <Input
        value={poolSearch}
        aria-label={t('Search policy pools')}
        placeholder={t('Search pools')}
        onChange={(event) => {
          setPoolSearch(event.target.value)
          setPoolPage(1)
        }}
      />
      <div className='max-h-72 space-y-1 overflow-auto lg:max-h-[34rem]'>
        {visiblePools.map((pool) => (
          <button
            key={pool.pool_id}
            type='button'
            className='hover:bg-muted focus-visible:ring-ring data-[selected=true]:bg-muted flex min-h-11 w-full min-w-0 items-center justify-between gap-2 rounded-md px-2.5 py-2 text-left text-sm outline-none focus-visible:ring-2'
            data-selected={pool.pool_id === props.selectedPoolId}
            onClick={() => props.onSelectPool(pool.pool_id)}
          >
            <span className='min-w-0'>
              <span className='block truncate font-medium'>
                {pool.display_name || pool.group_name}
              </span>
              <span className='text-muted-foreground block truncate text-xs'>
                #{pool.pool_id} · {pool.group_name}
              </span>
            </span>
            <ChannelRoutingStatusBadge status={pool.deployment_stage} />
          </button>
        ))}
      </div>
      <ChannelRoutingPagination
        page={poolPage}
        pageSize={poolPageSize}
        total={filteredPools.length}
        onPageChange={setPoolPage}
        onPageSizeChange={(size) => {
          setPoolPageSize(size)
          setPoolPage(1)
        }}
      />
    </aside>
  )
}
