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
import { useEffect, useId, useMemo, useState } from 'react'
import { useForm, useFormContext, useWatch } from 'react-hook-form'
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
import { Checkbox } from '@/components/ui/checkbox'
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
  createCostBindingSchema,
  type CostBindingFormValues,
} from '../lib/cost-binding'
import type {
  RoutingCostBinding,
  RoutingCostBindingActionResult,
  RoutingCostBindingCredentialMasks,
} from '../types'

type CredentialValueName =
  | 'newApiAccessToken'
  | 'gatewayApiKey'
  | 'sub2apiEmail'
  | 'sub2apiPassword'
  | 'sub2apiToken'

type CredentialClearName =
  | 'clearNewApiAccessToken'
  | 'clearGatewayApiKey'
  | 'clearSub2apiEmail'
  | 'clearSub2apiPassword'
  | 'clearSub2apiToken'

const credentialRows: Array<{
  key: keyof RoutingCostBindingCredentialMasks
  label: string
}> = [
  { key: 'new_api_access_token', label: 'New API Access Token' },
  { key: 'gateway_api_key', label: 'Gateway API Key' },
  { key: 'sub2api_email', label: 'Sub2API Email' },
  { key: 'sub2api_password', label: 'Sub2API Password' },
  { key: 'sub2api_token', label: 'Sub2API Token' },
  { key: 'custom_ca_configured', label: 'Custom CA' },
]

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

export function CostSourceCredentialSummary(props: {
  masks: RoutingCostBindingCredentialMasks
  error?: string
}) {
  const { t } = useTranslation()
  const saved = credentialRows.filter((row) => props.masks[row.key])

  return (
    <div className='space-y-3'>
      {saved.length === 0 ? (
        <p className='text-muted-foreground text-sm'>
          {t('No credentials are saved for this cost source.')}
        </p>
      ) : (
        <dl className='divide-y rounded-md border'>
          {saved.map((row) => (
            <div
              key={row.key}
              className='grid min-w-0 gap-1 px-3 py-2.5 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] sm:items-center'
            >
              <dt className='text-muted-foreground text-xs'>{t(row.label)}</dt>
              <dd className='min-w-0 font-mono text-xs break-all sm:text-end'>
                {typeof props.masks[row.key] === 'boolean'
                  ? t('Configured')
                  : props.masks[row.key]}
              </dd>
            </div>
          ))}
        </dl>
      )}
      {props.error ? (
        <Alert variant='destructive' role='alert'>
          <TriangleAlert aria-hidden='true' />
          <AlertTitle>{t('Stored credentials could not be read')}</AlertTitle>
          <AlertDescription className='break-words'>
            {props.error}
          </AlertDescription>
        </Alert>
      ) : null}
    </div>
  )
}

function CustomCACredentialField(props: { configured: boolean }) {
  const { t } = useTranslation()
  const form = useFormContext<CostBindingFormValues>()
  const clear = Boolean(
    useWatch({ control: form.control, name: 'clearCustomCaPem' })
  )

  return (
    <FormField
      control={form.control}
      name='customCaPem'
      render={({ field }) => (
        <FormItem>
          <FormLabel>{t('Custom CA')}</FormLabel>
          <FormControl>
            <Textarea
              {...field}
              value={field.value}
              rows={6}
              disabled={clear}
              spellCheck={false}
              autoComplete='off'
              className='min-h-32 resize-y font-mono text-xs'
              placeholder={
                props.configured
                  ? t('Leave blank to keep the saved value')
                  : t('Paste a PEM-encoded CA certificate')
              }
            />
          </FormControl>
          <FormDescription>
            {props.configured
              ? t('A custom CA certificate is saved.')
              : t(
                  'System trust remains active when no custom CA is configured.'
                )}
          </FormDescription>
          {props.configured ? (
            <FormField
              control={form.control}
              name='clearCustomCaPem'
              render={({ field: clearField }) => (
                <label className='text-foreground flex min-h-11 cursor-pointer items-center gap-2 text-sm'>
                  <Checkbox
                    checked={clearField.value}
                    onCheckedChange={(checked) =>
                      clearField.onChange(Boolean(checked))
                    }
                  />
                  <span>{t('Clear the saved custom CA')}</span>
                </label>
              )}
            />
          ) : null}
          <FormMessage />
        </FormItem>
      )}
    />
  )
}

function CredentialField(props: {
  valueName: CredentialValueName
  clearName: CredentialClearName
  label: string
  mask?: string
  type?: 'text' | 'password' | 'email'
  autoComplete?: string
}) {
  const { t } = useTranslation()
  const form = useFormContext<CostBindingFormValues>()
  const clear = Boolean(
    useWatch({ control: form.control, name: props.clearName })
  )

  return (
    <FormField
      control={form.control}
      name={props.valueName}
      render={({ field }) => (
        <FormItem>
          <FormLabel>{t(props.label)}</FormLabel>
          <FormControl>
            <Input
              {...field}
              value={field.value}
              type={props.type ?? 'password'}
              autoComplete={props.autoComplete ?? 'new-password'}
              disabled={clear}
              placeholder={
                props.mask
                  ? t('Leave blank to keep the saved value')
                  : t('Optional')
              }
            />
          </FormControl>
          {props.mask ? (
            <>
              <FormDescription>
                <span className='block font-mono text-xs break-all'>
                  {t('Current saved value: {{mask}}', { mask: props.mask })}
                </span>
              </FormDescription>
              <FormField
                control={form.control}
                name={props.clearName}
                render={({ field: clearField }) => (
                  <label className='text-foreground flex min-h-11 cursor-pointer items-center gap-2 text-sm'>
                    <Checkbox
                      checked={clearField.value}
                      onCheckedChange={(checked) =>
                        clearField.onChange(Boolean(checked))
                      }
                    />
                    <span>{t('Clear the saved credential')}</span>
                  </label>
                )}
              />
            </>
          ) : (
            <FormDescription>
              {t('Credentials are encrypted and never shown again.')}
            </FormDescription>
          )}
          <FormMessage />
        </FormItem>
      )}
    />
  )
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
    if (!props.open) return
    setWorkingBinding(props.binding)
    setConflict(null)
    setGroups([])
    setActionResult(null)
    setActionError('')
    form.reset(costBindingFormValues(props.binding))
  }, [form, props.binding, props.open])

  const saveMutation = useMutation({
    mutationFn: async (values: CostBindingFormValues) => {
      if (!props.canSensitiveWrite) {
        throw new Error('Cost source write permission is unavailable')
      }
      const request = costBindingRequest(values)
      return workingBinding
        ? updateChannelRoutingCostBinding(workingBinding, request)
        : createChannelRoutingCostBinding(request)
    },
    onSuccess: async (binding) => {
      await invalidateCostBindingSurfaces(queryClient)
      toast.success(
        workingBinding ? t('Cost source updated') : t('Cost source created')
      )
      props.onSaved(binding)
      props.onOpenChange(false)
    },
    onError: (error) => {
      if (error instanceof ChannelRoutingCostBindingConflictError) {
        setConflict(error)
        form.setError('root.server', {
          message: t('This cost source changed before your save completed.'),
        })
        return
      }
      const failure = getChannelRoutingCostBindingApiError(error)
      const message = saveErrorMessage(error, t)
      if (
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
    }) => testChannelRoutingCostBinding(payload.channelId, payload.request),
    onSuccess: (result) => {
      setActionError('')
      setActionResult({ kind: 'test', result })
      toast.success(
        t('Connection test found {{count}} priced models', {
          count: result.model_count,
        })
      )
    },
    onError: (error) => setActionError(actionErrorMessage(error, t)),
  })

  const groupsMutation = useMutation({
    mutationFn: (payload: {
      channelId: number | 'new'
      request?: ReturnType<typeof costBindingRequest>
    }) =>
      loadChannelRoutingCostBindingGroups(payload.channelId, payload.request),
    onSuccess: (result) => {
      setActionError('')
      setGroups(result.groups)
      setActionResult({ kind: 'groups', result })
      if (result.groups.length > 0 && !form.getValues('upstreamGroup').trim()) {
        form.setValue('upstreamGroup', result.groups[0] ?? '', {
          shouldDirty: true,
          shouldValidate: true,
        })
      }
      toast.success(
        t('Loaded {{count}} upstream groups', { count: result.groups.length })
      )
    },
    onError: (error) => setActionError(actionErrorMessage(error, t)),
  })
  const actionPending = testMutation.isPending || groupsMutation.isPending
  const actionDisabled =
    !props.canOperate ||
    actionPending ||
    saveMutation.isPending ||
    conflict != null

  const runAction = async (kind: 'test' | 'groups') => {
    if (!props.canOperate) return
    setActionError('')
    let request: ReturnType<typeof costBindingRequest> | undefined
    if (workingBinding == null || props.canSensitiveWrite) {
      const valid = await form.trigger()
      if (!valid) return
      request = costBindingRequest(form.getValues())
    }
    const payload = {
      channelId: workingBinding?.channel_id ?? ('new' as const),
      request,
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
    setWorkingBinding(current)
    form.reset(costBindingFormValues(current))
    setConflict(null)
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

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
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
            onSubmit={form.handleSubmit((values) =>
              saveMutation.mutate(values)
            )}
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
                          onChange={(event) =>
                            field.onChange(event.target.value)
                          }
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
                      <datalist id={groupListId}>
                        {groups.map((group) => (
                          <option key={group} value={group} />
                        ))}
                      </datalist>
                      <FormDescription>
                        {groups.length > 0
                          ? t('{{count}} upstream groups are available.', {
                              count: groups.length,
                            })
                          : t(
                              'Load upstream groups to verify and select a provider group.'
                            )}
                      </FormDescription>
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
                      className={sideDrawerSwitchItemClassName('border-y-0')}
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
                        className={sideDrawerSwitchItemClassName('border-y-0')}
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
                  <CustomCACredentialField
                    configured={Boolean(
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
                  </AlertDescription>
                </Alert>
              ) : null}
            </SideDrawerSection>

            <SideDrawerSection>
              <SideDrawerSectionHeader
                title={t('Credentials')}
                description={
                  readOnly
                    ? t('Only masked credential values are available.')
                    : t(
                        'Leave a field blank to keep its saved value, or explicitly clear it.'
                      )
                }
                icon={<KeyRound className='size-4' aria-hidden='true' />}
              />
              {readOnly ? (
                <CostSourceCredentialSummary
                  masks={workingBinding?.credential_masks ?? {}}
                  error={workingBinding?.credential_error}
                />
              ) : (
                <div className='grid gap-4 sm:grid-cols-2'>
                  {upstreamType === 'newapi' ? (
                    <CredentialField
                      valueName='newApiAccessToken'
                      clearName='clearNewApiAccessToken'
                      label='New API Access Token'
                      mask={
                        workingBinding?.credential_masks.new_api_access_token
                      }
                    />
                  ) : null}
                  <CredentialField
                    valueName='gatewayApiKey'
                    clearName='clearGatewayApiKey'
                    label='Gateway API Key'
                    mask={workingBinding?.credential_masks.gateway_api_key}
                  />
                  {upstreamType === 'sub2api' ? (
                    <>
                      <CredentialField
                        valueName='sub2apiEmail'
                        clearName='clearSub2apiEmail'
                        label='Sub2API Email'
                        mask={workingBinding?.credential_masks.sub2api_email}
                        type='email'
                        autoComplete='off'
                      />
                      <CredentialField
                        valueName='sub2apiPassword'
                        clearName='clearSub2apiPassword'
                        label='Sub2API Password'
                        mask={workingBinding?.credential_masks.sub2api_password}
                      />
                      <CredentialField
                        valueName='sub2apiToken'
                        clearName='clearSub2apiToken'
                        label='Sub2API Token'
                        mask={workingBinding?.credential_masks.sub2api_token}
                      />
                    </>
                  ) : null}
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
            onClick={() => props.onOpenChange(false)}
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
