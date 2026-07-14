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

import type { ManualBillingReviewItem } from '../billing-review-types'
import {
  buildManualBillingReviewResolution,
  createManualBillingReviewSchema,
  getManualBillingReviewBlockerDisplay,
  getManualBillingReviewBlockerLabelKey,
  getManualBillingReviewConfirmationImpact,
  getManualBillingReviewConsequenceRows,
  getManualBillingReviewKindDisplay,
  getManualBillingReviewKindLabelKey,
  getManualBillingReviewNextCursor,
  ManualBillingReviewSession,
  manualBillingReviewHasUnknownBlocker,
  manualBillingReviewKindIsSupported,
  manualBillingReviewNeedsRefresh,
  type ManualBillingReviewValidationMessages,
} from './manual-billing-review'

const messages: ManualBillingReviewValidationMessages = {
  actionRequired: 'action required',
  actionUnavailable: 'action unavailable',
  upstreamTaskRequired: 'upstream required',
  upstreamTaskTooLong: 'upstream too long',
  evidenceRequired: 'evidence required',
  evidenceTooLong: 'evidence too long',
  reasonRequired: 'reason required',
  reasonTooLong: 'reason too long',
  singleLineRequired: 'single line required',
  checkedTimeRequired: 'checked time required',
  checkedTimeTooEarly: 'checked time too early',
  checkedTimeFuture: 'checked time future',
}

function review(
  patch: Partial<ManualBillingReviewItem> = {}
): ManualBillingReviewItem {
  return {
    reservation_id: 42,
    kind: 'task',
    review_kind: 'send_outcome',
    public_task_id: 'task-public',
    upstream_task_id: '',
    user_id: 7,
    state: 'manual_review',
    current_quota: 100,
    accepted_quota: 100,
    review_version: 3,
    etag: '"async-billing-review-42-v3"',
    manual_review_since_ms: 1_700_000_000_000,
    reason: 'ambiguous send',
    can_accept: true,
    can_reject: true,
    blockers: [],
    financial_consequences: {
      current_charge: 100,
      accept_additional_charge: 20,
      accept_final_charge: 120,
      reject_refund: 100,
      reject_final_charge: 0,
      reject_write_off: 7,
    },
    attempts: [],
    ...patch,
  }
}

describe('manual billing review decisions', () => {
  test('maps provider evidence statuses exactly for all review kinds', () => {
    const checkedAt = '2023-11-14T22:13:20'
    const common = {
      upstream_task_id: 'upstream-1',
      provider_checked_at: checkedAt,
      evidence_reference: 'provider/case-1',
      reason: 'confirmed in provider console',
      rejection_provider_status: 'confirmed_rejected' as const,
    }

    assert.equal(
      buildManualBillingReviewResolution(review(), {
        ...common,
        action: 'confirmed_accepted',
      }).provider_status,
      'confirmed_accepted'
    )
    assert.equal(
      buildManualBillingReviewResolution(review(), {
        ...common,
        action: 'confirmed_rejected',
        rejection_provider_status: 'confirmed_not_found',
      }).provider_status,
      'confirmed_not_found'
    )
    assert.equal(
      buildManualBillingReviewResolution(
        review({ review_kind: 'acceptance_overage' }),
        { ...common, action: 'confirmed_accepted' }
      ).provider_status,
      'confirmed_accepted'
    )
    assert.equal(
      buildManualBillingReviewResolution(
        review({ review_kind: 'acceptance_overage' }),
        { ...common, action: 'confirmed_rejected' }
      ).provider_status,
      'confirmed_accepted'
    )
    assert.equal(
      buildManualBillingReviewResolution(
        review({ review_kind: 'accepted_handoff', can_reject: false }),
        { ...common, action: 'confirmed_accepted' }
      ).provider_status,
      'confirmed_accepted'
    )
    assert.equal(
      buildManualBillingReviewResolution(
        review({ review_kind: 'terminal_overage' }),
        { ...common, action: 'confirmed_accepted' }
      ).provider_status,
      'terminal_usage_verified'
    )
    assert.equal(
      buildManualBillingReviewResolution(
        review({
          review_kind: 'terminal_usage',
          upstream_task_id: 'frozen-upstream-task',
          can_reject: false,
        }),
        {
          ...common,
          upstream_task_id: 'stale-form-value',
          action: 'confirmed_accepted',
        }
      ).provider_status,
      'terminal_usage_verified'
    )
    assert.equal(
      buildManualBillingReviewResolution(
        review({ review_kind: 'terminal_overage' }),
        { ...common, action: 'confirmed_rejected' }
      ).provider_status,
      'terminal_usage_verified'
    )
  })

  test('uses every financial consequence value directly from the server DTO', () => {
    const rows = getManualBillingReviewConsequenceRows(review())
    assert.deepEqual(
      rows.map((row) => [row.key, row.value]),
      [
        ['current_charge', 100],
        ['accept_additional_charge', 20],
        ['accept_final_charge', 120],
        ['reject_refund', 100],
        ['reject_final_charge', 0],
        ['reject_write_off', 7],
      ]
    )
  })

  test('labels negative accepted handoff deltas as charge adjustments', () => {
    const rows = getManualBillingReviewConsequenceRows(
      review({
        review_kind: 'accepted_handoff',
        can_reject: false,
        financial_consequences: {
          current_charge: 100,
          accept_additional_charge: -25,
          accept_final_charge: 75,
          reject_refund: 0,
          reject_final_charge: 100,
          reject_write_off: 0,
        },
      })
    )

    assert.deepEqual(
      rows.find((row) => row.key === 'accept_additional_charge'),
      {
        key: 'accept_additional_charge',
        labelKey: 'Charge adjustment if accepted',
        value: -25,
        outcome: 'accept',
      }
    )
  })

  test('keeps accepted handoff confirmation adjustments explicit for every sign', () => {
    for (const adjustment of [-25, 0, 25]) {
      const item = review({
        review_kind: 'accepted_handoff',
        can_reject: false,
        financial_consequences: {
          current_charge: 100,
          accept_additional_charge: adjustment,
          accept_final_charge: 100 + adjustment,
          reject_refund: 0,
          reject_final_charge: 100,
          reject_write_off: 0,
        },
      })

      assert.deepEqual(
        getManualBillingReviewConfirmationImpact(item, 'confirmed_accepted'),
        {
          kind: 'accepted_handoff',
          adjustment,
          final: 100 + adjustment,
        }
      )
    }
  })

  test('enforces UTF-8 byte limits, forbidden controls and future evidence time', () => {
    const schema = createManualBillingReviewSchema(
      review(),
      messages,
      () => 1_700_000_000_000
    )
    const base = {
      action: 'confirmed_accepted' as const,
      rejection_provider_status: 'confirmed_rejected' as const,
      upstream_task_id: 'upstream-1',
      provider_checked_at: '2023-11-14T22:13:20.000Z',
      evidence_reference: 'provider/case-1',
      reason: 'confirmed',
    }

    assert.equal(schema.safeParse(base).success, true)
    assert.equal(
      schema.safeParse({ ...base, upstream_task_id: '界'.repeat(64) }).success,
      false
    )
    assert.equal(
      schema.safeParse({ ...base, evidence_reference: 'case\n2' }).success,
      false
    )
    assert.equal(
      schema.safeParse({ ...base, reason: `approved\0hidden` }).success,
      false
    )
    assert.equal(
      schema.safeParse({
        ...base,
        provider_checked_at: '2023-11-14T22:19:00.000Z',
      }).success,
      false
    )
  })

  test('requires provider evidence after review and send authorization began', () => {
    const reviewStarted = 1_700_000_000_000
    const authorizedAt = reviewStarted + 5_000
    const schema = createManualBillingReviewSchema(
      review({
        manual_review_since_ms: reviewStarted,
        attempts: [
          {
            attempt_index: 1,
            state: 'authorized',
            channel_id: 8,
            credential_id: 9,
            channel_version: 'generation-1',
            authorized_ms: authorizedAt,
            send_deadline_ms: authorizedAt + 30_000,
          },
        ],
      }),
      messages,
      () => authorizedAt + 60_000
    )
    const input = {
      action: 'confirmed_accepted' as const,
      rejection_provider_status: 'confirmed_rejected' as const,
      upstream_task_id: 'upstream-1',
      provider_checked_at: new Date(authorizedAt - 1_000).toISOString(),
      evidence_reference: 'provider/case-1',
      reason: 'confirmed',
    }

    const tooEarly = schema.safeParse(input)
    assert.equal(tooEarly.success, false)
    if (!tooEarly.success) {
      assert.equal(
        tooEarly.error.issues.some(
          (issue) => issue.message === messages.checkedTimeTooEarly
        ),
        true
      )
    }
    assert.equal(
      schema.safeParse({
        ...input,
        provider_checked_at: new Date(authorizedAt).toISOString(),
      }).success,
      true
    )
  })

  test('requires a frozen upstream task id for terminal usage verification', () => {
    const sendSchema = createManualBillingReviewSchema(review(), messages)
    const terminalSchema = createManualBillingReviewSchema(
      review({
        review_kind: 'terminal_usage',
        upstream_task_id: 'frozen-upstream-task',
        can_reject: false,
      }),
      messages
    )
    const base = {
      rejection_provider_status: 'confirmed_rejected' as const,
      upstream_task_id: '',
      provider_checked_at: '2023-11-14T22:13:20.000Z',
      evidence_reference: 'provider/case-1',
      reason: 'confirmed',
    }

    assert.equal(
      sendSchema.safeParse({ ...base, action: 'confirmed_accepted' }).success,
      false
    )
    assert.equal(
      sendSchema.safeParse({ ...base, action: 'confirmed_rejected' }).success,
      true
    )
    assert.equal(
      terminalSchema.safeParse({
        ...base,
        action: 'confirmed_accepted',
        upstream_task_id: 'frozen-upstream-task',
      }).success,
      true
    )
    assert.equal(
      terminalSchema.safeParse({
        ...base,
        action: 'confirmed_rejected',
        upstream_task_id: 'frozen-upstream-task',
      }).success,
      false
    )
    assert.equal(
      createManualBillingReviewSchema(
        review({
          review_kind: 'terminal_usage',
          upstream_task_id: '',
          can_reject: false,
        }),
        messages
      ).safeParse({ ...base, action: 'confirmed_accepted' }).success,
      false
    )
  })

  test('keeps terminal usage consequences limited to the retained charge', () => {
    const item = review({
      review_kind: 'terminal_usage',
      upstream_task_id: 'frozen-upstream-task',
      can_reject: false,
      financial_consequences: {
        current_charge: 100,
        accept_additional_charge: 0,
        accept_final_charge: 100,
        reject_refund: 0,
        reject_final_charge: 100,
        reject_write_off: 0,
      },
    })

    assert.deepEqual(getManualBillingReviewConsequenceRows(item), [
      {
        key: 'current_charge',
        labelKey: 'Current charge',
        value: 100,
        outcome: 'current',
      },
      {
        key: 'accept_final_charge',
        labelKey: 'Charge retained after verification',
        value: 100,
        outcome: 'accept',
      },
    ])
    assert.deepEqual(
      getManualBillingReviewConfirmationImpact(item, 'confirmed_accepted'),
      { kind: 'terminal_usage', final: 100 }
    )
    assert.equal(
      buildManualBillingReviewResolution(item, {
        action: 'confirmed_accepted',
        rejection_provider_status: 'confirmed_rejected',
        upstream_task_id: 'tampered-form-value',
        provider_checked_at: '2023-11-14T22:13:20.000Z',
        evidence_reference: 'provider/case-1',
        reason: 'verified terminal usage',
      }).upstream_task_id,
      'frozen-upstream-task'
    )
    assert.throws(() =>
      buildManualBillingReviewResolution(item, {
        action: 'confirmed_rejected',
        rejection_provider_status: 'confirmed_rejected',
        upstream_task_id: 'frozen-upstream-task',
        provider_checked_at: '2023-11-14T22:13:20.000Z',
        evidence_reference: 'provider/case-1',
        reason: 'invalid rejection',
      })
    )
  })

  test('rejects an accepted handoff decision even if a stale capability says it is allowed', () => {
    const schema = createManualBillingReviewSchema(
      review({ review_kind: 'accepted_handoff', can_reject: true }),
      messages
    )
    const base = {
      rejection_provider_status: 'confirmed_rejected' as const,
      upstream_task_id: 'upstream-1',
      provider_checked_at: '2023-11-14T22:13:20.000Z',
      evidence_reference: 'provider/case-1',
      reason: 'confirmed',
    }

    assert.equal(
      schema.safeParse({ ...base, action: 'confirmed_rejected' }).success,
      false
    )
    assert.equal(
      schema.safeParse({ ...base, action: 'confirmed_accepted' }).success,
      true
    )
  })

  test('keeps accept available while an active send lease blocks only rejection', () => {
    const schema = createManualBillingReviewSchema(
      review({
        can_accept: true,
        can_reject: false,
        blockers: ['submission_send_lease_active'],
      }),
      messages
    )
    const base = {
      rejection_provider_status: 'confirmed_rejected' as const,
      upstream_task_id: 'upstream-1',
      provider_checked_at: '2023-11-14T22:13:20.000Z',
      evidence_reference: 'provider/case-1',
      reason: 'confirmed',
    }

    assert.equal(
      schema.safeParse({ ...base, action: 'confirmed_accepted' }).success,
      true
    )
    assert.equal(
      schema.safeParse({ ...base, action: 'confirmed_rejected' }).success,
      false
    )
  })

  test('blocks accepted handoff submission when the server capability rejects corrupt context', () => {
    const schema = createManualBillingReviewSchema(
      review({
        review_kind: 'accepted_handoff',
        can_accept: false,
        can_reject: false,
        blockers: ['accepted_handoff_context_missing_or_invalid'],
      }),
      messages
    )
    const result = schema.safeParse({
      action: 'confirmed_accepted',
      rejection_provider_status: 'confirmed_rejected',
      upstream_task_id: 'upstream-1',
      provider_checked_at: '2023-11-14T22:13:20.000Z',
      evidence_reference: 'provider/case-1',
      reason: 'confirmed',
    })

    assert.equal(result.success, false)
  })

  test('fails closed for a review kind introduced by a newer server', () => {
    const futureReview = review({
      review_kind: 'provider_usage_reconciliation_v2',
      can_accept: true,
      can_reject: true,
      blockers: [],
    })
    const schema = createManualBillingReviewSchema(futureReview, messages)
    const values = {
      action: 'confirmed_accepted' as const,
      rejection_provider_status: 'confirmed_rejected' as const,
      upstream_task_id: 'upstream-1',
      provider_checked_at: '2023-11-14T22:13:20.000Z',
      evidence_reference: 'provider/case-1',
      reason: 'confirmed',
    }

    assert.equal(
      manualBillingReviewKindIsSupported(futureReview.review_kind),
      false
    )
    assert.equal(schema.safeParse(values).success, false)
    assert.throws(() =>
      buildManualBillingReviewResolution(futureReview, values)
    )
    assert.equal(
      getManualBillingReviewKindDisplay(futureReview.review_kind, (key) => key),
      'Unknown review type: provider_usage_reconciliation_v2'
    )
  })

  test('fails closed for an unknown blocker even when stale action flags allow resolution', () => {
    const futureBlockerReview = review({
      can_accept: true,
      can_reject: true,
      blockers: ['provider_receipt_pending_v2'],
    })
    const schema = createManualBillingReviewSchema(
      futureBlockerReview,
      messages
    )

    assert.equal(
      manualBillingReviewHasUnknownBlocker(futureBlockerReview),
      true
    )
    assert.equal(
      schema.safeParse({
        action: 'confirmed_accepted',
        rejection_provider_status: 'confirmed_rejected',
        upstream_task_id: 'upstream-1',
        provider_checked_at: '2023-11-14T22:13:20.000Z',
        evidence_reference: 'provider/case-1',
        reason: 'confirmed',
      }).success,
      false
    )
  })

  test('maps every review kind and stable blocker to user-facing keys', () => {
    assert.deepEqual(
      [
        'send_outcome',
        'acceptance_overage',
        'accepted_handoff',
        'terminal_overage',
        'terminal_usage',
      ].map(getManualBillingReviewKindLabelKey),
      [
        'Send outcome',
        'Acceptance overage',
        'Accepted handoff',
        'Terminal overage',
        'Terminal usage verification',
      ]
    )
    assert.deepEqual(
      [
        'submission_send_lease_active',
        'acceptance_overage_context_missing_or_invalid',
        'accepted_handoff_context_missing_or_invalid',
      ].map(getManualBillingReviewBlockerLabelKey),
      [
        'The submission send lease is still active',
        'The acceptance overage context is missing or invalid',
        'The accepted handoff context is missing or invalid',
      ]
    )
    assert.equal(getManualBillingReviewKindLabelKey('future_kind'), null)
    assert.equal(getManualBillingReviewBlockerLabelKey('future_blocker'), null)
    assert.equal(
      getManualBillingReviewBlockerDisplay('future_blocker', (key) => key),
      'Unknown: future_blocker'
    )
  })

  test('requires a valid advancing cursor when the server reports more rows', () => {
    assert.equal(
      getManualBillingReviewNextCursor({ has_more: true, next_cursor: 84 }, 42),
      84
    )
    assert.equal(getManualBillingReviewNextCursor({ has_more: true }, 42), 0)
    assert.equal(
      getManualBillingReviewNextCursor({ has_more: true, next_cursor: 42 }, 42),
      0
    )
    assert.equal(
      getManualBillingReviewNextCursor(
        { has_more: false, next_cursor: 84 },
        42
      ),
      0
    )
  })

  test('refreshes capabilities after resolve permission is revoked', () => {
    assert.equal(manualBillingReviewNeedsRefresh({ status: 403 }), true)
  })
})

describe('manual billing review request sessions', () => {
  test('blocks duplicate submits and reuses the same key for an unchanged retry', () => {
    const session = new ManualBillingReviewSession()
    const generation = session.open()
    const first = session.claimSubmission('payload-a', () => 'stable-key')
    const duplicate = session.claimSubmission('payload-a', () => 'other-key')

    assert.equal(first?.key, 'stable-key')
    assert.equal(duplicate, null)

    session.releaseSubmission(generation, 'payload-a')
    const retry = session.claimSubmission('payload-a', () => 'other-key')
    assert.equal(retry?.key, 'stable-key')
  })

  test('aborts the old request and invalidates stale callbacks on review switch', () => {
    const session = new ManualBillingReviewSession()
    const firstGeneration = session.open()
    const first = session.claimSubmission('payload-a', () => 'first-key')

    const secondGeneration = session.open()

    assert.equal(first?.signal.aborted, true)
    assert.equal(session.isCurrent(firstGeneration), false)
    assert.equal(session.isCurrent(secondGeneration), true)
  })
})
