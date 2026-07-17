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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useRef } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
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
  simulateChannelRoutingGroup,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingHistoricalSimulationResults } from '../components/historical-simulation-results'

type SimulationFormValues = {
  limit: number
  weightAvailability: number
  weightLatency: number
  weightThroughput: number
  weightCost: number
  topK: number
}

export function ChannelRoutingSimulationSheet(props: {
  poolId: number
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
          weightAvailability: z
            .number({ error: t('Enter a valid number') })
            .min(
              0,
              t('Value must be between {{min}} and {{max}}', { min: 0, max: 1 })
            )
            .max(
              1,
              t('Value must be between {{min}} and {{max}}', { min: 0, max: 1 })
            ),
          weightLatency: z
            .number({ error: t('Enter a valid number') })
            .min(
              0,
              t('Value must be between {{min}} and {{max}}', { min: 0, max: 1 })
            )
            .max(
              1,
              t('Value must be between {{min}} and {{max}}', { min: 0, max: 1 })
            ),
          weightThroughput: z
            .number({ error: t('Enter a valid number') })
            .min(
              0,
              t('Value must be between {{min}} and {{max}}', { min: 0, max: 1 })
            )
            .max(
              1,
              t('Value must be between {{min}} and {{max}}', { min: 0, max: 1 })
            ),
          weightCost: z
            .number({ error: t('Enter a valid number') })
            .min(
              0,
              t('Value must be between {{min}} and {{max}}', { min: 0, max: 1 })
            )
            .max(
              1,
              t('Value must be between {{min}} and {{max}}', { min: 0, max: 1 })
            ),
          topK: z
            .number({ error: t('Enter a valid number') })
            .int(t('Value must be an integer'))
            .min(
              1,
              t('Value must be between {{min}} and {{max}}', {
                min: 1,
                max: 64,
              })
            )
            .max(
              64,
              t('Value must be between {{min}} and {{max}}', {
                min: 1,
                max: 64,
              })
            ),
        })
        .refine(
          (value) =>
            value.weightAvailability +
              value.weightLatency +
              value.weightThroughput +
              value.weightCost >
            0,
          {
            path: ['weightAvailability'],
            message: t('At least one weight is required'),
          }
        ),
    [t]
  )
  const form = useForm<SimulationFormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      limit: 50,
      weightAvailability: 0.35,
      weightLatency: 0.25,
      weightThroughput: 0.2,
      weightCost: 0.2,
      topK: 8,
    },
  })
  const simulation = useMutation({
    mutationFn: (input: { values: SimulationFormValues; cursor?: number }) => {
      const payload = {
        cursor: input.cursor || undefined,
        limit: input.values.limit,
        selector: {
          weight_availability: input.values.weightAvailability,
          weight_latency: input.values.weightLatency,
          weight_throughput: input.values.weightThroughput,
          weight_cost: input.values.weightCost,
          top_k: input.values.topK,
        },
      }
      const signature = JSON.stringify(payload)
      if (idempotencyRef.current?.signature !== signature) {
        idempotencyRef.current = {
          signature,
          key: createChannelRoutingIdempotencyKey('historical-simulation'),
        }
      }
      return simulateChannelRoutingGroup(
        props.poolId,
        payload,
        idempotencyRef.current.key
      )
    },
    onSuccess: async () => {
      idempotencyRef.current = null
      await queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.operationsRoot(),
      })
    },
    meta: { handleErrorLocally: true },
  })
  const resetSimulation = simulation.reset

  useEffect(() => {
    if (!props.open) return
    idempotencyRef.current = null
    resetSimulation()
  }, [props.open, props.poolId, resetSimulation])

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent
        className={sideDrawerContentClassName(
          'channel-routing-touch-surface max-w-none max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11 sm:!max-w-3xl'
        )}
      >
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle className='flex items-center gap-2'>
            <HugeiconsIcon
              icon={FlaskConicalIcon}
              className='size-4'
              aria-hidden='true'
            />
            {t('Historical simulation')}
          </SheetTitle>
          <SheetDescription>
            {t(
              'Compare selector weights against replayable routing decisions.'
            )}
          </SheetDescription>
        </SheetHeader>

        <Form {...form}>
          <form
            id='channel-routing-simulation-form'
            className={sideDrawerFormClassName('gap-5')}
            onSubmit={form.handleSubmit((values) =>
              simulation.mutate({ values })
            )}
          >
            <div className='grid grid-cols-2 gap-3 sm:grid-cols-3'>
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
              <FormField
                control={form.control}
                name='topK'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Top candidates')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={64}
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

            <div className='grid grid-cols-2 gap-3 sm:grid-cols-4'>
              {(
                [
                  ['weightAvailability', 'Availability'],
                  ['weightLatency', 'Latency'],
                  ['weightThroughput', 'Throughput'],
                  ['weightCost', 'Cost'],
                ] as const
              ).map(([name, label]) => (
                <FormField
                  key={name}
                  control={form.control}
                  name={name}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t(label)}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={0}
                          max={1}
                          step={0.05}
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
              ))}
            </div>

            {simulation.isError ? (
              <div className='border-destructive/30 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm'>
                {t(
                  'Simulation failed. Review the selector values and try again.'
                )}
              </div>
            ) : null}

            {simulation.data ? (
              <ChannelRoutingHistoricalSimulationResults
                result={simulation.data.result}
                pending={simulation.isPending}
                onNextBatch={(cursor) => {
                  void form.handleSubmit((values) =>
                    simulation.mutate({ values, cursor })
                  )()
                }}
              />
            ) : null}
          </form>
        </Form>

        <SheetFooter className={sideDrawerFooterClassName()}>
          <Button
            type='submit'
            form='channel-routing-simulation-form'
            disabled={simulation.isPending}
          >
            <HugeiconsIcon
              icon={PlayIcon}
              data-icon='inline-start'
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
