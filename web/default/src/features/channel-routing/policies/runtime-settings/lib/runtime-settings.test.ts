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

import type { SmartRoutingSetting } from '../../../types'
import {
  changedRuntimeSettingFields,
  displayRuntimeSettingValue,
  mergeRuntimeSettingsConflict,
  runtimeSettingsSchema,
} from './runtime-settings'

const base: SmartRoutingSetting = {
  enabled: false,
  request_profile_enabled: false,
  mode: 'observe',
  weight_availability: 0.45,
  weight_latency: 0.25,
  weight_throughput: 0.1,
  weight_cost: 0.2,
  availability_floor: 0.95,
  min_volume: 50,
  top_k: 3,
  consecutive_5xx: 5,
  failure_rate_pct: 50,
  base_cooldown_sec: 30,
  max_cooldown_sec: 300,
  max_ejected_pct: 50,
  half_open_probes: 1,
  max_switches: 2,
  retry_token_capacity: 100,
  retry_token_refill_per_sec: 10,
  failover_deadline_ms: 120_000,
  retry_extra_cost_multiplier: 2,
  backoff_base_ms_5xx: 50,
  backoff_base_ms_429: 1_000,
  backoff_cap_ms: 20_000,
  first_byte_failover_enabled: true,
  first_byte_min_ms: 3_000,
  first_byte_cap_ms: 12_000,
  first_byte_p95_multiplier: 2,
  hedge_enabled: false,
  hedge_max_concurrent: 8,
  hedge_max_response_bytes: 4 << 20,
  hedge_max_buffered_bytes: 64 << 20,
  hedge_ratio_window_sec: 60,
  hedge_max_extra_basis_points: 500,
  hedge_audit_retention_days: 30,
  snapshot_live_sec: 300,
  snapshot_stale_sec: 1_800,
  balance_margin_usd: 1,
  sync_interval_min: 5,
  hotcache_refresh_sec: 3,
  metric_bucket_sec: 60,
  flush_interval_min: 1,
  retention_days: 7,
  active_probe_enabled: false,
  active_probe_healthy_sec: 900,
  active_probe_degraded_sec: 120,
  active_probe_open_sec: 30,
  active_probe_timeout_ms: 15_000,
  active_probe_max_targets: 128,
  active_probe_concurrency: 4,
  active_probe_per_host: 1,
  active_probe_token_budget: 4_096,
  active_probe_cost_budget_usd: 0.25,
  agent_enabled: false,
  agent_auto_apply: false,
  agent_model: 'claude-opus-4-8',
}

describe('runtime settings form contract', () => {
  test('marks only overlapping three-way changes as conflicts', () => {
    const draft = { ...base, top_k: 5, hedge_enabled: true }
    const current = {
      ...base,
      min_volume: 80,
      hedge_enabled: true,
      top_k: 7,
    }

    const result = mergeRuntimeSettingsConflict(base, draft, current)

    assert.deepEqual(result.conflicts, ['top_k'])
    assert.deepEqual(result.draftChanges, ['top_k', 'hedge_enabled'])
    assert.equal(result.merged.top_k, 5)
    assert.equal(result.merged.hedge_enabled, true)
    assert.equal(result.merged.min_volume, 80)
  })

  test('reports exact changed fields and enforces cross-field bounds', () => {
    assert.deepEqual(changedRuntimeSettingFields(base, { ...base, top_k: 4 }), [
      'top_k',
    ])
    const invalid = runtimeSettingsSchema.safeParse({
      ...base,
      backoff_base_ms_429: 2_000,
      backoff_cap_ms: 1_000,
    })
    assert.equal(invalid.success, false)
    if (!invalid.success) {
      assert.deepEqual(invalid.error.issues[0]?.path, ['backoff_base_ms_429'])
    }
  })

  test('localizes routing modes without translating administrator model IDs', () => {
    const translate = (key: string) => `translated:${key}`
    assert.equal(
      displayRuntimeSettingValue('mode', 'enterprise_slo', translate),
      'translated:Enterprise SLO'
    )
    assert.equal(
      displayRuntimeSettingValue('agent_model', 'claude-opus-4-8', translate),
      'claude-opus-4-8'
    )
  })

  test('uses translatable messages for primitive numeric boundaries', () => {
    const invalid = runtimeSettingsSchema.safeParse({
      ...base,
      weight_cost: 2,
      active_probe_cost_budget_usd: 0,
    })
    assert.equal(invalid.success, false)
    if (!invalid.success) {
      assert.equal(
        invalid.error.issues.find((issue) => issue.path[0] === 'weight_cost')
          ?.message,
        'Enter a value from 0 to 1'
      )
      assert.equal(
        invalid.error.issues.find(
          (issue) => issue.path[0] === 'active_probe_cost_budget_usd'
        )?.message,
        'Enter a value greater than zero'
      )
    }
  })
})
