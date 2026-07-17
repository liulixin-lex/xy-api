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
  Add01Icon,
  ArrowReloadHorizontalIcon,
  Delete02Icon,
  ShieldKeyIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
  deleteChannelRoutingPolicyDraft,
  deleteChannelRoutingPolicyDrafts,
  getCurrentChannelRoutingPolicy,
  listChannelRoutingPolicyDrafts,
  validateChannelRoutingPolicyDraft,
} from '../../api/client'
import { channelRoutingQueryKeys } from '../../api/query-keys'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../../components/page-state'
import { ChannelRoutingCursorPagination } from '../../components/pagination-bar'
import { ChannelRoutingStatusBadge } from '../../components/status-badge'
import { useChannelRoutingFormatters } from '../../lib/format'
import {
  mostRecentWorkingPolicyDraft,
  policyDraftReadinessLabel,
} from '../../lib/policy-draft-workspace'
import type { PolicyDraftSummary } from '../../types'
import { ChannelRoutingCurrentPolicySection } from '../current-policy-section'
import { ChannelRoutingPolicyActivationSheet } from '../policy-activation-sheet'
import { ChannelRoutingPolicyDraftActions } from '../policy-draft-actions'
import { ChannelRoutingPolicyDraftSheet } from '../policy-draft-sheet'
import { ChannelRoutingPolicyRollbackSheet } from '../policy-rollback-sheet'
import { ChannelRoutingPolicySimulationSheet } from '../policy-simulation-sheet'

const route = getRouteApi('/_authenticated/channel-routing/$section')
const workflow = [
  'Draft',
  'Validate',
  'Simulate',
  'Publish',
  'Monitor',
  'Rollback',
]

export function ChannelRoutingVersionedPolicies() {
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
  const [editorOpen, setEditorOpen] = useState(false)
  const [selectedDraft, setSelectedDraft] = useState<PolicyDraftSummary | null>(
    null
  )
  const [simulationDraft, setSimulationDraft] =
    useState<PolicyDraftSummary | null>(null)
  const [activationDraft, setActivationDraft] =
    useState<PolicyDraftSummary | null>(null)
  const [selectedDraftIds, setSelectedDraftIds] = useState<Set<number>>(
    () => new Set()
  )
  const [deleteTargets, setDeleteTargets] = useState<PolicyDraftSummary[]>([])
  const autoOpenedDraftRef = useRef(false)
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
    meta: { handleErrorLocally: true },
  })
  const deleteDrafts = useMutation({
    mutationFn: (drafts: PolicyDraftSummary[]) =>
      drafts.length === 1
        ? deleteChannelRoutingPolicyDraft(drafts[0])
        : deleteChannelRoutingPolicyDrafts(drafts),
    onSuccess: async (response) => {
      setDeleteTargets([])
      setSelectedDraftIds(new Set())
      await queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.policyDraftsRoot(),
      })
      toast.success(
        t('{{count}} policy drafts permanently deleted', {
          count: response.deleted_ids.length,
        })
      )
    },
    meta: { handleErrorLocally: true },
  })

  useEffect(() => {
    if (autoOpenedDraftRef.current || !draftsQuery.data) return
    autoOpenedDraftRef.current = true
    const draft = mostRecentWorkingPolicyDraft(draftsQuery.data.items)
    if (draft) {
      setSelectedDraft(draft)
      setEditorOpen(true)
    }
  }, [draftsQuery.data])

  useEffect(() => {
    setSelectedDraftIds(new Set())
  }, [cursor])

  const openEditor = (draft: PolicyDraftSummary | null) => {
    setSelectedDraft(draft)
    setEditorOpen(true)
  }
  const openActivation = (draft: PolicyDraftSummary) => {
    setActivationDraft(draft)
  }
  const canCreateDraft =
    canWrite &&
    currentPolicyQuery.data != null &&
    !currentPolicyQuery.isRefetchError &&
    !draftsQuery.isRefetchError
  const draftMutationsDisabled =
    draftsQuery.isRefetchError || currentPolicyQuery.isRefetchError
  const currentDrafts = draftsQuery.data?.items ?? []
  const selectedDrafts = currentDrafts.filter((draft) =>
    selectedDraftIds.has(draft.id)
  )
  const selectableDrafts = currentDrafts.filter(
    (draft) => canWrite && draft.can_delete
  )
  const allSelectableSelected =
    selectableDrafts.length > 0 &&
    selectableDrafts.every((draft) => selectedDraftIds.has(draft.id))
  const toggleDraft = (draft: PolicyDraftSummary, checked: boolean) => {
    setSelectedDraftIds((current) => {
      const next = new Set(current)
      if (checked) {
        next.add(draft.id)
      } else {
        next.delete(draft.id)
      }
      return next
    })
  }

  return (
    <>
      <div className='flex flex-col gap-5 pb-2'>
        <div className='flex flex-wrap items-center justify-between gap-3'>
          <div>
            <h2 className='text-base font-semibold'>
              {t('Versioned policies')}
            </h2>
            <p className='text-muted-foreground mt-1 text-xs'>
              {t(
                'Continue unpublished drafts, validate deterministic rules, optionally simulate, then publish or roll back directly.'
              )}
            </p>
          </div>
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
              <HugeiconsIcon
                icon={ArrowReloadHorizontalIcon}
                strokeWidth={2}
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
                <HugeiconsIcon
                  icon={Add01Icon}
                  strokeWidth={2}
                  data-icon='inline-start'
                  aria-hidden='true'
                />
                {t('New parallel draft')}
              </Button>
            ) : (
              <Badge variant='outline'>
                <HugeiconsIcon icon={ShieldKeyIcon} aria-hidden='true' />
                {canDeploy ? t('Deployment access') : t('Read only')}
              </Badge>
            )}
          </div>
        </div>
        <ol className='bg-border grid grid-cols-2 gap-px overflow-hidden rounded-lg border sm:grid-cols-3 lg:grid-cols-6'>
          {workflow.map((step, index) => (
            <li
              key={step}
              className='bg-background flex min-w-0 items-center gap-2 px-2 py-2 text-xs'
            >
              <span className='text-muted-foreground font-mono tabular-nums'>
                {index + 1}
              </span>
              <span className='min-w-0 font-medium break-words'>{t(step)}</span>
            </li>
          ))}
        </ol>

        {!canWrite ? (
          <div className='bg-muted/40 text-muted-foreground flex items-center gap-2 rounded-lg border px-3 py-2 text-sm'>
            <HugeiconsIcon
              icon={ShieldKeyIcon}
              className='shrink-0'
              aria-hidden='true'
            />
            <span>
              {t('Policy draft editing is unavailable for your role.')}
            </span>
          </div>
        ) : null}

        <ChannelRoutingCurrentPolicySection
          current={currentPolicyQuery.data}
          isLoading={currentPolicyQuery.isLoading}
          isFetching={currentPolicyQuery.isFetching}
          error={currentPolicyQuery.error}
          canDeploy={canDeploy && !currentPolicyQuery.isRefetchError}
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
                {t(
                  'Only unpublished drafts appear here. Published drafts remain in immutable policy history and control audit.'
                )}
              </p>
            </div>
            {selectedDrafts.length > 0 ? (
              <Button
                size='sm'
                variant='destructive'
                disabled={draftMutationsDisabled || deleteDrafts.isPending}
                onClick={() => setDeleteTargets(selectedDrafts)}
              >
                <HugeiconsIcon
                  icon={Delete02Icon}
                  data-icon='inline-start'
                  strokeWidth={2}
                  aria-hidden='true'
                />
                {t('Permanently delete {{count}} drafts', {
                  count: selectedDrafts.length,
                })}
              </Button>
            ) : null}
          </div>

          {validateDraft.isError ? (
            <div className='border-destructive/30 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm'>
              {t(
                'Could not validate this draft. Refresh it and review the policy document.'
              )}
            </div>
          ) : null}
          {deleteDrafts.isError ? (
            <div className='border-destructive/30 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm'>
              {t(
                'No draft was deleted. Refresh the workspace and review version conflicts before retrying.'
              )}
            </div>
          ) : null}
          {draftsQuery.isLoading ? <ChannelRoutingLoadingState /> : null}
          {draftsQuery.isError && !draftsQuery.data ? (
            <ChannelRoutingErrorState
              error={draftsQuery.error}
              onRetry={() => void draftsQuery.refetch()}
            />
          ) : null}
          {draftsQuery.isRefetchError && draftsQuery.data ? (
            <ChannelRoutingRefetchErrorAlert
              isFetching={draftsQuery.isFetching}
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
                    <HugeiconsIcon
                      icon={Add01Icon}
                      strokeWidth={2}
                      data-icon='inline-start'
                      aria-hidden='true'
                    />
                    {t('New parallel draft')}
                  </Button>
                ) : undefined
              }
            />
          ) : null}

          {draftsQuery.data && draftsQuery.data.items.length > 0 ? (
            <>
              <div className='hidden overflow-hidden rounded-lg border md:block'>
                <Table scrollAreaLabel={t('Policy drafts')}>
                  <TableHeader>
                    <TableRow>
                      <TableHead className='w-10'>
                        <Checkbox
                          aria-label={t(
                            'Select all deletable drafts on this page'
                          )}
                          checked={allSelectableSelected}
                          disabled={
                            selectableDrafts.length === 0 ||
                            draftMutationsDisabled
                          }
                          onCheckedChange={(value) => {
                            const checked = value === true
                            setSelectedDraftIds((current) => {
                              const next = new Set(current)
                              for (const draft of selectableDrafts) {
                                if (checked) next.add(draft.id)
                                else next.delete(draft.id)
                              }
                              return next
                            })
                          }}
                        />
                      </TableHead>
                      <TableHead>{t('Draft')}</TableHead>
                      <TableHead>{t('Workspace')}</TableHead>
                      <TableHead>{t('Base revision')}</TableHead>
                      <TableHead>{t('Readiness')}</TableHead>
                      <TableHead>{t('Updated')}</TableHead>
                      <TableHead className='w-44'>
                        <span className='sr-only'>{t('Actions')}</span>
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {draftsQuery.data.items.map((draft) => (
                      <TableRow key={draft.id}>
                        <TableCell>
                          <Checkbox
                            aria-label={t('Select draft #{{id}}', {
                              id: draft.id,
                            })}
                            checked={selectedDraftIds.has(draft.id)}
                            disabled={
                              !canWrite ||
                              !draft.can_delete ||
                              draftMutationsDisabled
                            }
                            onCheckedChange={(value) =>
                              toggleDraft(draft, value === true)
                            }
                          />
                        </TableCell>
                        <TableCell>
                          <button
                            type='button'
                            className='text-left font-medium hover:underline disabled:cursor-not-allowed disabled:opacity-50'
                            disabled={draftsQuery.isRefetchError}
                            onClick={() => openEditor(draft)}
                          >
                            #{draft.id}
                          </button>
                          <div className='text-muted-foreground text-xs'>
                            v{draft.version}{' '}
                            {format.shortHash(draft.document_hash)}
                          </div>
                        </TableCell>
                        <TableCell>
                          <div className='flex flex-wrap gap-1'>
                            <ChannelRoutingStatusBadge
                              status={draft.workspace_state}
                            />
                            <ChannelRoutingStatusBadge status={draft.status} />
                          </div>
                        </TableCell>
                        <TableCell>r{draft.base_revision}</TableCell>
                        <TableCell>
                          <div className='text-xs'>
                            {t(policyDraftReadinessLabel(draft))}
                          </div>
                        </TableCell>
                        <TableCell>
                          <div>{format.timestamp(draft.updated_time_ms)}</div>
                          <div className='text-muted-foreground text-xs'>
                            {t('Updated by #{{id}}', { id: draft.updated_by })}
                          </div>
                        </TableCell>
                        <TableCell>
                          <ChannelRoutingPolicyDraftActions
                            draft={draft}
                            canWrite={canWrite}
                            canDeploy={canDeploy}
                            mutationsDisabled={draftMutationsDisabled}
                            validating={
                              validateDraft.isPending &&
                              validateDraft.variables?.id === draft.id
                            }
                            deleting={
                              deleteDrafts.isPending &&
                              deleteDrafts.variables?.some(
                                (item) => item.id === draft.id
                              ) === true
                            }
                            onValidate={() => validateDraft.mutate(draft)}
                            onSimulate={() => setSimulationDraft(draft)}
                            onPublish={() => openActivation(draft)}
                            onDelete={() => setDeleteTargets([draft])}
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
                      <div className='flex min-w-0 items-start gap-3'>
                        <Checkbox
                          aria-label={t('Select draft #{{id}}', {
                            id: draft.id,
                          })}
                          checked={selectedDraftIds.has(draft.id)}
                          disabled={
                            !canWrite ||
                            !draft.can_delete ||
                            draftMutationsDisabled
                          }
                          onCheckedChange={(value) =>
                            toggleDraft(draft, value === true)
                          }
                        />
                        <div className='min-w-0'>
                          <div className='text-sm font-medium'>
                            {t('Draft #{{id}}', { id: draft.id })}
                          </div>
                          <div className='text-muted-foreground text-xs'>
                            v{draft.version} r{draft.base_revision}
                          </div>
                        </div>
                      </div>
                      <div className='flex flex-wrap justify-end gap-1'>
                        <ChannelRoutingStatusBadge
                          status={draft.workspace_state}
                        />
                        <ChannelRoutingStatusBadge status={draft.status} />
                      </div>
                    </div>
                    <div className='text-muted-foreground mt-3 flex flex-col gap-1 text-xs'>
                      <span>{t(policyDraftReadinessLabel(draft))}</span>
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
                        mutationsDisabled={draftMutationsDisabled}
                        validating={
                          validateDraft.isPending &&
                          validateDraft.variables?.id === draft.id
                        }
                        deleting={
                          deleteDrafts.isPending &&
                          deleteDrafts.variables?.some(
                            (item) => item.id === draft.id
                          ) === true
                        }
                        onValidate={() => validateDraft.mutate(draft)}
                        onSimulate={() => setSimulationDraft(draft)}
                        onPublish={() => openActivation(draft)}
                        onDelete={() => setDeleteTargets([draft])}
                        onView={() => openEditor(draft)}
                      />
                    </div>
                  </article>
                ))}
              </div>

              <ChannelRoutingCursorPagination
                cursor={cursor}
                nextCursor={draftsQuery.data.next_cursor}
                disabled={draftsQuery.isRefetchError}
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
      </div>

      <ChannelRoutingPolicyDraftSheet
        draft={selectedDraft}
        baseRevision={currentPolicyQuery.data?.head.current_revision ?? 0}
        currentDocument={currentPolicyQuery.data?.document}
        canWrite={
          canWrite &&
          (selectedDraft == null || selectedDraft.workspace_state === 'working')
        }
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
      <ConfirmDialog
        open={deleteTargets.length > 0}
        onOpenChange={(open) => {
          if (!open && !deleteDrafts.isPending) setDeleteTargets([])
        }}
        title={
          deleteTargets.length === 1
            ? t('Permanently delete draft #{{id}}?', {
                id: deleteTargets[0]?.id,
              })
            : t('Permanently delete {{count}} drafts?', {
                count: deleteTargets.length,
              })
        }
        desc={t(
          'This only deletes unpublished drafts. Batch deletion is all or nothing and cannot be undone.'
        )}
        confirmText={
          deleteDrafts.isPending ? t('Deleting') : t('Permanently delete')
        }
        destructive
        isLoading={deleteDrafts.isPending}
        handleConfirm={() => {
          if (deleteTargets.length > 0) deleteDrafts.mutate(deleteTargets)
        }}
      />
    </>
  )
}
