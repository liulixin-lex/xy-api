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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Cable,
  CheckCircle2,
  KeyRound,
  RefreshCw,
  Save,
  ShieldAlert,
  ShieldCheck,
  TriangleAlert,
} from 'lucide-react'
import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
  SideDrawerSection,
  SideDrawerSectionHeader,
  sideDrawerSwitchItemClassName,
} from '@/components/drawer-layout'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import {
  ChannelRoutingCostBindingConflictError,
  createChannelRoutingCostBinding,
  getChannelRoutingCostBindingApiError,
  listChannelRoutingChannels,
  loadChannelRoutingCostBindingGroups,
  testChannelRoutingCostBinding,
  updateChannelRoutingCostBinding,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import {
  costBindingFormValues,
  costBindingRequest,
  boundedCostBindingGroups,
  createCostBindingSchema,
  type CostBindingFormValues,
} from '../lib/cost-binding'
import {
  costBindingServerFieldError,
  CostBindingEditorSessionManager,
  mergeCostBindingConflictDraft,
  type CostBindingEditorSession,
} from '../lib/cost-binding-editor'
import type {
  RoutingCostBinding,
  RoutingCostBindingActionResult,
} from '../types'
import {
  CostSourceCredentialFields,
  CostSourceCredentialRecoveryAlert,
  CostSourceCredentialSummary,
  CostSourceCustomCAField,
} from './cost-source-credentials'
import { CostSourceGroupDatalist } from './cost-source-groups'

const credentialClearFields = [
  'clearNewApiAccessToken',
  'clearGatewayApiKey',
  'clearSub2apiEmail',
  'clearSub2apiPassword',
  'clearSub2apiToken',
  'clearCustomCaPem',
] as const

async function invalidateCostBindingSurfaces(
  queryClient: ReturnType<typeof useQueryClient>
) {
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

function saveErrorMessage(error: unknown, t: (key: string) => string): string {
  const failure = getChannelRoutingCostBindingApiError(error)
  switch (failure.code) {
    case 'channel_not_found':
      return t('The selected channel no longer exists.')
    case 'cost_binding_exists':
      return t('This channel already has a cost source.')
    case 'invalid_cost_binding':
      return t('Review the highlighted fields and try again.')
    case 'insufficient_privilege':
      return t('You do not have permission to change cost sources.')
    default:
      return t('Could not save the cost source. Try again.')
  }
}

function actionErrorMessage(
  error: unknown,
  t: (key: string) => string
): string {
  const failure = getChannelRoutingCostBindingApiError(error)
  if (failure.status === 403) {
    return t('Operate permission is required for this action.')
  }
  if (failure.status === 502) {
    return failure.detail || t('The upstream endpoint or credentials failed.')
  }
  return t('Could not contact the upstream cost source. Try again.')
}

function serverFieldError(error: unknown, t: (key: string) => string) {
  return costBindingServerFieldError(
    getChannelRoutingCostBindingApiError(error),
    t
  )
}

export function ChannelRoutingCostSourceSheet(props: {
  open: boolean
  binding: RoutingCostBinding | null
  canOperate: boolean
  canSensitiveWrite: boolean
  notice?: string
  onOpenChange: (open: boolean) => void
  onSaved: (binding: RoutingCostBinding) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const channelListId = useId()
  const groupListId = useId()
  const schema = useMemo(() => createCostBindingSchema(t), [t])
  const [workingBinding, setWorkingBinding] =
    useState<RoutingCostBinding | null>(props.binding)
  const [conflict, setConflict] =
    useState<ChannelRoutingCostBindingConflictError | null>(null)
  const [groups, setGroups] = useState<string[]>([])
  const [actionResult, setActionResult] = useState<{
    kind: 'test' | 'groups'
    result: RoutingCostBindingActionResult
  } | null>(null)
  const [actionError, setActionError] = useState('')
  const [conflictMerge, setConflictMerge] = useState<{
    serverChangedLabels: string[]
    overlappingLabels: string[]
  } | null>(null)
  const baselineBindingRef = useRef<RoutingCostBinding | null>(props.binding)
  const sessionManagerRef = useRef<CostBindingEditorSessionManager | null>(null)
  if (sessionManagerRef.current == null) {
    sessionManagerRef.current = new CostBindingEditorSessionManager()
  }
  const sessionManager = sessionManagerRef.current
  let editorSubject = 'binding:closed'
  if (props.open) {
    editorSubject = props.binding
      ? `binding:${props.binding.channel_id}:${props.binding.etag}`
      : 'binding:new'
  }
  const editorSubjectRef = useRef(editorSubject)
  editorSubjectRef.current = editorSubject
  const form = useForm<CostBindingFormValues>({
    resolver: zodResolver(schema),
    defaultValues: costBindingFormValues(props.binding),
  })
  const upstreamType = useWatch({
    control: form.control,
    name: 'upstreamType',
  })
  const channelId = useWatch({ control: form.control, name: 'channelId' })
  const readOnly = workingBinding != null && !props.canSensitiveWrite
  const providerChanged =
    workingBinding != null && upstreamType !== workingBinding.upstream_type

  const channelsQuery = useQuery({
    queryKey: [...channelRoutingQueryKeys.channelsRoot(), 'cost-source-picker'],
    queryFn: () =>
      listChannelRoutingChannels({ page: 1, page_size: 100, search: '' }),
    enabled: props.open && workingBinding == null && props.canSensitiveWrite,
    staleTime: 30_000,
    meta: { handleErrorLocally: true },
  })
  const selectedChannel = channelsQuery.data?.items.find(
    (channel) => String(channel.id) === channelId
  )

  useEffect(() => {
    if (!props.open) {
      sessionManager.deactivate()
      return
    }
    sessionManager.activate(editorSubject)
    baselineBindingRef.current = props.binding
    setWorkingBinding(props.binding)
    setConflict(null)
    setConflictMerge(null)
    setGroups([])
    setActionResult(null)
    setActionError('')
    form.reset(costBindingFormValues(props.binding))
    return () => sessionManager.deactivate()
  }, [editorSubject, form, props.binding, props.open, sessionManager])

  const isCurrentSession = (session: CostBindingEditorSession) =>
    sessionManager.isCurrent(session, editorSubjectRef.current)

  const handleOpenChange = (open: boolean) => {
    if (!open) sessionManager.deactivate()
    props.onOpenChange(open)
  }

  const saveMutation = useMutation({
    mutationFn: async (payload: {
      values: CostBindingFormValues
      binding: RoutingCostBinding | null
      session: CostBindingEditorSession
    }) => {
      if (!props.canSensitiveWrite) {
        throw new Error('Cost source write permission is unavailable')
      }
      const request = costBindingRequest(payload.values)
      return payload.binding
        ? updateChannelRoutingCostBinding(
            payload.binding,
            request,
            payload.session.signal
          )
        : createChannelRoutingCostBinding(request, payload.session.signal)
    },
    onSuccess: async (binding, payload) => {
      await invalidateCostBindingSurfaces(queryClient)
      if (!isCurrentSession(payload.session)) return
      toast.success(
        payload.binding ? t('Cost source updated') : t('Cost source created')
      )
      sessionManager.deactivate()
      props.onSaved(binding)
      props.onOpenChange(false)
    },
    onError: (error, payload) => {
      if (!isCurrentSession(payload.session)) return
      if (error instanceof ChannelRoutingCostBindingConflictError) {
        setConflict(error)
        form.setError('root.server', {
          message: t('This cost source changed before your save completed.'),
        })
        return
      }
      const failure = getChannelRoutingCostBindingApiError(error)
      const fieldError = serverFieldError(error, t)
      const message = saveErrorMessage(error, t)
      if (fieldError) {
        form.setError(fieldError.name, { message: fieldError.message })
      } else if (
        failure.code === 'channel_not_found' ||
        failure.code === 'cost_binding_exists'
      ) {
        form.setError('channelId', { message })
      } else {
        form.setError('root.server', { message })
      }
      toast.error(message)
    },
  })

  const testMutation = useMutation({
    mutationFn: (payload: {
      channelId: number | 'new'
      request?: ReturnType<typeof costBindingRequest>
      session: CostBindingEditorSession
    }) =>
      testChannelRoutingCostBinding(
        payload.channelId,
        payload.request,
        payload.session.signal
      ),
    onSuccess: (result, payload) => {
      if (!isCurrentSession(payload.session)) return
      const boundedResult = boundedCostBindingGroups(result)
      setActionError('')
      setActionResult({ kind: 'test', result: boundedResult })
      toast.success(
        t('Connection test found {{count}} priced models', {
          count: result.model_count,
        })
      )
    },
    onError: (error, payload) => {
      if (!isCurrentSession(payload.session)) return
      const fieldError = serverFieldError(error, t)
      if (fieldError) {
        form.setError(fieldError.name, { message: fieldError.message })
      }
      setActionError(actionErrorMessage(error, t))
    },
  })

  const groupsMutation = useMutation({
    mutationFn: (payload: {
      channelId: number | 'new'
      request?: ReturnType<typeof costBindingRequest>
      session: CostBindingEditorSession
    }) =>
      loadChannelRoutingCostBindingGroups(
        payload.channelId,
        payload.request,
        payload.session.signal
      ),
    onSuccess: (result, payload) => {
      if (!isCurrentSession(payload.session)) return
      const boundedResult = boundedCostBindingGroups(result)
      setActionError('')
      setGroups(boundedResult.groups)
      setActionResult({ kind: 'groups', result: boundedResult })
      if (
        boundedResult.groups.length > 0 &&
        !form.getValues('upstreamGroup').trim()
      ) {
        form.setValue('upstreamGroup', boundedResult.groups[0] ?? '', {
          shouldDirty: true,
          shouldValidate: true,
        })
      }
      toast.success(
        t('Loaded {{count}} upstream groups', {
          count: boundedResult.groups.length,
        })
      )
    },
    onError: (error, payload) => {
      if (!isCurrentSession(payload.session)) return
      const fieldError = serverFieldError(error, t)
      if (fieldError) {
        form.setError(fieldError.name, { message: fieldError.message })
      }
      setActionError(actionErrorMessage(error, t))
    },
  })
  const actionPending = testMutation.isPending || groupsMutation.isPending
  const actionDisabled =
    !props.canOperate ||
    actionPending ||
    saveMutation.isPending ||
    conflict != null

  const runAction = async (kind: 'test' | 'groups') => {
    if (!props.canOperate) return
    const session = sessionManager.activate(editorSubjectRef.current)
    setActionError('')
    let request: ReturnType<typeof costBindingRequest> | undefined
    if (workingBinding == null || props.canSensitiveWrite) {
      const valid = await form.trigger()
      if (!valid || !isCurrentSession(session)) return
      request = costBindingRequest(form.getValues())
    }
    const payload = {
      channelId: workingBinding?.channel_id ?? ('new' as const),
      request,
      session,
    }
    if (kind === 'test') {
      testMutation.mutate(payload)
    } else {
      groupsMutation.mutate(payload)
    }
  }

  const loadConflictCurrent = () => {
    if (!conflict?.current) return
    const current = {
      ...conflict.current,
      etag: conflict.currentETag || conflict.current.etag,
    }
    const baseline = baselineBindingRef.current ?? workingBinding
    if (!baseline) return
    const merged = mergeCostBindingConflictDraft({
      baseline,
      latest: current,
      draft: form.getValues(),
      dirtyFields: form.formState.dirtyFields,
    })
    sessionManager.rotate(editorSubjectRef.current)
    baselineBindingRef.current = current
    setWorkingBinding(current)
    form.reset(merged.values, { keepDirty: true, keepTouched: true })
    setConflict(null)
    setConflictMerge({
      serverChangedLabels: merged.serverChangedLabels,
      overlappingLabels: merged.overlappingLabels,
    })
    form.clearErrors('root.server')
    setGroups([])
    setActionResult(null)
    setActionError('')
  }

  let title = t('Create cost source')
  let description = t(
    'Connect a local channel to an upstream source of routing cost data.'
  )
  if (workingBinding && readOnly) {
    title = t('Cost source details')
    description = t(
      'Review the connector configuration and test its saved credentials.'
    )
  } else if (workingBinding) {
    title = t('Edit cost source')
    description = t(
      'Blank credential fields keep their saved values. Clear them explicitly when needed.'
    )
  }
  let channelDescription = t(
    'Select a suggestion or enter an exact channel ID.'
  )
  if (workingBinding) {
    channelDescription =
      workingBinding.channel_name ||
      t('Channel cannot be changed after creation.')
  } else if (selectedChannel) {
    channelDescription = selectedChannel.name
  } else if (channelsQuery.isError) {
    channelDescription = t(
      'Channel suggestions could not be loaded. You can still enter an ID.'
    )
  }
  let groupDescription = t(
    'Load upstream groups to verify and select a provider group.'
  )
  if (groups.length > 0) {
    groupDescription = t('{{count}} upstream groups are available.', {
      count: groups.length,
    })
  }
  if (actionResult?.kind === 'groups' && actionResult.result.groups_truncated) {
    const total = actionResult.result.groups_total ?? groups.length
    groupDescription =
      total > groups.length
        ? t(
            'Showing {{shown}} of {{total}} upstream groups. Enter an exact group if it is not listed.',
            { shown: groups.length, total }
          )
        : t(
            'Showing the first {{count}} upstream groups. Enter an exact group if it is not listed.',
            { count: groups.length }
          )
  }
  let credentialDescription = t(
    'Leave a field blank to keep its saved value, or explicitly clear it.'
  )
  if (providerChanged) {
    credentialDescription = t(
      'Enter credentials for the new provider. Previous saved values will not be reused.'
    )
  }
  if (readOnly) {
    credentialDescription = t('Only masked credential values are available.')
  }

  return (
    <Sheet open={props.open} onOpenChange={handleOpenChange}>
      <SheetContent
        className={sideDrawerContentClassName(
          'max-w-none max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11 sm:!max-w-2xl'
        )}
      >
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle className='flex min-w-0 items-center gap-2'>
            <Cable className='size-4 shrink-0' aria-hidden='true' />
            <span className='min-w-0 break-words'>{title}</span>
          </SheetTitle>
          <SheetDescription>{description}</SheetDescription>
        </SheetHeader>

        <Form {...form}>
          <form
            id='channel-routing-cost-source-form'
            className={sideDrawerFormClassName()}
            tabIndex={0}
            aria-label={t('Cost source details')}
            onSubmit={form.handleSubmit((values) => {
              const session = sessionManager.activate(editorSubjectRef.current)
              saveMutation.mutate({
                values,
                binding: workingBinding,
                session,
              })
            })}
          >
            {conflict ? (
              <Alert variant='destructive' role='alert'>
                <ShieldAlert aria-hidden='true' />
                <AlertTitle>
                  {conflict.current
                    ? t('Cost source changed elsewhere')
                    : t('Cost source was deleted elsewhere')}
                </AlertTitle>
                <AlertDescription className='space-y-3'>
                  <p>
                    {conflict.current
                      ? t(
                          'Your draft is still available. Load the latest version, review it, then apply your changes again.'
                        )
                      : t(
                          'Close this sheet and create a new cost source if the connector is still needed.'
                        )}
                  </p>
                  {conflict.current ? (
                    <Button
                      type='button'
                      size='sm'
                      variant='outline'
                      onClick={loadConflictCurrent}
                    >
                      <RefreshCw aria-hidden='true' />
                      {t('Load latest version')}
                    </Button>
                  ) : null}
                </AlertDescription>
              </Alert>
            ) : null}

            {props.notice && !conflict ? (
              <Alert role='status'>
                <ShieldAlert aria-hidden='true' />
                <AlertTitle>{t('Latest version loaded')}</AlertTitle>
                <AlertDescription>{props.notice}</AlertDescription>
              </Alert>
            ) : null}

            {conflictMerge && !conflict ? (
              <Alert role='status' aria-live='polite'>
                <ShieldCheck aria-hidden='true' />
                <AlertTitle>
                  {t('Draft preserved on latest version')}
                </AlertTitle>
                <AlertDescription className='space-y-3'>
                  <p>
                    {t(
                      'Your unsaved changes and newly entered credentials were kept.'
                    )}
                  </p>
                  {conflictMerge.serverChangedLabels.length > 0 ? (
                    <div className='space-y-1.5'>
                      <p className='text-xs font-medium'>
                        {t('Changed elsewhere')}
                      </p>
                      <div className='flex flex-wrap gap-1.5'>
                        {conflictMerge.serverChangedLabels.map((label) => (
                          <Badge key={label} variant='outline'>
                            {t(label)}
                          </Badge>
                        ))}
                      </div>
                    </div>
                  ) : null}
                  {conflictMerge.overlappingLabels.length > 0 ? (
                    <div className='space-y-1.5'>
                      <p className='text-xs font-medium'>
                        {t('Changed here and elsewhere')}
                      </p>
                      <div className='flex flex-wrap gap-1.5'>
                        {conflictMerge.overlappingLabels.map((label) => (
                          <Badge key={label} variant='secondary'>
                            {t(label)}
                          </Badge>
                        ))}
                      </div>
                      <p className='text-xs'>
                        {t('Review these fields before saving again.')}
                      </p>
                    </div>
                  ) : null}
                </AlertDescription>
              </Alert>
            ) : null}

            {form.formState.errors.root?.server?.message && !conflict ? (
              <Alert variant='destructive' role='alert'>
                <TriangleAlert aria-hidden='true' />
                <AlertTitle>{t('Cost source was not saved')}</AlertTitle>
                <AlertDescription>
                  {form.formState.errors.root.server.message}
                </AlertDescription>
              </Alert>
            ) : null}

            <SideDrawerSection>
              <SideDrawerSectionHeader
                title={t('Connector')}
                description={t(
                  'Choose the channel and upstream pricing endpoint used for cost sync.'
                )}
                icon={<Cable className='size-4' aria-hidden='true' />}
              />
              <div className='grid gap-4 sm:grid-cols-2'>
                <FormField
                  control={form.control}
                  name='channelId'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Channel')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          value={field.value}
                          inputMode='numeric'
                          pattern='[0-9]*'
                          list={workingBinding ? undefined : channelListId}
                          disabled={workingBinding != null || readOnly}
                          placeholder={t('Enter a channel ID')}
                        />
                      </FormControl>
                      <datalist id={channelListId}>
                        {channelsQuery.data?.items.map((channel) => (
                          <option key={channel.id} value={channel.id}>
                            {channel.name}
                          </option>
                        ))}
                      </datalist>
                      <FormDescription>{channelDescription}</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='upstreamType'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Upstream type')}</FormLabel>
                      <FormControl>
                        <NativeSelect
                          value={field.value}
                          disabled={readOnly}
                          onChange={(event) => {
                            const nextProvider = event.target.value as
                              | 'newapi'
                              | 'sub2api'
                            if (nextProvider !== field.value) {
                              for (const clearField of credentialClearFields) {
                                form.setValue(clearField, false, {
                                  shouldDirty: true,
                                })
                              }
                            }
                            field.onChange(nextProvider)
                          }}
                        >
                          <NativeSelectOption value='newapi'>
                            New API
                          </NativeSelectOption>
                          <NativeSelectOption value='sub2api'>
                            Sub2API
                          </NativeSelectOption>
                        </NativeSelect>
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                {providerChanged ? (
                  <Alert role='status' className='sm:col-span-2'>
                    <ShieldAlert aria-hidden='true' />
                    <AlertTitle>
                      {t('Credentials must be reconfigured')}
                    </AlertTitle>
                    <AlertDescription>
                      {t(
                        'Changing the provider clears all saved credentials and the custom CA. Re-enter credentials and trust settings for the new provider.'
                      )}
                    </AlertDescription>
                  </Alert>
                ) : null}
                <FormField
                  control={form.control}
                  name='baseUrl'
                  render={({ field }) => (
                    <FormItem className='sm:col-span-2'>
                      <FormLabel>{t('Base URL')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          value={field.value}
                          type='url'
                          inputMode='url'
                          autoComplete='url'
                          disabled={readOnly}
                          placeholder='https://api.example.com'
                        />
                      </FormControl>
                      <FormDescription>
                        {t(
                          'HTTPS is required. Do not place tokens or passwords in the URL.'
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='upstreamGroup'
                  render={({ field }) => (
                    <FormItem className='sm:col-span-2'>
                      <FormLabel>{t('Upstream group')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          value={field.value}
                          list={groupListId}
                          disabled={readOnly}
                          autoComplete='off'
                        />
                      </FormControl>
                      <CostSourceGroupDatalist
                        id={groupListId}
                        groups={groups}
                      />
                      <FormDescription>{groupDescription}</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                {upstreamType === 'newapi' ? (
                  <FormField
                    control={form.control}
                    name='newApiUserId'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('New API user ID')}</FormLabel>
                        <FormControl>
                          <Input
                            {...field}
                            value={field.value}
                            inputMode='numeric'
                            pattern='[0-9]*'
                            disabled={readOnly}
                            placeholder={t('Optional')}
                          />
                        </FormControl>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                ) : null}
              </div>

              <div className='divide-y border-y'>
                <FormField
                  control={form.control}
                  name='enabled'
                  render={({ field }) => (
                    <div
                      className={sideDrawerSwitchItemClassName(
                        'border-y-0 pr-3'
                      )}
                    >
                      <div className='min-w-0'>
                        <div className='text-sm font-medium'>
                          {t('Cost sync enabled')}
                        </div>
                        <p className='text-muted-foreground mt-1 text-xs'>
                          {t(
                            'Allow this connector to refresh routing cost snapshots.'
                          )}
                        </p>
                      </div>
                      <Switch
                        checked={field.value}
                        disabled={readOnly}
                        aria-label={t('Cost sync enabled')}
                        onCheckedChange={field.onChange}
                      />
                    </div>
                  )}
                />
                {upstreamType === 'sub2api' ? (
                  <FormField
                    control={form.control}
                    name='servesClaudeCode'
                    render={({ field }) => (
                      <div
                        className={sideDrawerSwitchItemClassName(
                          'border-y-0 pr-3'
                        )}
                      >
                        <div className='min-w-0'>
                          <div className='text-sm font-medium'>
                            {t('Serves Claude Code')}
                          </div>
                          <p className='text-muted-foreground mt-1 text-xs'>
                            {t(
                              'Mark this Sub2API account as eligible for Claude Code routing.'
                            )}
                          </p>
                        </div>
                        <Switch
                          checked={field.value}
                          disabled={readOnly}
                          aria-label={t('Serves Claude Code')}
                          onCheckedChange={field.onChange}
                        />
                      </div>
                    )}
                  />
                ) : null}
              </div>
            </SideDrawerSection>

            <SideDrawerSection>
              <SideDrawerSectionHeader
                title={t('Network trust')}
                description={t(
                  'Private access is denied unless an explicit CIDR exception is saved.'
                )}
                icon={<ShieldCheck className='size-4' aria-hidden='true' />}
              />
              {readOnly ? (
                <div className='space-y-3'>
                  <div>
                    <div className='text-sm font-medium'>
                      {t('Private egress CIDRs')}
                    </div>
                    {(workingBinding?.egress_allowed_private_cidrs ?? [])
                      .length > 0 ? (
                      <div className='mt-2 flex flex-wrap gap-1.5'>
                        {(
                          workingBinding?.egress_allowed_private_cidrs ?? []
                        ).map((cidr) => (
                          <Badge
                            key={cidr}
                            variant='outline'
                            className='max-w-full font-mono text-xs'
                          >
                            <span className='break-all'>{cidr}</span>
                          </Badge>
                        ))}
                      </div>
                    ) : (
                      <p className='text-muted-foreground mt-1 text-sm'>
                        {t('No private network exceptions')}
                      </p>
                    )}
                  </div>
                  {workingBinding?.egress_policy_error ? (
                    <Alert variant='destructive' role='alert'>
                      <TriangleAlert aria-hidden='true' />
                      <AlertTitle>
                        {t('Network trust policy is invalid')}
                      </AlertTitle>
                      <AlertDescription className='break-words'>
                        {workingBinding.egress_policy_error}
                      </AlertDescription>
                    </Alert>
                  ) : null}
                </div>
              ) : (
                <div className='space-y-4'>
                  <FormField
                    control={form.control}
                    name='egressAllowedPrivateCidrs'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Private egress CIDRs')}</FormLabel>
                        <FormControl>
                          <Textarea
                            {...field}
                            value={field.value}
                            rows={4}
                            spellCheck={false}
                            className='min-h-24 resize-y font-mono text-xs'
                            placeholder='10.20.30.0/24'
                          />
                        </FormControl>
                        <FormDescription>
                          {t(
                            'One private RFC 1918 or ULA CIDR range per line, up to 32 ranges.'
                          )}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                  <CostSourceCustomCAField
                    configured={Boolean(
                      !providerChanged &&
                      workingBinding?.credential_masks.custom_ca_configured
                    )}
                  />
                </div>
              )}
            </SideDrawerSection>

            <SideDrawerSection>
              <SideDrawerSectionHeader
                title={t('Connection check')}
                description={t(
                  'Use the current draft when permitted, or the saved connector in read-only mode.'
                )}
                icon={<RefreshCw className='size-4' aria-hidden='true' />}
              />
              <div className='flex flex-wrap gap-2'>
                <Button
                  type='button'
                  variant='outline'
                  disabled={actionDisabled}
                  onClick={() => void runAction('test')}
                >
                  <CheckCircle2 aria-hidden='true' />
                  {testMutation.isPending
                    ? t('Testing connection')
                    : t('Test connection')}
                </Button>
                <Button
                  type='button'
                  variant='outline'
                  disabled={actionDisabled}
                  onClick={() => void runAction('groups')}
                >
                  <RefreshCw
                    aria-hidden='true'
                    className={
                      groupsMutation.isPending
                        ? 'animate-spin motion-reduce:animate-none'
                        : undefined
                    }
                  />
                  {groupsMutation.isPending
                    ? t('Loading groups')
                    : t('Load upstream groups')}
                </Button>
              </div>
              {!props.canOperate ? (
                <Alert role='status'>
                  <ShieldAlert aria-hidden='true' />
                  <AlertTitle>{t('Operate permission required')}</AlertTitle>
                  <AlertDescription>
                    {t(
                      'You can review this connector, but connection tests are unavailable for your role.'
                    )}
                  </AlertDescription>
                </Alert>
              ) : null}
              {actionError ? (
                <Alert variant='destructive' role='alert'>
                  <TriangleAlert aria-hidden='true' />
                  <AlertTitle>{t('Upstream check failed')}</AlertTitle>
                  <AlertDescription className='break-words'>
                    {actionError}
                  </AlertDescription>
                </Alert>
              ) : null}
              {actionResult ? (
                <Alert role='status' aria-live='polite'>
                  <CheckCircle2 aria-hidden='true' />
                  <AlertTitle>
                    {actionResult.kind === 'test'
                      ? t('Connection succeeded')
                      : t('Upstream groups loaded')}
                  </AlertTitle>
                  <AlertDescription className='space-y-2'>
                    <p>
                      {t('{{count}} priced models reported', {
                        count: actionResult.result.model_count,
                      })}
                      {actionResult.result.pricing_version
                        ? ` · ${actionResult.result.pricing_version}`
                        : ''}
                    </p>
                    {actionResult.result.groups.length > 0 ? (
                      <div className='flex max-w-full flex-wrap gap-1'>
                        {actionResult.result.groups.slice(0, 8).map((group) => (
                          <Badge
                            key={group}
                            variant='secondary'
                            className='max-w-full min-w-0'
                          >
                            <span className='truncate'>{group}</span>
                          </Badge>
                        ))}
                        {actionResult.result.groups.length > 8 ? (
                          <Badge variant='outline'>
                            +{actionResult.result.groups.length - 8}
                          </Badge>
                        ) : null}
                      </div>
                    ) : null}
                    {actionResult.result.groups_truncated ? (
                      <p>
                        {t(
                          'Group results are limited in this view. Enter an exact group name when needed.'
                        )}
                      </p>
                    ) : null}
                  </AlertDescription>
                </Alert>
              ) : null}
            </SideDrawerSection>

            <SideDrawerSection>
              <SideDrawerSectionHeader
                title={t('Credentials')}
                description={credentialDescription}
                icon={<KeyRound className='size-4' aria-hidden='true' />}
              />
              {readOnly ? (
                <CostSourceCredentialSummary
                  masks={workingBinding?.credential_masks ?? {}}
                  error={workingBinding?.credential_error}
                />
              ) : (
                <div className='space-y-4'>
                  {workingBinding?.credential_error ? (
                    <CostSourceCredentialRecoveryAlert canEdit />
                  ) : null}
                  <CostSourceCredentialFields
                    upstreamType={upstreamType}
                    binding={providerChanged ? null : workingBinding}
                  />
                </div>
              )}
            </SideDrawerSection>
          </form>
        </Form>

        <SheetFooter className={sideDrawerFooterClassName('grid-cols-1')}>
          <Button
            type='button'
            variant='outline'
            disabled={saveMutation.isPending}
            onClick={() => handleOpenChange(false)}
          >
            {readOnly ? t('Close') : t('Cancel')}
          </Button>
          {props.canSensitiveWrite ? (
            <Button
              type='submit'
              form='channel-routing-cost-source-form'
              disabled={
                saveMutation.isPending ||
                actionPending ||
                conflict != null ||
                (props.binding != null && workingBinding == null)
              }
            >
              <Save aria-hidden='true' />
              {saveMutation.isPending ? t('Saving') : t('Save cost source')}
            </Button>
          ) : null}
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
