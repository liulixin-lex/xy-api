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
  Database01Icon,
  Key01Icon,
  Layers02Icon,
  Location01Icon,
  Money03Icon,
  Route01Icon,
  WalletCardsIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { TableCell, TableRow } from '@/components/ui/table'
import {
  CHANNEL_STATUS_LABELS,
  CHANNEL_TYPES,
} from '@/features/channels/constants'

import { ChannelRoutingIdentityText } from '../components/identity-text'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import type {
  ChannelSnapshot,
  RoutingChannelConfigurationCostSource,
  RoutingChannelConfigurationTrafficClass,
  RoutingChannelFailureDomainStatus,
} from '../types'

function costSourceLabel(
  source: RoutingChannelConfigurationCostSource,
  t: TFunction
): string {
  switch (source) {
    case 'manual':
      return t('Manual')
    case 'legacy_migrated':
      return t('Legacy migrated')
    case 'defaulted':
      return t('System default')
  }
}

function trafficClassLabel(
  trafficClass: RoutingChannelConfigurationTrafficClass,
  t: TFunction
): string {
  return trafficClass === 'claude_code_only'
    ? t('Claude Code only')
    : t('All eligible traffic')
}

function failureDomainStatusLabel(
  status: RoutingChannelFailureDomainStatus,
  t: TFunction
): string {
  switch (status) {
    case 'configured':
      return t('Configured')
    case 'historical_migrated':
      return t('Historical migrated')
    case 'unconfigured':
      return t('Not configured')
  }
}

function ChannelCredentialSummary(props: { channel: ChannelSnapshot }) {
  const { t } = useTranslation()
  const visibleIds = props.channel.credential_ids.slice(0, 3)
  const hiddenCount = Math.max(
    0,
    props.channel.credential_count - visibleIds.length
  )
  const identityText = visibleIds.map((id) => `#${id}`).join(' · ')

  return (
    <div className='min-w-0'>
      <span className='inline-flex items-center gap-1.5'>
        <HugeiconsIcon
          icon={Key01Icon}
          className='text-muted-foreground size-3.5'
          aria-hidden='true'
        />
        {t('{{count}} credentials', {
          count: props.channel.credential_count,
        })}
      </span>
      {identityText ? (
        <ChannelRoutingIdentityText
          text={
            props.channel.credentials_truncated && hiddenCount > 0
              ? `${identityText} · ${t('{{count}} more', { count: hiddenCount })}`
              : identityText
          }
          className='text-muted-foreground mt-1 font-mono text-xs'
        />
      ) : null}
    </div>
  )
}

function ChannelModelCoverage(props: { channel: ChannelSnapshot }) {
  const { t } = useTranslation()
  const names = props.channel.models ?? []
  const modelCount = Math.max(0, props.channel.model_count ?? names.length)
  const hiddenCount = Math.max(0, modelCount - names.length)
  let detail = t('Model names unavailable')
  if (modelCount === 0) {
    detail = t('No models')
  } else if (names.length > 0) {
    detail = names.join(', ')
    if (props.channel.models_truncated && hiddenCount > 0) {
      detail = `${detail} · ${t('{{count}} more', { count: hiddenCount })}`
    }
  }

  return (
    <div className='min-w-0'>
      <span className='inline-flex items-center gap-1.5'>
        <HugeiconsIcon
          icon={Layers02Icon}
          className='text-muted-foreground size-3.5'
          aria-hidden='true'
        />
        {t('{{count}} models', { count: modelCount })}
      </span>
      <ChannelRoutingIdentityText
        text={detail}
        className='text-muted-foreground mt-1 text-xs'
      />
    </div>
  )
}

function ChannelCostPlan(props: { channel: ChannelSnapshot }) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()

  return (
    <div className='flex min-w-0 flex-col gap-2'>
      <div className='flex flex-wrap items-center gap-2'>
        <span className='inline-flex items-center gap-1 font-mono font-semibold'>
          <HugeiconsIcon
            icon={Money03Icon}
            className='text-muted-foreground size-3.5'
            aria-hidden='true'
          />
          {format.cost(props.channel.upstream_cost_multiplier)}×
        </span>
        <Badge variant='outline'>
          {costSourceLabel(props.channel.cost_source, t)}
        </Badge>
        <Badge variant='outline'>
          <HugeiconsIcon
            icon={
              props.channel.cost_confirmed ? CheckmarkCircle02Icon : Alert02Icon
            }
            aria-hidden='true'
          />
          {props.channel.cost_confirmed ? t('Confirmed') : t('Pending review')}
        </Badge>
      </div>
      <div className='text-muted-foreground flex flex-wrap items-center gap-x-3 gap-y-1 text-xs'>
        <span className='inline-flex items-center gap-1'>
          <HugeiconsIcon
            icon={
              props.channel.cost_basis_available
                ? CheckmarkCircle02Icon
                : Alert02Icon
            }
            className='size-3'
            aria-hidden='true'
          />
          {props.channel.cost_basis_available
            ? t('Cost basis available')
            : t('Cost basis unavailable')}
        </span>
        <span>
          {t('{{count}} effective models', {
            count: props.channel.effective_model_count,
          })}
        </span>
      </div>
    </div>
  )
}

function ChannelTrafficPlan(props: { channel: ChannelSnapshot }) {
  const { t } = useTranslation()
  const failureDomain =
    props.channel.failure_domain_label ||
    failureDomainStatusLabel(props.channel.failure_domain_status, t)

  return (
    <div className='flex min-w-0 flex-col gap-2 text-xs'>
      <div>
        <div className='text-muted-foreground'>{t('Traffic scope')}</div>
        <div className='mt-1 inline-flex items-center gap-1 font-medium'>
          <HugeiconsIcon
            icon={Route01Icon}
            className='text-muted-foreground size-3.5'
            aria-hidden='true'
          />
          {trafficClassLabel(props.channel.traffic_class, t)}
        </div>
      </div>
      <div>
        <div className='text-muted-foreground'>{t('Failure domain')}</div>
        <div className='mt-1 inline-flex max-w-full items-center gap-1 font-medium'>
          <HugeiconsIcon
            icon={
              props.channel.failure_domain_status === 'historical_migrated'
                ? Database01Icon
                : Location01Icon
            }
            className='text-muted-foreground size-3.5 shrink-0'
            aria-hidden='true'
          />
          <ChannelRoutingIdentityText text={failureDomain} />
        </div>
      </div>
      <div className='text-muted-foreground'>
        {t('Configuration r{{revision}}', {
          revision: props.channel.configuration_revision,
        })}
      </div>
    </div>
  )
}

function channelStatusLabel(channel: ChannelSnapshot, t: TFunction): string {
  const label =
    CHANNEL_STATUS_LABELS[
      channel.status as keyof typeof CHANNEL_STATUS_LABELS
    ] ?? 'Unknown'
  return t(label)
}

function ChannelIdentity(props: { channel: ChannelSnapshot }) {
  const { t } = useTranslation()
  const channelType =
    CHANNEL_TYPES[props.channel.type as keyof typeof CHANNEL_TYPES] ?? 'Unknown'
  return (
    <div className='min-w-0'>
      <ChannelRoutingIdentityText
        text={props.channel.name || `#${props.channel.id}`}
        className='font-medium'
      />
      <div className='text-muted-foreground text-xs'>{t(channelType)}</div>
      <ChannelRoutingIdentityText
        text={
          props.channel.endpoint_authority ||
          props.channel.endpoint ||
          t('Default endpoint')
        }
        className='text-muted-foreground mt-1 font-mono text-xs'
        breakAll
      />
      <div className='text-muted-foreground mt-1 inline-flex items-center gap-1 text-xs'>
        <HugeiconsIcon
          icon={Location01Icon}
          className='size-3'
          aria-hidden='true'
        />
        {props.channel.region || t('Unknown region')}
      </div>
    </div>
  )
}

function ChannelServingHealth(props: { channel: ChannelSnapshot }) {
  const { t } = useTranslation()
  return (
    <div className='flex flex-wrap items-center gap-2'>
      <ChannelRoutingStatusBadge
        status={props.channel.status}
        label={channelStatusLabel(props.channel, t)}
      />
      {props.channel.auth_failure ? (
        <ChannelRoutingStatusBadge
          status='failed'
          label={t('Credential failure')}
        />
      ) : null}
      <ChannelRoutingStatusBadge
        status={
          props.channel.endpoint_state.known
            ? props.channel.endpoint_state.state || 'unknown'
            : 'unknown'
        }
      />
    </div>
  )
}

export function PhysicalChannelTableRow(props: { channel: ChannelSnapshot }) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  return (
    <TableRow>
      <TableCell className='max-w-80 align-top'>
        <ChannelIdentity channel={props.channel} />
      </TableCell>
      <TableCell className='align-top'>
        <ChannelServingHealth channel={props.channel} />
        <div className='text-muted-foreground mt-2 text-xs'>
          {t('Updated {{time}}', {
            time: format.timestamp(
              Math.max(
                props.channel.auth_failure_updated_at,
                props.channel.balance_updated_at
              )
            ),
          })}
        </div>
      </TableCell>
      <TableCell className='max-w-64 align-top'>
        <div className='flex flex-col gap-3'>
          <ChannelCredentialSummary channel={props.channel} />
          <ChannelModelCoverage channel={props.channel} />
          <span className='inline-flex items-center gap-1.5'>
            <HugeiconsIcon
              icon={WalletCardsIcon}
              className='text-muted-foreground size-3.5'
              aria-hidden='true'
            />
            {props.channel.balance_known
              ? format.number(props.channel.balance)
              : t('Unknown balance')}
          </span>
        </div>
      </TableCell>
      <TableCell className='min-w-64 align-top'>
        <ChannelCostPlan channel={props.channel} />
      </TableCell>
      <TableCell className='min-w-52 align-top'>
        <ChannelTrafficPlan channel={props.channel} />
      </TableCell>
    </TableRow>
  )
}

export function PhysicalChannelCard(props: { channel: ChannelSnapshot }) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  return (
    <article className='min-w-0 p-3'>
      <div className='flex min-w-0 items-start justify-between gap-3'>
        <ChannelIdentity channel={props.channel} />
        <ChannelRoutingStatusBadge
          status={props.channel.status}
          label={channelStatusLabel(props.channel, t)}
        />
      </div>
      <div className='mt-3'>
        <ChannelServingHealth channel={props.channel} />
      </div>
      <div className='mt-3 grid gap-3 border-y py-3 sm:grid-cols-2'>
        <ChannelCredentialSummary channel={props.channel} />
        <ChannelModelCoverage channel={props.channel} />
        <div>
          <div className='text-muted-foreground text-xs'>{t('Balance')}</div>
          <div className='mt-1 inline-flex items-center gap-1.5 text-sm font-medium'>
            <HugeiconsIcon
              icon={WalletCardsIcon}
              className='text-muted-foreground size-3.5'
              aria-hidden='true'
            />
            {props.channel.balance_known
              ? format.number(props.channel.balance)
              : t('Unknown')}
          </div>
        </div>
      </div>
      <div className='mt-3 flex flex-col gap-3'>
        <ChannelCostPlan channel={props.channel} />
        <ChannelTrafficPlan channel={props.channel} />
      </div>
      <div className='text-muted-foreground mt-3 text-xs'>
        {t('Updated {{time}}', {
          time: format.timestamp(
            Math.max(
              props.channel.auth_failure_updated_at,
              props.channel.balance_updated_at
            )
          ),
        })}
      </div>
    </article>
  )
}
