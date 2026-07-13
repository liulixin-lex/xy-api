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
import {
  Cable,
  Eye,
  MoreHorizontal,
  Pencil,
  Plus,
  Search,
  ShieldAlert,
  Trash2,
  X,
} from 'lucide-react'
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
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import {
  ChannelRoutingCostBindingConflictError,
  deleteChannelRoutingCostBinding,
  getChannelRoutingCostBindingApiError,
  listChannelRoutingCostBindings,
  testChannelRoutingCostBinding,
  updateChannelRoutingCostBinding,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingIdentityText } from '../components/identity-text'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
} from '../components/page-state'
import { ChannelRoutingPagination } from '../components/pagination-bar'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import {
  costBindingCredentialCount,
  costBindingUpdateRequest,
} from '../lib/cost-binding'
import { useChannelRoutingFormatters } from '../lib/format'
import type { RoutingCostBinding } from '../types'
import { ChannelRoutingCostSourceSheet } from './cost-source-sheet'

const route = getRouteApi('/_authenticated/channel-routing/$section')

function SyncHealth(props: { binding: RoutingCostBinding }) {
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

export function ChannelRoutingCostSourcesSection(props: {
  canOperate: boolean
  canSensitiveWrite: boolean
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
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
                {items.map((binding) => {
                  const credentialCount = costBindingCredentialCount(binding)
                  const testing =
                    testMutation.isPending &&
                    testMutation.variables?.channel_id === binding.channel_id
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
                        <SyncHealth binding={binding} />
                      </TableCell>
                      <TableCell className='text-muted-foreground text-xs'>
                        {format.timestamp(binding.updated_time)}
                      </TableCell>
                      <TableCell>
                        <div className='flex min-h-11 items-center gap-2'>
                          <Switch
                            checked={binding.enabled}
                            disabled={
                              !props.canSensitiveWrite ||
                              toggleMutation.isPending
                            }
                            aria-label={costSourceToggleLabel(binding, t)}
                            onCheckedChange={(value) =>
                              toggleMutation.mutate({
                                binding,
                                enabled: value,
                              })
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
                          testDisabled={testMutation.isPending}
                          onOpen={() => openBinding(binding)}
                          onTest={() => testMutation.mutate(binding)}
                          onDelete={() => setPendingDelete(binding)}
                        />
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </div>

          <div className='divide-y rounded-lg border lg:hidden'>
            {items.map((binding) => {
              const credentialCount = costBindingCredentialCount(binding)
              const testing =
                testMutation.isPending &&
                testMutation.variables?.channel_id === binding.channel_id
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
                        {binding.upstream_type === 'newapi'
                          ? 'New API'
                          : 'Sub2API'}
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
                      <dt className='text-muted-foreground'>
                        {t('Credentials')}
                      </dt>
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
                    <SyncHealth binding={binding} />
                  </div>
                  <div className='mt-3 flex min-h-11 items-center justify-between gap-3 border-t pt-3'>
                    <label className='flex min-h-11 cursor-pointer items-center gap-2 text-sm'>
                      <Switch
                        checked={binding.enabled}
                        disabled={
                          !props.canSensitiveWrite || toggleMutation.isPending
                        }
                        aria-label={costSourceToggleLabel(binding, t)}
                        onCheckedChange={(value) =>
                          toggleMutation.mutate({ binding, enabled: value })
                        }
                      />
                      <span>
                        {binding.enabled ? t('Enabled') : t('Disabled')}
                      </span>
                    </label>
                    <CostSourceActions
                      canOperate={props.canOperate}
                      canSensitiveWrite={props.canSensitiveWrite}
                      testing={testing}
                      testDisabled={testMutation.isPending}
                      onOpen={() => openBinding(binding)}
                      onTest={() => testMutation.mutate(binding)}
                      onDelete={() => setPendingDelete(binding)}
                    />
                  </div>
                </article>
              )
            })}
          </div>
        </>
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
