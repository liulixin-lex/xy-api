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
export type ManualBillingReviewKind =
  | 'send_outcome'
  | 'acceptance_overage'
  | 'accepted_handoff'
  | 'terminal_overage'
  | 'terminal_usage'

export type ManualBillingReviewAction =
  | 'confirmed_accepted'
  | 'confirmed_rejected'

export type ManualBillingProviderStatus =
  | 'confirmed_accepted'
  | 'confirmed_rejected'
  | 'confirmed_not_found'
  | 'terminal_usage_verified'

export type ManualBillingReviewAttempt = {
  attempt_index: number
  state: string
  channel_id: number
  credential_id: number
  channel_version: string
  authorized_ms: number
  send_deadline_ms: number
  resolved_ms?: number
}

export type ManualBillingReviewFinancialConsequences = {
  current_charge: number
  accept_additional_charge: number
  accept_final_charge: number
  reject_refund: number
  reject_final_charge: number
  reject_write_off: number
}

export type ManualBillingReviewItem = {
  reservation_id: number
  kind: string
  // The server may add review kinds before this client is upgraded. Keep the
  // response forward-compatible and fail closed in the resolution workflow.
  review_kind: string
  public_task_id: string
  upstream_task_id?: string
  user_id: number
  state: string
  current_quota: number
  accepted_quota: number
  review_version: number
  etag: string
  manual_review_since_ms: number
  reason: string
  can_accept: boolean
  can_reject: boolean
  blockers: string[]
  financial_consequences: ManualBillingReviewFinancialConsequences
  attempts: ManualBillingReviewAttempt[]
}

export type ManualBillingReviewPage = {
  pending_count: number
  oldest_age_seconds: number
  items: ManualBillingReviewItem[]
  next_cursor?: number
  has_more: boolean
  capabilities?: {
    can_resolve?: boolean
  }
}

export type ManualBillingReviewResolutionRequest = {
  action: ManualBillingReviewAction
  expected_version: number
  upstream_task_id: string
  provider_status: ManualBillingProviderStatus
  provider_checked_ms: number
  evidence_reference: string
  reason: string
}

export type ManualBillingReviewResolutionResult = {
  reservation_id: number
  state: string
  review_version: number
  etag: string
  current_quota: number
  resolution: {
    id: number
    action: string
    review_kind: string
    before_state: string
    after_state: string
    before_quota: number
    after_quota: number
    quota_delta: number
    resolved_time_ms: number
  }
}

export type ManualBillingReviewApiError = {
  status?: number
  code?: string
  message?: string
}
