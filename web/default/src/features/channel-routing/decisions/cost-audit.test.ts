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

import {
  hasCurrentChannelCostAudit,
  hasCurrentRoutingAttemptCostAudit,
} from '../lib/cost-audit'
import type { RoutingAttempt, RoutingCostEstimate } from '../types'

const historicalEstimate: RoutingCostEstimate = {
  known: true,
  cost: 1,
  upstream_cost_multiplier: 0,
  updated_unix: 1,
}

describe('decision channel cost audit detection', () => {
  test('does not misread a missing historical multiplier as an explicit free channel', () => {
    assert.equal(hasCurrentChannelCostAudit(historicalEstimate), false)
  })

  test('recognizes current zero-multiplier and unknown-baseline audits', () => {
    assert.equal(
      hasCurrentChannelCostAudit({
        ...historicalEstimate,
        configuration_revision: 7,
      }),
      true
    )
    assert.equal(
      hasCurrentChannelCostAudit({
        ...historicalEstimate,
        baseline_expected_known: false,
      }),
      true
    )
  })
})

const historicalAttempt: RoutingAttempt = {
  node_epoch_id: 'node-epoch',
  stable_node_known: false,
  policy_revision: 1,
  algorithm_version: 'channel-routing-balanced-v2',
  execution_mode: 'serial',
  attempt_index: 0,
  role: 'serial',
  state: 'completed',
  result: 'success',
  winner: true,
  member_id: 11,
  channel_id: 101,
  region: 'default',
  endpoint_authority: 'api.example.com',
  failure_domain_hash: '',
  cost_known: true,
  expected_cost: 1,
  actual_cost_known: true,
  actual_cost: 1,
  upstream_sent: true,
  client_committed: true,
  will_retry: false,
  final_attempt: true,
  started_time_ms: 1,
}

describe('attempt channel cost audit detection', () => {
  test('does not misread historical zero-value columns as a current 0× audit', () => {
    assert.equal(
      hasCurrentRoutingAttemptCostAudit({
        ...historicalAttempt,
        upstream_cost_multiplier: 0,
        baseline_expected_known: false,
        baseline_worst_case_known: false,
      }),
      false
    )
  })

  test('recognizes current known and unknown attempt audits', () => {
    assert.equal(
      hasCurrentRoutingAttemptCostAudit({
        ...historicalAttempt,
        pricing_identity: 'billing:hash:channel-config:7',
        configuration_revision: 7,
        upstream_cost_multiplier: 0,
      }),
      true
    )
    assert.equal(
      hasCurrentRoutingAttemptCostAudit({
        ...historicalAttempt,
        cost_known: false,
        unknown_reason: 'system_pricing_missing',
      }),
      true
    )
  })
})
