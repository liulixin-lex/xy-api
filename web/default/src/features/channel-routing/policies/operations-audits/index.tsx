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
import { getRouteApi } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { ChannelRoutingOperationsSection } from '../operations-section'
import { ChannelRoutingControlAuditsSection } from './control-audits-section'
import { ChannelRoutingNodesSection } from './nodes-section'

const route = getRouteApi('/_authenticated/channel-routing/$section')

export function ChannelRoutingOperationsAudits() {
  const { t } = useTranslation()
  const search = route.useSearch()
  const navigate = route.useNavigate()

  return (
    <div className='space-y-5 pb-2'>
      <div className='max-w-3xl'>
        <h2 className='text-base font-semibold'>
          {t('Operations and control audits')}
        </h2>
        <p className='text-muted-foreground mt-1 text-sm text-pretty'>
          {t(
            'Inspect persistent operations, immutable control changes, and per-node propagation from one operational timeline.'
          )}
        </p>
      </div>

      <ChannelRoutingOperationsSection
        cursor={search.operationCursor ?? 0}
        operationType={search.operationType ?? ''}
        operationStatus={search.operationStatus ?? ''}
        onSearchChange={(patch) =>
          void navigate({
            search: (previous) => ({ ...previous, ...patch }),
            replace: true,
          })
        }
      />
      <ChannelRoutingControlAuditsSection />
      <ChannelRoutingNodesSection />
    </div>
  )
}
