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
import { useTranslation } from 'react-i18next'

import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { channelRoutingAlgorithmKind } from './algorithm'

export function ChannelRoutingAlgorithmIdentity(props: {
  algorithm: string
  compact?: boolean
}) {
  const { t } = useTranslation()
  const kind = channelRoutingAlgorithmKind(props.algorithm)
  let status = 'unknown'
  let label = t('Unrecognized algorithm identifier')
  if (kind === 'current') {
    status = 'known'
    label = t('Current algorithm')
  } else if (kind === 'historical') {
    status = 'historical'
    label = t('Historical algorithm identifier')
  }

  return (
    <div className='min-w-0'>
      <div className='flex min-w-0 flex-wrap items-center gap-1.5'>
        <code className='min-w-0 text-xs break-all'>{props.algorithm}</code>
        <ChannelRoutingStatusBadge status={status} label={label} />
      </div>
      {!props.compact && kind === 'historical' ? (
        <p className='text-muted-foreground mt-1 text-xs'>
          {t(
            'Historical decisions keep their original algorithm identifier for audit and replay.'
          )}
        </p>
      ) : null}
    </div>
  )
}
