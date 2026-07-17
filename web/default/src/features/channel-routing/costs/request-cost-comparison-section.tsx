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
import { Alert02Icon, CalculatorIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { useForm, useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Empty, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
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
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'

import {
  estimateChannelRoutingCosts,
  listChannelRoutingCostCatalogMembers,
  listChannelRoutingCostCatalogPools,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingIdentityText } from '../components/identity-text'
import {
  buildCostComparisonRequest,
  costComparisonDefaultValues,
  costComparisonFormSchema,
  getCurrentRoutingCostComparison,
  type CostComparisonFormValues,
} from '../lib/cost-comparison'
import { RequestCostResults } from './request-cost-results'

type CostNumberFieldName =
  | 'input_tokens'
  | 'maximum_input_tokens'
  | 'output_tokens'
  | 'maximum_output_tokens'
  | 'cache_read_tokens'
  | 'cache_write_tokens'
  | 'cache_write_1h_tokens'
  | 'image_input_tokens'
  | 'image_output_tokens'
  | 'audio_input_tokens'
  | 'audio_output_tokens'
  | 'image_units'
  | 'audio_seconds'
  | 'video_seconds'
  | 'task_units'
  | 'max_attempts'
  | 'retry_probability'
  | 'hedge_probability'

function CostNumberField(props: {
  name: CostNumberFieldName
  label: string
  min?: number
  max?: number
  step?: number | 'any'
}) {
  const form = useFormContext<CostComparisonFormValues>()
  return (
    <FormField
      control={form.control}
      name={props.name}
      render={({ field }) => (
        <FormItem>
          <FormLabel>{props.label}</FormLabel>
          <FormControl>
            <Input
              {...field}
              type='number'
              inputMode='decimal'
              min={props.min ?? 0}
              max={props.max ?? 1_000_000_000_000}
              step={props.step ?? 1}
            />
          </FormControl>
          <FormMessage />
        </FormItem>
      )}
    />
  )
}

export function RequestCostComparisonSection() {
  const { t } = useTranslation()
  const [selectedMembers, setSelectedMembers] = useState<Set<number>>(
    () => new Set()
  )
  const [comparisonCatalogUpdatedAt, setComparisonCatalogUpdatedAt] = useState<
    number | null
  >(null)
  const form = useForm<CostComparisonFormValues>({
    resolver: zodResolver(costComparisonFormSchema),
    defaultValues: costComparisonDefaultValues,
  })
  const source = useWatch({ control: form.control, name: 'source' })
  const poolId = Number(useWatch({ control: form.control, name: 'pool_id' }))
  const pools = useQuery({
    queryKey: channelRoutingQueryKeys.costCatalogPools({
      page: 1,
      page_size: 100,
    }),
    queryFn: () =>
      listChannelRoutingCostCatalogPools({ page: 1, page_size: 100 }),
    meta: { handleErrorLocally: true },
  })
  const members = useQuery({
    queryKey: channelRoutingQueryKeys.costCatalogMembers(poolId || 0, {
      page: 1,
      page_size: 100,
    }),
    queryFn: () =>
      listChannelRoutingCostCatalogMembers(poolId, {
        page: 1,
        page_size: 100,
      }),
    enabled: poolId > 0,
    meta: { handleErrorLocally: true },
  })
  const comparison = useMutation({
    mutationFn: (input: {
      values: CostComparisonFormValues
      catalogUpdatedAt: number
    }) =>
      estimateChannelRoutingCosts(
        buildCostComparisonRequest(input.values, [...selectedMembers])
      ),
    onMutate: () => setComparisonCatalogUpdatedAt(null),
    onSuccess: (_result, input) =>
      setComparisonCatalogUpdatedAt(input.catalogUpdatedAt),
    meta: { handleErrorLocally: true },
  })
  const resetComparison = () => {
    setComparisonCatalogUpdatedAt(null)
    comparison.reset()
  }
  const currentComparison = getCurrentRoutingCostComparison({
    result: comparison.data,
    comparisonCatalogUpdatedAt,
    catalog: pools.data,
    catalogUpdatedAt: pools.dataUpdatedAt,
    catalogFetching: pools.isFetching,
    catalogError: pools.isError,
  })
  const toggleMember = (memberId: number, checked: boolean) => {
    resetComparison()
    setSelectedMembers((current) => {
      const next = new Set(current)
      if (checked) {
        next.add(memberId)
      } else {
        next.delete(memberId)
      }
      return next
    })
  }

  let memberPickerContent: ReactNode = (
    <Empty className='min-h-40 rounded-none border-0 p-4'>
      <EmptyHeader>
        <EmptyDescription>
          {t('Select a pool to choose candidate channels.')}
        </EmptyDescription>
      </EmptyHeader>
    </Empty>
  )
  if (poolId > 0 && members.isLoading) {
    memberPickerContent = (
      <div
        className='flex flex-col gap-2 p-3'
        role='status'
        aria-live='polite'
        aria-busy='true'
      >
        <span className='sr-only'>{t('Loading')}</span>
        {Array.from({ length: 4 }, (_, index) => (
          <Skeleton key={index} className='h-10 motion-reduce:animate-none' />
        ))}
      </div>
    )
  } else if (poolId > 0 && members.isError) {
    memberPickerContent = (
      <div className='p-3'>
        <Alert variant='destructive' role='alert'>
          <HugeiconsIcon
            icon={Alert02Icon}
            strokeWidth={2}
            aria-hidden='true'
          />
          <AlertTitle>{t('Candidate channels unavailable')}</AlertTitle>
          <AlertDescription className='flex flex-col items-start gap-3'>
            <span>{t('Unable to load candidate channels.')}</span>
            <Button
              type='button'
              size='sm'
              variant='outline'
              onClick={() => void members.refetch()}
            >
              {t('Retry')}
            </Button>
          </AlertDescription>
        </Alert>
      </div>
    )
  } else if (members.data?.items.length) {
    memberPickerContent = (
      <div className='divide-y'>
        {members.data.items.map((member) => {
          const checked = selectedMembers.has(member.member_id)
          const id = `cost-member-${member.member_id}`
          return (
            <div
              key={member.member_id}
              className='flex items-center gap-3 px-3 py-2'
            >
              <Checkbox
                id={id}
                checked={checked}
                onCheckedChange={(value) =>
                  toggleMember(member.member_id, value === true)
                }
              />
              <Label htmlFor={id} className='min-w-0 flex-1'>
                <span className='block min-w-0 text-sm font-medium break-words'>
                  {member.channel_name || `#${member.channel_id}`}
                </span>
                <ChannelRoutingIdentityText
                  text={member.routing_generation}
                  className='text-muted-foreground mt-0.5 font-mono text-[11px]'
                  withinInteractive
                  breakAll
                />
              </Label>
              <span className='text-muted-foreground shrink-0 text-xs tabular-nums'>
                {member.upstream_cost_multiplier}×
              </span>
            </div>
          )
        })}
      </div>
    )
  } else if (poolId > 0) {
    memberPickerContent = (
      <Empty className='min-h-40 rounded-none border-0 p-4'>
        <EmptyHeader>
          <EmptyDescription>
            {t('No channels are available in this pool.')}
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    )
  }

  return (
    <div className='flex flex-col gap-5'>
      <div className='border-y py-2'>
        <p className='text-muted-foreground max-w-3xl text-sm'>
          {t(
            'Compare complete request cost across eligible channel lifecycles using one shared request profile and one pinned pricing snapshot.'
          )}
        </p>
      </div>

      <Form {...form}>
        <form
          className='flex flex-col gap-5'
          noValidate
          onChangeCapture={() => {
            if (comparison.data || comparison.error) {
              resetComparison()
            }
          }}
          onSubmit={form.handleSubmit((values) =>
            comparison.mutate({
              values,
              catalogUpdatedAt: pools.dataUpdatedAt,
            })
          )}
        >
          <section className='grid gap-4 rounded-lg border p-4 md:grid-cols-2 xl:grid-cols-4'>
            <FormField
              control={form.control}
              name='source'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Profile source')}</FormLabel>
                  <FormControl>
                    <NativeSelect
                      {...field}
                      className='w-full'
                      onChange={(event) => {
                        const nextSource = event.target.value
                        field.onChange(event)
                        if (nextSource === 'recent_decision') {
                          form.setValue('model_name', '')
                        } else {
                          form.setValue('decision_id', '')
                        }
                        resetComparison()
                      }}
                    >
                      <NativeSelectOption value='manual'>
                        {t('Manual profile')}
                      </NativeSelectOption>
                      <NativeSelectOption value='recent_decision'>
                        {t('Recent decision')}
                      </NativeSelectOption>
                    </NativeSelect>
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='pool_id'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Pool')}</FormLabel>
                  <FormControl>
                    <NativeSelect
                      {...field}
                      className='w-full'
                      disabled={pools.isLoading || pools.isError}
                      onChange={(event) => {
                        field.onChange(event)
                        setSelectedMembers(new Set())
                        resetComparison()
                      }}
                    >
                      <NativeSelectOption value=''>
                        {t('Select a routing pool')}
                      </NativeSelectOption>
                      {pools.data?.items.map((pool) => (
                        <NativeSelectOption
                          key={pool.pool_id}
                          value={String(pool.pool_id)}
                        >
                          {pool.display_name || pool.group_name}
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
              name='model_name'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Model name')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      disabled={source === 'recent_decision'}
                      placeholder={t('Enter model name')}
                    />
                  </FormControl>
                  <FormDescription>
                    {source === 'recent_decision'
                      ? t(
                          'The recent decision supplies the model when this is empty.'
                        )
                      : t('Use the logical model requested by the client.')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='decision_id'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Decision ID')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      disabled={source === 'manual'}
                      placeholder={t('Enter a recent decision ID')}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </section>

          {pools.isError ? (
            <Alert variant='destructive' role='alert'>
              <HugeiconsIcon
                icon={Alert02Icon}
                strokeWidth={2}
                aria-hidden='true'
              />
              <AlertTitle>{t('Routing pools unavailable')}</AlertTitle>
              <AlertDescription className='flex flex-col items-start gap-3'>
                <span>
                  {t('Unable to load routing pools for cost comparison.')}
                </span>
                <Button
                  type='button'
                  size='sm'
                  variant='outline'
                  onClick={() => void pools.refetch()}
                >
                  {t('Retry')}
                </Button>
              </AlertDescription>
            </Alert>
          ) : null}

          {source === 'manual' ? (
            <section
              className='flex flex-col gap-4'
              aria-labelledby='manual-cost-profile'
            >
              <div>
                <h3 id='manual-cost-profile' className='text-sm font-semibold'>
                  {t('Manual request profile')}
                </h3>
                <p className='text-muted-foreground mt-1 text-xs'>
                  {t(
                    'Leave an unknown quantity blank. Entering 0 explicitly records a known zero quantity.'
                  )}
                </p>
              </div>
              <div className='grid gap-4 sm:grid-cols-2 lg:grid-cols-4'>
                <CostNumberField
                  name='input_tokens'
                  label={t('Input tokens')}
                />
                <CostNumberField
                  name='maximum_input_tokens'
                  label={t('Maximum input tokens')}
                />
                <CostNumberField
                  name='output_tokens'
                  label={t('Output tokens')}
                />
                <CostNumberField
                  name='maximum_output_tokens'
                  label={t('Maximum output tokens')}
                />
                <CostNumberField
                  name='cache_read_tokens'
                  label={t('Cache read tokens')}
                />
                <CostNumberField
                  name='cache_write_tokens'
                  label={t('Cache write tokens')}
                />
                <CostNumberField
                  name='cache_write_1h_tokens'
                  label={t('1h cache write tokens')}
                />
                <CostNumberField
                  name='image_input_tokens'
                  label={t('Image input tokens')}
                />
                <CostNumberField
                  name='image_output_tokens'
                  label={t('Image output tokens')}
                />
                <CostNumberField
                  name='audio_input_tokens'
                  label={t('Audio input tokens')}
                />
                <CostNumberField
                  name='audio_output_tokens'
                  label={t('Audio output tokens')}
                />
                <CostNumberField
                  name='image_units'
                  label={t('Image units')}
                  step='any'
                />
                <CostNumberField
                  name='audio_seconds'
                  label={t('Audio seconds')}
                  step='any'
                />
                <CostNumberField
                  name='video_seconds'
                  label={t('Video seconds')}
                  step='any'
                />
                <CostNumberField
                  name='task_units'
                  label={t('Task units')}
                  step='any'
                />
              </div>

              <div className='grid gap-4 border-t pt-4 sm:grid-cols-2 lg:grid-cols-4'>
                <CostNumberField
                  name='max_attempts'
                  label={t('Maximum attempts')}
                  min={1}
                  max={16}
                />
                <CostNumberField
                  name='retry_probability'
                  label={t('Retry probability')}
                  max={1}
                  step='any'
                />
                <CostNumberField
                  name='hedge_probability'
                  label={t('Hedge probability')}
                  max={1}
                  step='any'
                />
                <FormField
                  control={form.control}
                  name='hedge_allowed'
                  render={({ field }) => (
                    <FormItem className='flex items-center justify-between gap-3 rounded-lg border px-3 py-2'>
                      <div>
                        <FormLabel>{t('Allow hedge')}</FormLabel>
                        <FormDescription>
                          {t('Include expected speculative request cost.')}
                        </FormDescription>
                      </div>
                      <FormControl>
                        <Switch
                          checked={field.value}
                          onCheckedChange={(checked) =>
                            field.onChange(checked === true)
                          }
                        />
                      </FormControl>
                    </FormItem>
                  )}
                />
              </div>
            </section>
          ) : null}

          <section
            className='flex flex-col gap-2'
            aria-labelledby='candidate-members'
          >
            <div className='flex flex-wrap items-center justify-between gap-2'>
              <div>
                <h3 id='candidate-members' className='text-sm font-semibold'>
                  {t('Candidate channels')}
                </h3>
                <p className='text-muted-foreground mt-1 text-xs'>
                  {selectedMembers.size === 0
                    ? t('All members in the selected pool will be compared.')
                    : t('{{count}} members selected', {
                        count: selectedMembers.size,
                      })}
                </p>
              </div>
              {selectedMembers.size > 0 ? (
                <Button
                  type='button'
                  size='sm'
                  variant='ghost'
                  onClick={() => {
                    setSelectedMembers(new Set())
                    resetComparison()
                  }}
                >
                  {t('Use all members')}
                </Button>
              ) : null}
            </div>
            <ScrollArea className='h-44 rounded-lg border'>
              {memberPickerContent}
            </ScrollArea>
          </section>

          {comparison.isError ? (
            <Alert variant='destructive' role='alert'>
              <HugeiconsIcon icon={Alert02Icon} strokeWidth={2} />
              <AlertTitle>{t('Request cost comparison failed')}</AlertTitle>
              <AlertDescription>
                {t(
                  'The comparison was not saved. Review the request profile and try again.'
                )}
              </AlertDescription>
            </Alert>
          ) : null}

          <div className='flex justify-end border-t pt-3'>
            <Button
              type='submit'
              disabled={
                comparison.isPending ||
                pools.isFetching ||
                pools.isError ||
                members.isFetching ||
                members.isError
              }
            >
              {comparison.isPending ? (
                <Spinner data-icon='inline-start' />
              ) : (
                <HugeiconsIcon
                  icon={CalculatorIcon}
                  data-icon='inline-start'
                  strokeWidth={2}
                />
              )}
              {comparison.isPending ? t('Comparing costs') : t('Compare costs')}
            </Button>
          </div>
        </form>
      </Form>

      {currentComparison ? (
        <RequestCostResults result={currentComparison} />
      ) : null}
    </div>
  )
}
