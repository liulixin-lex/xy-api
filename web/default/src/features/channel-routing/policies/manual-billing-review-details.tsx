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
import { Clock3, ReceiptText, ShieldAlert, Waypoints } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  SideDrawerSection,
  SideDrawerSectionHeader,
} from '@/components/drawer-layout'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

import type { ManualBillingReviewItem } from '../billing-review-types'
import { useChannelRoutingFormatters } from '../lib/format'
import {
  getManualBillingReviewBlockerLabelKey,
  getManualBillingReviewConsequenceRows,
  getManualBillingReviewKindLabelKey,
  manualBillingReviewIsOverage,
} from '../lib/manual-billing-review'

export function ManualBillingReviewFinancialOutcomes(props: {
  review: ManualBillingReviewItem
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const rows = getManualBillingReviewConsequenceRows(props.review)

  return (
    <SideDrawerSection>
      <SideDrawerSectionHeader
        icon={<ReceiptText className='size-4' aria-hidden='true' />}
        title={t('Server-calculated financial outcomes')}
        description={t(
          'Every amount below comes directly from the billing service. No value is estimated in this interface.'
        )}
      />

      {manualBillingReviewIsOverage(props.review.review_kind) ? (
        <Alert className='border-amber-500/30 bg-amber-500/5'>
          <ShieldAlert
            className='text-amber-700 dark:text-amber-300'
            aria-hidden='true'
          />
          <AlertTitle>{t('Overage billing decision')}</AlertTitle>
          <AlertDescription>
            {t(
              'Accepting applies the additional charge. Rejecting keeps the current charge and writes off the verified overage.'
            )}
          </AlertDescription>
        </Alert>
      ) : null}
      {props.review.review_kind === 'accepted_handoff' &&
      props.review.financial_consequences.accept_additional_charge < 0 ? (
        <Alert>
          <ReceiptText aria-hidden='true' />
          <AlertTitle>{t('Accepted handoff adjustment')}</AlertTitle>
          <AlertDescription>
            {t(
              'A negative accepted adjustment reduces the current charge to the server-calculated final charge.'
            )}
          </AlertDescription>
        </Alert>
      ) : null}

      <dl className='divide-border/70 overflow-hidden rounded-lg border'>
        {rows.map((row) => (
          <div
            key={row.key}
            data-outcome={row.outcome}
            className={cn(
              'grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-3 px-3 py-2.5 text-sm',
              row.outcome === 'accept' && 'bg-emerald-500/[0.04]',
              row.outcome === 'reject' && 'bg-destructive/[0.025]'
            )}
          >
            <dt className='text-muted-foreground min-w-0 break-words'>
              {t(row.labelKey)}
            </dt>
            <dd className='font-mono font-semibold tabular-nums'>
              {format.number(row.value)}
            </dd>
          </div>
        ))}
      </dl>
    </SideDrawerSection>
  )
}

export function ManualBillingReviewCaseDetails(props: {
  review: ManualBillingReviewItem
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()

  return (
    <>
      <SideDrawerSection>
        <SideDrawerSectionHeader
          icon={<Clock3 className='size-4' aria-hidden='true' />}
          title={t('Review context')}
          description={t(
            'Immutable identifiers and the reason this reservation entered manual review.'
          )}
        />
        <dl className='grid min-w-0 gap-x-6 gap-y-3 text-sm sm:grid-cols-2'>
          <div className='min-w-0'>
            <dt className='text-muted-foreground text-xs'>
              {t('Reservation ID')}
            </dt>
            <dd className='mt-1 font-mono break-all'>
              {props.review.reservation_id}
            </dd>
          </div>
          <div className='min-w-0'>
            <dt className='text-muted-foreground text-xs'>
              {t('Review type')}
            </dt>
            <dd className='mt-1'>
              {t(getManualBillingReviewKindLabelKey(props.review.review_kind))}
            </dd>
          </div>
          <div className='min-w-0'>
            <dt className='text-muted-foreground text-xs'>
              {t('Public task ID')}
            </dt>
            <dd className='mt-1 font-mono break-all'>
              {props.review.public_task_id || t('Not available')}
            </dd>
          </div>
          <div className='min-w-0'>
            <dt className='text-muted-foreground text-xs'>
              {t('Upstream task ID')}
            </dt>
            <dd className='mt-1 font-mono break-all'>
              {props.review.upstream_task_id || t('Not recorded')}
            </dd>
          </div>
          <div>
            <dt className='text-muted-foreground text-xs'>{t('User')}</dt>
            <dd className='mt-1 font-mono'>#{props.review.user_id}</dd>
          </div>
          <div>
            <dt className='text-muted-foreground text-xs'>
              {t('Entered review')}
            </dt>
            <dd className='mt-1'>
              {format.timestamp(props.review.manual_review_since_ms)}
            </dd>
          </div>
          <div>
            <dt className='text-muted-foreground text-xs'>
              {t('Reservation state')}
            </dt>
            <dd className='mt-1'>
              <Badge variant='outline'>{props.review.state}</Badge>
            </dd>
          </div>
          <div>
            <dt className='text-muted-foreground text-xs'>
              {t('Review version')}
            </dt>
            <dd className='mt-1 font-mono'>v{props.review.review_version}</dd>
          </div>
        </dl>
        <div className='border-border/70 bg-muted/30 rounded-lg border px-3 py-2.5'>
          <div className='text-muted-foreground text-xs'>
            {t('System reason')}
          </div>
          <p className='mt-1 text-sm leading-6 break-words whitespace-pre-wrap'>
            {props.review.reason || t('No system reason was provided.')}
          </p>
        </div>
      </SideDrawerSection>

      <ManualBillingReviewFinancialOutcomes review={props.review} />

      <SideDrawerSection>
        <SideDrawerSectionHeader
          icon={<Waypoints className='size-4' aria-hidden='true' />}
          title={t('Attempt evidence')}
          description={t(
            'Routing identifiers are shown for audit correlation. Credentials and secret values are never displayed.'
          )}
        />
        {props.review.attempts.length === 0 ? (
          <p className='text-muted-foreground text-sm'>
            {t('No attempt evidence is available.')}
          </p>
        ) : (
          <div className='divide-border/70 divide-y overflow-hidden rounded-lg border'>
            {props.review.attempts.map((attempt) => (
              <div
                key={`${attempt.attempt_index}-${attempt.channel_id}-${attempt.credential_id}`}
                className='grid min-w-0 gap-3 px-3 py-3 text-xs sm:grid-cols-[auto_minmax(0,1fr)_minmax(0,1fr)] sm:items-center'
              >
                <div className='flex items-center gap-2'>
                  <Badge variant='outline'>#{attempt.attempt_index}</Badge>
                  <span>{attempt.state}</span>
                </div>
                <div className='text-muted-foreground min-w-0 break-words'>
                  {t('Channel #{{channel}} · Credential #{{credential}}', {
                    channel: attempt.channel_id,
                    credential: attempt.credential_id,
                  })}
                </div>
                <div className='text-muted-foreground min-w-0 break-all sm:text-right'>
                  {attempt.channel_version || t('Version unavailable')}
                </div>
                <dl className='border-border/60 grid min-w-0 gap-2 border-t pt-2 sm:col-span-3 sm:grid-cols-2'>
                  <div className='min-w-0'>
                    <dt className='text-muted-foreground'>
                      {t('Authorized at')}
                    </dt>
                    <dd className='mt-0.5 break-words'>
                      {attempt.authorized_ms > 0
                        ? format.timestamp(attempt.authorized_ms)
                        : t('Not available')}
                    </dd>
                  </div>
                  <div className='min-w-0 sm:text-right'>
                    <dt className='text-muted-foreground'>
                      {t('Send lease deadline')}
                    </dt>
                    <dd className='mt-0.5 break-words'>
                      {attempt.send_deadline_ms > 0
                        ? format.timestamp(attempt.send_deadline_ms)
                        : t('Not available')}
                    </dd>
                  </div>
                </dl>
              </div>
            ))}
          </div>
        )}
      </SideDrawerSection>

      {props.review.blockers.length > 0 ? (
        <SideDrawerSection>
          <SideDrawerSectionHeader
            icon={<ShieldAlert className='size-4' aria-hidden='true' />}
            title={t('Resolution blockers')}
            description={t(
              'Blocked actions remain unavailable until the server reports complete evidence.'
            )}
          />
          <Alert variant='destructive'>
            <ShieldAlert aria-hidden='true' />
            <AlertTitle>
              {t('{{count}} blockers require attention', {
                count: props.review.blockers.length,
              })}
            </AlertTitle>
            <AlertDescription>
              <ul className='list-disc space-y-1 pl-4'>
                {props.review.blockers.map((blocker) => (
                  <li key={blocker}>
                    {t(getManualBillingReviewBlockerLabelKey(blocker))}
                  </li>
                ))}
              </ul>
            </AlertDescription>
          </Alert>
        </SideDrawerSection>
      ) : null}
    </>
  )
}
