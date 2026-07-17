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
  analyzePolicyDocument,
  formatPolicyDocumentPath,
  formatPolicyDocumentText,
  starterPolicyDocumentText,
} from './policy-document'

const validDocument = {
  schema_version: 2,
  pools: [
    {
      pool_id: 1,
      group_name: 'prod',
      display_name: 'Production',
      deployment_stage: 'canary',
      policy_profile: 'balanced',
      policy: {},
      default_enabled: true,
      default_priority: 0,
      default_weight: 100,
      members: [
        {
          member_id: 11,
          channel_id: 101,
          routing_generation: '00000000000000000000000000000065',
          enabled: true,
          priority: 0,
          weight: 100,
          enabled_override: true,
          priority_override: 0,
          weight_override: 100,
          credential_ids: [1001],
          overrides: {},
        },
      ],
    },
  ],
}

describe('channel routing policy document analysis', () => {
  test('accepts the persisted schema and reports its operational shape', () => {
    const analysis = analyzePolicyDocument(JSON.stringify(validDocument))
    assert.equal(analysis.valid, true)
    assert.equal(analysis.summary.poolCount, 1)
    assert.equal(analysis.summary.memberCount, 1)
    assert.equal(analysis.summary.enabledMemberCount, 1)
    assert.deepEqual(analysis.summary.stages, { canary: 1 })
    assert.deepEqual(analysis.summary.profiles, { balanced: 1 })
    assert.deepEqual(analysis.document, validDocument)
  })

  test('normalizes nullable dynamic members and omitted v2 pool defaults', () => {
    const source = {
      schema_version: 2,
      pools: [
        {
          pool_id: 1,
          group_name: 'default',
          display_name: 'Default',
          deployment_stage: 'observe',
          policy_profile: 'balanced',
          policy: {},
          default_enabled: false,
          default_priority: 0,
          default_weight: 0,
          members: null,
        },
        {
          pool_id: 2,
          group_name: 'secondary',
          display_name: 'Secondary',
          deployment_stage: 'shadow',
          policy_profile: 'cost_aware',
          policy: {},
        },
      ],
    }

    const analysis = analyzePolicyDocument(JSON.stringify(source))

    assert.equal(analysis.valid, true)
    assert.equal(analysis.summary.memberCount, 0)
    assert.deepEqual(analysis.document?.pools[0].members, [])
    assert.deepEqual(analysis.document?.pools[1].members, [])
    assert.equal(analysis.document?.pools[0].default_enabled, false)
    assert.equal(analysis.document?.pools[0].default_priority, 0)
    assert.equal(analysis.document?.pools[0].default_weight, 0)
    assert.equal(analysis.document?.pools[1].default_enabled, true)
    assert.equal(analysis.document?.pools[1].default_priority, 0)
    assert.equal(analysis.document?.pools[1].default_weight, 100)
  })

  test('preserves explicit zero and false v2 member overrides', () => {
    const source = structuredClone(validDocument)
    const member = source.pools[0].members[0]
    member.enabled = true
    member.priority = 9
    member.weight = 100
    member.enabled_override = false
    member.priority_override = 0
    member.weight_override = 0

    const analysis = analyzePolicyDocument(JSON.stringify(source))

    assert.equal(analysis.valid, true)
    assert.equal(analysis.summary.enabledMemberCount, 0)
    assert.equal(analysis.document?.pools[0].members[0].enabled, false)
    assert.equal(analysis.document?.pools[0].members[0].priority, 0)
    assert.equal(analysis.document?.pools[0].members[0].weight, 0)
    assert.equal(analysis.document?.pools[0].members[0].weight_override, 0)
  })

  test('accepts legacy v1 history without rewriting its schema', () => {
    const legacyDocument = structuredClone(validDocument) as PolicyDocument
    legacyDocument.schema_version = 1
    delete legacyDocument.pools[0].default_enabled
    delete legacyDocument.pools[0].default_priority
    delete legacyDocument.pools[0].default_weight
    const member = legacyDocument.pools[0].members[0]
    delete member.routing_generation
    delete member.enabled_override
    delete member.priority_override
    delete member.weight_override

    const analysis = analyzePolicyDocument(JSON.stringify(legacyDocument))

    assert.equal(analysis.valid, true)
    assert.deepEqual(analysis.document, legacyDocument)
  })

  test('diagnoses v2-only defaults and overrides in legacy documents', () => {
    const legacyDocument = structuredClone(validDocument)
    legacyDocument.schema_version = 1

    const analysis = analyzePolicyDocument(JSON.stringify(legacyDocument))

    assert.equal(analysis.valid, false)
    assert.ok(
      analysis.issues.some(
        (issue) =>
          issue.code === 'unsupported_schema_field' &&
          formatPolicyDocumentPath(issue.path) === '$.pools[0].default_weight'
      )
    )
    assert.ok(
      analysis.issues.some(
        (issue) =>
          issue.code === 'unsupported_schema_field' &&
          formatPolicyDocumentPath(issue.path) ===
            '$.pools[0].members[0].weight_override'
      )
    )
  })

  test('rejects malformed v2 routing generations and overrides', () => {
    const source = structuredClone(validDocument)
    const member = source.pools[0].members[0]
    member.routing_generation = 'GENERATION-101'
    member.enabled_override = 'yes' as unknown as boolean
    member.weight_override = -1

    const analysis = analyzePolicyDocument(JSON.stringify(source, null, 2))

    assert.equal(analysis.valid, false)
    assert.deepEqual(
      analysis.issues.map((issue) => [
        issue.code,
        formatPolicyDocumentPath(issue.path),
      ]),
      [
        [
          'invalid_routing_generation',
          '$.pools[0].members[0].routing_generation',
        ],
        ['expected_boolean', '$.pools[0].members[0].enabled_override'],
        [
          'expected_nonnegative_integer',
          '$.pools[0].members[0].weight_override',
        ],
      ]
    )
  })

  test('uses schema v2 for new starter documents', () => {
    assert.deepEqual(JSON.parse(starterPolicyDocumentText()), {
      schema_version: 2,
      pools: [],
    })
  })

  test('locates JSON syntax errors by line and column', () => {
    const analysis = analyzePolicyDocument(
      '{\n  "schema_version": 2,\n  "pools": [}\n'
    )
    assert.equal(analysis.valid, false)
    assert.equal(analysis.issues[0]?.kind, 'syntax')
    assert.equal(analysis.issues[0]?.line, 3)
    assert.ok((analysis.issues[0]?.column ?? 0) > 0)
  })

  test('reports duplicate identities at the exact JSON path', () => {
    const duplicate = structuredClone(validDocument)
    duplicate.pools[0].members.push({
      ...duplicate.pools[0].members[0],
      channel_id: 102,
    })
    const analysis = analyzePolicyDocument(JSON.stringify(duplicate, null, 2))
    const issue = analysis.issues.find(
      (candidate) =>
        candidate.code === 'duplicate_value' &&
        formatPolicyDocumentPath(candidate.path) ===
          '$.pools[0].members[1].member_id'
    )
    assert.ok(issue)
    assert.ok(issue.line > 1)
  })

  test('formats valid JSON without changing numeric policy values', () => {
    const source = JSON.stringify(validDocument)
    const formatted = formatPolicyDocumentText(source)
    assert.ok(formatted)
    assert.equal(formatted.endsWith('\n'), true)
    assert.deepEqual(JSON.parse(formatted), validDocument)
  })
})
