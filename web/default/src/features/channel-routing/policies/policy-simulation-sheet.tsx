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
import { FlaskConical, Play } from 'lucide-react'
import { useEffect, useMemo, useRef } from 'react'
import { useForm } from 'react-hook-form'
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
import type { PolicyDraftSummary } from '../types'
import { ChannelRoutingPolicySimulationRiskSection } from './policy-simulation-risk-section'

type PolicySimulationFormValues = {
  poolId: number
  cursor: number
  limit: number
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
      z.object({
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
            t('Value must be between {{min}} and {{max}}', { min: 1, max: 50 })
          )
          .max(
            50,
            t('Value must be between {{min}} and {{max}}', { min: 1, max: 50 })
          ),
      }),
    [t]
  )
  const form = useForm<PolicySimulationFormValues>({
    resolver: zodResolver(schema),
    defaultValues: { poolId: 0, cursor: 0, limit: 50 },
  })
  const detailQuery = useQuery({
    queryKey: channelRoutingQueryKeys.policyDraft(props.draft?.id ?? 0),
    queryFn: () => getChannelRoutingPolicyDraft(props.draft?.id ?? 0),
    enabled: props.open && props.draft != null,
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
    onSuccess: async (response) => {
      idempotencyRef.current = null
      if (props.draft) {
        queryClient.setQueryData(
          channelRoutingQueryKeys.policySimulationRisk(props.draft.id),
          response.result.risk ?? null
        )
      }
      await queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.operationsRoot(),
      })
      toast.success(t('Policy simulation completed'))
    },
  })
  const resetSimulation = simulation.reset

  useEffect(() => {
    if (!props.open) return
    form.reset({ poolId: poolOptions[0]?.id ?? 0, cursor: 0, limit: 50 })
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
            <FlaskConical className='size-4' aria-hidden='true' />
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
            onSubmit={form.handleSubmit((values) =>
              simulation.mutate({ values })
            )}
          >
            <div className='grid gap-3 sm:grid-cols-3'>
              <FormField
                control={form.control}
                name='poolId'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Routing group')}</FormLabel>
                    <FormControl>
                      {poolOptions.length > 0 ? (
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
                      ) : (
                        <Input
                          type='number'
                          min={1}
                          value={field.value || ''}
                          onChange={(event) =>
                            field.onChange(event.target.valueAsNumber)
                          }
                        />
                      )}
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
          </form>
        </Form>

        <SheetFooter className={sideDrawerFooterClassName()}>
          <Button
            type='submit'
            form='channel-routing-policy-simulation-form'
            disabled={simulation.isPending || detailQuery.isLoading}
          >
            <Play aria-hidden='true' />
            {simulation.isPending
              ? t('Running simulation')
              : t('Run simulation')}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
