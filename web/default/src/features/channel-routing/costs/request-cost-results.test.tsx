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
  RoutingCostBreakdown,
  RoutingCostComparisonCandidate,
  RoutingCostComparisonResponse,
  RoutingRequestCostEstimate,
} from '../types'
import { RequestCostResults } from './request-cost-results'

const zeroBreakdown: RoutingCostBreakdown = {
  input: 0,
  output: 0,
  cache_read: 0,
  cache_write: 0,
  cache_write_1h: 0,
  image_input: 0,
  image_output: 0,
  image_units: 0,
  audio_input: 0,
  audio_output: 0,
  audio_seconds: 0,
  video_seconds: 0,
  task_units: 0,
  per_request: 0,
  expression: 0,
  total: 0,
}

function estimate(overrides: Partial<RoutingRequestCostEstimate> = {}) {
  return {
    known: true,
    expected_known: true,
    worst_case_known: true,
    expected_effective_known: true,
    expected_cost: 0,
    worst_case_cost: 0,
    expected_effective_cost: 0,
    currency: 'USD',
    unit: 'request',
    confidence_score: 1,
    freshness_score: 1,
    expected_breakdown: zeroBreakdown,
    worst_case_single_breakdown: zeroBreakdown,
    ...overrides,
  } satisfies RoutingRequestCostEstimate
}

function candidate(
  memberId: number,
  overrides: Partial<RoutingCostComparisonCandidate> = {}
) {
  return {
    pool_id: 7,
    member_id: memberId,
    channel_id: memberId + 100,
    routing_identity: `routing-identity-${memberId}`,
    routing_generation: `routing-generation-${memberId}`,
    channel_name: `Channel ${memberId}`,
    model_name: 'gpt-cost',
    comparable: true,
    single_attempt: estimate(),
    before_multiplier: estimate(),
    upstream_cost_multiplier: 1,
    pricing_identity: `pricing-${memberId}`,
    pricing_hash: 'a'.repeat(64),
    ...overrides,
  } satisfies RoutingCostComparisonCandidate
}

describe('request cost comparison results', () => {
  test('shows free candidates and explains missing context for incomplete ones', () => {
    const result: RoutingCostComparisonResponse = {
      profile_source: 'manual',
      model_name: 'gpt-cost',
      pool_id: 7,
      pricing_epoch: 11,
      pricing_hash: 'b'.repeat(64),
      generated_at: 1_700_000_000,
      quantity_sources: { input_tokens: 'manual' },
      candidates: [
        candidate(1),
        candidate(2, {
          comparable: false,
          missing_context: ['audio_seconds'],
          single_attempt: estimate({
            known: false,
            expected_known: false,
            worst_case_known: false,
            expected_effective_known: false,
            unknown_reason: 'missing_context',
            missing_context: ['audio_seconds'],
          }),
        }),
      ],
    }

    const html = renderToStaticMarkup(
      createElement(RequestCostResults, { result })
    )

    assert.match(html, /Free/)
    assert.match(html, /Not comparable/)
    assert.match(html, /Missing context/)
    assert.match(html, /Audio seconds/)
    assert.match(html, /b{64}/)
    assert.match(html, /class="shrink-0">Pricing hash:/)
  })
})
