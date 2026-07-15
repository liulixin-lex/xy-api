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
import { costBindingFormValues, costBindingRequest } from './cost-binding'
import {
  costBindingAccountPreviewMatchesRequest,
  costBindingServerFieldError,
  costBindingTestPreviewMatchesRequest,
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
  test('uses account identity for groups and the complete target for tests', () => {
    const values = costBindingFormValues(binding)
    values.newApiAccessToken = 'access-token'
    values.gatewayApiKey = 'gateway-key'
    values.customCaPem = 'ca-one'
    const previous = costBindingRequest(values)

    for (const mutate of [
      (draft: typeof values) => {
        draft.upstreamType = 'sub2api'
      },
      (draft: typeof values) => {
        draft.baseUrl = 'https://other.example.com'
      },
      (draft: typeof values) => {
        draft.egressAllowedPrivateCidrs = '10.30.0.0/16'
      },
      (draft: typeof values) => {
        draft.newApiUserId = '9'
      },
      (draft: typeof values) => {
        draft.newApiAccessToken = 'rotated-access-token'
      },
      (draft: typeof values) => {
        draft.gatewayApiKey = 'rotated-gateway-key'
      },
      (draft: typeof values) => {
        draft.customCaPem = 'ca-two'
      },
      (draft: typeof values) => {
        draft.clearNewApiAccessToken = true
      },
      (draft: typeof values) => {
        draft.clearGatewayApiKey = true
      },
      (draft: typeof values) => {
        draft.clearCustomCaPem = true
      },
    ]) {
      const draft = { ...values }
      mutate(draft)
      const request = costBindingRequest(draft)
      assert.equal(
        costBindingAccountPreviewMatchesRequest(previous, request),
        false
      )
      assert.equal(
        costBindingTestPreviewMatchesRequest(previous, request),
        false
      )
    }

    const groupOnly = { ...values, upstreamGroup: 'enterprise' }
    const groupRequest = costBindingRequest(groupOnly)
    assert.equal(
      costBindingAccountPreviewMatchesRequest(previous, groupRequest),
      true
    )
    assert.equal(
      costBindingTestPreviewMatchesRequest(previous, groupRequest),
      false
    )
    const sub2apiValues = {
      ...values,
      upstreamType: 'sub2api' as const,
      newApiAccessToken: '',
      sub2apiEmail: 'owner@example.com',
      sub2apiPassword: 'password-one',
      sub2apiToken: 'jwt-one',
    }
    const sub2apiPrevious = costBindingRequest(sub2apiValues)
    const claudeOnly = { ...sub2apiValues, servesClaudeCode: true }
    const claudeRequest = costBindingRequest(claudeOnly)
    assert.equal(
      costBindingAccountPreviewMatchesRequest(sub2apiPrevious, claudeRequest),
      true
    )
    assert.equal(
      costBindingTestPreviewMatchesRequest(sub2apiPrevious, claudeRequest),
      false
    )
    for (const mutate of [
      (draft: typeof sub2apiValues) => {
        draft.sub2apiEmail = 'other@example.com'
      },
      (draft: typeof sub2apiValues) => {
        draft.sub2apiPassword = 'password-two'
      },
      (draft: typeof sub2apiValues) => {
        draft.sub2apiToken = 'jwt-two'
      },
      (draft: typeof sub2apiValues) => {
        draft.clearSub2apiEmail = true
      },
      (draft: typeof sub2apiValues) => {
        draft.clearSub2apiPassword = true
      },
      (draft: typeof sub2apiValues) => {
        draft.clearSub2apiToken = true
      },
    ]) {
      const draft = { ...sub2apiValues }
      mutate(draft)
      const request = costBindingRequest(draft)
      assert.equal(
        costBindingAccountPreviewMatchesRequest(sub2apiPrevious, request),
        false
      )
      assert.equal(
        costBindingTestPreviewMatchesRequest(sub2apiPrevious, request),
        false
      )
    }
  })

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

  test('keeps deferred group loads but drops tests after the target changes', async () => {
    const values = {
      ...costBindingFormValues(binding),
      upstreamType: 'sub2api' as const,
      upstreamGroup: '44',
      sub2apiToken: 'jwt-one',
    }
    const request = costBindingRequest(values)

    for (const mutate of [
      (draft: typeof values) => {
        draft.upstreamGroup = 'subscription-plan'
      },
      (draft: typeof values) => {
        draft.servesClaudeCode = true
      },
    ]) {
      const current = { ...values }
      mutate(current)
      const currentRequest = costBindingRequest(current)
      const groups = deferred<string>()
      const testResult = deferred<string>()
      const applied: string[] = []
      const completions = [
        groups.promise.then((value) => {
          if (
            costBindingAccountPreviewMatchesRequest(request, currentRequest)
          ) {
            applied.push(value)
          }
        }),
        testResult.promise.then((value) => {
          if (costBindingTestPreviewMatchesRequest(request, currentRequest)) {
            applied.push(value)
          }
        }),
      ]

      groups.resolve('groups')
      testResult.resolve('test')
      await Promise.all(completions)

      assert.deepEqual(applied, ['groups'])
    }
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
      [
        'query_or_fragment_not_allowed',
        'Enter a Base URL without query parameters or a fragment.',
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

  test('maps management authentication failures to provider credentials', () => {
    const translate = (key: string) => `translated:${key}`
    const failure = {
      field: 'credentials',
      reason: 'management_auth_required',
    }

    assert.deepEqual(
      costBindingServerFieldError(failure, translate, 'newapi'),
      {
        name: 'newApiAccessToken',
        message:
          'translated:New API account access requires both an Access Token and user ID. A separate Gateway API Key verifies which models the channel can serve.',
      }
    )
    assert.deepEqual(
      costBindingServerFieldError(failure, translate, 'sub2api'),
      {
        name: 'sub2apiToken',
        message:
          'translated:Sub2API account access requires a JWT or both email and password. Gateway API Key is kept separate and does not authorize account balance access.',
      }
    )
    assert.equal(
      costBindingServerFieldError(
        { field: 'credentials', reason: 'invalid' },
        translate,
        'sub2api'
      ),
      null
    )
  })

  test('maps serving authentication failures to the gateway key field', () => {
    const translate = (key: string) => `translated:${key}`

    assert.deepEqual(
      costBindingServerFieldError(
        { field: 'gateway_api_key', reason: 'serving_auth_required' },
        translate,
        'newapi'
      ),
      {
        name: 'gatewayApiKey',
        message:
          'translated:Gateway API Key is required to verify which models the channel can serve.',
      }
    )
  })
})
