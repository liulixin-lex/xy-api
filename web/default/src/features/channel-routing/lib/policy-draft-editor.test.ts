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

import type { PolicyDraftDetail } from '../types'
import {
  policyDraftDetailBlocksEditor,
  policyDraftDetailIdentity,
  policyDraftDetailUpdate,
} from './policy-draft-editor'

const detail = {
  id: 7,
  base_revision: 3,
  base_hash: 'a'.repeat(64),
  version: 4,
  etag: 'b'.repeat(64),
  server_etag: `"crd.7.4.${'b'.repeat(64)}"`,
  document_hash: 'c'.repeat(64),
  status: 'editing',
  created_by: 1,
  updated_by: 1,
  validated_head_revision: 0,
  validated_head_hash: '',
  published_revision: 0,
  created_time_ms: 100,
  updated_time_ms: 100,
  validated_time_ms: 0,
  published_time_ms: 0,
  workspace_state: 'working',
  stale: false,
  can_validate: true,
  can_publish: false,
  can_delete: true,
  blocking_reason: 'draft_requires_validation',
  document: { schema_version: 2, pools: [] },
} satisfies PolicyDraftDetail

describe('policy draft editor concurrency', () => {
  test('keeps dirty values when SSE delivers a newer authoritative detail', () => {
    const next = {
      ...detail,
      version: 5,
      etag: 'd'.repeat(64),
      server_etag: `"crd.7.5.${'d'.repeat(64)}"`,
      updated_time_ms: 200,
    }

    assert.equal(policyDraftDetailUpdate(detail, next, true), 'defer')
    assert.equal(policyDraftDetailUpdate(detail, next, false), 'apply')
  })

  test('applies the first detail and ignores the same authoritative version', () => {
    assert.equal(policyDraftDetailUpdate(null, detail, true), 'apply')
    assert.equal(policyDraftDetailUpdate(detail, { ...detail }, true), 'ignore')
    assert.equal(
      policyDraftDetailIdentity(detail),
      `7:4:"crd.7.4.${'b'.repeat(64)}"`
    )
  })

  test('keeps the editor mounted when a background refetch fails with cache', () => {
    assert.equal(
      policyDraftDetailBlocksEditor({
        editing: true,
        isError: true,
        hasCachedData: true,
        hasAuthority: true,
      }),
      false
    )
    assert.equal(
      policyDraftDetailBlocksEditor({
        editing: true,
        isError: true,
        hasCachedData: false,
        hasAuthority: false,
      }),
      true
    )
  })
})
