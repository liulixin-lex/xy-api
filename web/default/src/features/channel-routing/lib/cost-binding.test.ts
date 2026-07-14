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
import {
  boundedCostBindingGroups,
  COST_BINDING_GROUP_DOM_LIMIT,
  costBindingFormValues,
  costBindingRequest,
  costBindingUpdateRequest,
  createCostBindingSchema,
} from './cost-binding'

const translate = (key: string) => key

const binding: RoutingCostBinding = {
  id: 4,
  channel_id: 42,
  channel_name: 'Primary upstream',
  etag: '"crb.4.100.hash"',
  upstream_type: 'newapi',
  base_url: 'https://upstream.example.com',
  upstream_group: 'default',
  serves_claude_code: false,
  egress_allowed_private_cidrs: ['10.20.30.0/24', 'fd12:3456::/64'],
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

describe('channel routing cost binding form contract', () => {
  test('keeps stored credentials when edit fields stay blank', () => {
    const request = costBindingRequest(costBindingFormValues(binding))

    assert.deepEqual(request.credentials, {})
    assert.equal(request.channel_id, 42)
    assert.equal(request.new_api_user_id, 8)
    assert.deepEqual(request.egress_allowed_private_cidrs, [
      '10.20.30.0/24',
      'fd12:3456::/64',
    ])
  })

  test('sends an explicit empty string only for credentials marked for removal', () => {
    const values = costBindingFormValues(binding)
    values.clearGatewayApiKey = true
    values.newApiAccessToken = ' replacement-token '

    assert.deepEqual(costBindingRequest(values).credentials, {
      new_api_access_token: 'replacement-token',
      gateway_api_key: '',
    })
  })

  test('preserves custom trust settings unless an operator changes them explicitly', () => {
    const values = costBindingFormValues({
      ...binding,
      credential_masks: {
        ...binding.credential_masks,
        custom_ca_configured: true,
      },
    })

    assert.equal(
      values.egressAllowedPrivateCidrs,
      '10.20.30.0/24\nfd12:3456::/64'
    )
    assert.equal(
      costBindingRequest(values).credentials.custom_ca_pem,
      undefined
    )

    values.clearCustomCaPem = true
    assert.equal(costBindingRequest(values).credentials.custom_ca_pem, '')
  })

  test('normalizes provider-only fields without dropping an explicit disabled state', () => {
    const values = costBindingFormValues(binding)
    values.upstreamType = 'sub2api'
    values.servesClaudeCode = true
    values.enabled = false

    const request = costBindingRequest(values)
    assert.equal(request.new_api_user_id, undefined)
    assert.equal(request.serves_claude_code, true)
    assert.equal(request.enabled, false)
  })

  test('serializes only provider credentials plus shared gateway and CA settings', () => {
    const values = costBindingFormValues(binding)
    values.newApiAccessToken = 'newapi-token'
    values.sub2apiEmail = 'operator@example.com'
    values.sub2apiPassword = 'sub2api-password'
    values.sub2apiToken = 'sub2api-token'
    values.gatewayApiKey = 'gateway-key'
    values.customCaPem = 'shared-ca'

    assert.deepEqual(costBindingRequest(values).credentials, {
      new_api_access_token: 'newapi-token',
      gateway_api_key: 'gateway-key',
      custom_ca_pem: 'shared-ca',
    })

    values.upstreamType = 'sub2api'
    assert.deepEqual(costBindingRequest(values).credentials, {
      sub2api_email: 'operator@example.com',
      sub2api_password: 'sub2api-password',
      sub2api_token: 'sub2api-token',
      gateway_api_key: 'gateway-key',
      custom_ca_pem: 'shared-ca',
    })
  })

  test('does not clear credentials owned by an inactive provider', () => {
    const values = costBindingFormValues(binding)
    values.upstreamType = 'sub2api'
    values.clearNewApiAccessToken = true
    values.clearSub2apiToken = true

    assert.deepEqual(costBindingRequest(values).credentials, {
      sub2api_token: '',
    })
  })

  test('omits an optional user ID that contains only whitespace', () => {
    const values = costBindingFormValues(binding)
    values.newApiUserId = '   '

    assert.equal(costBindingRequest(values).new_api_user_id, undefined)
  })

  test('rejects insecure or secret-bearing endpoint URLs', () => {
    const schema = createCostBindingSchema(translate)
    const values = costBindingFormValues(binding)

    assert.equal(
      schema.safeParse({ ...values, baseUrl: 'http://example.com' }).success,
      false
    )
    assert.equal(
      schema.safeParse({
        ...values,
        baseUrl: 'https://user:pass@example.com',
      }).success,
      false
    )
    assert.equal(
      schema.safeParse({
        ...values,
        baseUrl: 'https://example.com?api_key=secret',
      }).success,
      false
    )
    assert.equal(schema.safeParse(values).success, true)
  })

  test('accepts only bounded private CIDR exceptions', () => {
    const schema = createCostBindingSchema(translate)
    const values = costBindingFormValues(binding)

    assert.equal(
      schema.safeParse({
        ...values,
        egressAllowedPrivateCidrs: '10.0.0.0/8\n172.20.0.0/16\nfd12::/64',
      }).success,
      true
    )
    for (const cidr of [
      '127.0.0.0/8',
      '169.254.0.0/16',
      '192.168.0.0/8',
      '2001:db8::/64',
      'not-a-cidr',
    ]) {
      assert.equal(
        schema.safeParse({ ...values, egressAllowedPrivateCidrs: cidr })
          .success,
        false,
        cidr
      )
    }
    assert.equal(
      schema.safeParse({
        ...values,
        egressAllowedPrivateCidrs: Array.from(
          { length: 33 },
          (_, index) => `10.${index}.0.0/16`
        ).join('\n'),
      }).success,
      false
    )
  })

  test('builds a complete CAS update payload for a quick enable toggle', () => {
    assert.deepEqual(costBindingUpdateRequest(binding, { enabled: false }), {
      channel_id: 42,
      upstream_type: 'newapi',
      base_url: 'https://upstream.example.com',
      upstream_group: 'default',
      serves_claude_code: false,
      egress_allowed_private_cidrs: ['10.20.30.0/24', 'fd12:3456::/64'],
      new_api_user_id: 8,
      enabled: false,
      credentials: {},
    })
  })

  test('bounds and deduplicates untrusted upstream groups for UI state', () => {
    const groups = Array.from(
      { length: COST_BINDING_GROUP_DOM_LIMIT + 50 },
      (_, index) => `group-${index}`
    )
    groups.splice(2, 0, 'group-1', '', 'x'.repeat(129))

    const result = boundedCostBindingGroups({
      channel_id: 42,
      upstream_type: 'newapi',
      groups,
      groups_total: 500,
      groups_truncated: true,
      model_count: 12,
    })

    assert.equal(result.groups.length, COST_BINDING_GROUP_DOM_LIMIT)
    assert.equal(new Set(result.groups).size, COST_BINDING_GROUP_DOM_LIMIT)
    assert.equal(result.groups_total, 500)
    assert.equal(result.groups_truncated, true)
    assert.equal(result.groups.includes(''), false)
    assert.equal(
      result.groups.some((group) => group.length > 128),
      false
    )
  })
})
