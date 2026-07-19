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
import { ArrowLeft01Icon, ArrowRight01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

export function TablePagination(props: {
  page: number
  pageSize: number
  total: number
  disabled?: boolean
  onPageChange: (page: number) => void
}) {
  const { t } = useTranslation()
  const totalPages = Math.max(1, Math.ceil(props.total / props.pageSize))
  const firstItem =
    props.total === 0 ? 0 : (props.page - 1) * props.pageSize + 1
  const lastItem = Math.min(props.page * props.pageSize, props.total)

  return (
    <div className='flex flex-col items-center justify-between gap-2 border-t px-3 py-2 sm:flex-row'>
      <p className='text-muted-foreground text-xs tabular-nums'>
        {t('{{start}}–{{end}} of {{total}}', {
          start: firstItem,
          end: lastItem,
          total: props.total,
        })}
      </p>
      <div className='flex items-center gap-1'>
        <Button
          type='button'
          variant='outline'
          size='icon-sm'
          disabled={props.disabled || props.page <= 1}
          aria-label={t('Previous page')}
          onClick={() => props.onPageChange(props.page - 1)}
        >
          <HugeiconsIcon icon={ArrowLeft01Icon} strokeWidth={2} />
        </Button>
        <span className='min-w-20 text-center text-xs tabular-nums'>
          {t('Page {{page}} of {{total}}', {
            page: props.page,
            total: totalPages,
          })}
        </span>
        <Button
          type='button'
          variant='outline'
          size='icon-sm'
          disabled={props.disabled || props.page >= totalPages}
          aria-label={t('Next page')}
          onClick={() => props.onPageChange(props.page + 1)}
        >
          <HugeiconsIcon icon={ArrowRight01Icon} strokeWidth={2} />
        </Button>
      </div>
    </div>
  )
}
