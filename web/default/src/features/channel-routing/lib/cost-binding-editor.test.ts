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

import type { RoutingCostBinding } from '../types'
import { costBindingFormValues } from './cost-binding'
import {
  costBindingServerFieldError,
  CostBindingEditorSessionManager,
  mergeCostBindingConflictDraft,
} from './cost-binding-editor'

const binding: RoutingCostBinding = {
  id: 4,
  channel_id: 42,
  channel_name: 'Primary upstream',
  etag: '"crb.4.100.hash"',
  upstream_type: 'newapi',
  base_url: 'https://upstream.example.com',
  upstream_group: 'default',
  serves_claude_code: false,
  egress_allowed_private_cidrs: ['10.20.30.0/24'],
  new_api_user_id: 8,
  enabled: true,
  sync_failure_count: 0,
  sync_backoff_until: 0,
  credential_masks: {
    new_api_access_token: '****1234',
    gateway_api_key: '****5678',
  },
  created_time: 100,
  updated_time: 100,
}

function deferred<T>() {
  let resolve: (value: T) => void = () => undefined
  const promise = new Promise<T>((promiseResolve) => {
    resolve = promiseResolve
  })
  return { promise, resolve }
}

describe('cost binding editor concurrency', () => {
  test('invalidates deferred save, test, and groups callbacks after subject rotation', async () => {
    const manager = new CostBindingEditorSessionManager()
    const oldSession = manager.activate('binding:42:old')
    const applied: string[] = []
    const operations = ['save', 'test', 'groups'].map((name) => {
      const result = deferred<string>()
      const completion = result.promise.then((value) => {
        if (manager.isCurrent(oldSession, 'binding:42:old')) {
          applied.push(value)
        }
      })
      return { name, ...result, completion }
    })

    const currentSession = manager.activate('binding:77:new')
    assert.equal(oldSession.signal.aborted, true)
    assert.equal(currentSession.signal.aborted, false)
    for (const operation of operations) operation.resolve(operation.name)
    await Promise.all(operations.map((operation) => operation.completion))

    assert.deepEqual(applied, [])
    assert.equal(manager.isCurrent(currentSession, 'binding:77:new'), true)
  })

  test('keeps dirty draft values and new credentials on top of the latest ETag', () => {
    const latest: RoutingCostBinding = {
      ...binding,
      etag: '"crb.4.101.hash"',
      base_url: 'https://server.example.com',
      enabled: false,
      credential_masks: {
        ...binding.credential_masks,
        new_api_access_token: '****9999',
      },
      updated_time: 101,
    }
    const draft = costBindingFormValues(binding)
    draft.baseUrl = 'https://draft.example.com'
    draft.upstreamGroup = 'enterprise'
    draft.newApiAccessToken = 'new-secret'

    const merged = mergeCostBindingConflictDraft({
      baseline: binding,
      latest,
      draft,
      dirtyFields: {
        baseUrl: true,
        upstreamGroup: true,
        newApiAccessToken: true,
      },
    })

    assert.equal(merged.values.baseUrl, 'https://draft.example.com')
    assert.equal(merged.values.upstreamGroup, 'enterprise')
    assert.equal(merged.values.enabled, false)
    assert.equal(merged.values.newApiAccessToken, 'new-secret')
    assert.deepEqual(merged.serverChangedLabels, [
      'Base URL',
      'Cost sync enabled',
      'New API Access Token',
    ])
    assert.deepEqual(merged.overlappingLabels, [
      'Base URL',
      'New API Access Token',
    ])
  })

  test('maps structured server field and reason values to form errors', () => {
    const translate = (key: string) => `translated:${key}`
    const cases = [
      ['required', 'This field is required.'],
      ['unsupported', 'This value is not supported.'],
      ['invalid', 'Enter a valid value.'],
      ['too_long', 'This value is too long.'],
      [
        'insecure_scheme',
        'HTTPS is required. Do not place tokens or passwords in the URL.',
      ],
      [
        'credentials_not_allowed',
        'HTTPS is required. Do not place tokens or passwords in the URL.',
      ],
      [
        'sensitive_query_not_allowed',
        'HTTPS is required. Do not place tokens or passwords in the URL.',
      ],
      ['unsafe_target', 'This target is blocked by the network trust policy.'],
      ['invalid_json', 'Enter a valid value.'],
      ['invalid_type', 'Enter a valid value.'],
      ['unknown_field', 'This value is not supported.'],
    ] as const

    for (const [reason, message] of cases) {
      assert.deepEqual(
        costBindingServerFieldError(
          { field: 'credentials.gateway_api_key', reason },
          translate
        ),
        {
          name: 'gatewayApiKey',
          message: `translated:${message}`,
        },
        reason
      )
    }
    assert.deepEqual(
      costBindingServerFieldError(
        { field: 'base_url', reason: 'provider-specific-reason' },
        translate
      ),
      {
        name: 'baseUrl',
        message:
          'translated:The server rejected this value. Review it and try again.',
      }
    )
    assert.equal(
      costBindingServerFieldError(
        { field: 'unrecognized_field', reason: 'invalid' },
        translate
      ),
      null
    )
  })
})
