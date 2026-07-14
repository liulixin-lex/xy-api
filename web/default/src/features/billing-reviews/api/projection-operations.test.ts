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
import { afterEach, describe, test } from 'node:test'

import { AxiosHeaders, type InternalAxiosRequestConfig } from 'axios'

import type { ApiEnvelope } from '@/features/channel-routing/types'
import { api } from '@/lib/api'

import type {
  BillingLogSinkConflict,
  BillingProjectionOperationResult,
  BillingProjectionPage,
  FailedBillingProjection,
} from '../projection-types'
import {
  listFailedBillingProjections,
  listOpenBillingLogSinkConflicts,
  requeueFailedBillingProjection,
  resolveBillingLogSinkConflict,
} from './projection-operations'

const originalAdapter = api.defaults.adapter

const projection: FailedBillingProjection = {
  id: 12,
  kind: 'accepted',
  reference_id: 88,
  operation_key_hash: 'a'.repeat(64),
  state: 'failed',
  failure_code: 'retry_exhausted',
  error: 'sink unavailable',
  attempts: 4,
  created_time_ms: 1_700_000_000_000,
  updated_time_ms: 1_700_000_100_000,
  completed_time_ms: 0,
  outcome: { user: 'completed', channel: 'failed' },
  requeueable: true,
  etag: '"billing-stats-projection.12.1700000100000.hash"',
}

const conflict: BillingLogSinkConflict = {
  id: 7,
  projection_id: 19,
  operation_key_hash: 'b'.repeat(64),
  state: 'open',
  version: 2,
  distinct_receipts: 2,
  physical_rows: 3,
  first_detected_ms: 1_700_000_000_000,
  last_detected_ms: 1_700_000_100_000,
  etag: '"billing-log-sink-conflict.7.v2"',
}

afterEach(() => {
  api.defaults.adapter = originalAdapter
})

describe('billing projection operations API', () => {
  test('uses bounded cursor endpoints for projection and conflict pages', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      const items = config.url?.includes('conflicts')
        ? [conflict]
        : [projection]
      const data: ApiEnvelope<BillingProjectionPage<unknown>> = {
        success: true,
        data: { items, count: 1, has_more: false, next_cursor: 0 },
      }
      return {
        data,
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }

    await listFailedBillingProjections('logs', { cursor: 4, limit: 25 })
    await listOpenBillingLogSinkConflicts({ cursor: 5, limit: 25 })

    assert.equal(
      captured[0]?.url,
      '/api/system-info/billing-projections/logs/failed'
    )
    assert.deepEqual(captured[0]?.params, { cursor: 4, limit: 25 })
    assert.equal(
      captured[1]?.url,
      '/api/system-info/billing-projections/log-sink-conflicts/open'
    )
    assert.equal(captured[0]?.disableDuplicate, true)
    assert.equal(captured[1]?.disableDuplicate, true)
  })

  test('freezes compare-and-swap headers and failure code on requeue', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    const result: BillingProjectionOperationResult = {
      operation_id: 1,
      action: 'stats_requeue',
      target_id: projection.id,
      state: 'completed',
      outcome: 'succeeded',
      completed_time_ms: 1_700_000_200_000,
      replayed: false,
    }
    api.defaults.adapter = async (config) => {
      captured.push(config)
      return {
        data: { success: true, data: result },
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }

    await requeueFailedBillingProjection(
      'stats',
      projection,
      'billing-projection-key-1'
    )

    assert.equal(
      captured[0]?.url,
      '/api/system-info/billing-projections/stats/failed/12/requeue'
    )
    assert.equal(captured[0]?.headers.get('If-Match'), projection.etag)
    assert.equal(
      captured[0]?.headers.get('Idempotency-Key'),
      'billing-projection-key-1'
    )
    assert.deepEqual(JSON.parse(captured[0]?.data as string), {
      expected_failure_code: 'retry_exhausted',
    })
  })

  test('sends the frozen conflict version and operator reason', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      return {
        data: {
          success: true,
          data: {
            operation_id: 2,
            action: 'conflict_resolve',
            target_id: conflict.id,
            state: 'completed',
            outcome: 'succeeded',
            completed_time_ms: 1_700_000_200_000,
            replayed: false,
          },
        },
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }

    await resolveBillingLogSinkConflict(
      conflict,
      'Verified duplicate receipt remediation',
      'billing-projection-key-2'
    )

    assert.equal(
      captured[0]?.url,
      '/api/system-info/billing-projections/log-sink-conflicts/7/resolve-requeue'
    )
    assert.equal(captured[0]?.headers.get('If-Match'), conflict.etag)
    assert.deepEqual(JSON.parse(captured[0]?.data as string), {
      expected_version: 2,
      reason: 'Verified duplicate receipt remediation',
    })
  })
})
