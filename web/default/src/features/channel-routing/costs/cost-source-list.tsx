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
import { Cable, Eye, MoreHorizontal, Pencil, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { ChannelRoutingIdentityText } from '../components/identity-text'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { costBindingCredentialCount } from '../lib/cost-binding'
import { useChannelRoutingFormatters } from '../lib/format'
import type { RoutingCostBinding } from '../types'

function CostSourceSyncHealth(props: { binding: RoutingCostBinding }) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const binding = props.binding
  const backoffActive = binding.sync_backoff_until * 1_000 > Date.now()

  if (binding.egress_policy_error) {
    return (
      <div className='space-y-1'>
        <ChannelRoutingStatusBadge
          status='failed'
          label={t('Network trust error')}
        />
        <ChannelRoutingIdentityText
          text={binding.egress_policy_error}
          className='text-destructive text-xs whitespace-normal'
        />
      </div>
    )
  }
  if (binding.credential_error) {
    return (
      <div className='space-y-1'>
        <ChannelRoutingStatusBadge
          status='failed'
          label={t('Credential error')}
        />
        <ChannelRoutingIdentityText
          text={binding.credential_error}
          className='text-destructive text-xs whitespace-normal'
        />
      </div>
    )
  }
  if (backoffActive) {
    return (
      <div className='space-y-1'>
        <ChannelRoutingStatusBadge status='warning' label={t('Backoff')} />
        <p className='text-muted-foreground text-xs'>
          {t('Retry after {{time}}', {
            time: format.timestamp(binding.sync_backoff_until),
          })}
        </p>
      </div>
    )
  }
  if (binding.last_sync_error || binding.sync_failure_count > 0) {
    return (
      <div className='space-y-1'>
        <ChannelRoutingStatusBadge
          status='failed'
          label={t('Sync needs attention')}
        />
        {binding.last_sync_error ? (
          <ChannelRoutingIdentityText
            text={binding.last_sync_error}
            className='text-destructive text-xs whitespace-normal'
          />
        ) : null}
      </div>
    )
  }
  return <ChannelRoutingStatusBadge status='healthy' />
}

function CostSourceActions(props: {
  canOperate: boolean
  canSensitiveWrite: boolean
  testing: boolean
  testDisabled: boolean
  onOpen: () => void
  onTest: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            type='button'
            size='icon-sm'
            variant='ghost'
            aria-label={t('Open cost source actions')}
            className='data-popup-open:bg-muted'
          />
        }
      >
        <MoreHorizontal aria-hidden='true' />
      </DropdownMenuTrigger>
      <DropdownMenuContent align='end' className='w-52'>
        <DropdownMenuItem onClick={props.onOpen}>
          {props.canSensitiveWrite ? (
            <Pencil aria-hidden='true' />
          ) : (
            <Eye aria-hidden='true' />
          )}
          {props.canSensitiveWrite ? t('Edit cost source') : t('View details')}
        </DropdownMenuItem>
        {props.canOperate ? (
          <DropdownMenuItem
            disabled={props.testDisabled}
            onClick={props.onTest}
          >
            <Cable aria-hidden='true' />
            {props.testing ? t('Testing connection') : t('Test connection')}
          </DropdownMenuItem>
        ) : null}
        {props.canSensitiveWrite ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant='destructive' onClick={props.onDelete}>
              <Trash2 aria-hidden='true' />
              {t('Delete cost source')}
            </DropdownMenuItem>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function costSourceToggleLabel(
  binding: RoutingCostBinding,
  t: (key: string, options?: Record<string, unknown>) => string
): string {
  if (binding.enabled) {
    return t('Disable cost source for channel {{id}}', {
      id: binding.channel_id,
    })
  }
  return t('Enable cost source for channel {{id}}', {
    id: binding.channel_id,
  })
}

export function CostSourceList(props: {
  items: RoutingCostBinding[]
  canOperate: boolean
  canSensitiveWrite: boolean
  testingChannelId?: number
  testDisabled: boolean
  toggleDisabled: boolean
  onOpen: (binding: RoutingCostBinding) => void
  onTest: (binding: RoutingCostBinding) => void
  onDelete: (binding: RoutingCostBinding) => void
  onToggle: (binding: RoutingCostBinding, enabled: boolean) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()

  return (
    <>
      <div className='hidden overflow-hidden rounded-lg border lg:block'>
        <Table
          className='min-w-[72rem] table-fixed'
          scrollAreaLabel={t('Cost sources table')}
        >
          <TableHeader>
            <TableRow>
              <TableHead className='w-56'>{t('Channel')}</TableHead>
              <TableHead className='w-72'>{t('Upstream source')}</TableHead>
              <TableHead className='w-36'>{t('Credentials')}</TableHead>
              <TableHead className='w-52'>{t('Sync health')}</TableHead>
              <TableHead className='w-36'>{t('Updated')}</TableHead>
              <TableHead className='w-32'>{t('Enabled')}</TableHead>
              <TableHead className='w-10'>
                <span className='sr-only'>{t('Actions')}</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {props.items.map((binding) => {
              const credentialCount = costBindingCredentialCount(binding)
              const testing = props.testingChannelId === binding.channel_id
              return (
                <TableRow key={binding.channel_id}>
                  <TableCell className='min-w-0 overflow-hidden align-top'>
                    <ChannelRoutingIdentityText
                      text={
                        binding.channel_name ||
                        t('Channel #{{id}}', { id: binding.channel_id })
                      }
                      className='font-medium whitespace-normal'
                    />
                    <div className='text-muted-foreground mt-1 font-mono text-xs'>
                      #{binding.channel_id}
                    </div>
                  </TableCell>
                  <TableCell className='min-w-0 overflow-hidden align-top'>
                    <div className='flex min-w-0 items-center gap-2'>
                      <Badge variant='outline'>
                        {binding.upstream_type === 'newapi'
                          ? 'New API'
                          : 'Sub2API'}
                      </Badge>
                      <span className='text-muted-foreground min-w-0 truncate text-xs'>
                        {binding.upstream_group}
                      </span>
                    </div>
                    <ChannelRoutingIdentityText
                      text={binding.base_url}
                      breakAll
                      className='mt-1 font-mono text-xs whitespace-normal'
                    />
                  </TableCell>
                  <TableCell className='min-w-0 overflow-hidden align-top'>
                    <div className='text-sm'>
                      {t('{{count}} saved credentials', {
                        count: credentialCount,
                      })}
                    </div>
                  </TableCell>
                  <TableCell className='min-w-0 overflow-hidden align-top'>
                    <CostSourceSyncHealth binding={binding} />
                  </TableCell>
                  <TableCell className='text-muted-foreground text-xs'>
                    {format.timestamp(binding.updated_time)}
                  </TableCell>
                  <TableCell>
                    <div className='flex min-h-11 items-center gap-2'>
                      <Switch
                        checked={binding.enabled}
                        disabled={
                          !props.canSensitiveWrite || props.toggleDisabled
                        }
                        aria-label={costSourceToggleLabel(binding, t)}
                        onCheckedChange={(value) =>
                          props.onToggle(binding, value)
                        }
                      />
                      <span className='text-muted-foreground text-xs'>
                        {binding.enabled ? t('Enabled') : t('Disabled')}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell>
                    <CostSourceActions
                      canOperate={props.canOperate}
                      canSensitiveWrite={props.canSensitiveWrite}
                      testing={testing}
                      testDisabled={props.testDisabled}
                      onOpen={() => props.onOpen(binding)}
                      onTest={() => props.onTest(binding)}
                      onDelete={() => props.onDelete(binding)}
                    />
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </div>

      <div className='divide-y rounded-lg border lg:hidden'>
        {props.items.map((binding) => {
          const credentialCount = costBindingCredentialCount(binding)
          const testing = props.testingChannelId === binding.channel_id
          return (
            <article key={binding.channel_id} className='min-w-0 p-3'>
              <div className='flex min-w-0 items-start justify-between gap-3'>
                <div className='min-w-0'>
                  <h3 className='text-sm font-medium break-words'>
                    {binding.channel_name ||
                      t('Channel #{{id}}', { id: binding.channel_id })}
                  </h3>
                  <p className='text-muted-foreground mt-1 font-mono text-xs'>
                    #{binding.channel_id}
                  </p>
                </div>
                <ChannelRoutingStatusBadge
                  status={binding.enabled ? 'enabled' : 'disabled'}
                />
              </div>
              <div className='mt-3 min-w-0'>
                <div className='flex flex-wrap items-center gap-2'>
                  <Badge variant='outline'>
                    {binding.upstream_type === 'newapi' ? 'New API' : 'Sub2API'}
                  </Badge>
                  <span className='text-muted-foreground text-xs break-all'>
                    {binding.upstream_group}
                  </span>
                </div>
                <ChannelRoutingIdentityText
                  text={binding.base_url}
                  breakAll
                  className='mt-2 font-mono text-xs'
                />
              </div>
              <dl className='mt-3 grid grid-cols-2 gap-3 text-xs'>
                <div className='min-w-0'>
                  <dt className='text-muted-foreground'>{t('Credentials')}</dt>
                  <dd className='mt-1 font-medium'>
                    {t('{{count}} saved', { count: credentialCount })}
                  </dd>
                </div>
                <div className='min-w-0'>
                  <dt className='text-muted-foreground'>{t('Updated')}</dt>
                  <dd className='mt-1 font-medium break-words'>
                    {format.timestamp(binding.updated_time)}
                  </dd>
                </div>
              </dl>
              <div className='mt-3'>
                <CostSourceSyncHealth binding={binding} />
              </div>
              <div className='mt-3 flex min-h-11 items-center justify-between gap-3 border-t pt-3'>
                <label className='flex min-h-11 cursor-pointer items-center gap-2 text-sm'>
                  <Switch
                    checked={binding.enabled}
                    disabled={!props.canSensitiveWrite || props.toggleDisabled}
                    aria-label={costSourceToggleLabel(binding, t)}
                    onCheckedChange={(value) => props.onToggle(binding, value)}
                  />
                  <span>{binding.enabled ? t('Enabled') : t('Disabled')}</span>
                </label>
                <CostSourceActions
                  canOperate={props.canOperate}
                  canSensitiveWrite={props.canSensitiveWrite}
                  testing={testing}
                  testDisabled={props.testDisabled}
                  onOpen={() => props.onOpen(binding)}
                  onTest={() => props.onTest(binding)}
                  onDelete={() => props.onDelete(binding)}
                />
              </div>
            </article>
          )
        })}
      </div>
    </>
  )
}
