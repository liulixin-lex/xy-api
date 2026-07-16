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
  RoutingChannelConfiguration,
  RoutingChannelConfigurationPage,
  RoutingChannelConfigurationUpdate,
} from '../types'
import {
  ChannelRoutingConfigurationConflictError,
  getChannelRoutingConfiguration,
  getChannelRoutingConfigurationApiError,
  listChannelRoutingConfigurations,
  updateChannelRoutingConfiguration,
} from './client'

const originalAdapter = api.defaults.adapter

const configuration: RoutingChannelConfiguration = {
  channel_id: 77,
  channel_name: 'Primary upstream',
  upstream_cost_multiplier: 1,
  cost_source: 'defaulted',
  cost_confirmed: false,
  traffic_class: 'all',
  failure_domain_status: 'unconfigured',
  failure_domain_label: '',
  effective_model_count: 12,
  cost_basis_available: true,
  revision: 4,
  updated_by: 1,
  created_time: 100,
  updated_time: 100,
  etag: '"routing-channel-configuration:77:4"',
}

const request: RoutingChannelConfigurationUpdate = {
  upstream_cost_multiplier: 0,
  traffic_class: 'claude_code_only',
  failure_domain_label: 'provider-account-a',
  clear_failure_domain: false,
}

afterEach(() => {
  api.defaults.adapter = originalAdapter
})

describe('channel routing configuration API', () => {
  test('sends list filters and unwraps pagination metadata', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      const data: ApiEnvelope<RoutingChannelConfigurationPage> = {
        success: true,
        data: {
          items: [configuration],
          total: 21,
          page: 2,
          page_size: 10,
        },
      }
      return {
        data,
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
      }
    }

    const result = await listChannelRoutingConfigurations({
      page: 2,
      page_size: 10,
      search: 'Primary',
      cost_confirmed: false,
      cost_source: 'defaulted',
      traffic_class: 'claude_code_only',
    })

    assert.equal(
      captured[0]?.url,
      '/api/channel-routing/channel-configurations'
    )
    assert.deepEqual(captured[0]?.params, {
      page: 2,
      page_size: 10,
      search: 'Primary',
      cost_confirmed: false,
      cost_source: 'defaulted',
      traffic_class: 'claude_code_only',
    })
    assert.equal(result.total, 21)
    assert.equal(result.items[0]?.channel_id, 77)
  })

  test('prefers the response ETag for subsequent writes', async () => {
    api.defaults.adapter = async (config) => ({
      data: { success: true, data: configuration },
      status: 200,
      statusText: 'OK',
      headers: new AxiosHeaders({
        etag: '"routing-channel-configuration:77:5"',
      }),
      config,
    })

    const result = await getChannelRoutingConfiguration(77)

    assert.equal(result.etag, '"routing-channel-configuration:77:5"')
  })

  test('sends the opaque If-Match value and AbortSignal when updating', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      return {
        data: {
          success: true,
          data: {
            ...configuration,
            ...request,
            cost_source: 'manual',
            cost_confirmed: true,
            revision: 5,
          },
        },
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders({
          etag: '"routing-channel-configuration:77:5"',
        }),
        config,
      }
    }
    const controller = new AbortController()

    const result = await updateChannelRoutingConfiguration(
      configuration,
      request,
      controller.signal
    )

    assert.equal(
      captured[0]?.url,
      '/api/channel-routing/channel-configurations/77'
    )
    assert.equal(captured[0]?.headers.get('If-Match'), configuration.etag)
    assert.equal(captured[0]?.signal, controller.signal)
    assert.deepEqual(JSON.parse(String(captured[0]?.data)), request)
    assert.equal(result.upstream_cost_multiplier, 0)
    assert.equal(result.etag, '"routing-channel-configuration:77:5"')
  })

  test('turns a stale ETag response into a recoverable conflict', async () => {
    const current: RoutingChannelConfiguration = {
      ...configuration,
      upstream_cost_multiplier: 2,
      revision: 6,
      etag: '"routing-channel-configuration:77:6"',
    }
    api.defaults.adapter = async (config) => {
      const response = {
        data: {
          success: false,
          code: 'channel_configuration_conflict',
          message: 'channel configuration changed',
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
      () => updateChannelRoutingConfiguration(configuration, request),
      (error: unknown) => {
        assert.ok(error instanceof ChannelRoutingConfigurationConflictError)
        assert.equal(error.current?.upstream_cost_multiplier, 2)
        assert.equal(error.currentETag, current.etag)
        return true
      }
    )
  })

  test('preserves structured validation metadata', async () => {
    api.defaults.adapter = async (config) => {
      const response = {
        data: {
          success: false,
          code: 'invalid_channel_configuration',
          message: 'invalid channel configuration',
          field: 'upstream_cost_multiplier',
          reason: 'out_of_range',
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
      () => updateChannelRoutingConfiguration(configuration, request),
      (error: unknown) => {
        assert.deepEqual(getChannelRoutingConfigurationApiError(error), {
          status: 400,
          code: 'invalid_channel_configuration',
          message: 'invalid channel configuration',
          detail: undefined,
          field: 'upstream_cost_multiplier',
          reason: 'out_of_range',
        })
        return true
      }
    )
  })
})
