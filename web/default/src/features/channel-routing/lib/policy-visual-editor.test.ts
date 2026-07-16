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

import type { PolicyDocument } from '../types'
import {
  parsePolicyCredentialIds,
  parsePolicyOverrides,
  updatePolicyMemberDocument,
  updatePolicyPoolDocument,
  updatePolicyPoolPath,
  updatePolicyPoolProfile,
} from './policy-visual-editor'

const document: PolicyDocument = {
  schema_version: 1,
  extension_root: { keep: true },
  pools: [
    {
      pool_id: 1,
      group_name: 'default',
      display_name: 'Default',
      deployment_stage: 'shadow',
      policy_profile: 'balanced',
      extension_pool: 'keep',
      policy: {
        weight_cost: 0.2,
        canary: {
          slow_start: { minimum_factor: 0.1, extension_nested: true },
          extension_canary: true,
        },
        enterprise: {
          hedge: { enabled: false, extension_hedge: true },
          extension_enterprise: true,
        },
        extension_policy: 'keep',
      },
      members: [
        {
          member_id: 11,
          channel_id: 101,
          enabled: true,
          priority: 10,
          weight: 100,
          credential_ids: [7],
          overrides: { extension_override: true },
          extension_member: 'keep',
        },
      ],
    },
  ],
}

describe('policy visual editor transforms', () => {
  test('preserves unknown fields while editing pools, nested policy, and members', () => {
    const renamed = updatePolicyPoolDocument(document, 0, {
      display_name: 'Primary',
    })
    const slowed = updatePolicyPoolPath(
      renamed,
      0,
      ['canary', 'slow_start', 'minimum_factor'],
      0.25
    )
    const ramped = updatePolicyPoolPath(
      slowed,
      0,
      ['canary', 'slow_start', 'ramp_seconds'],
      600
    )
    const hedged = updatePolicyPoolPath(
      ramped,
      0,
      ['enterprise', 'hedge', 'enabled'],
      true
    )
    const member = updatePolicyMemberDocument(hedged, 0, 0, {
      weight: 0,
    })

    assert.deepEqual(member.extension_root, { keep: true })
    assert.equal(member.pools[0].extension_pool, 'keep')
    assert.equal(member.pools[0].policy.extension_policy, 'keep')
    assert.deepEqual(member.pools[0].policy.canary, {
      extension_canary: true,
      slow_start: {
        minimum_factor: 0.25,
        ramp_seconds: 600,
        extension_nested: true,
      },
    })
    assert.deepEqual(member.pools[0].policy.enterprise, {
      extension_enterprise: true,
      hedge: { enabled: true, extension_hedge: true },
    })
    assert.equal(member.pools[0].members[0].extension_member, 'keep')
    assert.deepEqual(member.pools[0].members[0].overrides, {
      extension_override: true,
    })
  })

  test('accepts unique positive credential ids and rejects ambiguous input', () => {
    assert.deepEqual(parsePolicyCredentialIds('1, 2 3'), {
      ok: true,
      value: [1, 2, 3],
    })
    assert.equal(parsePolicyCredentialIds('1,1').ok, false)
    assert.equal(parsePolicyCredentialIds('-1').ok, false)
    assert.equal(parsePolicyCredentialIds('1.5').ok, false)
  })

  test('accepts only object-shaped member overrides', () => {
    assert.deepEqual(parsePolicyOverrides('{"capacity":{"rpm":10}}'), {
      ok: true,
      value: { capacity: { rpm: 10 } },
    })
    assert.equal(parsePolicyOverrides('[]').ok, false)
    assert.equal(parsePolicyOverrides('{').ok, false)
  })

  test('normalizes enterprise-only settings when the policy profile changes', () => {
    const source = updatePolicyPoolDocument(document, 0, {
      policy: {
        extension_policy: 'keep',
        enterprise: {
          extension_enterprise: true,
          capacity: {
            mode: 'local_soft',
            rpm: 0,
            input_tpm: 200,
            output_tpm: 300,
            total_tpm: 100,
            inflight: 0,
            extension_capacity: 'keep',
          },
          hedge: {
            enabled: true,
            cross_region: true,
            extension_hedge: true,
          },
        },
      },
    })

    const entered = updatePolicyPoolProfile(source, 0, 'enterprise_slo')
    const enteredEnterprise = entered.pools[0].policy.enterprise as Record<
      string,
      unknown
    >
    const enteredCapacity = enteredEnterprise.capacity as Record<
      string,
      unknown
    >
    assert.equal(entered.pools[0].policy_profile, 'enterprise_slo')
    assert.equal(entered.pools[0].policy.extension_policy, 'keep')
    assert.equal(enteredEnterprise.extension_enterprise, true)
    assert.equal(enteredCapacity.extension_capacity, 'keep')
    assert.equal(enteredCapacity.mode, 'redis_block')
    assert.equal(enteredCapacity.rpm, 600)
    assert.equal(enteredCapacity.total_tpm, 300)
    assert.equal(enteredCapacity.inflight, 32)
    assert.deepEqual(enteredEnterprise.hedge, { enabled: false })

    const left = updatePolicyPoolProfile(entered, 0, 'balanced')
    const leftEnterprise = left.pools[0].policy.enterprise as Record<
      string,
      unknown
    >
    const leftCapacity = leftEnterprise.capacity as Record<string, unknown>
    assert.equal(left.pools[0].policy_profile, 'balanced')
    assert.equal(leftCapacity.mode, 'local_soft')
    assert.equal(leftCapacity.extension_capacity, 'keep')
    assert.equal('hedge' in leftEnterprise, false)
  })
})
