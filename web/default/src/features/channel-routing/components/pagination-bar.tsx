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
import { ChevronLeft, ChevronRight, ChevronsLeft } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { CHANNEL_ROUTING_PAGE_SIZES } from '../lib/pagination'

export function ChannelRoutingPagination(props: {
  page: number
  pageSize: number
  total: number
  onPageChange: (page: number) => void
  onPageSizeChange: (pageSize: number) => void
}) {
  const { t } = useTranslation()
  const totalPages = Math.max(1, Math.ceil(props.total / props.pageSize))

  return (
    <div className='flex flex-wrap items-center justify-between gap-2 border-t pt-3'>
      <div className='text-muted-foreground text-xs'>
        {t('{{count}} records', { count: props.total })}
      </div>
      <div className='flex items-center gap-2'>
        <NativeSelect
          size='sm'
          value={String(props.pageSize)}
          aria-label={t('Rows per page')}
          onChange={(event) =>
            props.onPageSizeChange(Number(event.target.value))
          }
        >
          {CHANNEL_ROUTING_PAGE_SIZES.map((size) => (
            <NativeSelectOption key={size} value={size}>
              {size}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        <span className='text-muted-foreground min-w-24 text-center text-xs'>
          {t('Page {{page}} of {{total}}', {
            page: props.page,
            total: totalPages,
          })}
        </span>
        <Button
          size='icon-sm'
          variant='outline'
          aria-label={t('Previous page')}
          disabled={props.page <= 1}
          onClick={() => props.onPageChange(props.page - 1)}
        >
          <ChevronLeft aria-hidden='true' />
        </Button>
        <Button
          size='icon-sm'
          variant='outline'
          aria-label={t('Next page')}
          disabled={props.page >= totalPages}
          onClick={() => props.onPageChange(props.page + 1)}
        >
          <ChevronRight aria-hidden='true' />
        </Button>
      </div>
    </div>
  )
}

export function ChannelRoutingCursorPagination(props: {
  cursor: number
  nextCursor: number | string
  onCursorChange: (cursor: number) => void
}) {
  const { t } = useTranslation()
  const next = Number(props.nextCursor || 0)
  return (
    <div className='flex flex-wrap items-center justify-end gap-2 border-t pt-3'>
      <Button
        size='sm'
        variant='outline'
        disabled={props.cursor <= 0}
        onClick={() => props.onCursorChange(0)}
      >
        <ChevronsLeft aria-hidden='true' />
        {t('First page')}
      </Button>
      <Button
        size='sm'
        variant='outline'
        disabled={!Number.isFinite(next) || next <= 0}
        onClick={() => props.onCursorChange(next)}
      >
        {t('Next page')}
        <ChevronRight aria-hidden='true' />
      </Button>
    </div>
  )
}
