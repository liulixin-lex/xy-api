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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import '@/i18n/config'

import type {
  ChannelRoutingCostDetailResponse,
  RoutingCostCatalogMember,
  RoutingCostCatalogModel,
  RoutingNormalizedPricing,
} from '../types'
import { CostContractDetail } from './cost-contract-detail'

const pricing: RoutingNormalizedPricing = {
  quota_type: 0,
  billing_mode: 'dimensions',
  currency: 'USD',
  unit: 'request',
  group_ratio: null,
  base_ratio: null,
  completion_ratio: null,
  model_price: null,
  input_cost_per_million: 0,
  output_cost_per_million: null,
  cache_read_cost_per_million: null,
  cache_write_cost_per_million: null,
  cache_write_1h_cost_per_million: null,
  image_input_cost_per_million: null,
  image_output_cost_per_million: null,
  image_cost: null,
  per_image_cost: null,
  audio_input_cost_per_million: null,
  audio_output_cost_per_million: null,
  audio_cost_per_second: null,
  video_cost_per_second: null,
  per_task_cost: null,
  per_request_cost: null,
  billing_expression: '',
  tiers: null,
  extras: null,
  contract_v2: {
    schema_version: 2,
    mode: 'dimensions',
    currency: 'USD',
  },
}

const member: RoutingCostCatalogMember = {
  pool_id: 7,
  member_id: 12,
  channel_id: 21,
  routing_identity: 'routing-identity-21',
  routing_generation: 'routing-generation-21-a',
  channel_name: 'Primary channel',
  channel_type: 1,
  physical_status: 1,
  model_count: 1,
  known_contract_count: 1,
  unknown_contract_count: 0,
  configuration_revision: 4,
  upstream_cost_multiplier: 1.25,
}

const model: RoutingCostCatalogModel = {
  pool_id: 7,
  member_id: 12,
  channel_id: 21,
  routing_generation: 'routing-generation-21-a',
  model_name: 'gpt-cost',
  known: true,
  currency: 'USD',
  contract_mode: 'dimensions',
  configured_dimensions: ['input_tokens'],
  explicit_free_dimensions: ['input_tokens'],
  configuration_revision: 4,
  upstream_cost_multiplier: 1.25,
  pricing_identity: 'pricing-contract-21',
}

const detail: ChannelRoutingCostDetailResponse = {
  item: {
    pool_id: 7,
    group_name: 'default',
    member_id: 12,
    channel_id: 21,
    routing_identity: 'routing-identity-21',
    routing_generation: 'routing-generation-21-a',
    channel_name: 'Primary channel',
    model_name: 'gpt-cost',
    known: true,
    currency: 'USD',
    unit: 'request',
    pricing_identity: 'pricing-contract-21',
    configuration_revision: 4,
    upstream_cost_multiplier: 1.25,
    confidence: 'known',
    confidence_score: 1,
    freshness: 'fresh',
    freshness_score: 1,
    snapshot_time: 1_700_000_000,
    pricing,
  },
  snapshot_revision: 9,
  snapshot_built_at: 1_700_000_000,
}

describe('routing pricing contract detail', () => {
  test('shows explicit zero as free and omits unconfigured dimensions', () => {
    const html = renderToStaticMarkup(
      createElement(CostContractDetail, {
        member,
        model,
        detail,
        loading: false,
      })
    )

    assert.match(html, /Input \/ million/)
    assert.match(html, /Free/)
    assert.doesNotMatch(html, /Output \/ million/)
  })
})
