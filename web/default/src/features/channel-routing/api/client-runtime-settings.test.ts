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
  ChannelRoutingRuntimeSettings,
  SmartRoutingSetting,
} from '../types'
import {
  ChannelRoutingRuntimeSettingsConflictError,
  getChannelRoutingRuntimeSettings,
  getChannelRoutingRuntimeSettingsApiError,
  updateChannelRoutingRuntimeSettings,
} from './client'

const originalAdapter = api.defaults.adapter
const settings = {
  enabled: true,
  mode: 'balanced',
  top_k: 3,
} as SmartRoutingSetting
const runtimeSettings: ChannelRoutingRuntimeSettings = {
  settings,
  stored_settings: settings,
  revision: 4,
  document_hash: 'a'.repeat(64),
  updated_by: 7,
  updated_time_ms: 1_700_000_000_000,
  etag: `"crs.4.${'a'.repeat(64)}"`,
}

afterEach(() => {
  api.defaults.adapter = originalAdapter
})

describe('channel routing runtime settings API', () => {
  test('uses response ETags and sends the complete draft with If-Match', async () => {
    const captured: InternalAxiosRequestConfig[] = []
    api.defaults.adapter = async (config) => {
      captured.push(config)
      const data: ApiEnvelope<ChannelRoutingRuntimeSettings> = {
        success: true,
        data: runtimeSettings,
      }
      return {
        data,
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders({ etag: runtimeSettings.etag }),
        config,
      }
    }

    const loaded = await getChannelRoutingRuntimeSettings()
    const controller = new AbortController()
    const updated = await updateChannelRoutingRuntimeSettings(
      loaded,
      settings,
      controller.signal
    )

    assert.equal(captured[0]?.url, '/api/channel-routing/runtime-settings')
    assert.equal(captured[1]?.headers.get('If-Match'), runtimeSettings.etag)
    assert.equal(captured[1]?.signal, controller.signal)
    assert.deepEqual(JSON.parse(String(captured[1]?.data)), settings)
    assert.equal(updated.etag, runtimeSettings.etag)
  })

  test('turns a stale ETag into a recoverable conflict with current truth', async () => {
    const current = {
      ...runtimeSettings,
      revision: 5,
      etag: `"crs.5.${'b'.repeat(64)}"`,
    }
    api.defaults.adapter = async (config) => {
      const response = {
        data: {
          success: false,
          code: 'runtime_settings_conflict',
          message: 'runtime settings changed',
          conflict: { current, current_etag: current.etag },
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
      () => updateChannelRoutingRuntimeSettings(runtimeSettings, settings),
      (error: unknown) => {
        assert.ok(error instanceof ChannelRoutingRuntimeSettingsConflictError)
        assert.equal(error.current?.revision, 5)
        assert.equal(error.currentETag, current.etag)
        return true
      }
    )
  })

  test('preserves field-level validation errors for the form', async () => {
    api.defaults.adapter = async (config) => {
      const response = {
        data: {
          success: false,
          code: 'invalid_runtime_settings',
          message: 'invalid runtime settings',
          field: 'top_k',
          reason: 'expected_integer',
          field_errors: { top_k: 'expected_integer' },
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
      () => updateChannelRoutingRuntimeSettings(runtimeSettings, settings),
      (error: unknown) => {
        assert.deepEqual(getChannelRoutingRuntimeSettingsApiError(error), {
          status: 400,
          code: 'invalid_runtime_settings',
          message: 'invalid runtime settings',
          field: 'top_k',
          reason: 'expected_integer',
          fieldErrors: { top_k: 'expected_integer' },
        })
        return true
      }
    )
  })
})
