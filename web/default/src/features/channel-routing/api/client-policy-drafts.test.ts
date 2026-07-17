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

import type {
  ApiEnvelope,
  PolicyDraftDetail,
  PolicyDraftSummary,
  PolicyPublishResponse,
  PolicySimulationResponse,
} from '../types'
import {
  deleteChannelRoutingPolicyDraft,
  deleteChannelRoutingPolicyDrafts,
  getChannelRoutingPolicyDraft,
  publishChannelRoutingPolicyDraft,
  simulateChannelRoutingPolicyDraft,
  updateChannelRoutingPolicyDraft,
} from './client'

const originalAdapter = api.defaults.adapter
const responseETag = `"crd.7.5.${'d'.repeat(64)}"`
const detail = {
  id: 7,
  base_revision: 3,
  base_hash: 'a'.repeat(64),
  version: 5,
  etag: 'd'.repeat(64),
  server_etag: responseETag,
  document_hash: 'c'.repeat(64),
  status: 'editing',
  created_by: 1,
  updated_by: 2,
  validated_head_revision: 0,
  validated_head_hash: '',
  published_revision: 0,
  created_time_ms: 100,
  updated_time_ms: 200,
  validated_time_ms: 0,
  published_time_ms: 0,
  workspace_state: 'working',
  stale: false,
  can_validate: true,
  can_publish: false,
  can_delete: true,
  blocking_reason: 'draft_requires_validation',
  document: { schema_version: 2, pools: [] },
} satisfies PolicyDraftDetail

afterEach(() => {
  api.defaults.adapter = originalAdapter
})

describe('channel routing policy draft API', () => {
  test('keeps the response ETag and forwards cancellation for detail reads', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      const { server_etag: _, ...body } = detail
      const data: ApiEnvelope<Omit<PolicyDraftDetail, 'server_etag'>> = {
        success: true,
        data: body,
      }
      return {
        data,
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders({ etag: responseETag }),
        config,
      }
    }
    const controller = new AbortController()

    const result = await getChannelRoutingPolicyDraft(7, controller.signal)

    assert.equal(captured[0]?.signal, controller.signal)
    assert.equal(result.server_etag, responseETag)
  })

  test('uses the authoritative detail response ETag for compare-and-swap save', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      const data: ApiEnvelope<PolicyDraftSummary> = {
        success: true,
        data: { ...detail, version: 6, etag: 'e'.repeat(64) },
      }
      return {
        data,
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }

    await updateChannelRoutingPolicyDraft(detail, detail.document)

    assert.equal(captured[0]?.headers.get('If-Match'), responseETag)
  })

  test('uses ETags for single and all-or-nothing batch deletion', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      const data: ApiEnvelope<{ deleted_ids: number[] }> = {
        success: true,
        data: { deleted_ids: [7] },
      }
      return {
        data,
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }

    await deleteChannelRoutingPolicyDraft(detail)
    await deleteChannelRoutingPolicyDrafts([detail])

    assert.equal(captured[0]?.headers.get('If-Match'), responseETag)
    assert.deepEqual(JSON.parse(String(captured[1]?.data)), {
      items: [{ id: 7, etag: responseETag }],
    })
  })

  test('only sends risk acceptance when the operator explicitly accepts it', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      const data: ApiEnvelope<PolicyPublishResponse> = {
        success: true,
        data: {} as PolicyPublishResponse,
      }
      return {
        data,
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }
    const activation = {
      stage: 'active' as const,
      traffic_basis_points: 0,
      reason: 'validated rollout',
    }

    await publishChannelRoutingPolicyDraft(detail, activation, 'publish-one')
    await publishChannelRoutingPolicyDraft(detail, activation, 'publish-two', {
      accepted: true,
      reason: 'Known capacity risk is covered by the monitored rollout.',
    })

    const withoutAcceptance = JSON.parse(String(captured[0]?.data))
    const withAcceptance = JSON.parse(String(captured[1]?.data))
    assert.equal(withoutAcceptance.accept_simulation_risk, undefined)
    assert.equal(withAcceptance.accept_simulation_risk, true)
    assert.equal(
      withAcceptance.risk_acceptance_reason,
      'Known capacity risk is covered by the monitored rollout.'
    )
  })

  test('binds simulation evidence to the exact deployment target', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      const data: ApiEnvelope<PolicySimulationResponse> = {
        success: true,
        data: {} as PolicySimulationResponse,
      }
      return {
        data,
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }

    await simulateChannelRoutingPolicyDraft(
      detail,
      {
        pool_id: 11,
        limit: 50,
        target_stage: 'canary',
        target_traffic_basis_points: 300,
      },
      'simulation-target-0001'
    )

    assert.deepEqual(JSON.parse(String(captured[0]?.data)), {
      pool_id: 11,
      limit: 50,
      target_stage: 'canary',
      target_traffic_basis_points: 300,
    })
  })
})
