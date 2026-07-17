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
import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'

import {
  routingControlAuditChanges,
  routingControlAuditEntries,
  routingControlAuditFieldLabel,
  routingControlAuditHumanizeKey,
  routingControlAuditRecord,
} from '../../lib/control-audits'
import { useChannelRoutingFormatters } from '../../lib/format'

type AuditFormatters = ReturnType<typeof useChannelRoutingFormatters>

export function ChannelRoutingControlAuditChanges(props: { value: unknown }) {
  const { t } = useTranslation()
  const changes = routingControlAuditChanges(props.value)
  const keyedChanges = keyedAuditValues(changes)
  const record = routingControlAuditRecord(props.value)
  if (changes.length === 0) return null

  return (
    <section className='flex flex-col gap-3 border-t pt-4'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <h3 className='text-sm font-semibold'>{t('Changes')}</h3>
        {record?.truncated === true ? (
          <Badge variant='outline'>{t('Change list truncated')}</Badge>
        ) : null}
      </div>
      <div className='divide-y rounded-lg border'>
        {keyedChanges.map(({ key, value: change }) => (
          <article key={key} className='flex flex-col gap-3 p-3'>
            <div className='flex flex-wrap items-center gap-2'>
              <Badge variant='outline'>
                {t(routingControlAuditHumanizeKey(change.scope))}
              </Badge>
              <span className='text-sm font-medium'>
                {t(routingControlAuditHumanizeKey(change.change))}
              </span>
              {change.field ? (
                <span className='text-muted-foreground text-xs'>
                  <AuditFieldLabel field={change.field} />
                </span>
              ) : null}
            </div>
            {change.pool_id || change.member_id || change.routing_generation ? (
              <div className='text-muted-foreground flex min-w-0 flex-wrap gap-x-4 gap-y-1 text-xs'>
                {change.pool_id ? (
                  <span>{t('Pool #{{id}}', { id: change.pool_id })}</span>
                ) : null}
                {change.group_name ? (
                  <span className='min-w-0 break-words'>
                    {change.group_name}
                  </span>
                ) : null}
                {change.member_id ? (
                  <span>{t('Member #{{id}}', { id: change.member_id })}</span>
                ) : null}
                {change.routing_generation ? (
                  <span className='font-mono break-all'>
                    {change.routing_generation}
                  </span>
                ) : null}
              </div>
            ) : null}
            {change.before !== undefined || change.after !== undefined ? (
              <div className='grid grid-cols-1 gap-3 sm:grid-cols-2'>
                <AuditChangeValue label={t('Before')} value={change.before} />
                <AuditChangeValue label={t('After')} value={change.after} />
              </div>
            ) : null}
          </article>
        ))}
      </div>
    </section>
  )
}

export function ChannelRoutingControlAuditStructuredSection(props: {
  title: string
  value: unknown
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const entries = routingControlAuditEntries(props.value)
  if (entries.length === 0) return null

  return (
    <section className='flex flex-col gap-3 border-t pt-4'>
      <h3 className='text-sm font-semibold'>{props.title}</h3>
      <dl className='grid grid-cols-1 gap-x-6 gap-y-4 sm:grid-cols-2'>
        {entries.map(([key, value]) => (
          <div key={key} className='min-w-0'>
            <dt className='text-muted-foreground text-xs'>
              <AuditFieldLabel field={key} />
            </dt>
            <dd className='mt-1 text-sm break-words'>
              <AuditValue
                value={value}
                field={key}
                depth={0}
                format={format}
                translate={t}
              />
            </dd>
          </div>
        ))}
      </dl>
    </section>
  )
}

function AuditChangeValue(props: { label: string; value: unknown }) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()

  return (
    <div className='bg-muted/30 min-w-0 rounded-md border p-2.5'>
      <div className='text-muted-foreground text-xs'>{props.label}</div>
      <div className='mt-1 text-sm break-words'>
        <AuditValue
          value={props.value}
          field=''
          depth={0}
          format={format}
          translate={t}
        />
      </div>
    </div>
  )
}

function AuditValue(props: {
  value: unknown
  field: string
  depth: number
  format: AuditFormatters
  translate: TFunction
}) {
  if (props.value == null) return props.translate('Not available')
  if (typeof props.value === 'boolean') {
    return props.value ? props.translate('Yes') : props.translate('No')
  }
  if (typeof props.value === 'number') {
    if (props.field.endsWith('basis_points')) {
      return props.format.percent(props.value / 10_000)
    }
    if (props.field.endsWith('_time_ms')) {
      return props.format.timestamp(props.value)
    }
    return props.format.number(props.value)
  }
  if (typeof props.value === 'string') {
    if (auditEnumField(props.field)) {
      return props.translate(routingControlAuditHumanizeKey(props.value))
    }
    if (auditIdentityField(props.field)) {
      return <span className='font-mono text-xs break-all'>{props.value}</span>
    }
    return props.value
  }
  if (Array.isArray(props.value)) {
    if (props.value.length === 0) return props.translate('None')
    if (props.depth >= 3) {
      return props.translate('{{count}} items', { count: props.value.length })
    }
    const items = keyedAuditValues(props.value)
    return (
      <ul className='flex flex-col gap-1.5'>
        {items.map(({ key, value: item }) => (
          <li key={key} className='min-w-0'>
            <AuditValue
              value={item}
              field={props.field}
              depth={props.depth + 1}
              format={props.format}
              translate={props.translate}
            />
          </li>
        ))}
      </ul>
    )
  }
  const entries = routingControlAuditEntries(props.value)
  if (entries.length === 0) return props.translate('None')
  if (props.depth >= 3) {
    return props.translate('{{count}} fields', { count: entries.length })
  }
  return (
    <dl className='flex flex-col gap-2'>
      {entries.map(([key, value]) => (
        <div key={key} className='min-w-0'>
          <dt className='text-muted-foreground text-xs'>
            <AuditFieldLabel field={key} />
          </dt>
          <dd className='mt-0.5 break-words'>
            <AuditValue
              value={value}
              field={key}
              depth={props.depth + 1}
              format={props.format}
              translate={props.translate}
            />
          </dd>
        </div>
      ))}
    </dl>
  )
}

function auditEnumField(field: string): boolean {
  return (
    field === 'action' ||
    field === 'change' ||
    field === 'scope' ||
    field === 'source' ||
    field === 'stage' ||
    field === 'status' ||
    field.endsWith('_category') ||
    field.endsWith('_state') ||
    field.endsWith('_status') ||
    field.endsWith('_type')
  )
}

function auditIdentityField(field: string): boolean {
  return (
    field.endsWith('_generation') ||
    field.endsWith('_identity') ||
    field === 'correlation_id' ||
    field === 'endpoint'
  )
}

function AuditFieldLabel(props: { field: string }) {
  const { t } = useTranslation()
  const label = routingControlAuditFieldLabel(props.field)
  if (label) return t(label)
  return <span className='font-mono'>{props.field}</span>
}

function keyedAuditValues<T>(values: T[]): Array<{ key: string; value: T }> {
  const occurrences = new Map<string, number>()
  return values.map((value) => {
    const signature = auditValueSignature(value)
    const occurrence = (occurrences.get(signature) ?? 0) + 1
    occurrences.set(signature, occurrence)
    return { key: `${signature}:${occurrence}`, value }
  })
}

function auditValueSignature(value: unknown): string {
  if (value == null || typeof value !== 'object') {
    return `${typeof value}:${String(value)}`
  }
  try {
    return JSON.stringify(value).slice(0, 256)
  } catch {
    return Object.prototype.toString.call(value)
  }
}
