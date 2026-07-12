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
import { BadgeCheck, Rocket } from 'lucide-react'
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
import { Progress } from '@/components/ui/progress'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Textarea } from '@/components/ui/textarea'
import { useDebounce } from '@/hooks'

import {
  approveChannelRoutingPolicyDraft,
  createChannelRoutingIdempotencyKey,
  listChannelRoutingPolicyApprovals,
  publishChannelRoutingPolicyDraft,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import type {
  PolicyActivationSpec,
  PolicyDraftSummary,
  PolicySimulationRiskAssessment,
} from '../types'
import { ChannelRoutingPolicySimulationRiskSection } from './policy-simulation-risk-section'

type PolicyActivationIntent = 'approve' | 'publish'

type PolicyActivationFormValues = {
  stage: PolicyActivationSpec['stage']
  trafficBasisPoints: number
  reason: string
}

export function ChannelRoutingPolicyActivationSheet(props: {
  draft: PolicyDraftSummary | null
  intent: PolicyActivationIntent
  canDeploy: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const queryClient = useQueryClient()
  const [confirmationValues, setConfirmationValues] =
    useState<PolicyActivationFormValues | null>(null)
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
        })
        .superRefine((value, context) => {
          if (
            value.stage === 'canary' &&
            (value.trafficBasisPoints < 100 || value.trafficBasisPoints > 500)
          ) {
            context.addIssue({
              code: 'custom',
              path: ['trafficBasisPoints'],
              message: t('Canary traffic must be between 1% and 5%'),
            })
          }
          if (value.stage !== 'canary' && value.trafficBasisPoints !== 0) {
            context.addIssue({
              code: 'custom',
              path: ['trafficBasisPoints'],
              message: t('Traffic allocation must be zero outside Canary'),
            })
          }
        }),
    [t]
  )
  const form = useForm<PolicyActivationFormValues>({
    resolver: zodResolver(schema),
    defaultValues: { stage: 'canary', trafficBasisPoints: 100, reason: '' },
  })
  const watchedStage = useWatch({ control: form.control, name: 'stage' })
  const watchedTraffic = useWatch({
    control: form.control,
    name: 'trafficBasisPoints',
  })
  const watchedReason = useWatch({ control: form.control, name: 'reason' })
  const target = useDebounce(
    {
      stage: watchedStage,
      traffic_basis_points: watchedTraffic,
      reason: watchedReason.trim(),
    } satisfies PolicyActivationSpec,
    400
  )
  const targetIsValid = schema.safeParse({
    stage: target.stage,
    trafficBasisPoints: target.traffic_basis_points,
    reason: target.reason,
  }).success
  const targetMatchesForm =
    target.stage === watchedStage &&
    target.traffic_basis_points === watchedTraffic &&
    target.reason === watchedReason.trim()
  const approvalsQuery = useQuery({
    queryKey: channelRoutingQueryKeys.policyApprovals(props.draft?.id ?? 0, {
      target: targetIsValid ? target : 'all',
    }),
    queryFn: () =>
      listChannelRoutingPolicyApprovals(
        props.draft?.id ?? 0,
        targetIsValid ? target : undefined
      ),
    enabled: props.open && props.draft != null,
  })
  const approve = useMutation({
    mutationFn: (values: PolicyActivationFormValues) => {
      if (!props.draft) throw new Error('Policy draft is required')
      if (approvalsQuery.data?.requires_approval !== true) {
        throw new Error('Policy approval is not required')
      }
      return approveChannelRoutingPolicyDraft(props.draft, {
        stage: values.stage,
        traffic_basis_points: values.trafficBasisPoints,
        reason: values.reason.trim(),
      })
    },
    onSuccess: async (response) => {
      await queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.policyDraftsRoot(),
      })
      toast.success(
        response.created
          ? t('Policy approval recorded')
          : t('Approval already recorded')
      )
      props.onOpenChange(false)
    },
  })
  const publish = useMutation({
    mutationFn: (values: PolicyActivationFormValues) => {
      if (!props.draft) throw new Error('Policy draft is required')
      const activation: PolicyActivationSpec = {
        stage: values.stage,
        traffic_basis_points: values.trafficBasisPoints,
        reason: values.reason.trim(),
      }
      const signature = JSON.stringify(activation)
      if (idempotencyRef.current?.signature !== signature) {
        idempotencyRef.current = {
          signature,
          key: createChannelRoutingIdempotencyKey('publish'),
        }
      }
      return publishChannelRoutingPolicyDraft(
        props.draft,
        activation,
        idempotencyRef.current.key
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
  })
  const resetApprove = approve.reset
  const resetPublish = publish.reset

  useEffect(() => {
    if (!props.open) return
    form.reset({ stage: 'canary', trafficBasisPoints: 100, reason: '' })
    idempotencyRef.current = null
    setConfirmationValues(null)
    resetApprove()
    resetPublish()
  }, [
    form,
    props.draft?.id,
    props.intent,
    props.open,
    resetApprove,
    resetPublish,
  ])

  const isPublishing = props.intent === 'publish'
  const simulationRisk = props.draft
    ? queryClient.getQueryData<PolicySimulationRiskAssessment | null>(
        channelRoutingQueryKeys.policySimulationRisk(props.draft.id)
      )
    : undefined
  const simulationBlocked = simulationRisk?.state === 'fail'
  const mutation = isPublishing ? publish : approve
  const approvalReady =
    targetIsValid &&
    targetMatchesForm &&
    approvalsQuery.data != null &&
    !approvalsQuery.isLoading &&
    !approvalsQuery.isError
  const requiresApproval =
    approvalReady && approvalsQuery.data.requires_approval === true
  const targetApprovalCount = targetIsValid
    ? (approvalsQuery.data?.count ?? 0)
    : 0
  const requiredApprovals = Math.max(1, approvalsQuery.data?.required ?? 2)
  const quorum = approvalReady && approvalsQuery.data.quorum === true
  const matchingApprovals = approvalsQuery.data?.target_activation_hash
    ? (approvalsQuery.data.items.filter(
        (approval) =>
          approval.activation_hash ===
          approvalsQuery.data?.target_activation_hash
      ) ?? [])
    : []

  const submit = (values: PolicyActivationFormValues) => {
    if (isPublishing) {
      if (simulationBlocked) return
      setConfirmationValues(values)
      return
    }
    if (!requiresApproval) return
    approve.mutate(values)
  }

  let approvalDescription = t(
    'Complete the activation details to check approval status.'
  )
  let approvalStatus = 'pending'
  let approvalStatusLabel = t('Pending')
  if (targetIsValid && approvalsQuery.isLoading) {
    approvalDescription = t('Checking whether this deployment needs approval.')
  } else if (targetIsValid && approvalsQuery.isError) {
    approvalDescription = t(
      'Approval status could not be loaded. Refresh it before continuing.'
    )
    approvalStatus = 'failed'
    approvalStatusLabel = t('Unavailable')
  } else if (approvalReady && !requiresApproval) {
    approvalDescription = t(
      'This deployment does not require approval. You can publish it directly.'
    )
    approvalStatus = 'ready'
    approvalStatusLabel = t('Not required')
  } else if (approvalReady) {
    approvalDescription = t('{{count}} of {{required}} eligible approvals', {
      count: targetApprovalCount,
      required: requiredApprovals,
    })
    approvalStatus = quorum ? 'succeeded' : 'pending'
    approvalStatusLabel = quorum ? t('Ready') : t('Pending')
  }
  let submitLabel = t('Record approval')
  if (isPublishing) submitLabel = t('Review publish')
  else if (!requiresApproval) submitLabel = t('Approval not required')
  if (isPublishing && simulationBlocked) {
    submitLabel = t('Blocked by simulation risk')
  }

  return (
    <>
      <Sheet open={props.open} onOpenChange={props.onOpenChange}>
        <SheetContent
          className={sideDrawerContentClassName(
            'max-w-none max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11 sm:!max-w-2xl'
          )}
        >
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle className='flex items-center gap-2'>
              {isPublishing ? (
                <Rocket className='size-4' aria-hidden='true' />
              ) : (
                <BadgeCheck className='size-4' aria-hidden='true' />
              )}
              {isPublishing
                ? t('Publish policy draft #{{id}}', { id: props.draft?.id })
                : t('Approve policy draft #{{id}}', { id: props.draft?.id })}
            </SheetTitle>
            <SheetDescription>
              {t(
                'Approvals are bound to the exact stage, traffic allocation, and reason.'
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
                          onChange={(event) =>
                            field.onChange(event.target.valueAsNumber)
                          }
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

              <section
                className='space-y-3 border-t pt-4'
                aria-labelledby='approval-status-heading'
              >
                <div className='flex items-center justify-between gap-3'>
                  <div>
                    <h3
                      id='approval-status-heading'
                      className='text-sm font-semibold'
                    >
                      {t('Approval quorum')}
                    </h3>
                    <p className='text-muted-foreground mt-1 text-xs'>
                      {approvalDescription}
                    </p>
                  </div>
                  <ChannelRoutingStatusBadge
                    status={approvalStatus}
                    label={approvalStatusLabel}
                  />
                </div>
                {requiresApproval ? (
                  <Progress
                    aria-label={t('Approval progress')}
                    value={Math.min(
                      100,
                      (targetApprovalCount / requiredApprovals) * 100
                    )}
                  />
                ) : null}
                {requiresApproval && matchingApprovals.length > 0 ? (
                  <ul className='divide-y rounded-lg border text-sm'>
                    {matchingApprovals.map((approval) => (
                      <li
                        key={approval.id}
                        className='flex items-center justify-between gap-3 px-3 py-2'
                      >
                        <span>
                          {t('Approver #{{id}}', { id: approval.actor_id })}
                        </span>
                        <span className='text-muted-foreground text-xs'>
                          {format.timestamp(approval.created_time_ms)}
                        </span>
                      </li>
                    ))}
                  </ul>
                ) : null}
              </section>

              {isPublishing ? (
                <ChannelRoutingPolicySimulationRiskSection
                  risk={simulationRisk}
                  compact
                />
              ) : null}

              {mutation.isError ? (
                <div className='border-destructive/30 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm'>
                  {isPublishing
                    ? t(
                        'Could not publish this policy. Refresh the draft and approval status.'
                      )
                    : t(
                        'Could not record this approval. Refresh the draft and try again.'
                      )}
                </div>
              ) : null}
            </form>
          </Form>

          <SheetFooter className={sideDrawerFooterClassName()}>
            <Button
              type='submit'
              form='channel-routing-policy-activation-form'
              disabled={
                !props.canDeploy ||
                mutation.isPending ||
                approvalsQuery.isLoading ||
                approvalsQuery.isError ||
                !approvalReady ||
                (!isPublishing && !requiresApproval) ||
                (isPublishing && requiresApproval && !quorum) ||
                (isPublishing && simulationBlocked)
              }
            >
              {isPublishing ? (
                <Rocket aria-hidden='true' />
              ) : (
                <BadgeCheck aria-hidden='true' />
              )}
              {submitLabel}
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
          </dl>
        ) : null}
      </ConfirmDialog>
    </>
  )
}
