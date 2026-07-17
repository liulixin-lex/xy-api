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
  Audit02Icon,
  ArrowDown01Icon,
  SourceCodeIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  sideDrawerContentClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import { getChannelRoutingControlAuditTechnical } from '../../api/client'
import { channelRoutingQueryKeys } from '../../api/query-keys'
import { ChannelRoutingErrorState } from '../../components/page-state'
import { ChannelRoutingStatusBadge } from '../../components/status-badge'
import {
  routingControlAuditActorRoleLabel,
  routingControlAuditActionLabel,
  routingControlAuditSourceLabel,
  routingControlAuditSubjectLabel,
} from '../../lib/control-audits'
import { useChannelRoutingFormatters } from '../../lib/format'
import type { ChannelRoutingControlAudit } from '../../types'
import {
  ChannelRoutingControlAuditChanges,
  ChannelRoutingControlAuditStructuredSection,
} from './control-audit-content'

export function ChannelRoutingControlAuditSheet(props: {
  audit: ChannelRoutingControlAudit | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const user = useAuthStore((state) => state.auth.user)
  const canViewTechnical = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.AUDIT_EXPORT
  )
  const audit = props.audit

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent
        className={sideDrawerContentClassName(
          'channel-routing-touch-surface max-w-none max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11 sm:!max-w-3xl'
        )}
      >
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle className='flex items-center gap-2'>
            <HugeiconsIcon
              icon={Audit02Icon}
              className='size-4'
              strokeWidth={2}
              aria-hidden='true'
            />
            {t('Control audit #{{id}}', { id: audit?.id ?? '' })}
          </SheetTitle>
          <SheetDescription>
            {audit
              ? t('{{subject}}, recorded {{time}}', {
                  subject: t(
                    routingControlAuditSubjectLabel(audit.subject_type)
                  ),
                  time: format.timestamp(audit.created_time_ms),
                })
              : t('Immutable channel routing control record')}
          </SheetDescription>
        </SheetHeader>

        <div className={sideDrawerFormClassName('gap-5')}>
          {audit ? (
            <>
              <div className='flex flex-wrap items-center gap-2'>
                <ChannelRoutingStatusBadge status={audit.result} />
                <Badge variant='outline'>
                  {t(routingControlAuditActionLabel(audit.action))}
                </Badge>
                <Badge variant='outline'>
                  {t(routingControlAuditSourceLabel(audit.source))}
                </Badge>
              </div>

              {audit.needs_attention ? (
                <Alert>
                  <AlertTitle>{t('Operator attention required')}</AlertTitle>
                  <AlertDescription>
                    {t(
                      'Review the recorded impact, recommendations, and related operation before resolving the control issue.'
                    )}
                  </AlertDescription>
                </Alert>
              ) : null}

              <section className='flex flex-col gap-3'>
                <div>
                  <h3 className='text-base font-semibold break-words'>
                    {audit.subject_name}
                  </h3>
                  <p className='text-muted-foreground mt-1 text-xs'>
                    {t(routingControlAuditSubjectLabel(audit.subject_type))}
                    {audit.subject_id > 0 ? ` #${audit.subject_id}` : ''}
                  </p>
                </div>
                <dl className='grid grid-cols-1 gap-x-6 gap-y-3 text-sm sm:grid-cols-2'>
                  <div className='min-w-0'>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Subject identity')}
                    </dt>
                    <dd className='mt-1 font-mono text-xs break-all'>
                      {audit.subject_identity}
                    </dd>
                  </div>
                  {audit.subject_generation ? (
                    <div className='min-w-0'>
                      <dt className='text-muted-foreground text-xs'>
                        {t('Routing generation')}
                      </dt>
                      <dd className='mt-1 font-mono text-xs break-all'>
                        {audit.subject_generation}
                      </dd>
                    </div>
                  ) : null}
                  <div className='min-w-0'>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Actor snapshot')}
                    </dt>
                    <dd className='mt-1 font-medium break-words'>
                      {audit.actor_name}
                      {audit.actor_id > 0 ? ` (#${audit.actor_id})` : ''}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Actor role')}
                    </dt>
                    <dd className='mt-1 font-medium'>
                      {t(
                        routingControlAuditActorRoleLabel(
                          audit.actor_id,
                          audit.actor_role,
                          audit.source
                        )
                      )}
                    </dd>
                  </div>
                  {audit.correlation_id ? (
                    <div className='min-w-0 sm:col-span-2'>
                      <dt className='text-muted-foreground text-xs'>
                        {t('Correlation ID')}
                      </dt>
                      <dd className='mt-1 font-mono text-xs break-all'>
                        {audit.correlation_id}
                      </dd>
                    </div>
                  ) : null}
                </dl>
              </section>

              <section className='flex flex-col gap-2 border-t pt-4'>
                <h3 className='text-sm font-semibold'>{t('Reason')}</h3>
                <p className='text-muted-foreground text-sm break-words'>
                  {audit.reason || t('Not available')}
                </p>
              </section>

              {audit.error_message || audit.error_code ? (
                <Alert variant='destructive'>
                  <AlertTitle>
                    {audit.error_code || t('Control action failed')}
                  </AlertTitle>
                  <AlertDescription className='break-words'>
                    {audit.error_message || t('No error message recorded')}
                  </AlertDescription>
                </Alert>
              ) : null}

              <ChannelRoutingControlAuditStructuredSection
                title={t('Summary')}
                value={audit.summary}
              />
              <ChannelRoutingControlAuditChanges value={audit.changes} />
              <ChannelRoutingControlAuditStructuredSection
                title={t('Impact')}
                value={audit.impact}
              />
              <ChannelRoutingControlAuditStructuredSection
                title={t('Recommendations')}
                value={audit.recommendations}
              />
              <ChannelRoutingControlAuditStructuredSection
                title={t('Related objects')}
                value={audit.relations}
              />
              <ChannelRoutingControlAuditStructuredSection
                title={t('Subject snapshot')}
                value={audit.subject}
              />

              {canViewTechnical ? (
                <ControlAuditTechnicalSection
                  auditId={audit.id}
                  sheetOpen={props.open}
                />
              ) : null}
            </>
          ) : null}
        </div>
      </SheetContent>
    </Sheet>
  )
}

function ControlAuditTechnicalSection(props: {
  auditId: number
  sheetOpen: boolean
}) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState(false)
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.controlAuditTechnical(props.auditId),
    queryFn: ({ signal }) =>
      getChannelRoutingControlAuditTechnical(props.auditId, signal),
    enabled: props.sheetOpen && expanded,
  })
  const technical = query.data?.technical

  return (
    <Collapsible
      className='border-t pt-4'
      open={expanded}
      onOpenChange={setExpanded}
    >
      <CollapsibleTrigger className='hover:bg-muted/50 focus-visible:ring-ring flex min-h-10 w-full items-center justify-between rounded-md px-2 text-left text-sm font-semibold outline-none focus-visible:ring-2'>
        <span className='flex items-center gap-2'>
          <HugeiconsIcon
            icon={SourceCodeIcon}
            className='size-4'
            strokeWidth={2}
            aria-hidden='true'
          />
          {t('Technical details')}
        </span>
        <HugeiconsIcon
          icon={ArrowDown01Icon}
          className={cn(
            'size-4 transition-transform duration-150 motion-reduce:transition-none',
            expanded ? 'rotate-180' : undefined
          )}
          strokeWidth={2}
          aria-hidden='true'
        />
      </CollapsibleTrigger>
      <CollapsibleContent className='flex flex-col gap-4 px-2 pt-3'>
        {query.isLoading ? (
          <div
            className='flex flex-col gap-2'
            role='status'
            aria-live='polite'
            aria-busy='true'
          >
            <span className='sr-only'>{t('Loading')}</span>
            <Skeleton className='h-4 w-2/3' />
            <Skeleton className='h-4 w-1/2' />
            <Skeleton className='h-24 w-full' />
          </div>
        ) : null}
        {query.isError ? (
          <ChannelRoutingErrorState
            error={query.error}
            onRetry={() => void query.refetch()}
          />
        ) : null}
        {technical ? (
          <>
            <dl className='grid grid-cols-1 gap-x-6 gap-y-3 text-sm sm:grid-cols-2'>
              {technical.before_hash ? (
                <div className='min-w-0'>
                  <dt className='text-muted-foreground text-xs'>
                    {t('Before hash')}
                  </dt>
                  <dd className='mt-1 font-mono text-xs break-all'>
                    {technical.before_hash}
                  </dd>
                </div>
              ) : null}
              {technical.after_hash ? (
                <div className='min-w-0'>
                  <dt className='text-muted-foreground text-xs'>
                    {t('After hash')}
                  </dt>
                  <dd className='mt-1 font-mono text-xs break-all'>
                    {technical.after_hash}
                  </dd>
                </div>
              ) : null}
            </dl>
            <ChannelRoutingControlAuditStructuredSection
              title={t('Technical payload')}
              value={technical.details}
            />
          </>
        ) : null}
      </CollapsibleContent>
    </Collapsible>
  )
}
