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
  CursorResponse,
  EndpointBreakerPage,
  PagedResponse,
  RoutingProbeResult,
  ChannelSnapshot,
} from '../types'
import {
  listChannelRoutingChannels,
  listChannelRoutingEndpoints,
  listChannelRoutingProbes,
} from './client'

const originalAdapter = api.defaults.adapter

afterEach(() => {
  api.defaults.adapter = originalAdapter
})

describe('channel health tab APIs', () => {
  test('keeps physical, endpoint, and probe filters on their own endpoints', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      let data: ApiEnvelope<unknown>
      if (config.url?.endsWith('/channels')) {
        data = {
          success: true,
          data: {
            items: [],
            total: 0,
            page: 2,
            page_size: 20,
            snapshot_revision: 4,
            snapshot_built_at: 100,
          } satisfies PagedResponse<ChannelSnapshot>,
        }
      } else if (config.url?.endsWith('/endpoints')) {
        data = {
          success: true,
          data: {
            items: [],
            total: 0,
            page: 3,
            page_size: 10,
            region: 'gateway-eu',
            stable_node_id: 'node-a',
            endpoint_quorum_eligible: true,
          } satisfies EndpointBreakerPage,
        }
      } else {
        data = {
          success: true,
          data: {
            items: [],
            next_cursor: '72',
          } satisfies CursorResponse<RoutingProbeResult>,
        }
      }
      return {
        data,
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }

    await listChannelRoutingChannels({
      page: 2,
      page_size: 20,
      search: 'primary',
      status: 1,
      type: 14,
    })
    await listChannelRoutingEndpoints({
      page: 3,
      page_size: 10,
      search: 'api.example',
      region: 'gateway-eu',
    })
    const probes = await listChannelRoutingProbes({
      limit: 50,
      cursor: 91,
      channel_id: 77,
      outcome: 'failure',
    })

    assert.equal(captured[0]?.url, '/api/channel-routing/channels')
    assert.deepEqual(captured[0]?.params, {
      page: 2,
      page_size: 20,
      search: 'primary',
      status: 1,
      type: 14,
    })
    assert.equal(captured[1]?.url, '/api/channel-routing/endpoints')
    assert.deepEqual(captured[1]?.params, {
      page: 3,
      page_size: 10,
      search: 'api.example',
      region: 'gateway-eu',
    })
    assert.equal(captured[2]?.url, '/api/channel-routing/probes')
    assert.deepEqual(captured[2]?.params, {
      limit: 50,
      cursor: 91,
      channel_id: 77,
      outcome: 'failure',
    })
    assert.equal(probes.next_cursor, '72')
  })
})
