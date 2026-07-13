import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  analyzePolicyDocument,
  formatPolicyDocumentPath,
  formatPolicyDocumentText,
} from './policy-document'

const validDocument = {
  schema_version: 1,
  pools: [
    {
      pool_id: 1,
      group_name: 'prod',
      display_name: 'Production',
      deployment_stage: 'canary',
      policy_profile: 'balanced',
      policy: {},
      members: [
        {
          member_id: 11,
          channel_id: 101,
          enabled: true,
          priority: 0,
          weight: 100,
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

  test('locates JSON syntax errors by line and column', () => {
    const analysis = analyzePolicyDocument(
      '{\n  "schema_version": 1,\n  "pools": [}\n'
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
