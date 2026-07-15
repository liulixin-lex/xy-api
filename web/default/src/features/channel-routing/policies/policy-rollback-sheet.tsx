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
import { AxiosError } from 'axios'
import { BadgeCheck, RefreshCw, RotateCcw, ShieldCheck } from 'lucide-react'
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
  approveChannelRoutingPolicyRollback,
  createChannelRoutingIdempotencyKey,
  getChannelRoutingPolicyRevision,
  listChannelRoutingPolicyRollbackApprovals,
  rollbackChannelRoutingPolicy,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import type { CurrentRoutingPolicy, PolicyActivationSpec } from '../types'

type PolicyRollbackFormValues = {
  sourceRevision: number
  stage: PolicyActivationSpec['stage']
  trafficBasisPoints: number
  reason: string
}

const deploymentStages = new Set<PolicyActivationSpec['stage']>([
  'observe',
  'shadow',
  'canary',
  'active',
])

function rollbackRequestErrorMessage(
  error: unknown,
  translate: (key: string) => string
): string {
  const status =
    error instanceof AxiosError ? error.response?.status : undefined
  if (status === 403) {
    return translate(
      'You do not have permission to approve or execute this rollback.'
    )
  }
  if (status === 409 || status === 412 || status === 428) {
    return translate(
      'The current policy changed. Refresh the page before continuing.'
    )
  }
  return translate(
    'The rollback request failed. Refresh the approval status and try again.'
  )
}

export function ChannelRoutingPolicyRollbackSheet(props: {
  current: CurrentRoutingPolicy | null
  canDeploy: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const queryClient = useQueryClient()
  const [confirmationValues, setConfirmationValues] =
    useState<PolicyRollbackFormValues | null>(null)
  const idempotencyRef = useRef<{ signature: string; key: string } | null>(null)
  const currentRevision = props.current?.head.current_revision ?? 0
  const schema = useMemo(
    () =>
      z
        .object({
          sourceRevision: z
            .number({ error: t('Enter a valid number') })
            .int(t('Value must be an integer'))
            .min(1, t('Source revision must be positive'))
            .max(
              Math.max(1, currentRevision - 1),
              t('Select a revision older than the current revision')
            ),
          stage: z.enum(['observe', 'shadow', 'canary', 'active']),
          trafficBasisPoints: z
            .number({ error: t('Enter a valid number') })
            .int(t('Value must be an integer'))
            .min(0, t('Traffic allocation cannot be negative'))
            .max(500, t('Canary traffic must be between 1% and 5%')),
          reason: z
            .string()
            .trim()
            .min(1, t('Rollback reason is required'))
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
    [currentRevision, t]
  )
  const form = useForm<PolicyRollbackFormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      sourceRevision: Math.max(1, currentRevision - 1),
      stage: 'active',
      trafficBasisPoints: 0,
      reason: '',
    },
  })
  const sourceRevision = useWatch({
    control: form.control,
    name: 'sourceRevision',
  })
  const watchedStage = useWatch({ control: form.control, name: 'stage' })
  const watchedTraffic = useWatch({
    control: form.control,
    name: 'trafficBasisPoints',
  })
  const watchedReason = useWatch({ control: form.control, name: 'reason' })
  const debouncedSourceRevision = useDebounce(sourceRevision, 350)
  const sourceQuery = useQuery({
    queryKey: channelRoutingQueryKeys.policyRevision(debouncedSourceRevision),
    queryFn: () => getChannelRoutingPolicyRevision(debouncedSourceRevision),
    enabled:
      props.open &&
      Number.isInteger(debouncedSourceRevision) &&
      debouncedSourceRevision > 0 &&
      debouncedSourceRevision < currentRevision,
  })
  const approvalTarget = useDebounce(
    {
      sourceRevision,
      stage: watchedStage,
      trafficBasisPoints: watchedTraffic,
      reason: watchedReason.trim(),
    } satisfies PolicyRollbackFormValues,
    400
  )
  const approvalTargetIsValid = schema.safeParse(approvalTarget).success
  const approvalTargetMatchesForm =
    approvalTarget.sourceRevision === sourceRevision &&
    approvalTarget.stage === watchedStage &&
    approvalTarget.trafficBasisPoints === watchedTraffic &&
    approvalTarget.reason === watchedReason.trim()
  const approvalActivation: PolicyActivationSpec = {
    stage: approvalTarget.stage,
    traffic_basis_points: approvalTarget.trafficBasisPoints,
    reason: approvalTarget.reason,
  }
  const approvalsQuery = useQuery({
    queryKey: channelRoutingQueryKeys.policyRollbackApprovals(
      approvalTarget.sourceRevision,
      {
        expectedRevision: currentRevision,
        activation: approvalTargetIsValid ? approvalActivation : 'invalid',
      }
    ),
    queryFn: () =>
      listChannelRoutingPolicyRollbackApprovals(
        approvalTarget.sourceRevision,
        approvalActivation
      ),
    enabled:
      props.open &&
      props.current != null &&
      approvalTargetIsValid &&
      sourceQuery.data?.revision.revision === approvalTarget.sourceRevision,
  })
  const approveRollback = useMutation({
    mutationFn: (values: PolicyRollbackFormValues) => {
      if (!props.current) throw new Error('Current policy is required')
      if (approvalsQuery.data?.requires_approval !== true) {
        throw new Error('Rollback approval is not required')
      }
      return approveChannelRoutingPolicyRollback(
        values.sourceRevision,
        props.current,
        {
          stage: values.stage,
          traffic_basis_points: values.trafficBasisPoints,
          reason: values.reason.trim(),
        }
      )
    },
    onSuccess: async (response, values) => {
      await queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.policyRollbackApprovalsRoot(
          values.sourceRevision
        ),
      })
      toast.success(
        response.created
          ? t('Rollback approval recorded')
          : t('Rollback approval already recorded')
      )
    },
  })
  const rollback = useMutation({
    mutationFn: (values: PolicyRollbackFormValues) => {
      if (!props.current) throw new Error('Current policy is required')
      const activation: PolicyActivationSpec = {
        stage: values.stage,
        traffic_basis_points: values.trafficBasisPoints,
        reason: values.reason.trim(),
      }
      const signature = JSON.stringify({
        source_revision: values.sourceRevision,
        activation,
      })
      if (idempotencyRef.current?.signature !== signature) {
        idempotencyRef.current = {
          signature,
          key: createChannelRoutingIdempotencyKey('rollback'),
        }
      }
      return rollbackChannelRoutingPolicy(
        values.sourceRevision,
        props.current,
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
        t('Rollback published as revision {{revision}}', {
          revision: response.published.revision.revision,
        })
      )
      props.onOpenChange(false)
    },
  })
  const resetApproveRollback = approveRollback.reset
  const resetRollback = rollback.reset

  useEffect(() => {
    if (!props.open) return
    const stage = deploymentStages.has(
      props.current?.head.current_stage as PolicyActivationSpec['stage']
    )
      ? (props.current?.head.current_stage as PolicyActivationSpec['stage'])
      : 'active'
    form.reset({
      sourceRevision: Math.max(1, currentRevision - 1),
      stage,
      trafficBasisPoints: stage === 'canary' ? 100 : 0,
      reason: '',
    })
    idempotencyRef.current = null
    setConfirmationValues(null)
    resetApproveRollback()
    resetRollback()
  }, [
    currentRevision,
    form,
    props.current?.head.current_stage,
    props.open,
    resetApproveRollback,
    resetRollback,
  ])

  const approvalReady =
    approvalTargetIsValid &&
    approvalTargetMatchesForm &&
    approvalsQuery.data != null &&
    !approvalsQuery.isLoading &&
    !approvalsQuery.isError
  const requiresApproval =
    approvalReady && approvalsQuery.data.requires_approval === true
  const targetApprovalCount = approvalReady ? approvalsQuery.data.count : 0
  const requiredApprovals = Math.max(1, approvalsQuery.data?.required ?? 2)
  const quorum = approvalReady && approvalsQuery.data.quorum === true
  const matchingApprovals = approvalsQuery.data?.target_activation_hash
    ? (approvalsQuery.data.items.filter(
        (approval) =>
          approval.activation_hash ===
          approvalsQuery.data?.target_activation_hash
      ) ?? [])
    : []

  let approvalDescription = t(
    'Complete the rollback details to check approval status.'
  )
  let approvalStatus = 'pending'
  let approvalStatusLabel = t('Pending')
  if (approvalTargetIsValid && approvalsQuery.isLoading) {
    approvalDescription = t('Checking whether this rollback needs approval.')
  } else if (approvalTargetIsValid && approvalsQuery.isError) {
    approvalDescription = rollbackRequestErrorMessage(approvalsQuery.error, t)
    approvalStatus = 'failed'
    approvalStatusLabel = t('Unavailable')
  } else if (approvalReady && !requiresApproval) {
    approvalDescription = t(
      'This rollback does not require approval and can be executed directly.'
    )
    approvalStatus = 'ready'
    approvalStatusLabel = t('Not required')
  } else if (approvalReady) {
    approvalDescription = t(
      '{{count}} of {{required}} approvals are eligible for this executor',
      {
        count: targetApprovalCount,
        required: requiredApprovals,
      }
    )
    approvalStatus = quorum ? 'succeeded' : 'pending'
    approvalStatusLabel = quorum ? t('Ready') : t('Pending')
  }

  let approveButtonLabel = t('Approve rollback')
  if (approveRollback.isPending) approveButtonLabel = t('Approving rollback')
  else if (!requiresApproval) approveButtonLabel = t('Approval not required')

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
              <RotateCcw className='size-4' aria-hidden='true' />
              {t('Rollback policy')}
            </SheetTitle>
            <SheetDescription>
              {t(
                'Rollback creates a new immutable revision from an older policy document.'
              )}
            </SheetDescription>
          </SheetHeader>

          <Form {...form}>
            <form
              id='channel-routing-policy-rollback-form'
              className={sideDrawerFormClassName('gap-5')}
              onSubmit={form.handleSubmit((values) =>
                setConfirmationValues(values)
              )}
            >
              <FormField
                control={form.control}
                name='sourceRevision'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Source revision')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={Math.max(1, currentRevision - 1)}
                        value={field.value}
                        onChange={(event) =>
                          field.onChange(event.target.valueAsNumber)
                        }
                      />
                    </FormControl>
                    <FormDescription>
                      {t('Current revision: {{revision}}', {
                        revision: currentRevision,
                      })}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              {sourceQuery.data ? (
                <dl className='bg-muted/40 grid grid-cols-2 gap-3 rounded-lg border p-3 text-sm'>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Content hash')}
                    </dt>
                    <dd className='mt-1 font-mono text-xs'>
                      {format.shortHash(sourceQuery.data.revision.content_hash)}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Published')}
                    </dt>
                    <dd className='mt-1'>
                      {format.timestamp(sourceQuery.data.revision.created_time)}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Pools')}
                    </dt>
                    <dd className='mt-1'>
                      {sourceQuery.data.revision.pool_count}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Members')}
                    </dt>
                    <dd className='mt-1'>
                      {sourceQuery.data.revision.member_count}
                    </dd>
                  </div>
                </dl>
              ) : null}
              {sourceQuery.isError ? (
                <div className='border-destructive/30 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm'>
                  {t('The selected policy revision could not be loaded.')}
                </div>
              ) : null}

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
                    <FormLabel>{t('Rollback reason')}</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={4}
                        maxLength={512}
                        placeholder={t(
                          'Describe why the current revision must be rolled back'
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
                aria-labelledby='rollback-approval-heading'
              >
                <div className='flex items-start justify-between gap-3'>
                  <div className='min-w-0'>
                    <h3
                      id='rollback-approval-heading'
                      className='flex items-center gap-2 text-sm font-semibold'
                    >
                      <ShieldCheck className='size-4' aria-hidden='true' />
                      {t('Rollback approval')}
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
                  <>
                    <Progress
                      aria-label={t('Rollback approval progress')}
                      value={Math.min(
                        100,
                        (targetApprovalCount / requiredApprovals) * 100
                      )}
                    />
                    <div className='bg-muted/40 grid grid-cols-2 gap-3 rounded-lg border p-3 text-sm'>
                      <div>
                        <div className='text-muted-foreground text-xs'>
                          {t('Expected revision')}
                        </div>
                        <div className='mt-1 font-medium'>
                          r{approvalsQuery.data?.expected_revision}
                        </div>
                      </div>
                      <div>
                        <div className='text-muted-foreground text-xs'>
                          {t('Target revision')}
                        </div>
                        <div className='mt-1 font-medium'>
                          r{approvalsQuery.data?.target_revision}
                        </div>
                      </div>
                    </div>
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'Approvals are bound to the current policy head, target revision, stage, traffic allocation, and reason.'
                      )}
                    </p>
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'Your own approval is recorded for audit but does not count when you execute this rollback.'
                      )}
                    </p>
                  </>
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

                {approvalsQuery.isError ? (
                  <Button
                    type='button'
                    size='sm'
                    variant='outline'
                    onClick={() => void approvalsQuery.refetch()}
                  >
                    <RefreshCw aria-hidden='true' />
                    {t('Refresh approval status')}
                  </Button>
                ) : null}

                {approveRollback.isError ? (
                  <div
                    className='border-destructive/30 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm'
                    role='alert'
                  >
                    {rollbackRequestErrorMessage(approveRollback.error, t)}
                  </div>
                ) : null}
              </section>

              {rollback.isError ? (
                <div
                  className='border-destructive/30 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm'
                  role='alert'
                >
                  {rollbackRequestErrorMessage(rollback.error, t)}
                </div>
              ) : null}
            </form>
          </Form>

          <SheetFooter className={sideDrawerFooterClassName()}>
            <Button
              type='button'
              variant='outline'
              disabled={
                !props.canDeploy ||
                approveRollback.isPending ||
                rollback.isPending ||
                !approvalReady ||
                !requiresApproval
              }
              onClick={() =>
                void form.handleSubmit((values) =>
                  approveRollback.mutate(values)
                )()
              }
            >
              <BadgeCheck aria-hidden='true' />
              {approveButtonLabel}
            </Button>
            <Button
              type='submit'
              form='channel-routing-policy-rollback-form'
              variant='destructive'
              disabled={
                !props.canDeploy ||
                approveRollback.isPending ||
                rollback.isPending ||
                !sourceQuery.data ||
                !approvalReady ||
                (requiresApproval && !quorum)
              }
            >
              <RotateCcw aria-hidden='true' />
              {t('Execute rollback')}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      <ConfirmDialog
        open={confirmationValues != null}
        onOpenChange={(open) => {
          if (!open && !rollback.isPending) setConfirmationValues(null)
        }}
        title={t('Rollback to revision {{revision}}?', {
          revision: confirmationValues?.sourceRevision,
        })}
        desc={t(
          'The current revision remains in history. A new revision will activate the selected older policy.'
        )}
        confirmText={
          rollback.isPending ? t('Rolling back') : t('Rollback policy')
        }
        destructive
        isLoading={rollback.isPending}
        handleConfirm={() => {
          if (confirmationValues) rollback.mutate(confirmationValues)
        }}
      >
        {confirmationValues ? (
          <dl className='bg-muted/40 grid gap-2 rounded-lg border p-3 text-sm'>
            <div className='flex justify-between gap-3'>
              <dt className='text-muted-foreground'>{t('Source revision')}</dt>
              <dd>r{confirmationValues.sourceRevision}</dd>
            </div>
            <div className='flex justify-between gap-3'>
              <dt className='text-muted-foreground'>{t('Stage')}</dt>
              <dd>
                <ChannelRoutingStatusBadge status={confirmationValues.stage} />
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
