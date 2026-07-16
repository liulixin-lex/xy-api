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
import {
  Alert02Icon,
  ArrowReloadHorizontalIcon,
  FloppyDiskIcon,
  GitMergeIcon,
  Undo02Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { useForm, useFormState } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
} from '@/components/ui/alert'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogMedia,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Form } from '@/components/ui/form'
import { Spinner } from '@/components/ui/spinner'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import {
  ChannelRoutingRuntimeSettingsConflictError,
  getChannelRoutingRuntimeSettings,
  getChannelRoutingRuntimeSettingsApiError,
  updateChannelRoutingRuntimeSettings,
} from '../../api/client'
import { channelRoutingQueryKeys } from '../../api/query-keys'
import {
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
  ChannelRoutingRefetchErrorAlert,
} from '../../components/page-state'
import { useChannelRoutingFormatters } from '../../lib/format'
import type {
  ChannelRoutingRuntimeSettings as RuntimeSettingsResponse,
  SmartRoutingSetting,
  SmartRoutingSettingField,
} from '../../types'
import { RuntimeSettingsFormUIProvider } from './form-context'
import {
  changedRuntimeSettingFields,
  displayRuntimeSettingValue,
  highRiskRuntimeSettingFields,
  mergeRuntimeSettingsConflict,
  runtimeSettingFields,
  runtimeSettingLabels,
  runtimeSettingsSchema,
  type RuntimeSettingsFormValues,
} from './lib/runtime-settings'
import { RuntimeSettingsNavigationGuard } from './navigation-guard'
import { RuntimeActiveProbeSection } from './sections/active-probe-section'
import { RuntimeAgentSection } from './sections/agent-section'
import { RuntimeBasicsSection } from './sections/basics-section'
import { RuntimeBreakerSection } from './sections/breaker-section'
import { RuntimeFirstByteSection } from './sections/first-byte-section'
import { RuntimeHedgeSection } from './sections/hedge-section'
import { RuntimeRefreshRetentionSection } from './sections/refresh-retention-section'
import { RuntimeRetrySection } from './sections/retry-section'
import { RuntimeScoringSection } from './sections/scoring-section'

function isRuntimeSettingField(
  value: string
): value is SmartRoutingSettingField {
  return runtimeSettingFields.includes(value as SmartRoutingSettingField)
}

function serverReasonMessage(reason?: string): string {
  switch (reason) {
    case 'required':
      return 'This setting is required'
    case 'expected_boolean':
      return 'Enter an enabled or disabled value'
    case 'expected_integer':
      return 'Enter a whole number'
    case 'expected_number':
      return 'Enter a number'
    case 'expected_string':
      return 'Enter text'
    case 'must_be_finite':
    case 'invalid_value':
      return 'Enter a finite value within the supported range'
    default:
      return 'The server rejected this value'
  }
}

export function ChannelRoutingRuntimeSettings() {
  const query = useQuery({
    queryKey: channelRoutingQueryKeys.runtimeSettings(),
    queryFn: getChannelRoutingRuntimeSettings,
  })

  if (query.isLoading) return <ChannelRoutingLoadingState rows={9} />
  if (query.isError && !query.data) {
    return (
      <ChannelRoutingErrorState
        error={query.error}
        onRetry={() => void query.refetch()}
      />
    )
  }
  if (!query.data) return null

  return (
    <div className='space-y-4'>
      {query.isRefetchError ? (
        <ChannelRoutingRefetchErrorAlert
          isFetching={query.isFetching}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      <RuntimeSettingsEditor
        server={query.data}
        savingBlocked={query.isRefetchError}
        isRefreshing={query.isFetching}
        onRefresh={() => void query.refetch()}
      />
    </div>
  )
}

function RuntimeSettingsEditor(props: {
  server: RuntimeSettingsResponse
  savingBlocked: boolean
  isRefreshing: boolean
  onRefresh: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const user = useAuthStore((state) => state.auth.user)
  const canDeploy = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
    ADMIN_PERMISSION_ACTIONS.DEPLOY
  )
  const [authority, setAuthority] = useState(props.server)
  const [conflicts, setConflicts] = useState<Set<SmartRoutingSettingField>>(
    new Set()
  )
  const [pendingSave, setPendingSave] =
    useState<RuntimeSettingsFormValues | null>(null)
  const form = useForm<RuntimeSettingsFormValues>({
    resolver: zodResolver(runtimeSettingsSchema),
    defaultValues: props.server.stored_settings,
    mode: 'onBlur',
  })
  const { dirtyFields, isDirty } = useFormState({ control: form.control })
  const changedFields = runtimeSettingFields.filter(
    (field) => dirtyFields[field] === true
  )
  const overriddenFields = runtimeSettingFields.filter(
    (field) =>
      !Object.is(authority.settings[field], authority.stored_settings[field])
  )
  const newerServerAvailable = props.server.etag !== authority.etag

  const rebaseOnto = (latest: RuntimeSettingsResponse) => {
    const draft = form.getValues() as SmartRoutingSetting
    const result = mergeRuntimeSettingsConflict(
      authority.stored_settings,
      draft,
      latest.stored_settings
    )
    setAuthority(latest)
    setConflicts(new Set(result.conflicts))
    form.reset(latest.stored_settings)
    for (const field of result.draftChanges) {
      form.setValue(field, result.merged[field] as never, {
        shouldDirty: true,
        shouldValidate: false,
      })
    }
    if (result.conflicts.length > 0) {
      toast.warning(
        t(
          'The latest revision overlaps with your draft. Review the highlighted fields.'
        )
      )
    } else {
      toast.info(t('Your draft was rebased onto the latest runtime settings.'))
    }
  }

  useEffect(() => {
    if (props.server.etag === authority.etag || isDirty) return
    // External query truth may advance through SSE while the clean form is mounted.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setAuthority(props.server)
    setConflicts(new Set())
    form.reset(props.server.stored_settings)
  }, [authority.etag, form, isDirty, props.server])

  const mutation = useMutation({
    mutationFn: (values: RuntimeSettingsFormValues) =>
      updateChannelRoutingRuntimeSettings(authority, values),
    onSuccess: async (updated) => {
      setAuthority(updated)
      setConflicts(new Set())
      setPendingSave(null)
      form.reset(updated.stored_settings)
      queryClient.setQueryData(
        channelRoutingQueryKeys.runtimeSettings(),
        updated
      )
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: channelRoutingQueryKeys.overview(),
        }),
        queryClient.invalidateQueries({
          queryKey: channelRoutingQueryKeys.controlAuditsRoot(),
        }),
      ])
      toast.success(t('Runtime settings saved'))
    },
    onError: (error) => {
      setPendingSave(null)
      if (
        error instanceof ChannelRoutingRuntimeSettingsConflictError &&
        error.current
      ) {
        rebaseOnto(error.current)
        queryClient.setQueryData(
          channelRoutingQueryKeys.runtimeSettings(),
          error.current
        )
        return
      }

      const apiError = getChannelRoutingRuntimeSettingsApiError(error)
      let mapped = false
      for (const [field, reason] of Object.entries(
        apiError.fieldErrors ?? {}
      )) {
        if (!isRuntimeSettingField(field)) continue
        form.setError(field, {
          type: 'server',
          message: serverReasonMessage(reason),
        })
        mapped = true
      }
      if (!mapped && apiError.field && isRuntimeSettingField(apiError.field)) {
        form.setError(apiError.field, {
          type: 'server',
          message: serverReasonMessage(apiError.reason),
        })
        mapped = true
      }
      toast.error(
        mapped
          ? t('Review the highlighted runtime settings.')
          : t('Could not save runtime settings. Your draft was preserved.')
      )
    },
  })

  const submit = (values: RuntimeSettingsFormValues) => {
    const changed = changedRuntimeSettingFields(
      authority.stored_settings,
      values
    )
    if (changed.some((field) => highRiskRuntimeSettingFields.has(field))) {
      setPendingSave(values)
      return
    }
    mutation.mutate(values)
  }

  const highRiskChanges = useMemo(() => {
    if (!pendingSave) return []
    return changedRuntimeSettingFields(
      authority.stored_settings,
      pendingSave
    ).filter((field) => highRiskRuntimeSettingFields.has(field))
  }, [authority.stored_settings, pendingSave])

  const uiContext = useMemo(
    () => ({ server: authority, conflicts, readOnly: !canDeploy }),
    [authority, canDeploy, conflicts]
  )

  return (
    <>
      <RuntimeSettingsNavigationGuard when={isDirty && !mutation.isPending} />
      <div className='space-y-5'>
        <div className='flex flex-wrap items-start justify-between gap-3'>
          <div>
            <h2 className='text-base font-semibold'>{t('Runtime settings')}</h2>
            <p className='text-muted-foreground mt-1 max-w-3xl text-sm text-pretty'>
              {t(
                'Stored settings are editable. Effective settings include deployment environment overrides and are shown alongside affected fields.'
              )}
            </p>
          </div>
          <Button
            size='icon-sm'
            variant='outline'
            aria-label={t('Refresh')}
            disabled={props.isRefreshing}
            onClick={props.onRefresh}
          >
            <HugeiconsIcon
              icon={ArrowReloadHorizontalIcon}
              className={
                props.isRefreshing
                  ? 'animate-spin motion-reduce:animate-none'
                  : undefined
              }
              aria-hidden='true'
            />
          </Button>
        </div>

        {!canDeploy ? (
          <Alert>
            <HugeiconsIcon icon={Alert02Icon} aria-hidden='true' />
            <AlertTitle>{t('Deployment permission required')}</AlertTitle>
            <AlertDescription>
              {t(
                'You can inspect stored and effective runtime settings, but your role cannot save a new revision.'
              )}
            </AlertDescription>
          </Alert>
        ) : null}

        {newerServerAvailable && isDirty ? (
          <Alert className='has-data-[slot=alert-action]:pr-2.5 sm:has-data-[slot=alert-action]:pr-28'>
            <HugeiconsIcon icon={GitMergeIcon} aria-hidden='true' />
            <AlertTitle>
              {t('A newer runtime settings revision is available')}
            </AlertTitle>
            <AlertDescription>
              {t(
                'Compare it with your draft now, or save to trigger the same three-way conflict check.'
              )}
            </AlertDescription>
            <AlertAction className='static col-span-full mt-2 justify-self-start sm:absolute sm:col-auto sm:mt-0'>
              <Button
                size='sm'
                variant='outline'
                onClick={() => rebaseOnto(props.server)}
              >
                <HugeiconsIcon
                  icon={GitMergeIcon}
                  data-icon='inline-start'
                  aria-hidden='true'
                />
                {t('Compare now')}
              </Button>
            </AlertAction>
          </Alert>
        ) : null}

        {conflicts.size > 0 ? (
          <Alert className='border-destructive/30 bg-destructive/5'>
            <HugeiconsIcon
              icon={Alert02Icon}
              className='text-destructive'
              aria-hidden='true'
            />
            <AlertTitle>
              {t('{{count}} overlapping changes need review', {
                count: conflicts.size,
              })}
            </AlertTitle>
            <AlertDescription>
              {t(
                'Your draft is preserved. Only fields changed both locally and on the server are marked as conflicts.'
              )}
            </AlertDescription>
          </Alert>
        ) : null}

        <RuntimeSettingsMetadata server={authority} />
        <RuntimeEffectiveSettingsSummary
          server={authority}
          fields={overriddenFields}
        />

        <RuntimeSettingsFormUIProvider value={uiContext}>
          <Form {...form}>
            <form
              className='space-y-5'
              onSubmit={form.handleSubmit(submit)}
              noValidate
            >
              <RuntimeBasicsSection />
              <RuntimeScoringSection />
              <RuntimeBreakerSection />
              <RuntimeRetrySection />
              <RuntimeFirstByteSection />
              <RuntimeHedgeSection />
              <RuntimeActiveProbeSection />
              <RuntimeRefreshRetentionSection />
              <RuntimeAgentSection />

              {isDirty ? (
                <div className='bg-background/95 supports-backdrop-filter:bg-background/85 sticky bottom-3 z-20 flex flex-col gap-3 rounded-xl border p-3 shadow-sm backdrop-blur sm:flex-row sm:items-center sm:justify-between'>
                  <div className='min-w-0'>
                    <div className='text-sm font-medium'>
                      {t('{{count}} unsaved runtime setting changes', {
                        count: changedFields.length,
                      })}
                    </div>
                    <p className='text-muted-foreground truncate text-xs'>
                      {changedFields
                        .slice(0, 4)
                        .map((field) => t(runtimeSettingLabels[field]))
                        .join(' · ')}
                    </p>
                  </div>
                  <div className='flex gap-2 sm:shrink-0'>
                    <Button
                      type='button'
                      variant='outline'
                      className='min-h-11 flex-1 sm:min-h-8 sm:flex-none'
                      disabled={mutation.isPending}
                      onClick={() => {
                        form.reset(authority.stored_settings)
                        setConflicts(new Set())
                      }}
                    >
                      <HugeiconsIcon
                        icon={Undo02Icon}
                        data-icon='inline-start'
                        aria-hidden='true'
                      />
                      {t('Discard draft')}
                    </Button>
                    <Button
                      type='submit'
                      className='min-h-11 flex-1 sm:min-h-8 sm:flex-none'
                      disabled={
                        mutation.isPending || props.savingBlocked || !canDeploy
                      }
                    >
                      {mutation.isPending ? (
                        <Spinner data-icon='inline-start' />
                      ) : (
                        <HugeiconsIcon
                          icon={FloppyDiskIcon}
                          data-icon='inline-start'
                          aria-hidden='true'
                        />
                      )}
                      {mutation.isPending
                        ? t('Saving')
                        : t('Save runtime settings')}
                    </Button>
                  </div>
                </div>
              ) : null}
            </form>
          </Form>
        </RuntimeSettingsFormUIProvider>
      </div>

      <AlertDialog
        open={pendingSave != null}
        onOpenChange={(open) => {
          if (!open && !mutation.isPending) setPendingSave(null)
        }}
      >
        <AlertDialogContent className='channel-routing-touch-surface max-h-[min(88dvh,42rem)] w-[calc(100vw-1.5rem)] overflow-y-auto sm:max-w-lg'>
          <AlertDialogHeader>
            <AlertDialogMedia className='bg-warning/10 text-warning'>
              <HugeiconsIcon icon={Alert02Icon} aria-hidden='true' />
            </AlertDialogMedia>
            <AlertDialogTitle>
              {t('Confirm high-risk runtime changes')}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                'These changes can increase traffic, automated actions, or upstream spend. Review the summary before saving.'
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <ul className='divide-y rounded-lg border text-sm'>
            {highRiskChanges.map((field) => (
              <li key={field} className='space-y-1 px-3 py-2'>
                <div className='font-medium'>
                  {t(runtimeSettingLabels[field])}
                </div>
                <div className='text-muted-foreground break-words'>
                  {displayRuntimeSettingValue(
                    field,
                    authority.stored_settings[field],
                    t
                  )}
                  {' → '}
                  {pendingSave
                    ? displayRuntimeSettingValue(field, pendingSave[field], t)
                    : ''}
                </div>
              </li>
            ))}
          </ul>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={mutation.isPending}>
              {t('Keep editing')}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={mutation.isPending || !pendingSave}
              onClick={() => {
                if (pendingSave) mutation.mutate(pendingSave)
              }}
            >
              {mutation.isPending ? <Spinner data-icon='inline-start' /> : null}
              {t('Confirm and save')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function RuntimeSettingsMetadata(props: { server: RuntimeSettingsResponse }) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  return (
    <section aria-labelledby='runtime-settings-metadata'>
      <h2 id='runtime-settings-metadata' className='sr-only'>
        {t('Runtime settings revision')}
      </h2>
      <dl className='bg-muted/30 grid gap-px overflow-hidden rounded-lg border sm:grid-cols-2 lg:grid-cols-4'>
        <div className='bg-background px-3 py-2.5'>
          <dt className='text-muted-foreground text-xs'>{t('Revision')}</dt>
          <dd className='mt-1 font-mono text-sm'>r{props.server.revision}</dd>
        </div>
        <div className='bg-background min-w-0 px-3 py-2.5'>
          <dt className='text-muted-foreground text-xs'>
            {t('Document hash')}
          </dt>
          <dd
            className='mt-1 truncate font-mono text-sm'
            title={props.server.document_hash}
          >
            {format.shortHash(props.server.document_hash)}
          </dd>
        </div>
        <div className='bg-background px-3 py-2.5'>
          <dt className='text-muted-foreground text-xs'>{t('Updated by')}</dt>
          <dd className='mt-1 text-sm'>
            {props.server.updated_by > 0
              ? t('Administrator #{{id}}', { id: props.server.updated_by })
              : t('System')}
          </dd>
        </div>
        <div className='bg-background px-3 py-2.5'>
          <dt className='text-muted-foreground text-xs'>{t('Updated')}</dt>
          <dd className='mt-1 text-sm'>
            {props.server.updated_time_ms > 0
              ? format.timestamp(props.server.updated_time_ms)
              : t('Not recorded')}
          </dd>
        </div>
      </dl>
    </section>
  )
}

function RuntimeEffectiveSettingsSummary(props: {
  server: RuntimeSettingsResponse
  fields: SmartRoutingSettingField[]
}) {
  const { t } = useTranslation()
  return (
    <section
      className='space-y-3 border-t pt-5'
      aria-labelledby='runtime-effective-settings'
    >
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div>
          <h2
            id='runtime-effective-settings'
            className='text-base font-semibold'
          >
            {t('Effective settings')}
          </h2>
          <p className='text-muted-foreground mt-1 text-sm'>
            {t(
              'Values currently used by this node after deployment environment overrides.'
            )}
          </p>
        </div>
        <Badge variant={props.fields.length > 0 ? 'secondary' : 'outline'}>
          {props.fields.length > 0
            ? t('{{count}} deployment overrides', {
                count: props.fields.length,
              })
            : t('No deployment overrides')}
        </Badge>
      </div>
      {props.fields.length > 0 ? (
        <dl className='divide-y rounded-lg border'>
          {props.fields.map((field) => (
            <div
              key={field}
              className='grid gap-1 px-3 py-2.5 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)] sm:gap-3'
            >
              <dt className='font-medium'>{t(runtimeSettingLabels[field])}</dt>
              <dd className='text-muted-foreground text-xs sm:text-sm'>
                {t('Stored: {{value}}', {
                  value: displayRuntimeSettingValue(
                    field,
                    props.server.stored_settings[field],
                    t
                  ),
                })}
              </dd>
              <dd className='text-xs font-medium sm:text-sm'>
                {t('Effective: {{value}}', {
                  value: displayRuntimeSettingValue(
                    field,
                    props.server.settings[field],
                    t
                  ),
                })}
              </dd>
            </div>
          ))}
        </dl>
      ) : (
        <p className='text-muted-foreground rounded-lg border px-3 py-3 text-sm'>
          {t(
            'Stored and effective runtime settings are identical on this node.'
          )}
        </p>
      )}
    </section>
  )
}
