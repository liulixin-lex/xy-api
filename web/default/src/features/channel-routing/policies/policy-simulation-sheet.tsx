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
import { FlaskConicalIcon, PlayIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useRef } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import z from 'zod'

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
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'

import {
  createChannelRoutingIdempotencyKey,
  getChannelRoutingPolicyDraft,
  simulateChannelRoutingPolicyDraft,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingHistoricalSimulationResults } from '../components/historical-simulation-results'
import {
  ChannelRoutingEmptyState,
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
} from '../components/page-state'
import { policySimulationDraftState } from '../lib/policy-simulation'
import type { PolicyActivationSpec, PolicyDraftSummary } from '../types'
import { ChannelRoutingPolicySimulationRiskSection } from './policy-simulation-risk-section'

type PolicySimulationFormValues = {
  poolId: number
  cursor: number
  limit: number
  targetStage: PolicyActivationSpec['stage']
  targetTrafficBasisPoints: number
}

export function ChannelRoutingPolicySimulationSheet(props: {
  draft: PolicyDraftSummary | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const idempotencyRef = useRef<{ signature: string; key: string } | null>(null)
  const schema = useMemo(
    () =>
      z
        .object({
          poolId: z
            .number({ error: t('Enter a valid number') })
            .int(t('Value must be an integer'))
            .min(1, t('Select a routing group')),
          cursor: z
            .number({ error: t('Enter a valid number') })
            .int(t('Value must be an integer'))
            .min(0, t('Cursor cannot be negative')),
          limit: z
            .number({ error: t('Enter a valid number') })
            .int(t('Value must be an integer'))
            .min(
              1,
              t('Value must be between {{min}} and {{max}}', {
                min: 1,
                max: 50,
              })
            )
            .max(
              50,
              t('Value must be between {{min}} and {{max}}', {
                min: 1,
                max: 50,
              })
            ),
          targetStage: z.enum(['observe', 'shadow', 'canary', 'active']),
          targetTrafficBasisPoints: z
            .number({ error: t('Enter a valid number') })
            .int(t('Value must be an integer'))
            .min(0, t('Traffic allocation cannot be negative'))
            .max(500, t('Canary traffic must be between 1% and 5%')),
        })
        .superRefine((values, context) => {
          if (
            values.targetStage === 'canary' &&
            (values.targetTrafficBasisPoints < 100 ||
              values.targetTrafficBasisPoints > 500)
          ) {
            context.addIssue({
              code: 'custom',
              path: ['targetTrafficBasisPoints'],
              message: t('Canary traffic must be between 1% and 5%'),
            })
          }
          if (
            values.targetStage !== 'canary' &&
            values.targetTrafficBasisPoints !== 0
          ) {
            context.addIssue({
              code: 'custom',
              path: ['targetTrafficBasisPoints'],
              message: t('Traffic allocation must be zero outside Canary'),
            })
          }
        }),
    [t]
  )
  const form = useForm<PolicySimulationFormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      poolId: 0,
      cursor: 0,
      limit: 50,
      targetStage: 'canary',
      targetTrafficBasisPoints: 100,
    },
  })
  const targetStage = useWatch({ control: form.control, name: 'targetStage' })
  const detailQuery = useQuery({
    queryKey: channelRoutingQueryKeys.policyDraft(props.draft?.id ?? 0),
    queryFn: ({ signal }) =>
      getChannelRoutingPolicyDraft(props.draft?.id ?? 0, signal),
    enabled: props.open && props.draft != null,
    meta: { handleErrorLocally: true },
  })
  const poolOptions = useMemo(() => {
    const pools = detailQuery.data?.document.pools ?? []
    const options: Array<{ id: number; label: string }> = []
    for (const pool of pools) {
      if (pool == null || typeof pool !== 'object' || Array.isArray(pool)) {
        continue
      }
      const candidate = pool as Record<string, unknown>
      if (
        !Number.isInteger(candidate.pool_id) ||
        Number(candidate.pool_id) <= 0
      ) {
        continue
      }
      let name = `#${String(candidate.pool_id)}`
      if (
        typeof candidate.display_name === 'string' &&
        candidate.display_name.trim()
      ) {
        name = candidate.display_name.trim()
      } else if (
        typeof candidate.group_name === 'string' &&
        candidate.group_name.trim()
      ) {
        name = candidate.group_name.trim()
      }
      options.push({ id: Number(candidate.pool_id), label: name })
    }
    return options
  }, [detailQuery.data])
  const simulation = useMutation({
    mutationFn: (input: {
      values: PolicySimulationFormValues
      cursor?: number
    }) => {
      if (!props.draft) throw new Error('Policy draft is required')
      const cursor = input.cursor ?? input.values.cursor
      const payload = {
        pool_id: input.values.poolId,
        cursor: cursor || undefined,
        limit: input.values.limit,
        target_stage: input.values.targetStage,
        target_traffic_basis_points: input.values.targetTrafficBasisPoints,
      }
      const signature = JSON.stringify({
        draft_id: props.draft.id,
        draft_version: props.draft.version,
        draft_etag: props.draft.etag,
        payload,
      })
      if (idempotencyRef.current?.signature !== signature) {
        idempotencyRef.current = {
          signature,
          key: createChannelRoutingIdempotencyKey('policy-simulation'),
        }
      }
      return simulateChannelRoutingPolicyDraft(
        props.draft,
        payload,
        idempotencyRef.current.key
      )
    },
    onSuccess: async (response, input) => {
      idempotencyRef.current = null
      if (props.draft) {
        queryClient.setQueryData(
          channelRoutingQueryKeys.policySimulationRisk(props.draft.id, {
            stage: input.values.targetStage,
            traffic_basis_points: input.values.targetTrafficBasisPoints,
          }),
          response
        )
      }
      await queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.operationsRoot(),
      })
      toast.success(t('Policy simulation completed'))
    },
    meta: { handleErrorLocally: true },
  })
  const resetSimulation = simulation.reset
  const draftState = policySimulationDraftState({
    hasDetail: detailQuery.data != null,
    loading: detailQuery.isLoading,
    error: detailQuery.isError,
    poolCount: poolOptions.length,
  })

  useEffect(() => {
    if (!props.open) return
    form.reset({
      poolId: poolOptions[0]?.id ?? 0,
      cursor: 0,
      limit: 50,
      targetStage: 'canary',
      targetTrafficBasisPoints: 100,
    })
    idempotencyRef.current = null
    resetSimulation()
  }, [form, poolOptions, props.draft?.id, props.open, resetSimulation])

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent
        className={sideDrawerContentClassName(
          'channel-routing-touch-surface max-w-none max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11 sm:!max-w-4xl'
        )}
      >
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle className='flex items-center gap-2'>
            <HugeiconsIcon
              icon={FlaskConicalIcon}
              className='size-4'
              strokeWidth={2}
              aria-hidden='true'
            />
            {t('Simulate policy draft #{{id}}', { id: props.draft?.id })}
          </SheetTitle>
          <SheetDescription>
            {t(
              'Replay historical decisions against this validated policy document.'
            )}
          </SheetDescription>
        </SheetHeader>

        <Form {...form}>
          <form
            id='channel-routing-policy-simulation-form'
            className={sideDrawerFormClassName('gap-5')}
            onSubmit={form.handleSubmit((values) => {
              if (draftState !== 'ready') return
              simulation.mutate({ values })
            })}
          >
            {draftState === 'loading' ? (
              <ChannelRoutingLoadingState rows={3} />
            ) : null}
            {draftState === 'error' ? (
              <ChannelRoutingErrorState
                error={detailQuery.error}
                onRetry={() => void detailQuery.refetch()}
              />
            ) : null}
            {draftState === 'empty' ? (
              <ChannelRoutingEmptyState
                title={t('No policy pools')}
                description={t(
                  'No routing groups are available in the current snapshot.'
                )}
              />
            ) : null}
            {draftState === 'ready' ? (
              <>
                <div className='grid gap-3 sm:grid-cols-3'>
                  <FormField
                    control={form.control}
                    name='poolId'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Routing group')}</FormLabel>
                        <FormControl>
                          <NativeSelect
                            className='w-full'
                            value={String(field.value)}
                            onChange={(event) =>
                              field.onChange(Number(event.target.value))
                            }
                          >
                            {poolOptions.map((pool) => (
                              <NativeSelectOption key={pool.id} value={pool.id}>
                                {pool.label} (#{pool.id})
                              </NativeSelectOption>
                            ))}
                          </NativeSelect>
                        </FormControl>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                  <FormField
                    control={form.control}
                    name='cursor'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Start cursor')}</FormLabel>
                        <FormControl>
                          <Input
                            type='number'
                            min={0}
                            value={field.value}
                            onChange={(event) =>
                              field.onChange(event.target.valueAsNumber)
                            }
                          />
                        </FormControl>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                  <FormField
                    control={form.control}
                    name='limit'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Samples')}</FormLabel>
                        <FormControl>
                          <Input
                            type='number'
                            min={1}
                            max={50}
                            value={field.value}
                            onChange={(event) =>
                              field.onChange(event.target.valueAsNumber)
                            }
                          />
                        </FormControl>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                </div>

                <section
                  className='flex flex-col gap-3 border-t pt-4'
                  aria-labelledby='policy-simulation-target'
                >
                  <div>
                    <h3
                      id='policy-simulation-target'
                      className='text-sm font-semibold'
                    >
                      {t('Simulation deployment target')}
                    </h3>
                    <p className='text-muted-foreground mt-1 text-xs'>
                      {t(
                        'Failure evidence applies only to this exact stage and Canary traffic allocation.'
                      )}
                    </p>
                  </div>
                  <div className='grid gap-3 sm:grid-cols-2'>
                    <FormField
                      control={form.control}
                      name='targetStage'
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
                                  'targetTrafficBasisPoints',
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
                      name='targetTrafficBasisPoints'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Traffic allocation')}</FormLabel>
                          <FormControl>
                            <Input
                              type='number'
                              min={targetStage === 'canary' ? 100 : 0}
                              max={targetStage === 'canary' ? 500 : 0}
                              step={100}
                              disabled={targetStage !== 'canary'}
                              value={field.value}
                              onChange={(event) =>
                                field.onChange(event.target.valueAsNumber)
                              }
                            />
                          </FormControl>
                          <FormDescription>
                            {targetStage === 'canary'
                              ? t('Enter 100 to 500 basis points')
                              : t('Traffic allocation is zero outside Canary')}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  </div>
                </section>

                {simulation.isError ? (
                  <div className='border-destructive/30 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm'>
                    {t(
                      'Policy simulation failed. Refresh the draft and try again.'
                    )}
                  </div>
                ) : null}

                {simulation.data ? (
                  <div className='space-y-5'>
                    <ChannelRoutingPolicySimulationRiskSection
                      risk={simulation.data.result.risk}
                    />
                    <ChannelRoutingHistoricalSimulationResults
                      result={simulation.data.result}
                      pending={simulation.isPending}
                      onNextBatch={(cursor) => {
                        form.setValue('cursor', cursor, { shouldDirty: true })
                        void form.handleSubmit((values) =>
                          simulation.mutate({ values, cursor })
                        )()
                      }}
                    />
                  </div>
                ) : null}
              </>
            ) : null}
          </form>
        </Form>

        <SheetFooter className={sideDrawerFooterClassName()}>
          <Button
            type='submit'
            form='channel-routing-policy-simulation-form'
            disabled={simulation.isPending || draftState !== 'ready'}
          >
            <HugeiconsIcon
              icon={PlayIcon}
              data-icon='inline-start'
              strokeWidth={2}
              aria-hidden='true'
            />
            {simulation.isPending
              ? t('Running simulation')
              : t('Run simulation')}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
