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
  ArrowLeft01Icon,
  ArrowRight01Icon,
  ArrowTurnBackwardIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

export function OperationsAuditCursorPager(props: {
  hasPrevious: boolean
  hasNext: boolean
  disabled?: boolean
  onFirst: () => void
  onPrevious: () => void
  onNext: () => void
}) {
  const { t } = useTranslation()
  return (
    <div className='flex flex-wrap items-center justify-end gap-2 border-t pt-3'>
      <Button
        size='sm'
        variant='outline'
        disabled={props.disabled || !props.hasPrevious}
        onClick={props.onFirst}
      >
        <HugeiconsIcon
          icon={ArrowTurnBackwardIcon}
          data-icon='inline-start'
          aria-hidden='true'
        />
        {t('First page')}
      </Button>
      <Button
        size='sm'
        variant='outline'
        disabled={props.disabled || !props.hasPrevious}
        onClick={props.onPrevious}
      >
        <HugeiconsIcon
          icon={ArrowLeft01Icon}
          data-icon='inline-start'
          aria-hidden='true'
        />
        {t('Previous page')}
      </Button>
      <Button
        size='sm'
        variant='outline'
        disabled={props.disabled || !props.hasNext}
        onClick={props.onNext}
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
