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
import type { RoutingNormalizedPricing } from '../types'

const routingCostRateFields = [
  ['input_cost_per_million', 'Input / million'],
  ['output_cost_per_million', 'Output / million'],
  ['cache_read_cost_per_million', 'Cache read / million'],
  ['cache_write_cost_per_million', 'Cache write / million'],
  ['cache_write_1h_cost_per_million', '1h cache write / million'],
  ['image_input_cost_per_million', 'Image input / million'],
  ['image_output_cost_per_million', 'Image output / million'],
  ['per_image_cost', 'Per image'],
  ['audio_input_cost_per_million', 'Audio input / million'],
  ['audio_output_cost_per_million', 'Audio output / million'],
  ['audio_cost_per_second', 'Audio seconds'],
  ['video_cost_per_second', 'Video seconds'],
  ['per_task_cost', 'Task units'],
  ['per_request_cost', 'Per request'],
] as const satisfies readonly [keyof RoutingNormalizedPricing, string][]

const costDimensionLabels: Record<string, string> = {
  input_tokens: 'Input tokens',
  completion_tokens: 'Output tokens',
  output_tokens: 'Output tokens',
  cache_read_tokens: 'Cache read tokens',
  cache_write_tokens: 'Cache write tokens',
  cache_write_1h_tokens: '1h cache write tokens',
  image_input_tokens: 'Image input',
  image_output_tokens: 'Image output',
  image_units: 'Image units',
  audio_input_tokens: 'Audio input',
  audio_output_tokens: 'Audio output',
  audio_seconds: 'Audio seconds',
  video_seconds: 'Video seconds',
  task_units: 'Task units',
  request: 'Per request',
  request_fields: 'Request fields',
  request_profile: 'Request profile',
  request_pricing_features: 'Request pricing features',
  uncatalogued_surcharge: 'Uncatalogued surcharge',
  expression: 'Expression',
}

const profileSourceLabels: Record<string, string> = {
  manual: 'Manual profile',
  recent_decision: 'Recent decision',
}

const contractModeLabels: Record<string, string> = {
  dimensions: 'Dimensions',
  per_request: 'Per request',
  expression: 'Expression',
  free: 'Free',
}

export function routingCostDimensionLabel(value: string): string {
  return costDimensionLabels[value] ?? value
}

export function routingCostProfileSourceLabel(value: string): string {
  return profileSourceLabels[value] ?? value
}

export function routingCostContractModeLabel(value: string): string {
  return contractModeLabels[value] ?? value
}

export function routingConfiguredCostRates(pricing: RoutingNormalizedPricing) {
  return routingCostRateFields.flatMap(([field, label]) => {
    const value = pricing[field]
    return typeof value === 'number' && Number.isFinite(value)
      ? [{ field, label, value }]
      : []
  })
}
