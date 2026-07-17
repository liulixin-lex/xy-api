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
import { Alert02Icon, RocketIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import z from 'zod'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
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
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'

import {
  createChannelRoutingIdempotencyKey,
  getChannelRoutingPolicyApiError,
  publishChannelRoutingPolicyDraft,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import type {
  PolicyActivationSpec,
  PolicyDraftSummary,
  PolicySimulationResponse,
} from '../types'
import { ChannelRoutingPolicySimulationRiskSection } from './policy-simulation-risk-section'

type PolicyActivationFormValues = {
  stage: PolicyActivationSpec['stage']
  trafficBasisPoints: number
  reason: string
  acceptSimulationRisk: boolean
  riskAcceptanceReason: string
}

function activationTarget(values: PolicyActivationFormValues) {
  return {
    stage: values.stage,
    traffic_basis_points: values.trafficBasisPoints,
  }
}

function activationSignature(
  draft: PolicyDraftSummary,
  values: PolicyActivationFormValues
) {
  return JSON.stringify({
    draft_id: draft.id,
    draft_version: draft.version,
    ...activationTarget(values),
  })
}

export function ChannelRoutingPolicyActivationSheet(props: {
  draft: PolicyDraftSummary | null
  canDeploy: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const queryClient = useQueryClient()
  const [confirmationValues, setConfirmationValues] =
    useState<PolicyActivationFormValues | null>(null)
  const [serverRiskTarget, setServerRiskTarget] = useState<string | null>(null)
  const idempotencyRef = useRef<{ signature: string; key: string } | null>(null)
  const schema = useMemo(
    () =>
      z
        .object({
          stage: z.enum(['observe', 'shadow', 'canary', 'active']),
          trafficBasisPoints: z
            .number({ error: t('Enter a valid number') })
            .int(t('Value must be an integer'))
            .min(0, t('Traffic allocation cannot be negative'))
            .max(500, t('Canary traffic must be between 1% and 5%')),
          reason: z
            .string()
            .trim()
            .min(1, t('Change reason is required'))
            .max(512, t('Change reason must be 512 characters or fewer')),
          acceptSimulationRisk: z.boolean(),
          riskAcceptanceReason: z
            .string()
            .trim()
            .max(512, t('Change reason must be 512 characters or fewer')),
        })
        .superRefine((values, context) => {
          if (
            values.stage === 'canary' &&
            (values.trafficBasisPoints < 100 || values.trafficBasisPoints > 500)
          ) {
            context.addIssue({
              code: 'custom',
              path: ['trafficBasisPoints'],
              message: t('Canary traffic must be between 1% and 5%'),
            })
          }
          if (values.stage !== 'canary' && values.trafficBasisPoints !== 0) {
            context.addIssue({
              code: 'custom',
              path: ['trafficBasisPoints'],
              message: t('Traffic allocation must be zero outside Canary'),
            })
          }
          if (
            values.acceptSimulationRisk &&
            values.riskAcceptanceReason.trim() === ''
          ) {
            context.addIssue({
              code: 'custom',
              path: ['riskAcceptanceReason'],
              message: t('Risk acceptance reason is required'),
            })
          }
        }),
    [t]
  )
  const form = useForm<PolicyActivationFormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      stage: 'canary',
      trafficBasisPoints: 100,
      reason: '',
      acceptSimulationRisk: false,
      riskAcceptanceReason: '',
    },
  })
  const watchedStage = useWatch({ control: form.control, name: 'stage' })
  const watchedTraffic = useWatch({
    control: form.control,
    name: 'trafficBasisPoints',
  })
  const acceptedSimulationRisk = useWatch({
    control: form.control,
    name: 'acceptSimulationRisk',
  })
  const currentTarget = {
    stage: watchedStage,
    traffic_basis_points: watchedTraffic,
  }
  const cachedSimulation = props.draft
    ? queryClient.getQueryData<PolicySimulationResponse>(
        channelRoutingQueryKeys.policySimulationRisk(
          props.draft.id,
          currentTarget
        )
      )
    : undefined
  const currentSignature = props.draft
    ? activationSignature(props.draft, {
        ...form.getValues(),
        stage: watchedStage,
        trafficBasisPoints: watchedTraffic,
      })
    : ''
  const riskAcceptanceRequired =
    cachedSimulation?.result.risk?.state === 'fail' ||
    serverRiskTarget === currentSignature
  const publish = useMutation({
    mutationFn: (values: PolicyActivationFormValues) => {
      if (!props.draft) throw new Error('Policy draft is required')
      const activation: PolicyActivationSpec = {
        stage: values.stage,
        traffic_basis_points: values.trafficBasisPoints,
        reason: values.reason.trim(),
      }
      const riskAcceptance = values.acceptSimulationRisk
        ? {
            accepted: true,
            reason: values.riskAcceptanceReason.trim(),
          }
        : undefined
      const signature = JSON.stringify({ activation, riskAcceptance })
      if (idempotencyRef.current?.signature !== signature) {
        idempotencyRef.current = {
          signature,
          key: createChannelRoutingIdempotencyKey('publish'),
        }
      }
      return publishChannelRoutingPolicyDraft(
        props.draft,
        activation,
        idempotencyRef.current.key,
        riskAcceptance
      )
    },
    onSuccess: async (response) => {
      idempotencyRef.current = null
      setConfirmationValues(null)
      await queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.all,
      })
      toast.success(
        t('Policy revision {{revision}} published', {
          revision: response.published.revision.revision,
        })
      )
      props.onOpenChange(false)
    },
    onError: (error, values) => {
      const apiError = getChannelRoutingPolicyApiError(error)
      if (
        props.draft &&
        apiError.code === 'policy_simulation_risk_acceptance_required'
      ) {
        setServerRiskTarget(activationSignature(props.draft, values))
        requestAnimationFrame(() => form.setFocus('acceptSimulationRisk'))
      }
    },
    meta: { handleErrorLocally: true },
  })
  const resetPublish = publish.reset

  useEffect(() => {
    if (!props.open) return
    form.reset({
      stage: 'canary',
      trafficBasisPoints: 100,
      reason: '',
      acceptSimulationRisk: false,
      riskAcceptanceReason: '',
    })
    idempotencyRef.current = null
    setConfirmationValues(null)
    setServerRiskTarget(null)
    resetPublish()
  }, [form, props.draft?.id, props.open, resetPublish])

  const submit = (values: PolicyActivationFormValues) => {
    if (riskAcceptanceRequired && !values.acceptSimulationRisk) {
      form.setError('acceptSimulationRisk', {
        type: 'manual',
        message: t('Explicit risk acceptance is required'),
      })
      form.setFocus('acceptSimulationRisk')
      return
    }
    setConfirmationValues(values)
  }
  const publishError = getChannelRoutingPolicyApiError(publish.error)

  return (
    <>
      <Sheet open={props.open} onOpenChange={props.onOpenChange}>
        <SheetContent
          className={sideDrawerContentClassName(
            'channel-routing-touch-surface max-w-none max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11 sm:!max-w-2xl'
          )}
        >
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle className='flex items-center gap-2'>
              <HugeiconsIcon
                icon={RocketIcon}
                strokeWidth={2}
                aria-hidden='true'
              />
              {t('Publish policy draft #{{id}}', { id: props.draft?.id })}
            </SheetTitle>
            <SheetDescription>
              {t(
                'Simulation is optional. A matching known failure requires explicit risk acceptance before publishing.'
              )}
            </SheetDescription>
          </SheetHeader>

          <Form {...form}>
            <form
              id='channel-routing-policy-activation-form'
              className={sideDrawerFormClassName('gap-5')}
              onSubmit={form.handleSubmit(submit)}
            >
              <div className='grid gap-3 sm:grid-cols-2'>
                <FormField
                  control={form.control}
                  name='stage'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Deployment stage')}</FormLabel>
                      <FormControl>
                        <NativeSelect
                          className='w-full'
                          value={field.value}
                          onChange={(event) => {
                            const stage = event.target
                              .value as PolicyActivationSpec['stage']
                            field.onChange(stage)
                            form.setValue(
                              'trafficBasisPoints',
                              stage === 'canary' ? 100 : 0,
                              { shouldValidate: true }
                            )
                            form.setValue('acceptSimulationRisk', false)
                            form.setValue('riskAcceptanceReason', '')
                          }}
                        >
                          <NativeSelectOption value='observe'>
                            {t('Observe')}
                          </NativeSelectOption>
                          <NativeSelectOption value='shadow'>
                            {t('Shadow')}
                          </NativeSelectOption>
                          <NativeSelectOption value='canary'>
                            {t('Canary')}
                          </NativeSelectOption>
                          <NativeSelectOption value='active'>
                            {t('Active')}
                          </NativeSelectOption>
                        </NativeSelect>
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='trafficBasisPoints'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Traffic allocation')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={watchedStage === 'canary' ? 100 : 0}
                          max={watchedStage === 'canary' ? 500 : 0}
                          step={100}
                          disabled={watchedStage !== 'canary'}
                          value={field.value}
                          onChange={(event) => {
                            field.onChange(event.target.valueAsNumber)
                            form.setValue('acceptSimulationRisk', false)
                            form.setValue('riskAcceptanceReason', '')
                          }}
                        />
                      </FormControl>
                      <FormDescription>
                        {watchedStage === 'canary'
                          ? t('{{percent}}% of deterministic traffic', {
                              percent: format.number(field.value / 100),
                            })
                          : t('Full stage transition')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>

              <FormField
                control={form.control}
                name='reason'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Change reason')}</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={4}
                        maxLength={512}
                        placeholder={t(
                          'Describe the operational reason for this change'
                        )}
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <ChannelRoutingPolicySimulationRiskSection
                risk={cachedSimulation?.result.risk}
                compact
              />

              {riskAcceptanceRequired ? (
                <section
                  className='flex flex-col gap-3 border-t pt-4'
                  aria-labelledby='simulation-risk-acceptance'
                >
                  <Alert role='alert'>
                    <HugeiconsIcon
                      icon={Alert02Icon}
                      strokeWidth={2}
                      aria-hidden='true'
                    />
                    <AlertTitle id='simulation-risk-acceptance'>
                      {t('Known simulation risk requires acceptance')}
                    </AlertTitle>
                    <AlertDescription>
                      {t(
                        'Accepting this risk is recorded permanently with the published revision and your reason.'
                      )}
                    </AlertDescription>
                  </Alert>
                  <FormField
                    control={form.control}
                    name='acceptSimulationRisk'
                    render={({ field }) => (
                      <FormItem className='flex items-start gap-3 rounded-lg border p-3'>
                        <FormControl>
                          <Checkbox
                            checked={field.value}
                            onCheckedChange={(value) =>
                              field.onChange(value === true)
                            }
                          />
                        </FormControl>
                        <div className='grid gap-1'>
                          <FormLabel>
                            {t('Accept known simulation risk')}
                          </FormLabel>
                          <FormDescription>
                            {t(
                              'This does not bypass policy structure, baseline, member, credential, stage, or traffic validation.'
                            )}
                          </FormDescription>
                          <FormMessage />
                        </div>
                      </FormItem>
                    )}
                  />
                  <FormField
                    control={form.control}
                    name='riskAcceptanceReason'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Risk acceptance reason')}</FormLabel>
                        <FormControl>
                          <Textarea
                            rows={3}
                            maxLength={512}
                            disabled={!acceptedSimulationRisk}
                            placeholder={t(
                              'Explain why this known risk is acceptable for this deployment'
                            )}
                            {...field}
                          />
                        </FormControl>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                </section>
              ) : null}

              {publish.isError ? (
                <Alert variant='destructive' role='alert'>
                  <HugeiconsIcon
                    icon={Alert02Icon}
                    strokeWidth={2}
                    aria-hidden='true'
                  />
                  <AlertTitle>{t('Policy publish failed')}</AlertTitle>
                  <AlertDescription>
                    {publishError.code ===
                    'policy_simulation_risk_acceptance_required'
                      ? t(
                          'A matching simulation found a known failure. Review and accept the risk to continue.'
                        )
                      : t(
                          'Could not publish this policy. Refresh the draft and try again.'
                        )}
                  </AlertDescription>
                </Alert>
              ) : null}
            </form>
          </Form>

          <SheetFooter className={sideDrawerFooterClassName()}>
            <Button
              type='submit'
              form='channel-routing-policy-activation-form'
              disabled={!props.canDeploy || publish.isPending}
            >
              {publish.isPending ? (
                <Spinner data-icon='inline-start' />
              ) : (
                <HugeiconsIcon
                  icon={RocketIcon}
                  data-icon='inline-start'
                  strokeWidth={2}
                  aria-hidden='true'
                />
              )}
              {publish.isPending ? t('Publishing') : t('Review publish')}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      <ConfirmDialog
        open={confirmationValues != null}
        onOpenChange={(open) => {
          if (!open && !publish.isPending) setConfirmationValues(null)
        }}
        title={t('Publish this policy revision?')}
        desc={t(
          'This creates an immutable revision and activates it at the selected deployment stage.'
        )}
        confirmText={publish.isPending ? t('Publishing') : t('Publish policy')}
        isLoading={publish.isPending}
        handleConfirm={() => {
          if (confirmationValues) publish.mutate(confirmationValues)
        }}
      >
        {confirmationValues ? (
          <dl className='bg-muted/40 grid gap-2 rounded-lg border p-3 text-sm'>
            <div className='flex justify-between gap-3'>
              <dt className='text-muted-foreground'>{t('Stage')}</dt>
              <dd>
                <ChannelRoutingStatusBadge status={confirmationValues.stage} />
              </dd>
            </div>
            <div className='flex justify-between gap-3'>
              <dt className='text-muted-foreground'>{t('Traffic')}</dt>
              <dd>
                {confirmationValues.stage === 'canary'
                  ? `${format.number(confirmationValues.trafficBasisPoints / 100)}%`
                  : t('Full stage transition')}
              </dd>
            </div>
            <div>
              <dt className='text-muted-foreground'>{t('Reason')}</dt>
              <dd className='mt-1 break-words'>{confirmationValues.reason}</dd>
            </div>
            {confirmationValues.acceptSimulationRisk ? (
              <div>
                <dt className='text-muted-foreground'>
                  {t('Accepted simulation risk')}
                </dt>
                <dd className='mt-1 break-words'>
                  {confirmationValues.riskAcceptanceReason}
                </dd>
              </div>
            ) : null}
          </dl>
        ) : null}
      </ConfirmDialog>
    </>
  )
}
