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
import { RotateLeft01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import {
  getChannelRoutingDecision,
  replayChannelRoutingDecision,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import {
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../components/page-state'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import { ChannelRoutingAlgorithmIdentity } from './algorithm-identity'
import { ChannelRoutingDecisionCostSection } from './cost-estimate-section'
import { ChannelRoutingDecisionCandidatesSection } from './decision-candidates-section'
import { ChannelRoutingAttemptTimelineSection } from './hedge-audit-section'

export function ChannelRoutingDecisionSheet(props: {
  decisionId: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const user = useAuthStore((state) => state.auth.user)
  const canOperate = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )
  const decisionQuery = useQuery({
    queryKey: channelRoutingQueryKeys.decision(props.decisionId ?? ''),
    queryFn: () => getChannelRoutingDecision(props.decisionId ?? ''),
    enabled: props.open && Boolean(props.decisionId),
  })
  const replay = useMutation({
    mutationFn: () => replayChannelRoutingDecision(props.decisionId ?? ''),
  })
  const decision = decisionQuery.data
  const attemptTimeline = decision?.attempt_timeline ?? decision?.hedge

  return (
    <Sheet
      open={props.open}
      onOpenChange={(open) => {
        props.onOpenChange(open)
        if (!open) replay.reset()
      }}
    >
      <SheetContent
        className={sideDrawerContentClassName(
          'channel-routing-touch-surface max-w-none max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11 sm:!max-w-4xl'
        )}
      >
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle>{t('Routing decision')}</SheetTitle>
          <SheetDescription className='font-mono text-xs'>
            {props.decisionId}
          </SheetDescription>
        </SheetHeader>

        <div className='min-h-0 flex-1 overflow-auto px-4 pb-4'>
          {decisionQuery.isLoading ? (
            <ChannelRoutingLoadingState rows={7} />
          ) : null}
          {decisionQuery.isError && !decision ? (
            <ChannelRoutingErrorState
              error={decisionQuery.error}
              onRetry={() => void decisionQuery.refetch()}
            />
          ) : null}

          {decision ? (
            <div className='space-y-5'>
              {decisionQuery.isRefetchError ? (
                <ChannelRoutingRefetchErrorAlert
                  isFetching={decisionQuery.isFetching}
                  onRetry={() => void decisionQuery.refetch()}
                />
              ) : null}
              <div className='flex flex-wrap items-center gap-2'>
                <ChannelRoutingStatusBadge
                  status={
                    decision.observed_matches_actual ? 'success' : 'degraded'
                  }
                  label={
                    decision.observed_matches_actual
                      ? t('Matched active route')
                      : t('Selection differed')
                  }
                />
                <ChannelRoutingStatusBadge
                  status={decision.cohort || 'unknown'}
                  label={decision.cohort || t('No cohort')}
                />
                {decision.breaker_bypassed ? (
                  <ChannelRoutingStatusBadge
                    status='degraded'
                    label={t('Breaker bypassed')}
                  />
                ) : null}
              </div>

              <ChannelRoutingAlgorithmIdentity
                algorithm={decision.algorithm_version}
              />

              <dl className='bg-border grid grid-cols-2 gap-px overflow-hidden rounded-lg border sm:grid-cols-4'>
                {[
                  [t('Group'), decision.group_name],
                  [t('Model'), decision.model_name],
                  [t('Actual channel'), `#${decision.actual_channel_id}`],
                  [t('Selected channel'), `#${decision.observed_channel_id}`],
                  [t('Revision'), `r${decision.snapshot_revision}`],
                  [t('Retry index'), format.number(decision.retry_index)],
                  [t('Created'), format.timestamp(decision.created_time)],
                ].map(([label, value]) => (
                  <div key={label} className='bg-background min-w-0 p-3'>
                    <dt className='text-muted-foreground truncate text-xs'>
                      {label}
                    </dt>
                    <dd className='mt-1 truncate text-sm font-medium'>
                      {value}
                    </dd>
                  </div>
                ))}
              </dl>

              <ChannelRoutingDecisionCostSection
                actual={decision.actual_cost_estimate}
                observed={decision.observed_cost_estimate}
              />

              {attemptTimeline ? (
                <ChannelRoutingAttemptTimelineSection
                  timeline={attemptTimeline}
                />
              ) : null}

              <ChannelRoutingDecisionCandidatesSection
                decisionId={decision.decision_id}
                title={t('Candidate set')}
                expectedEligible={decision.eligible_count}
                expectedTotal={decision.candidate_count}
              />

              {decision.exclusion_summary.reasons.length > 0 ? (
                <section
                  className='border-t pt-4'
                  aria-labelledby='exclusions-title'
                >
                  <h3 id='exclusions-title' className='text-sm font-semibold'>
                    {t('Exclusion summary')}
                  </h3>
                  <div className='mt-2 flex flex-wrap gap-2'>
                    {decision.exclusion_summary.reasons.map((item) => (
                      <ChannelRoutingStatusBadge
                        key={item.reason}
                        status='failed'
                        label={`${item.reason} · ${item.count}`}
                      />
                    ))}
                  </div>
                </section>
              ) : null}

              {replay.isError ? (
                <div className='border-destructive/30 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm'>
                  {t(
                    'Decision replay failed integrity or availability checks.'
                  )}
                </div>
              ) : null}
              {replay.data ? (
                <section
                  className='border-t pt-4'
                  aria-labelledby='replay-result-title'
                >
                  <h3
                    id='replay-result-title'
                    className='text-sm font-semibold'
                  >
                    {t('Replay result')}
                  </h3>
                  <div className='mt-2 flex flex-wrap items-center gap-2 text-sm'>
                    <ChannelRoutingStatusBadge
                      status={replay.data.audit_verified ? 'success' : 'failed'}
                      label={t('Audit verified')}
                    />
                    <span>
                      {t(
                        'Stored channel #{{stored}} · replayed channel #{{replayed}}',
                        {
                          stored: replay.data.stored_channel_id,
                          replayed: replay.data.replayed_channel_id,
                        }
                      )}
                    </span>
                  </div>
                </section>
              ) : null}
            </div>
          ) : null}
        </div>

        {decision?.replayable && canOperate ? (
          <SheetFooter className={sideDrawerFooterClassName()}>
            <Button
              variant='outline'
              disabled={replay.isPending || decisionQuery.isRefetchError}
              onClick={() => replay.mutate()}
            >
              <HugeiconsIcon
                icon={RotateLeft01Icon}
                data-icon='inline-start'
                aria-hidden='true'
              />
              {replay.isPending ? t('Replaying') : t('Replay decision')}
            </Button>
          </SheetFooter>
        ) : null}
      </SheetContent>
    </Sheet>
  )
}
