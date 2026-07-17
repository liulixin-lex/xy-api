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

import { api } from '@/lib/api'

import type { ApiEnvelope, RoutingOperation } from '../types'
import {
  cancelChannelRoutingOperation,
  getChannelRoutingControlAuditTechnical,
  getChannelRoutingOperation,
  getChannelRoutingOperationTechnical,
  retryChannelRoutingOperation,
} from './client'

const originalAdapter = api.defaults.adapter

const operation = {
  id: 17,
  type: 'active_probe',
  subject_type: 'routing_probes',
  subject_id: 0,
  pool_id: 0,
  expected_revision: 3,
  expected_activation_id: 4,
  actor_id: 9,
  reason: 'manual probe',
  status: 'failed',
  attempts: 1,
  next_retry_ms: 0,
  last_error: 'provider unavailable',
  result_revision: 0,
  result_activation_id: 0,
  created_time_ms: 100,
  updated_time_ms: 200,
  completed_time_ms: 200,
} satisfies RoutingOperation

afterEach(() => {
  api.defaults.adapter = originalAdapter
})

describe('channel routing operation API', () => {
  test('forwards cancellation to public and technical detail reads', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      let responseData: unknown = operation
      if (config.url?.includes('control-audits')) {
        responseData = {
          id: 8,
          schema_version: 2,
          event_type: 'operation.failed',
          technical: {},
        }
      } else if (config.url?.endsWith('/technical')) {
        responseData = {
          id: 17,
          schema_version: 2,
          type: 'active_probe',
          technical: {
            idempotency_hash: 'a'.repeat(64),
            evaluation_hash: 'b'.repeat(64),
          },
        }
      }
      const data: ApiEnvelope<unknown> = {
        success: true,
        data: responseData,
      }
      return {
        data,
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }
    const controller = new AbortController()

    await getChannelRoutingOperation(17, controller.signal)
    await getChannelRoutingOperationTechnical(17, controller.signal)
    await getChannelRoutingControlAuditTechnical(8, controller.signal)

    assert.equal(captured.length, 3)
    for (const config of captured) {
      assert.equal(config.signal, controller.signal)
    }
  })

  test('sends operator reasons for retry and cancellation', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      const data: ApiEnvelope<unknown> = {
        success: true,
        data: config.url?.endsWith('/retry')
          ? {
              operation: { ...operation, id: 18, status: 'pending' },
              created: true,
            }
          : { ...operation, status: 'cancelled' },
      }
      return {
        data,
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }

    const retried = await retryChannelRoutingOperation(17, 'provider recovered')
    const cancelled = await cancelChannelRoutingOperation(
      18,
      'duplicate operator request'
    )

    assert.equal(retried.created, true)
    assert.equal(retried.operation.id, 18)
    assert.equal(cancelled.status, 'cancelled')
    assert.deepEqual(JSON.parse(String(captured[0]?.data)), {
      reason: 'provider recovered',
    })
    assert.deepEqual(JSON.parse(String(captured[1]?.data)), {
      reason: 'duplicate operator request',
    })
  })
})
