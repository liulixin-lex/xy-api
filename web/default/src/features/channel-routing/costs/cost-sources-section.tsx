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
import { Plus, Search, ShieldAlert, X } from 'lucide-react'
import {
  useCallback,
  useEffect,
  useState,
  type FormEvent,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import {
  ChannelRoutingCostBindingConflictError,
  deleteChannelRoutingCostBinding,
  getChannelRoutingCostBindingApiError,
  listChannelRoutingCostBindings,
  testChannelRoutingCostBinding,
  updateChannelRoutingCostBinding,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
} from '../components/page-state'
import { ChannelRoutingPagination } from '../components/pagination-bar'
import { costBindingUpdateRequest } from '../lib/cost-binding'
import type { RoutingCostBinding } from '../types'
import { CostSourceList } from './cost-source-list'
import { ChannelRoutingCostSourceSheet } from './cost-source-sheet'

const route = getRouteApi('/_authenticated/channel-routing/$section')

function failureMessage(error: unknown, t: (key: string) => string): string {
  const failure = getChannelRoutingCostBindingApiError(error)
  if (failure.status === 403) {
    return t('You do not have permission to perform this action.')
  }
  if (failure.status === 404) return t('This cost source no longer exists.')
  if (failure.status === 502) {
    return failure.detail || t('The upstream endpoint or credentials failed.')
  }
  return t('The cost source action failed. Try again.')
}

export function ChannelRoutingCostSourcesSection(props: {
  canOperate: boolean
  canSensitiveWrite: boolean
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const page = search.sourcePage ?? 1
  const pageSize = search.sourcePageSize ?? 20
  const sourceType = search.sourceType ?? 'all'
  const enabled = search.sourceEnabled ?? 'all'
  const sourceSearch = search.sourceSearch ?? ''
  const [sheetOpen, setSheetOpen] = useState(false)
  const [selectedBinding, setSelectedBinding] =
    useState<RoutingCostBinding | null>(null)
  const [sheetNotice, setSheetNotice] = useState('')
  const [pendingDelete, setPendingDelete] = useState<RoutingCostBinding | null>(
    null
  )
  const queryParams = {
    page,
    page_size: pageSize,
    search: sourceSearch || undefined,
    upstream_type: sourceType === 'all' ? undefined : sourceType,
    enabled: enabled === 'all' ? undefined : enabled,
  }
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.costBindings(queryParams),
    queryFn: () => listChannelRoutingCostBindings(queryParams),
    placeholderData: (previous) => previous,
    meta: { handleErrorLocally: true },
  })

  const updateSearch = useCallback(
    (patch: Record<string, string | number | boolean | undefined>) => {
      void navigate({
        search: (previous) => ({ ...previous, ...patch }),
        replace: true,
      })
    },
    [navigate]
  )

  useEffect(() => {
    if (!query.data) return
    const totalPages = Math.max(1, Math.ceil(query.data.total / pageSize))
    if (page > totalPages) updateSearch({ sourcePage: totalPages })
  }, [page, pageSize, query.data, updateSearch])

  const refreshAffectedQueries = async () => {
    await Promise.all([
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.costBindingsRoot(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.costsRoot(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.groupsRoot(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.channelsRoot(),
      }),
      queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.overview(),
      }),
    ])
  }

  const openBinding = (binding: RoutingCostBinding, notice = '') => {
    setSelectedBinding(binding)
    setSheetNotice(notice)
    setSheetOpen(true)
  }

  const handleConflict = (
    error: ChannelRoutingCostBindingConflictError,
    notice: string
  ) => {
    setPendingDelete(null)
    if (error.current) {
      openBinding(
        {
          ...error.current,
          etag: error.currentETag || error.current.etag,
        },
        notice
      )
    } else {
      toast.info(t('The cost source was already deleted.'))
    }
    void refreshAffectedQueries()
  }

  const toggleMutation = useMutation({
    mutationFn: (payload: { binding: RoutingCostBinding; enabled: boolean }) =>
      updateChannelRoutingCostBinding(
        payload.binding,
        costBindingUpdateRequest(payload.binding, {
          enabled: payload.enabled,
        })
      ),
    onSuccess: async (_, payload) => {
      await refreshAffectedQueries()
      toast.success(
        payload.enabled ? t('Cost source enabled') : t('Cost source disabled')
      )
    },
    onError: (error) => {
      if (error instanceof ChannelRoutingCostBindingConflictError) {
        handleConflict(
          error,
          t(
            'This cost source changed before the status update. The latest version is open for review.'
          )
        )
        return
      }
      toast.error(failureMessage(error, t))
    },
  })

  const deleteMutation = useMutation({
    mutationFn: deleteChannelRoutingCostBinding,
    onSuccess: async () => {
      setPendingDelete(null)
      await refreshAffectedQueries()
      toast.success(t('Cost source deleted'))
    },
    onError: (error) => {
      if (error instanceof ChannelRoutingCostBindingConflictError) {
        handleConflict(
          error,
          t(
            'This cost source changed before deletion. The latest version is open for review.'
          )
        )
        return
      }
      toast.error(failureMessage(error, t))
    },
  })

  const testMutation = useMutation({
    mutationFn: (binding: RoutingCostBinding) =>
      testChannelRoutingCostBinding(binding.channel_id),
    onSuccess: (result) => {
      toast.success(
        t('Connection test found {{count}} priced models', {
          count: result.model_count,
        })
      )
    },
    onError: (error) => toast.error(failureMessage(error, t)),
  })

  const handleFilters = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    updateSearch({
      sourcePage: 1,
      sourceSearch: String(form.get('sourceSearch') ?? '').trim(),
    })
  }
  const filtersActive =
    sourceSearch !== '' || sourceType !== 'all' || enabled !== 'all'
  const items = query.data?.items ?? []
  let emptyTitle = t('No cost sources')
  let emptyDescription = t(
    'Add a connector to import upstream pricing for channel routing.'
  )
  let emptyAction: ReactNode
  if (query.data && query.data.total > 0) {
    emptyTitle = t('This cost source page is empty')
    emptyDescription = t(
      'Return to the first page to continue browsing cost sources.'
    )
    emptyAction = (
      <Button
        type='button'
        variant='outline'
        onClick={() => updateSearch({ sourcePage: 1 })}
      >
        {t('First page')}
      </Button>
    )
  } else if (filtersActive) {
    emptyTitle = t('No cost sources match these filters')
    emptyDescription = t(
      'Clear or adjust the filters to see other cost sources.'
    )
    emptyAction = (
      <Button
        type='button'
        variant='outline'
        onClick={() =>
          updateSearch({
            sourcePage: 1,
            sourceSearch: '',
            sourceType: 'all',
            sourceEnabled: 'all',
          })
        }
      >
        <X aria-hidden='true' />
        {t('Clear filters')}
      </Button>
    )
  } else if (props.canSensitiveWrite) {
    emptyAction = (
      <Button
        type='button'
        onClick={() => {
          setSelectedBinding(null)
          setSheetNotice('')
          setSheetOpen(true)
        }}
      >
        <Plus aria-hidden='true' />
        {t('Add cost source')}
      </Button>
    )
  }

  return (
    <div className='space-y-3 pb-2'>
      {!props.canSensitiveWrite ? (
        <Alert role='status'>
          <ShieldAlert aria-hidden='true' />
          <AlertTitle>{t('Cost sources are read-only')}</AlertTitle>
          <AlertDescription>
            {props.canOperate
              ? t(
                  'You can review and test saved connectors, but changing credentials or configuration requires sensitive write permission.'
                )
              : t(
                  'You can review saved connectors, but your role cannot test or change them.'
                )}
          </AlertDescription>
        </Alert>
      ) : null}

      <div className='flex flex-col gap-2 lg:flex-row lg:items-center'>
        <form
          key={sourceSearch}
          className='grid min-w-0 flex-1 gap-2 sm:grid-cols-[minmax(12rem,1fr)_auto]'
          onSubmit={handleFilters}
        >
          <Input
            name='sourceSearch'
            defaultValue={sourceSearch}
            aria-label={t('Search cost sources')}
            placeholder={t('Search channel, URL, group, or ID')}
          />
          <Button type='submit' size='sm' variant='outline'>
            <Search aria-hidden='true' />
            {t('Apply filters')}
          </Button>
        </form>
        <div className='flex min-w-0 flex-wrap items-center gap-2'>
          <NativeSelect
            size='sm'
            value={sourceType}
            aria-label={t('Upstream type')}
            onChange={(event) =>
              updateSearch({
                sourcePage: 1,
                sourceType: event.target.value,
              })
            }
          >
            <NativeSelectOption value='all'>
              {t('All upstream types')}
            </NativeSelectOption>
            <NativeSelectOption value='newapi'>New API</NativeSelectOption>
            <NativeSelectOption value='sub2api'>Sub2API</NativeSelectOption>
          </NativeSelect>
          <NativeSelect
            size='sm'
            value={enabled === 'all' ? 'all' : String(enabled)}
            aria-label={t('Cost source status')}
            onChange={(event) =>
              updateSearch({
                sourcePage: 1,
                sourceEnabled:
                  event.target.value === 'all'
                    ? 'all'
                    : event.target.value === 'true',
              })
            }
          >
            <NativeSelectOption value='all'>
              {t('All statuses')}
            </NativeSelectOption>
            <NativeSelectOption value='true'>{t('Enabled')}</NativeSelectOption>
            <NativeSelectOption value='false'>
              {t('Disabled')}
            </NativeSelectOption>
          </NativeSelect>
          {filtersActive ? (
            <Button
              type='button'
              size='sm'
              variant='ghost'
              onClick={() =>
                updateSearch({
                  sourcePage: 1,
                  sourceSearch: '',
                  sourceType: 'all',
                  sourceEnabled: 'all',
                })
              }
            >
              <X aria-hidden='true' />
              {t('Clear')}
            </Button>
          ) : null}
          {props.canSensitiveWrite ? (
            <Button
              type='button'
              size='sm'
              className='sm:ml-auto'
              onClick={() => {
                setSelectedBinding(null)
                setSheetNotice('')
                setSheetOpen(true)
              }}
            >
              <Plus aria-hidden='true' />
              {t('Add cost source')}
            </Button>
          ) : null}
        </div>
      </div>

      {query.isLoading ? <ChannelRoutingLoadingState /> : null}
      {query.isError ? (
        <ChannelRoutingErrorState
          error={query.error}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      {query.data && items.length === 0 ? (
        <ChannelRoutingEmptyState
          title={emptyTitle}
          description={emptyDescription}
          action={emptyAction}
        />
      ) : null}

      {items.length > 0 ? (
        <CostSourceList
          items={items}
          canOperate={props.canOperate}
          canSensitiveWrite={props.canSensitiveWrite}
          testingChannelId={
            testMutation.isPending
              ? testMutation.variables?.channel_id
              : undefined
          }
          testDisabled={testMutation.isPending}
          toggleDisabled={toggleMutation.isPending}
          onOpen={openBinding}
          onTest={(binding) => testMutation.mutate(binding)}
          onDelete={setPendingDelete}
          onToggle={(binding, enabled) =>
            toggleMutation.mutate({ binding, enabled })
          }
        />
      ) : null}

      {query.data && query.data.total > 0 ? (
        <ChannelRoutingPagination
          page={page}
          pageSize={pageSize}
          total={query.data.total}
          onPageChange={(nextPage) => updateSearch({ sourcePage: nextPage })}
          onPageSizeChange={(nextSize) =>
            updateSearch({ sourcePage: 1, sourcePageSize: nextSize })
          }
        />
      ) : null}

      <ChannelRoutingCostSourceSheet
        open={sheetOpen}
        binding={selectedBinding}
        canOperate={props.canOperate}
        canSensitiveWrite={props.canSensitiveWrite}
        notice={sheetNotice}
        onOpenChange={(open) => {
          setSheetOpen(open)
          if (!open) {
            setSelectedBinding(null)
            setSheetNotice('')
          }
        }}
        onSaved={() => {
          setSelectedBinding(null)
          setSheetNotice('')
        }}
      />

      <ConfirmDialog
        open={pendingDelete != null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null)
        }}
        title={t('Delete cost source')}
        desc={
          <div className='space-y-2'>
            <p>
              {t('Delete the cost source for channel {{id}}?', {
                id: pendingDelete?.channel_id ?? '-',
              })}
            </p>
            <p>
              {t(
                'Future cost sync stops and current cost snapshots are removed. Routing health, breaker state, metrics, and historical cost records are preserved.'
              )}
            </p>
          </div>
        }
        confirmText={t('Delete cost source')}
        destructive
        isLoading={deleteMutation.isPending}
        handleConfirm={() => {
          if (pendingDelete) deleteMutation.mutate(pendingDelete)
        }}
      />
    </div>
  )
}

export default ChannelRoutingCostSourcesSection
