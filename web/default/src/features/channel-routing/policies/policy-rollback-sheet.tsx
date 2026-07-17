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
import { Alert02Icon, Undo02Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
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
import { useDebounce } from '@/hooks'

import {
  createChannelRoutingIdempotencyKey,
  getChannelRoutingPolicyApiError,
  getChannelRoutingPolicyRevision,
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
    meta: { handleErrorLocally: true },
  })
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
    resetRollback()
  }, [
    currentRevision,
    form,
    props.current?.head.current_stage,
    props.open,
    resetRollback,
  ])

  const rollbackError = getChannelRoutingPolicyApiError(rollback.error)
  let rollbackErrorDescription = t(
    'The rollback request failed. Refresh the current policy and try again.'
  )
  if (rollbackError.status === 403) {
    rollbackErrorDescription = t(
      'You do not have permission to execute this rollback.'
    )
  } else if (
    rollbackError.status === 409 ||
    rollbackError.status === 412 ||
    rollbackError.status === 428
  ) {
    rollbackErrorDescription = t(
      'The current policy changed. Refresh the page before continuing.'
    )
  }
  const sourceReady =
    sourceQuery.data?.revision.revision === sourceRevision &&
    sourceRevision > 0 &&
    sourceRevision < currentRevision

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
                icon={Undo02Icon}
                strokeWidth={2}
                aria-hidden='true'
              />
              {t('Rollback policy')}
            </SheetTitle>
            <SheetDescription>
              {t(
                'Rollback creates a new immutable revision from an older policy document and activates it at the selected stage.'
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
                <Alert variant='destructive' role='alert'>
                  <HugeiconsIcon
                    icon={Alert02Icon}
                    strokeWidth={2}
                    aria-hidden='true'
                  />
                  <AlertTitle>{t('Policy revision unavailable')}</AlertTitle>
                  <AlertDescription>
                    {t('The selected policy revision could not be loaded.')}
                  </AlertDescription>
                </Alert>
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

              {rollback.isError ? (
                <Alert variant='destructive' role='alert'>
                  <HugeiconsIcon
                    icon={Alert02Icon}
                    strokeWidth={2}
                    aria-hidden='true'
                  />
                  <AlertTitle>{t('Rollback failed')}</AlertTitle>
                  <AlertDescription>
                    {rollbackErrorDescription}
                  </AlertDescription>
                </Alert>
              ) : null}
            </form>
          </Form>

          <SheetFooter className={sideDrawerFooterClassName()}>
            <Button
              type='submit'
              form='channel-routing-policy-rollback-form'
              disabled={
                !props.canDeploy ||
                rollback.isPending ||
                sourceQuery.isLoading ||
                !sourceReady
              }
            >
              {rollback.isPending ? (
                <Spinner data-icon='inline-start' />
              ) : (
                <HugeiconsIcon
                  icon={Undo02Icon}
                  data-icon='inline-start'
                  strokeWidth={2}
                  aria-hidden='true'
                />
              )}
              {rollback.isPending ? t('Rolling back') : t('Review rollback')}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      <ConfirmDialog
        open={confirmationValues != null}
        onOpenChange={(open) => {
          if (!open && !rollback.isPending) setConfirmationValues(null)
        }}
        title={t('Roll back to revision {{revision}}?', {
          revision: confirmationValues?.sourceRevision,
        })}
        desc={t(
          'The current policy is preserved in history. Rollback creates a new higher revision from the selected document.'
        )}
        confirmText={rollback.isPending ? t('Rolling back') : t('Rollback')}
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
