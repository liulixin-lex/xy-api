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
import { BracesIcon, Undo02Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import {
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../components/page-state'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import type { CurrentRoutingPolicy } from '../types'

export function ChannelRoutingCurrentPolicySection(props: {
  current: CurrentRoutingPolicy | undefined
  isLoading: boolean
  isFetching: boolean
  error: Error | null
  canDeploy: boolean
  onRetry: () => void
  onRollback: () => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const [documentVisible, setDocumentVisible] = useState(false)

  return (
    <section className='space-y-3' aria-labelledby='current-policy-heading'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <div>
          <h2 id='current-policy-heading' className='text-base font-semibold'>
            {t('Current policy')}
          </h2>
          <p className='text-muted-foreground mt-1 text-xs'>
            {t('Active control-plane revision and deployment stage')}
          </p>
        </div>
        {props.current ? (
          <div className='flex items-center gap-2'>
            <Button
              size='sm'
              variant='outline'
              aria-expanded={documentVisible}
              onClick={() => setDocumentVisible((visible) => !visible)}
            >
              <HugeiconsIcon
                icon={BracesIcon}
                data-icon='inline-start'
                strokeWidth={2}
                aria-hidden='true'
              />
              {documentVisible ? t('Hide document') : t('View document')}
            </Button>
            {props.canDeploy && props.current.head.current_revision > 1 ? (
              <Button
                size='sm'
                variant='destructive'
                onClick={props.onRollback}
              >
                <HugeiconsIcon
                  icon={Undo02Icon}
                  data-icon='inline-start'
                  strokeWidth={2}
                  aria-hidden='true'
                />
                {t('Rollback')}
              </Button>
            ) : null}
          </div>
        ) : null}
      </div>

      {props.isLoading ? <ChannelRoutingLoadingState rows={2} /> : null}
      {props.error && !props.current ? (
        <ChannelRoutingErrorState error={props.error} onRetry={props.onRetry} />
      ) : null}
      {props.error && props.current ? (
        <ChannelRoutingRefetchErrorAlert
          isFetching={props.isFetching}
          onRetry={props.onRetry}
        />
      ) : null}
      {props.current ? (
        <div className='border-y py-4'>
          <div className='flex flex-wrap items-center gap-2'>
            <ChannelRoutingStatusBadge
              status={props.current.head.current_stage}
            />
            <span className='text-sm font-semibold'>
              {props.current.head.current_revision > 0
                ? t('Revision {{revision}}', {
                    revision: props.current.head.current_revision,
                  })
                : t('No published revision')}
            </span>
          </div>
          <dl className='mt-4 grid grid-cols-2 gap-x-6 gap-y-4 text-sm sm:grid-cols-3 lg:grid-cols-6'>
            {[
              [t('Activation'), `#${props.current.head.current_activation_id}`],
              [
                t('Content hash'),
                format.shortHash(props.current.head.current_hash),
              ],
              [
                t('Pools'),
                format.number(props.current.revision?.pool_count ?? 0),
              ],
              [
                t('Members'),
                format.number(props.current.revision?.member_count ?? 0),
              ],
              [t('Actor'), `#${props.current.revision?.actor_id ?? 0}`],
              [t('Updated'), format.timestamp(props.current.head.updated_time)],
            ].map(([label, value]) => (
              <div key={label} className='min-w-0'>
                <dt className='text-muted-foreground text-xs'>{label}</dt>
                <dd className='mt-1 font-medium break-words'>{value}</dd>
              </div>
            ))}
          </dl>

          {documentVisible ? (
            <pre className='bg-muted/40 mt-4 max-h-96 overflow-auto rounded-lg border p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap'>
              {JSON.stringify(props.current.document, null, 2)}
            </pre>
          ) : null}
        </div>
      ) : null}
    </section>
  )
}
