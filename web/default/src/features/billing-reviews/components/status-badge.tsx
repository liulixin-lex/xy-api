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
  Alert02Icon,
  CheckmarkCircle02Icon,
  Clock01Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'

export function BillingOperationsStatusBadge(props: { status: string }) {
  const { t } = useTranslation()
  const normalized = props.status.trim().toLowerCase()
  if (normalized === 'ready' || normalized === 'succeeded') {
    return (
      <Badge variant='outline' className='border-success/40 bg-success/15'>
        <HugeiconsIcon icon={CheckmarkCircle02Icon} aria-hidden='true' />
        {t(normalized === 'ready' ? 'Ready' : 'Succeeded')}
      </Badge>
    )
  }
  if (normalized === 'pending' || normalized === 'manual_review') {
    return (
      <Badge variant='outline' className='border-warning/40 bg-warning/15'>
        <HugeiconsIcon icon={Clock01Icon} aria-hidden='true' />
        {t(normalized === 'pending' ? 'Pending' : 'Manual review')}
      </Badge>
    )
  }
  return (
    <Badge variant='outline'>
      <HugeiconsIcon icon={Alert02Icon} aria-hidden='true' />
      {t(props.status)}
    </Badge>
  )
}
