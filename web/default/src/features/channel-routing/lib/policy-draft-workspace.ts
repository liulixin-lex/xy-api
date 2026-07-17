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
import type { PolicyDraftSummary } from '../types'

const blockingReasonLabels: Record<string, string> = {
  base_policy_changed:
    'The published policy changed after this draft was created.',
  draft_requires_validation: 'Validate this draft before publishing.',
  validation_evidence_stale:
    'Validation evidence no longer matches the published policy.',
  published: 'This draft has already been published.',
}

export function mostRecentWorkingPolicyDraft(
  drafts: PolicyDraftSummary[]
): PolicyDraftSummary | undefined {
  return drafts.find((draft) => draft.workspace_state === 'working')
}

export function policyDraftBlockingReasonLabel(reason: string | undefined) {
  if (!reason) return ''
  return blockingReasonLabels[reason] ?? reason
}

export function policyDraftReadinessLabel(draft: PolicyDraftSummary) {
  if (draft.can_publish) return 'Ready to publish'
  if (draft.can_validate) return 'Ready to validate'
  return policyDraftBlockingReasonLabel(draft.blocking_reason)
}
