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

import type { PolicyDraftSummary } from '../types'
import {
  mostRecentWorkingPolicyDraft,
  policyDraftBlockingReasonLabel,
} from './policy-draft-workspace'

function draft(
  id: number,
  workspaceState: PolicyDraftSummary['workspace_state']
): PolicyDraftSummary {
  return {
    id,
    base_revision: 3,
    base_hash: 'a'.repeat(64),
    version: 1,
    etag: 'b'.repeat(64),
    document_hash: 'c'.repeat(64),
    status: 'editing',
    created_by: 1,
    updated_by: 1,
    validated_head_revision: 0,
    validated_head_hash: '',
    published_revision: 0,
    created_time_ms: 100,
    updated_time_ms: 200,
    validated_time_ms: 0,
    published_time_ms: 0,
    workspace_state: workspaceState,
    stale: workspaceState === 'stale',
    can_validate: workspaceState === 'working',
    can_publish: false,
    can_delete: true,
    blocking_reason:
      workspaceState === 'stale' ? 'base_policy_changed' : undefined,
  }
}

describe('policy draft workspace', () => {
  test('opens the newest working draft and skips newer stale drafts', () => {
    const selected = mostRecentWorkingPolicyDraft([
      draft(9, 'stale'),
      draft(8, 'working'),
      draft(7, 'working'),
    ])

    assert.equal(selected?.id, 8)
  })

  test('maps stable blocking reasons to operator guidance', () => {
    assert.equal(
      policyDraftBlockingReasonLabel('base_policy_changed'),
      'The published policy changed after this draft was created.'
    )
    assert.equal(
      policyDraftBlockingReasonLabel('future_reason'),
      'future_reason'
    )
  })
})
