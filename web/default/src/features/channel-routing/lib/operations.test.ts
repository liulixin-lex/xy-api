import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import type { RoutingOperation } from '../types'
import {
  channelRoutingOperationActiveProbeResult,
  channelRoutingOperationAuditExportId,
  channelRoutingOperationBreakerResetResult,
  channelRoutingOperationDisplayStatus,
  channelRoutingOperationIsActive,
  channelRoutingOperationTypeLabel,
} from './operations'

function operation(
  type: string,
  status: string,
  result?: unknown
): RoutingOperation {
  return {
    id: 1,
    type,
    idempotency_hash: '',
    evaluation_hash: '',
    subject_type: '',
    subject_id: 0,
    pool_id: 0,
    expected_revision: 0,
    expected_activation_id: 0,
    actor_id: 1,
    reason: '',
    status,
    claim_until_ms: 0,
    attempts: 1,
    next_retry_ms: 0,
    last_error: '',
    result_revision: 0,
    result_activation_id: 0,
    result_outbox_id: 0,
    result_payload_hash: '',
    created_time_ms: 1,
    updated_time_ms: 1,
    completed_time_ms: 1,
    result,
  }
}

describe('channel routing operation presentation', () => {
  test('uses the persistent lifecycle and surfaces partial completion', () => {
    assert.equal(
      channelRoutingOperationDisplayStatus(operation('cost_sync', 'pending')),
      'pending'
    )
    assert.equal(
      channelRoutingOperationDisplayStatus(
        operation('cost_sync', 'succeeded', { execution_state: 'partial' })
      ),
      'partial'
    )
    assert.equal(
      channelRoutingOperationDisplayStatus(
        operation('cost_sync', 'succeeded', { execution_state: 'completed' })
      ),
      'succeeded'
    )
    assert.equal(
      channelRoutingOperationDisplayStatus(operation('cost_sync', 'failed')),
      'failed'
    )
  })

  test('polls only non-terminal operations', () => {
    assert.equal(
      channelRoutingOperationIsActive(operation('cost_sync', 'pending')),
      true
    )
    assert.equal(
      channelRoutingOperationIsActive(operation('cost_sync', 'running')),
      true
    )
    assert.equal(
      channelRoutingOperationIsActive(operation('cost_sync', 'succeeded')),
      false
    )
    assert.equal(
      channelRoutingOperationIsActive(operation('cost_sync', 'failed')),
      false
    )
  })

  test('presents breaker reset operations with a stable audit label', () => {
    assert.equal(
      channelRoutingOperationTypeLabel('breaker_reset'),
      'Breaker reset'
    )
    assert.equal(
      channelRoutingOperationTypeLabel('future_operation'),
      'future_operation'
    )
  })

  test('only exposes a validated audit export identifier', () => {
    const exportId = `rae_${'a'.repeat(32)}`
    assert.equal(
      channelRoutingOperationAuditExportId(
        operation('audit_export', 'succeeded', { export_id: exportId })
      ),
      exportId
    )
    assert.equal(
      channelRoutingOperationAuditExportId(
        operation('audit_export', 'succeeded', { export_id: '../secret' })
      ),
      null
    )
  })

  test('validates the persisted active probe result', () => {
    const stats = {
      cycles: 1,
      targets_considered: 3,
      targets_selected: 2,
      skipped_not_due: 1,
      skipped_budget: 0,
      lease_contended: 0,
      lease_errors: 0,
      executed: 2,
      succeeded: 1,
      failed: 1,
      timed_out: 0,
      canceled: 0,
      local_errors: 0,
      persistence_errors: 0,
      completion_errors: 0,
      effect_errors: 0,
      reserved_tokens: 256,
      reserved_cost_nano_usd: 1000,
      inflight: 0,
      max_inflight: 2,
    }
    assert.deepEqual(
      channelRoutingOperationActiveProbeResult(
        operation('active_probe', 'succeeded', { enabled: true, stats })
      ),
      { enabled: true, stats }
    )
    assert.equal(
      channelRoutingOperationActiveProbeResult(
        operation('active_probe', 'succeeded', {
          enabled: true,
          stats: { ...stats, executed: -1 },
        })
      ),
      null
    )
    assert.equal(
      channelRoutingOperationActiveProbeResult(
        operation('cost_sync', 'succeeded', { enabled: true, stats })
      ),
      null
    )
  })

  test('validates the persisted breaker reset result', () => {
    assert.deepEqual(
      channelRoutingOperationBreakerResetResult(
        operation('breaker_reset', 'succeeded', {
          scope: 'endpoint',
          generation: 7,
          outbox_id: 19,
          target: {
            scope: 'endpoint',
            endpoint_host: 'api.example.com',
            endpoint_authority: 'https://api.example.com:443',
            region: 'default',
          },
        })
      ),
      {
        scope: 'endpoint',
        generation: 7,
        outbox_id: 19,
        target: {
          scope: 'endpoint',
          endpoint_host: 'api.example.com',
          endpoint_authority: 'https://api.example.com:443',
          region: 'default',
        },
      }
    )
    assert.equal(
      channelRoutingOperationBreakerResetResult(
        operation('breaker_reset', 'succeeded', {
          scope: 'endpoint',
          generation: 0,
          outbox_id: 19,
          target: {
            scope: 'endpoint',
            endpoint_host: 'api.example.com',
            endpoint_authority: 'https://api.example.com:443',
            region: 'default',
          },
        })
      ),
      null
    )
    assert.equal(
      channelRoutingOperationBreakerResetResult(
        operation('breaker_reset', 'succeeded', {
          scope: 'member',
          generation: 2,
          outbox_id: 20,
          target: {
            scope: 'endpoint',
            endpoint_host: 'api.example.com',
            endpoint_authority: 'https://api.example.com:443',
            region: 'default',
          },
        })
      ),
      null
    )
  })
})
