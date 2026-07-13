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

import {
  AxiosError,
  AxiosHeaders,
  type InternalAxiosRequestConfig,
} from 'axios'

import { api } from '@/lib/api'

import type {
  ApiEnvelope,
  RoutingCostBinding,
  RoutingCostBindingPage,
  RoutingCostBindingRequest,
} from '../types'
import {
  ChannelRoutingCostBindingConflictError,
  createChannelRoutingCostBinding,
  getChannelRoutingCostBindingApiError,
  getChannelRoutingCostBinding,
  listChannelRoutingCostBindings,
  loadChannelRoutingCostBindingGroups,
  testChannelRoutingCostBinding,
  updateChannelRoutingCostBinding,
} from './client'

const originalAdapter = api.defaults.adapter

const binding: RoutingCostBinding = {
  id: 9,
  channel_id: 77,
  channel_name: 'Cost upstream',
  etag: '"crb.9.100.body"',
  upstream_type: 'newapi',
  base_url: 'https://cost.example.com',
  upstream_group: 'default',
  serves_claude_code: false,
  egress_allowed_private_cidrs: ['10.20.30.0/24'],
  enabled: true,
  sync_failure_count: 0,
  sync_backoff_until: 0,
  credential_masks: { gateway_api_key: '****abcd' },
  created_time: 100,
  updated_time: 100,
}

const request: RoutingCostBindingRequest = {
  channel_id: 77,
  upstream_type: 'newapi',
  base_url: 'https://cost.example.com',
  upstream_group: 'default',
  serves_claude_code: false,
  egress_allowed_private_cidrs: ['10.20.30.0/24'],
  enabled: true,
  credentials: {},
}

afterEach(() => {
  api.defaults.adapter = originalAdapter
})

describe('channel routing cost binding API', () => {
  test('sends server-side list filters and unwraps pagination metadata', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      const data: ApiEnvelope<RoutingCostBindingPage> = {
        success: true,
        data: { items: [binding], total: 21, page: 2, page_size: 10 },
      }
      return {
        data,
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }

    const result = await listChannelRoutingCostBindings({
      page: 2,
      page_size: 10,
      search: 'Cost upstream',
      upstream_type: 'newapi',
      enabled: true,
    })

    assert.equal(captured[0]?.url, '/api/channel-routing/v2/cost-bindings')
    assert.deepEqual(captured[0]?.params, {
      page: 2,
      page_size: 10,
      search: 'Cost upstream',
      upstream_type: 'newapi',
      enabled: true,
    })
    assert.equal(result.total, 21)
    assert.equal(result.items[0]?.channel_id, 77)
  })

  test('prefers the response ETag for subsequent compare-and-swap writes', async () => {
    api.defaults.adapter = async (config) => ({
      data: { success: true, data: binding },
      status: 200,
      statusText: 'OK',
      headers: new AxiosHeaders({ etag: '"crb.9.101.response"' }),
      config,
    })

    const result = await getChannelRoutingCostBinding(77)
    assert.equal(result.etag, '"crb.9.101.response"')
  })

  test('sends If-Match when updating a binding', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      return {
        data: { success: true, data: { ...binding, updated_time: 101 } },
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders({ etag: '"crb.9.101.updated"' }),
        config,
      }
    }

    const result = await updateChannelRoutingCostBinding(binding, request)

    assert.equal(captured[0]?.headers.get('If-Match'), '"crb.9.100.body"')
    assert.equal(result.etag, '"crb.9.101.updated"')
  })

  test('turns a stale ETag response into a recoverable conflict', async () => {
    const current = {
      ...binding,
      etag: '"crb.9.102.current"',
      upstream_group: 'enterprise',
      updated_time: 102,
    }
    api.defaults.adapter = async (config) => {
      const response = {
        data: {
          success: false,
          code: 'cost_binding_conflict',
          message: 'cost binding changed',
          conflict: {
            current,
            current_etag: current.etag,
          },
        },
        status: 409,
        statusText: 'Conflict',
        headers: new AxiosHeaders({ etag: current.etag }),
        config,
      }
      throw new AxiosError(
        'Request failed with status code 409',
        AxiosError.ERR_BAD_REQUEST,
        config,
        undefined,
        response
      )
    }

    await assert.rejects(
      () => updateChannelRoutingCostBinding(binding, request),
      (error: unknown) => {
        assert.ok(error instanceof ChannelRoutingCostBindingConflictError)
        assert.equal(error.current?.upstream_group, 'enterprise')
        assert.equal(error.currentETag, current.etag)
        return true
      }
    )
  })

  test('forwards AbortSignal to save and draft connection requests', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      const action =
        config.url?.endsWith('/test') || config.url?.endsWith('/groups')
      return {
        data: {
          success: true,
          data: action
            ? {
                channel_id: 77,
                upstream_type: 'newapi',
                groups: [],
                model_count: 1,
              }
            : binding,
        },
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }
    const controller = new AbortController()

    await createChannelRoutingCostBinding(request, controller.signal)
    await updateChannelRoutingCostBinding(binding, request, controller.signal)
    await testChannelRoutingCostBinding('new', request, controller.signal)
    await loadChannelRoutingCostBindingGroups('new', request, controller.signal)

    assert.equal(captured.length, 4)
    for (const config of captured) {
      assert.equal(config.signal, controller.signal)
    }
  })

  test('preserves structured field and reason validation metadata', async () => {
    api.defaults.adapter = async (config) => {
      const response = {
        data: {
          success: false,
          code: 'invalid_cost_binding',
          message: 'invalid cost binding',
          field: 'credentials.gateway_api_key',
          reason: 'required',
        },
        status: 400,
        statusText: 'Bad Request',
        headers: new AxiosHeaders(),
        config,
      }
      throw new AxiosError(
        'Request failed with status code 400',
        AxiosError.ERR_BAD_REQUEST,
        config,
        undefined,
        response
      )
    }

    await assert.rejects(
      () => createChannelRoutingCostBinding(request),
      (error: unknown) => {
        assert.deepEqual(getChannelRoutingCostBindingApiError(error), {
          status: 400,
          code: 'invalid_cost_binding',
          message: 'invalid cost binding',
          detail: undefined,
          field: 'credentials.gateway_api_key',
          reason: 'required',
        })
        return true
      }
    )
  })
})
