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

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { LockKeyhole, Plus, RefreshCw } from 'lucide-react'
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import {
  getCurrentChannelRoutingPolicy,
  listChannelRoutingPolicyDrafts,
  validateChannelRoutingPolicyDraft,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingPageFrame } from '../components/page-frame'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
} from '../components/page-state'
import { ChannelRoutingCursorPagination } from '../components/pagination-bar'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import type { PolicyDraftSummary } from '../types'
import { ChannelRoutingCurrentPolicySection } from './current-policy-section'
import { ManualBillingReviewsSection } from './manual-billing-reviews-section'
import { ChannelRoutingOperationsSection } from './operations-section'
import { ChannelRoutingPolicyActivationSheet } from './policy-activation-sheet'
import { ChannelRoutingPolicyDraftActions } from './policy-draft-actions'
import { ChannelRoutingPolicyDraftSheet } from './policy-draft-sheet'
import { ChannelRoutingPolicyRollbackSheet } from './policy-rollback-sheet'
import { ChannelRoutingPolicySimulationSheet } from './policy-simulation-sheet'

const route = getRouteApi('/_authenticated/channel-routing/$section')
const workflow = [
  'Draft',
  'Validate',
  'Replay',
  'Shadow',
  'Canary',
  'Approval',
  'Deploy',
  'Monitor',
  'Rollback',
]

export function ChannelRoutingPoliciesPage() {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const queryClient = useQueryClient()
  const user = useAuthStore((state) => state.auth.user)
  const canWrite = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.WRITE
  )
  const canDeploy = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.DEPLOY
  )
  const canReadBillingReviews = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.BILLING_REVIEW,
    ADMIN_PERMISSION_ACTIONS.READ
  )
  const canResolveBillingReviews = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.BILLING_REVIEW,
    ADMIN_PERMISSION_ACTIONS.RESOLVE
  )
  const [editorOpen, setEditorOpen] = useState(false)
  const [selectedDraft, setSelectedDraft] = useState<PolicyDraftSummary | null>(
    null
  )
  const handleBillingReviewCursorChange = useCallback(
    (billingReviewCursor: number) => {
      void navigate({
        search: (previous) => ({
          ...previous,
          billingReviewCursor,
        }),
        replace: true,
        hash: 'manual-billing-reviews',
      })
    },
    [navigate]
  )
  const [simulationDraft, setSimulationDraft] =
    useState<PolicyDraftSummary | null>(null)
  const [activationDraft, setActivationDraft] =
    useState<PolicyDraftSummary | null>(null)
  const [activationIntent, setActivationIntent] = useState<
    'approve' | 'publish'
  >('approve')
  const [rollbackOpen, setRollbackOpen] = useState(false)
  const cursor = search.draftCursor ?? search.cursor ?? 0
  const limit = search.limit ?? 20
  const draftsQuery = useQuery({
    queryKey: channelRoutingQueryKeys.policyDrafts({ cursor, limit }),
    queryFn: () =>
      listChannelRoutingPolicyDrafts({
        cursor: cursor || undefined,
        limit,
      }),
    placeholderData: (previous) => previous,
  })
  const currentPolicyQuery = useQuery({
    queryKey: channelRoutingQueryKeys.currentPolicy(),
    queryFn: getCurrentChannelRoutingPolicy,
  })
  const validateDraft = useMutation({
    mutationFn: validateChannelRoutingPolicyDraft,
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.all,
      })
      toast.success(t('Policy draft validated'))
    },
  })

  const openEditor = (draft: PolicyDraftSummary | null) => {
    setSelectedDraft(draft)
    setEditorOpen(true)
  }
  const openActivation = (
    draft: PolicyDraftSummary,
    intent: 'approve' | 'publish'
  ) => {
    setActivationDraft(draft)
    setActivationIntent(intent)
  }
  const canCreateDraft = canWrite && currentPolicyQuery.data != null

  return (
    <>
      <ChannelRoutingPageFrame
        activeSection='policies'
        title={t('Policies and changes')}
        actions={
          <div className='flex items-center gap-2'>
            <Button
              size='icon-sm'
              variant='outline'
              aria-label={t('Refresh')}
              disabled={draftsQuery.isFetching || currentPolicyQuery.isFetching}
              onClick={() =>
                void Promise.all([
                  draftsQuery.refetch(),
                  currentPolicyQuery.refetch(),
                ])
              }
            >
              <RefreshCw
                aria-hidden='true'
                className={
                  draftsQuery.isFetching || currentPolicyQuery.isFetching
                    ? 'animate-spin motion-reduce:animate-none'
                    : undefined
                }
              />
            </Button>
            {canWrite ? (
              <Button
                size='sm'
                disabled={!canCreateDraft}
                onClick={() => openEditor(null)}
              >
                <Plus aria-hidden='true' />
                {t('New draft')}
              </Button>
            ) : (
              <Badge variant='outline'>
                <LockKeyhole aria-hidden='true' />
                {canDeploy ? t('Deployment access') : t('Read only')}
              </Badge>
            )}
          </div>
        }
      >
        <div className='space-y-5 pb-2'>
          <ol className='bg-border grid grid-cols-3 gap-px overflow-hidden rounded-lg border lg:grid-cols-9'>
            {workflow.map((step, index) => (
              <li
                key={step}
                className='bg-background flex min-w-0 items-center gap-2 px-2 py-2 text-xs'
              >
                <span className='bg-muted text-muted-foreground flex size-5 shrink-0 items-center justify-center rounded-full font-medium'>
                  {index + 1}
                </span>
                <span className='min-w-0 font-medium break-words'>
                  {t(step)}
                </span>
              </li>
            ))}
          </ol>

          {!canWrite ? (
            <div className='bg-muted/40 text-muted-foreground flex items-center gap-2 rounded-lg border px-3 py-2 text-sm'>
              <LockKeyhole className='size-4 shrink-0' aria-hidden='true' />
              <span>
                {t('Policy draft editing is unavailable for your role.')}
              </span>
            </div>
          ) : null}

          <ChannelRoutingCurrentPolicySection
            current={currentPolicyQuery.data}
            isLoading={currentPolicyQuery.isLoading}
            error={currentPolicyQuery.error}
            canDeploy={canDeploy}
            onRetry={() => void currentPolicyQuery.refetch()}
            onRollback={() => setRollbackOpen(true)}
          />

          <section
            className='space-y-3 border-t pt-5'
            aria-labelledby='policy-drafts-heading'
          >
            <div className='flex flex-wrap items-center justify-between gap-3'>
              <div>
                <h2
                  id='policy-drafts-heading'
                  className='text-base font-semibold'
                >
                  {t('Policy drafts')}
                </h2>
                <p className='text-muted-foreground mt-1 text-xs'>
                  {t('Versioned policy candidates and deployment readiness')}
                </p>
              </div>
            </div>

            {validateDraft.isError ? (
              <div className='border-destructive/30 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm'>
                {t(
                  'Could not validate this draft. Refresh it and review the policy document.'
                )}
              </div>
            ) : null}
            {draftsQuery.isLoading ? <ChannelRoutingLoadingState /> : null}
            {draftsQuery.isError ? (
              <ChannelRoutingErrorState
                error={draftsQuery.error}
                onRetry={() => void draftsQuery.refetch()}
              />
            ) : null}
            {draftsQuery.data && draftsQuery.data.items.length === 0 ? (
              <ChannelRoutingEmptyState
                title={t('No policy drafts')}
                description={t('No policy changes have been drafted yet.')}
                action={
                  canWrite ? (
                    <Button
                      size='sm'
                      disabled={!canCreateDraft}
                      onClick={() => openEditor(null)}
                    >
                      <Plus aria-hidden='true' />
                      {t('New draft')}
                    </Button>
                  ) : undefined
                }
              />
            ) : null}

            {draftsQuery.data && draftsQuery.data.items.length > 0 ? (
              <>
                <div className='hidden overflow-hidden rounded-lg border md:block'>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{t('Draft')}</TableHead>
                        <TableHead>{t('Status')}</TableHead>
                        <TableHead>{t('Base revision')}</TableHead>
                        <TableHead>{t('Updated by')}</TableHead>
                        <TableHead>{t('Updated')}</TableHead>
                        <TableHead>{t('Published revision')}</TableHead>
                        <TableHead className='w-44'>
                          <span className='sr-only'>{t('Actions')}</span>
                        </TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {draftsQuery.data.items.map((draft) => (
                        <TableRow key={draft.id}>
                          <TableCell>
                            <button
                              type='button'
                              className='text-left font-medium hover:underline'
                              onClick={() => openEditor(draft)}
                            >
                              #{draft.id}
                            </button>
                            <div className='text-muted-foreground text-xs'>
                              v{draft.version} ·{' '}
                              {format.shortHash(draft.document_hash)}
                            </div>
                          </TableCell>
                          <TableCell>
                            <ChannelRoutingStatusBadge status={draft.status} />
                          </TableCell>
                          <TableCell>r{draft.base_revision}</TableCell>
                          <TableCell>#{draft.updated_by}</TableCell>
                          <TableCell>
                            {format.timestamp(draft.updated_time_ms)}
                          </TableCell>
                          <TableCell>
                            {draft.published_revision > 0
                              ? `r${draft.published_revision}`
                              : t('Not published')}
                          </TableCell>
                          <TableCell>
                            <ChannelRoutingPolicyDraftActions
                              draft={draft}
                              canWrite={canWrite}
                              canDeploy={canDeploy}
                              validating={
                                validateDraft.isPending &&
                                validateDraft.variables?.id === draft.id
                              }
                              onValidate={() => validateDraft.mutate(draft)}
                              onSimulate={() => setSimulationDraft(draft)}
                              onApprove={() => openActivation(draft, 'approve')}
                              onPublish={() => openActivation(draft, 'publish')}
                              onView={() => openEditor(draft)}
                            />
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>

                <div className='divide-y rounded-lg border md:hidden'>
                  {draftsQuery.data.items.map((draft) => (
                    <article key={draft.id} className='p-3'>
                      <div className='flex items-start justify-between gap-3'>
                        <div className='min-w-0'>
                          <div className='text-sm font-medium'>
                            {t('Draft #{{id}}', { id: draft.id })}
                          </div>
                          <div className='text-muted-foreground text-xs'>
                            v{draft.version} · r{draft.base_revision}
                          </div>
                        </div>
                        <ChannelRoutingStatusBadge status={draft.status} />
                      </div>
                      <div className='text-muted-foreground mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs'>
                        <span>
                          {t('Updated by #{{id}}', { id: draft.updated_by })}
                        </span>
                        <span>{format.timestamp(draft.updated_time_ms)}</span>
                      </div>
                      <div className='mt-3 border-t pt-2'>
                        <ChannelRoutingPolicyDraftActions
                          draft={draft}
                          canWrite={canWrite}
                          canDeploy={canDeploy}
                          validating={
                            validateDraft.isPending &&
                            validateDraft.variables?.id === draft.id
                          }
                          onValidate={() => validateDraft.mutate(draft)}
                          onSimulate={() => setSimulationDraft(draft)}
                          onApprove={() => openActivation(draft, 'approve')}
                          onPublish={() => openActivation(draft, 'publish')}
                          onView={() => openEditor(draft)}
                        />
                      </div>
                    </article>
                  ))}
                </div>

                <ChannelRoutingCursorPagination
                  cursor={cursor}
                  nextCursor={draftsQuery.data.next_cursor}
                  onCursorChange={(draftCursor) =>
                    void navigate({
                      search: (previous) => ({
                        ...previous,
                        cursor: undefined,
                        draftCursor,
                      }),
                      replace: true,
                    })
                  }
                />
              </>
            ) : null}
          </section>

          {canReadBillingReviews ? (
            <ManualBillingReviewsSection
              cursor={search.billingReviewCursor ?? 0}
              canResolve={canResolveBillingReviews}
              onCursorChange={handleBillingReviewCursorChange}
            />
          ) : null}

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
        </div>
      </ChannelRoutingPageFrame>

      <ChannelRoutingPolicyDraftSheet
        draft={selectedDraft}
        baseRevision={currentPolicyQuery.data?.head.current_revision ?? 0}
        currentDocument={currentPolicyQuery.data?.document}
        canWrite={canWrite}
        open={editorOpen}
        onOpenChange={(open) => {
          setEditorOpen(open)
          if (!open) setSelectedDraft(null)
        }}
      />
      <ChannelRoutingPolicySimulationSheet
        draft={simulationDraft}
        open={simulationDraft != null}
        onOpenChange={(open) => {
          if (!open) setSimulationDraft(null)
        }}
      />
      <ChannelRoutingPolicyActivationSheet
        draft={activationDraft}
        intent={activationIntent}
        canDeploy={canDeploy}
        open={activationDraft != null}
        onOpenChange={(open) => {
          if (!open) setActivationDraft(null)
        }}
      />
      <ChannelRoutingPolicyRollbackSheet
        current={currentPolicyQuery.data ?? null}
        canDeploy={canDeploy}
        open={rollbackOpen}
        onOpenChange={setRollbackOpen}
      />
    </>
  )
}
