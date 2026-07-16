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
import { FlaskConicalIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import {
  getChannelRoutingDecision,
  listChannelRoutingGroupReplayProfiles,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
} from '../components/page-state'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { ChannelRoutingDecisionCandidatesSection } from '../decisions/decision-candidates-section'
import { ChannelRoutingAttemptTimelineSection } from '../decisions/hedge-audit-section'
import { useChannelRoutingFormatters } from '../lib/format'

const replayProfileLimit = 20

export function ChannelRoutingGroupReplayRankingSection(props: {
  poolId: number
  canOperate: boolean
  onSimulate: () => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const [selectedDecisionId, setSelectedDecisionId] = useState('')
  const profilesQuery = useQuery({
    queryKey: channelRoutingQueryKeys.groupReplayProfiles(
      props.poolId,
      replayProfileLimit
    ),
    queryFn: () =>
      listChannelRoutingGroupReplayProfiles(props.poolId, replayProfileLimit),
  })
  const profiles = profilesQuery.data?.items ?? []
  const selectedProfile =
    profiles.find((profile) => profile.decision_id === selectedDecisionId) ??
    profiles[0]
  const decisionQuery = useQuery({
    queryKey: channelRoutingQueryKeys.decision(
      selectedProfile?.decision_id ?? ''
    ),
    queryFn: () =>
      getChannelRoutingDecision(selectedProfile?.decision_id ?? ''),
    enabled: selectedProfile != null,
  })
  const attemptTimeline =
    decisionQuery.data?.attempt_timeline ?? decisionQuery.data?.hedge

  if (profilesQuery.isLoading) {
    return <ChannelRoutingLoadingState rows={7} />
  }
  if (profilesQuery.isError) {
    return (
      <ChannelRoutingErrorState
        error={profilesQuery.error}
        onRetry={() => void profilesQuery.refetch()}
      />
    )
  }
  if (!selectedProfile) {
    return (
      <ChannelRoutingEmptyState
        title={t('No replayable routing profile')}
        description={t(
          'Run a historical simulation after real routing decisions are recorded. Rankings are never inferred from the current snapshot.'
        )}
        action={
          props.canOperate ? (
            <Button type='button' onClick={props.onSimulate}>
              <HugeiconsIcon
                icon={FlaskConicalIcon}
                data-icon='inline-start'
                aria-hidden='true'
              />
              {t('Run simulation')}
            </Button>
          ) : null
        }
      />
    )
  }

  return (
    <section
      className='space-y-4 border-t pt-4'
      aria-labelledby='resident-ranking-title'
    >
      <div className='flex flex-wrap items-end justify-between gap-3'>
        <div>
          <h2 id='resident-ranking-title' className='text-base font-semibold'>
            {t('Replay candidate ranking')}
          </h2>
          <p className='text-muted-foreground mt-0.5 max-w-3xl text-sm text-pretty'>
            {t(
              'Ranking uses the latest replayable decision for each model and request mode, including its original metrics and filters.'
            )}
          </p>
        </div>
        <label className='grid min-w-64 gap-1 text-xs'>
          <span className='text-muted-foreground'>{t('Replay profile')}</span>
          <NativeSelect
            size='sm'
            value={selectedProfile.decision_id}
            onChange={(event) => setSelectedDecisionId(event.target.value)}
          >
            {profiles.map((profile) => (
              <NativeSelectOption
                key={profile.decision_id}
                value={profile.decision_id}
              >
                {profile.model_name} ·{' '}
                {profile.is_stream ? t('Stream') : t('Non-stream')}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </label>
      </div>

      <dl className='bg-border grid grid-cols-2 gap-px overflow-hidden rounded-lg border sm:grid-cols-3 lg:grid-cols-6'>
        {[
          [t('Model'), selectedProfile.model_name],
          [
            t('Request mode'),
            selectedProfile.is_stream ? t('Stream') : t('Non-stream'),
          ],
          [t('Sample time'), format.timestamp(selectedProfile.created_time)],
          [t('Snapshot revision'), `r${selectedProfile.snapshot_revision}`],
          [t('Algorithm'), selectedProfile.algorithm_version],
          [t('Selected route'), `#${selectedProfile.observed_channel_id}`],
        ].map(([label, value]) => (
          <div key={label} className='bg-background min-w-0 p-3'>
            <dt className='text-muted-foreground truncate text-xs'>{label}</dt>
            <dd className='mt-1 truncate text-sm font-medium' title={value}>
              {value}
            </dd>
          </div>
        ))}
      </dl>

      <div className='flex flex-wrap items-center gap-2'>
        <ChannelRoutingStatusBadge
          status='replayable'
          label={t('Replayable')}
        />
        <ChannelRoutingStatusBadge
          status={selectedProfile.breaker_bypassed ? 'warning' : 'known'}
          label={
            selectedProfile.breaker_bypassed
              ? t('Breaker bypassed')
              : t('Breaker enforced')
          }
        />
        <span className='text-muted-foreground font-mono text-xs break-all'>
          {selectedProfile.decision_id}
        </span>
      </div>

      <ChannelRoutingDecisionCandidatesSection
        decisionId={selectedProfile.decision_id}
        title={t('Historical candidate ranking')}
        expectedEligible={selectedProfile.eligible_count}
        expectedTotal={selectedProfile.candidate_count}
      />

      <section className='border-t pt-4'>
        {decisionQuery.isLoading ? (
          <ChannelRoutingLoadingState rows={4} />
        ) : null}
        {decisionQuery.isError ? (
          <ChannelRoutingErrorState
            error={decisionQuery.error}
            onRetry={() => void decisionQuery.refetch()}
          />
        ) : null}
        {attemptTimeline ? (
          <ChannelRoutingAttemptTimelineSection
            timeline={attemptTimeline}
            title={t('Pool failover timeline')}
          />
        ) : null}
        {decisionQuery.data && !attemptTimeline ? (
          <div>
            <h3 id='pool-failover-title' className='text-sm font-semibold'>
              {t('Pool failover timeline')}
            </h3>
            <p className='text-muted-foreground mt-1 text-sm'>
              {t(
                'No retry, failover, or hedge attempts were recorded for this decision.'
              )}
            </p>
          </div>
        ) : null}
      </section>
    </section>
  )
}
