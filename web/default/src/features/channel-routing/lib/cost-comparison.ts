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
import z from 'zod'

import type {
  RoutingCostCatalogMetadata,
  RoutingCostComparisonRequest,
  RoutingCostComparisonResponse,
} from '../types'

const optionalQuantity = z
  .string()
  .trim()
  .refine(
    (value) =>
      value === '' ||
      (Number.isFinite(Number(value)) &&
        Number(value) >= 0 &&
        Number(value) <= 1_000_000_000_000),
    'Enter zero or a positive finite quantity'
  )

const probability = z
  .string()
  .trim()
  .refine(
    (value) =>
      value !== '' &&
      Number.isFinite(Number(value)) &&
      Number(value) >= 0 &&
      Number(value) <= 1,
    'Enter a value from 0 to 1'
  )

export const costComparisonFormSchema = z
  .object({
    source: z.enum(['manual', 'recent_decision']),
    pool_id: z
      .string()
      .trim()
      .refine(
        (value) => Number.isInteger(Number(value)) && Number(value) > 0,
        'Select a routing pool'
      ),
    model_name: z.string().trim().max(256, 'Enter 256 characters or fewer'),
    decision_id: z.string().trim().max(64, 'Enter 64 characters or fewer'),
    input_tokens: optionalQuantity,
    maximum_input_tokens: optionalQuantity,
    output_tokens: optionalQuantity,
    maximum_output_tokens: optionalQuantity,
    cache_read_tokens: optionalQuantity,
    cache_write_tokens: optionalQuantity,
    cache_write_1h_tokens: optionalQuantity,
    image_input_tokens: optionalQuantity,
    image_output_tokens: optionalQuantity,
    audio_input_tokens: optionalQuantity,
    audio_output_tokens: optionalQuantity,
    image_units: optionalQuantity,
    audio_seconds: optionalQuantity,
    video_seconds: optionalQuantity,
    task_units: optionalQuantity,
    max_attempts: z
      .string()
      .trim()
      .refine(
        (value) =>
          Number.isInteger(Number(value)) &&
          Number(value) >= 1 &&
          Number(value) <= 16,
        'Enter a whole number from 1 to 16'
      ),
    retry_probability: probability,
    hedge_probability: probability,
    hedge_allowed: z.boolean(),
  })
  .superRefine((values, context) => {
    if (values.source === 'manual' && values.model_name === '') {
      context.addIssue({
        code: 'custom',
        path: ['model_name'],
        message: 'Enter a model name',
      })
    }
    if (values.source === 'recent_decision' && values.decision_id === '') {
      context.addIssue({
        code: 'custom',
        path: ['decision_id'],
        message: 'Enter a recent decision ID',
      })
    }
  })

export type CostComparisonFormValues = z.infer<typeof costComparisonFormSchema>

export const costComparisonDefaultValues: CostComparisonFormValues = {
  source: 'manual',
  pool_id: '',
  model_name: '',
  decision_id: '',
  input_tokens: '',
  maximum_input_tokens: '',
  output_tokens: '',
  maximum_output_tokens: '',
  cache_read_tokens: '',
  cache_write_tokens: '',
  cache_write_1h_tokens: '',
  image_input_tokens: '',
  image_output_tokens: '',
  audio_input_tokens: '',
  audio_output_tokens: '',
  image_units: '',
  audio_seconds: '',
  video_seconds: '',
  task_units: '',
  max_attempts: '1',
  retry_probability: '0',
  hedge_probability: '0',
  hedge_allowed: false,
}

export function getCurrentRoutingCostComparison<
  T extends Pick<
    RoutingCostComparisonResponse,
    'pricing_epoch' | 'pricing_hash'
  >,
>(input: {
  result?: T
  comparisonCatalogUpdatedAt: number | null
  catalog?: Pick<RoutingCostCatalogMetadata, 'pricing_epoch' | 'pricing_hash'>
  catalogUpdatedAt: number
  catalogFetching: boolean
  catalogError: boolean
}): T | undefined {
  if (
    !input.result ||
    !input.catalog ||
    input.catalogFetching ||
    input.catalogError ||
    input.comparisonCatalogUpdatedAt == null ||
    input.comparisonCatalogUpdatedAt <= 0 ||
    input.comparisonCatalogUpdatedAt !== input.catalogUpdatedAt
  ) {
    return undefined
  }
  if (
    input.result.pricing_epoch !== input.catalog.pricing_epoch ||
    input.result.pricing_hash !== input.catalog.pricing_hash
  ) {
    return undefined
  }
  return input.result
}

export function buildCostComparisonRequest(
  values: CostComparisonFormValues,
  memberIds: number[]
): RoutingCostComparisonRequest {
  const base = {
    pool_id: Number(values.pool_id),
    model_name: values.model_name.trim(),
    member_ids: [...memberIds].sort((left, right) => left - right),
  }
  if (values.source === 'recent_decision') {
    return { ...base, decision_id: values.decision_id.trim() }
  }

  const numberOrUndefined = (value: string): number | undefined => {
    const normalized = value.trim()
    return normalized === '' ? undefined : Number(normalized)
  }
  const profile = {
    input_tokens: numberOrUndefined(values.input_tokens),
    maximum_input_tokens: numberOrUndefined(values.maximum_input_tokens),
    output_tokens: numberOrUndefined(values.output_tokens),
    maximum_output_tokens: numberOrUndefined(values.maximum_output_tokens),
    cache_read_tokens: numberOrUndefined(values.cache_read_tokens),
    cache_write_tokens: numberOrUndefined(values.cache_write_tokens),
    cache_write_1h_tokens: numberOrUndefined(values.cache_write_1h_tokens),
    image_input_tokens: numberOrUndefined(values.image_input_tokens),
    image_output_tokens: numberOrUndefined(values.image_output_tokens),
    audio_input_tokens: numberOrUndefined(values.audio_input_tokens),
    audio_output_tokens: numberOrUndefined(values.audio_output_tokens),
    image_units: numberOrUndefined(values.image_units),
    audio_seconds: numberOrUndefined(values.audio_seconds),
    video_seconds: numberOrUndefined(values.video_seconds),
    task_units: numberOrUndefined(values.task_units),
    max_attempts: Number(values.max_attempts),
    retry_probability: Number(values.retry_probability),
    hedge_probability: Number(values.hedge_probability),
    hedge_allowed: values.hedge_allowed,
  }
  return { ...base, profile }
}
