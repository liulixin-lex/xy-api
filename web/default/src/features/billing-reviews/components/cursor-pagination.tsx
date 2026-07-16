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
  ArrowLeftDoubleIcon,
  ArrowRight01Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

export function BillingOperationsCursorPagination(props: {
  cursor: number
  nextCursor: number | string
  disabled?: boolean
  onCursorChange: (cursor: number) => void
}) {
  const { t } = useTranslation()
  const next = Number(props.nextCursor || 0)
  return (
    <div className='flex flex-wrap items-center justify-end gap-2 border-t pt-3'>
      <Button
        size='sm'
        variant='outline'
        disabled={props.disabled || props.cursor <= 0}
        onClick={() => props.onCursorChange(0)}
      >
        <HugeiconsIcon
          icon={ArrowLeftDoubleIcon}
          data-icon='inline-start'
          aria-hidden='true'
        />
        {t('First page')}
      </Button>
      <Button
        size='sm'
        variant='outline'
        disabled={props.disabled || !Number.isFinite(next) || next <= 0}
        onClick={() => props.onCursorChange(next)}
      >
        {t('Next page')}
        <HugeiconsIcon
          icon={ArrowRight01Icon}
          data-icon='inline-end'
          aria-hidden='true'
        />
      </Button>
    </div>
  )
}
