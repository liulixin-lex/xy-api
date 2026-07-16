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
  Cancel01Icon,
  FloppyDiskIcon,
  InformationCircleIcon,
  MultiplicationSignIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { useMemo, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { FieldGroup, FieldLegend, FieldSet } from '@/components/ui/field'
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
import { FormNavigationGuard } from '@/features/system-settings/components/form-navigation-guard'

import {
  ChannelRoutingConfigurationConflictError,
  getChannelRoutingConfigurationApiError,
  updateChannelRoutingConfiguration,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import {
  channelConfigurationConflictSummary,
  channelConfigurationFormValues,
  channelConfigurationRequest,
  createChannelConfigurationSchema,
  FAILURE_DOMAIN_LABEL_MAXIMUM,
  type ChannelConfigurationFormValues,
} from '../lib/channel-configuration'
import { useChannelRoutingFormatters } from '../lib/format'
import type { RoutingChannelConfiguration } from '../types'

type PendingRiskConfirmation = {
  values: ChannelConfigurationFormValues
  confirmsZeroCost: boolean
  clearsFailureDomain: boolean
}

function configurationFieldLabel(label: string, t: TFunction): string {
  switch (label) {
    case 'Channel multiplier':
      return t('Channel multiplier')
    case 'Traffic class':
      return t('Traffic class')
    case 'Failure domain':
      return t('Failure domain')
    default:
      return label
  }
}

export function ChannelConfigurationSheet(props: {
  open: boolean
  configuration: RoutingChannelConfiguration
  onOpenChange: (open: boolean) => void
  onSaved: (configuration: RoutingChannelConfiguration) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const queryClient = useQueryClient()
  const schema = useMemo(() => createChannelConfigurationSchema(t), [t])
  const [workingConfiguration, setWorkingConfiguration] = useState(
    props.configuration
  )
  const [conflict, setConflict] =
    useState<ChannelRoutingConfigurationConflictError | null>(null)
  const [conflictMerge, setConflictMerge] = useState<{
    serverChangedLabels: string[]
    overlappingLabels: string[]
  } | null>(null)
  const [discardOpen, setDiscardOpen] = useState(false)
  const [pendingRisk, setPendingRisk] =
    useState<PendingRiskConfirmation | null>(null)
  const form = useForm<ChannelConfigurationFormValues>({
    resolver: zodResolver(schema),
    defaultValues: channelConfigurationFormValues(props.configuration),
  })
  const clearFailureDomain = useWatch({
    control: form.control,
    name: 'clearFailureDomain',
  })

  const saveMutation = useMutation({
    mutationFn: (values: ChannelConfigurationFormValues) =>
      updateChannelRoutingConfiguration(
        workingConfiguration,
        channelConfigurationRequest(values)
      ),
    onSuccess: async (updated) => {
      form.reset(channelConfigurationFormValues(updated))
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: channelRoutingQueryKeys.channelConfigurationsRoot(),
        }),
        queryClient.invalidateQueries({
          queryKey: channelRoutingQueryKeys.channelsRoot(),
        }),
        queryClient.invalidateQueries({
          queryKey: channelRoutingQueryKeys.costsRoot(),
        }),
        queryClient.invalidateQueries({
          queryKey: channelRoutingQueryKeys.decisionsRoot(),
        }),
        queryClient.invalidateQueries({
          queryKey: channelRoutingQueryKeys.overview(),
        }),
      ])
      toast.success(t('Channel configuration updated'))
      props.onSaved(updated)
      props.onOpenChange(false)
    },
    onError: (error) => {
      if (error instanceof ChannelRoutingConfigurationConflictError) {
        setConflict(error)
        form.setError('root.server', {
          message: t(
            'This channel configuration changed before your save completed.'
          ),
        })
        return
      }
      const failure = getChannelRoutingConfigurationApiError(error)
      let message = t('Could not save the channel configuration. Try again.')
      if (failure.status === 403) {
        message = t(
          'Sensitive write permission is required to edit channel multipliers.'
        )
      } else if (failure.code === 'channel_configuration_not_found') {
        message = t('This channel configuration no longer exists.')
      } else if (
        failure.code === 'invalid_if_match' ||
        failure.code === 'if_match_required'
      ) {
        message = t('Refresh the channel configuration before saving again.')
      } else if (failure.code === 'invalid_channel_configuration') {
        message = t('Review the highlighted fields and try again.')
      }

      if (failure.field === 'upstream_cost_multiplier') {
        form.setError('upstreamCostMultiplier', { message })
      } else if (failure.field === 'traffic_class') {
        form.setError('trafficClass', { message })
      } else if (
        failure.field === 'failure_domain_label' ||
        failure.field === 'clear_failure_domain'
      ) {
        form.setError('failureDomainLabel', { message })
      } else {
        form.setError('root.server', { message })
      }
      toast.error(message)
    },
  })

  const requestClose = () => {
    if (saveMutation.isPending) return
    if (form.formState.isDirty) {
      setDiscardOpen(true)
      return
    }
    props.onOpenChange(false)
  }

  const submitValues = (values: ChannelConfigurationFormValues) => {
    const request = channelConfigurationRequest(values)
    const confirmsZeroCost =
      request.upstream_cost_multiplier === 0 &&
      workingConfiguration.upstream_cost_multiplier !== 0
    const clearsFailureDomain =
      request.clear_failure_domain &&
      workingConfiguration.failure_domain_status !== 'unconfigured'
    if (confirmsZeroCost || clearsFailureDomain) {
      setPendingRisk({
        values,
        confirmsZeroCost,
        clearsFailureDomain,
      })
      return
    }
    saveMutation.mutate(values)
  }

  const loadLatestVersion = () => {
    if (!conflict?.current) return
    const draft = form.getValues()
    const latest = {
      ...conflict.current,
      etag: conflict.currentETag || conflict.current.etag,
    }
    const summary = channelConfigurationConflictSummary({
      baseline: workingConfiguration,
      latest,
      draft,
    })
    const latestValues = channelConfigurationFormValues(latest)
    setWorkingConfiguration(latest)
    form.reset(latestValues)
    form.setValue('upstreamCostMultiplier', draft.upstreamCostMultiplier, {
      shouldDirty:
        Number(draft.upstreamCostMultiplier) !==
        latest.upstream_cost_multiplier,
      shouldValidate: true,
    })
    form.setValue('trafficClass', draft.trafficClass, {
      shouldDirty: draft.trafficClass !== latest.traffic_class,
      shouldValidate: true,
    })
    form.setValue('failureDomainLabel', draft.failureDomainLabel, {
      shouldDirty:
        draft.failureDomainLabel.trim() !== latest.failure_domain_label.trim(),
      shouldValidate: true,
    })
    form.setValue('clearFailureDomain', draft.clearFailureDomain, {
      shouldDirty: draft.clearFailureDomain,
      shouldValidate: true,
    })
    setConflict(null)
    setConflictMerge(summary)
    form.clearErrors('root.server')
  }

  let riskTitle = t('Confirm high-risk channel changes')
  if (pendingRisk?.confirmsZeroCost && !pendingRisk.clearsFailureDomain) {
    riskTitle = t('Confirm free upstream cost')
  } else if (
    pendingRisk?.clearsFailureDomain &&
    !pendingRisk.confirmsZeroCost
  ) {
    riskTitle = t('Confirm failure domain removal')
  }
  let sourceLabel = t('System default')
  if (workingConfiguration.cost_source === 'manual') {
    sourceLabel = t('Manual')
  } else if (workingConfiguration.cost_source === 'legacy_migrated') {
    sourceLabel = t('Legacy migrated')
  }

  return (
    <>
      <FormNavigationGuard
        when={props.open && form.formState.isDirty}
        title={t('Unsaved channel configuration')}
        message={t(
          'You have unsaved channel configuration changes. Leave without saving them?'
        )}
      />
      <Sheet
        open={props.open}
        onOpenChange={(open) => {
          if (!open) requestClose()
        }}
      >
        <SheetContent
          className={sideDrawerContentClassName(
            'channel-routing-touch-surface max-w-none max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11 sm:!max-w-xl'
          )}
        >
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle className='flex min-w-0 items-center gap-2'>
              <HugeiconsIcon
                icon={MultiplicationSignIcon}
                strokeWidth={2}
                className='size-4 shrink-0'
                aria-hidden='true'
              />
              <span className='min-w-0 break-words'>
                {t('Edit channel multiplier')}
              </span>
            </SheetTitle>
            <SheetDescription>
              {workingConfiguration.channel_name} #
              {workingConfiguration.channel_id}
            </SheetDescription>
          </SheetHeader>

          <Form {...form}>
            <form
              id='channel-routing-configuration-form'
              className={sideDrawerFormClassName()}
              onSubmit={form.handleSubmit(submitValues)}
            >
              <Alert role='note'>
                <HugeiconsIcon
                  icon={InformationCircleIcon}
                  strokeWidth={2}
                  aria-hidden='true'
                />
                <AlertTitle>{t('How channel multipliers work')}</AlertTitle>
                <AlertDescription>
                  {t(
                    "Channel multiplier estimates this channel's upstream cost. 1× uses the system model baseline, 0.5× is half, and 2× is double. It does not change user group billing."
                  )}
                </AlertDescription>
              </Alert>

              {conflict ? (
                <Alert
                  variant='destructive'
                  role='alert'
                  className='*:data-[slot=alert-description]:text-foreground'
                >
                  <HugeiconsIcon
                    icon={Alert02Icon}
                    strokeWidth={2}
                    aria-hidden='true'
                  />
                  <AlertTitle>
                    {conflict.current
                      ? t('Channel configuration changed elsewhere')
                      : t('Channel configuration was deleted elsewhere')}
                  </AlertTitle>
                  <AlertDescription className='space-y-3'>
                    <p>
                      {conflict.current
                        ? t(
                            'Your draft is preserved. Load the latest version, review overlapping fields, then save again.'
                          )
                        : t(
                            'Close this sheet and refresh the channel multiplier list.'
                          )}
                    </p>
                    {conflict.current ? (
                      <Button
                        type='button'
                        size='sm'
                        variant='outline'
                        className='text-foreground'
                        onClick={loadLatestVersion}
                      >
                        <HugeiconsIcon
                          icon={ArrowReloadHorizontalIcon}
                          data-icon='inline-start'
                          strokeWidth={2}
                          aria-hidden='true'
                        />
                        {t('Load latest version')}
                      </Button>
                    ) : null}
                  </AlertDescription>
                </Alert>
              ) : null}

              {conflictMerge ? (
                <Alert role='status' aria-live='polite'>
                  <HugeiconsIcon
                    icon={InformationCircleIcon}
                    strokeWidth={2}
                    aria-hidden='true'
                  />
                  <AlertTitle>
                    {t('Draft preserved on latest version')}
                  </AlertTitle>
                  <AlertDescription className='space-y-3'>
                    <p>
                      {t(
                        'Review fields that changed elsewhere before saving your preserved draft.'
                      )}
                    </p>
                    {conflictMerge.serverChangedLabels.length > 0 ? (
                      <div className='flex flex-wrap gap-1.5'>
                        {conflictMerge.serverChangedLabels.map((label) => (
                          <Badge
                            key={label}
                            variant={
                              conflictMerge.overlappingLabels.includes(label)
                                ? 'destructive'
                                : 'outline'
                            }
                          >
                            {configurationFieldLabel(label, t)}
                          </Badge>
                        ))}
                      </div>
                    ) : null}
                  </AlertDescription>
                </Alert>
              ) : null}

              {form.formState.errors.root?.server?.message && !conflict ? (
                <Alert variant='destructive' role='alert'>
                  <HugeiconsIcon
                    icon={Alert02Icon}
                    strokeWidth={2}
                    aria-hidden='true'
                  />
                  <AlertTitle>{t('Could not save changes')}</AlertTitle>
                  <AlertDescription>
                    {form.formState.errors.root.server.message}
                  </AlertDescription>
                </Alert>
              ) : null}

              <div className='grid grid-cols-2 gap-3 rounded-lg border p-3 text-xs'>
                <div>
                  <div className='text-muted-foreground'>
                    {t('Confirmation')}
                  </div>
                  <div className='mt-1 font-medium'>
                    {workingConfiguration.cost_confirmed
                      ? t('Confirmed')
                      : t('Pending review')}
                  </div>
                </div>
                <div>
                  <div className='text-muted-foreground'>{t('Source')}</div>
                  <div className='mt-1 font-medium'>{sourceLabel}</div>
                </div>
                <div>
                  <div className='text-muted-foreground'>{t('Revision')}</div>
                  <div className='mt-1 font-medium'>
                    {format.number(workingConfiguration.revision)}
                  </div>
                </div>
                <div>
                  <div className='text-muted-foreground'>{t('Updated')}</div>
                  <div className='mt-1 font-medium'>
                    {format.timestamp(workingConfiguration.updated_time)}
                  </div>
                </div>
              </div>

              <FieldSet>
                <FieldLegend>{t('Routing channel configuration')}</FieldLegend>
                <FieldGroup>
                  <FormField
                    control={form.control}
                    name='upstreamCostMultiplier'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Channel multiplier')}</FormLabel>
                        <FormControl>
                          <Input
                            {...field}
                            type='number'
                            inputMode='decimal'
                            min={0}
                            max={1000}
                            step='any'
                            autoComplete='off'
                          />
                        </FormControl>
                        <FormDescription>
                          {t(
                            'Enter a finite value from 0 to 1000. Use 0 only for a confirmed free upstream.'
                          )}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />

                  <FormField
                    control={form.control}
                    name='trafficClass'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Traffic class')}</FormLabel>
                        <FormControl>
                          <NativeSelect
                            className='w-full'
                            value={field.value}
                            onBlur={field.onBlur}
                            onChange={field.onChange}
                          >
                            <NativeSelectOption value='all'>
                              {t('All eligible traffic')}
                            </NativeSelectOption>
                            <NativeSelectOption value='claude_code_only'>
                              {t('Claude Code only')}
                            </NativeSelectOption>
                          </NativeSelect>
                        </FormControl>
                        <FormDescription>
                          {t(
                            'Claude Code only excludes ordinary requests from this physical channel.'
                          )}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />

                  <FormField
                    control={form.control}
                    name='failureDomainLabel'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Failure domain label')}</FormLabel>
                        <FormControl>
                          <Input
                            {...field}
                            maxLength={FAILURE_DOMAIN_LABEL_MAXIMUM}
                            placeholder={t('Provider account A')}
                            autoComplete='off'
                            disabled={clearFailureDomain}
                          />
                        </FormControl>
                        <FormDescription>
                          {workingConfiguration.failure_domain_status ===
                          'historical_migrated'
                            ? t(
                                'A historical failure-domain relationship is preserved until you replace or explicitly clear it.'
                              )
                            : t(
                                'Use a stable administrator label. Credentials and internal hashes are never shown here.'
                              )}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />

                  {workingConfiguration.failure_domain_status !==
                  'unconfigured' ? (
                    <FormField
                      control={form.control}
                      name='clearFailureDomain'
                      render={({ field }) => (
                        <FormItem className='flex min-h-16 items-center justify-between gap-4 border-y py-3'>
                          <div className='space-y-1'>
                            <FormLabel>{t('Clear failure domain')}</FormLabel>
                            <FormDescription>
                              {t(
                                'This removes the configured or migrated independence signal used by Hedge safety checks.'
                              )}
                            </FormDescription>
                          </div>
                          <FormControl>
                            <Switch
                              checked={field.value}
                              onCheckedChange={(checked) => {
                                field.onChange(checked)
                                if (checked) {
                                  form.setValue('failureDomainLabel', '', {
                                    shouldDirty: true,
                                    shouldValidate: true,
                                  })
                                }
                              }}
                              aria-label={t('Clear failure domain')}
                            />
                          </FormControl>
                        </FormItem>
                      )}
                    />
                  ) : null}
                </FieldGroup>
              </FieldSet>
            </form>
          </Form>

          <SheetFooter className={sideDrawerFooterClassName()}>
            <Button
              type='button'
              variant='outline'
              disabled={saveMutation.isPending}
              onClick={requestClose}
            >
              <HugeiconsIcon
                icon={Cancel01Icon}
                data-icon='inline-start'
                strokeWidth={2}
                aria-hidden='true'
              />
              {t('Cancel')}
            </Button>
            <Button
              type='submit'
              form='channel-routing-configuration-form'
              disabled={
                saveMutation.isPending ||
                conflict != null ||
                !form.formState.isDirty
              }
            >
              <HugeiconsIcon
                icon={FloppyDiskIcon}
                data-icon='inline-start'
                strokeWidth={2}
                aria-hidden='true'
              />
              {saveMutation.isPending ? t('Saving...') : t('Save changes')}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      <ConfirmDialog
        open={discardOpen}
        onOpenChange={setDiscardOpen}
        title={t('Discard unsaved changes?')}
        desc={t(
          'Your channel multiplier, traffic class, and failure-domain edits will be lost.'
        )}
        cancelBtnText={t('Keep editing')}
        confirmText={t('Discard changes')}
        destructive
        handleConfirm={() => {
          setDiscardOpen(false)
          form.reset(channelConfigurationFormValues(workingConfiguration))
          props.onOpenChange(false)
        }}
      />

      <ConfirmDialog
        open={pendingRisk != null}
        onOpenChange={(open) => {
          if (!open) setPendingRisk(null)
        }}
        title={riskTitle}
        desc={
          <div className='space-y-2'>
            {pendingRisk?.confirmsZeroCost ? (
              <p>
                {t(
                  'Saving 0× marks this channel as a known zero-cost upstream for routing decisions.'
                )}
              </p>
            ) : null}
            {pendingRisk?.clearsFailureDomain ? (
              <p>
                {t(
                  'Clearing the failure domain removes an independence signal used to decide whether Hedge is safe.'
                )}
              </p>
            ) : null}
            <p>{t('Confirm that these changes are intentional.')}</p>
          </div>
        }
        cancelBtnText={t('Keep editing')}
        confirmText={t('Save changes')}
        handleConfirm={() => {
          const values = pendingRisk?.values
          setPendingRisk(null)
          if (values) saveMutation.mutate(values)
        }}
      />
    </>
  )
}
