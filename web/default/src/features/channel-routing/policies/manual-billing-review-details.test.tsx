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

import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import '@/i18n/config'

import type { ManualBillingReviewItem } from '../billing-review-types'
import {
  ManualBillingReviewCaseDetails,
  ManualBillingReviewFinancialOutcomes,
} from './manual-billing-review-details'

const review: ManualBillingReviewItem = {
  reservation_id: 9001,
  kind: 'task',
  review_kind: 'terminal_overage',
  public_task_id: 'public-task-9001',
  upstream_task_id: 'upstream-task-77',
  user_id: 12,
  state: 'manual_review',
  current_quota: 101,
  accepted_quota: 101,
  review_version: 4,
  etag: '"async-billing-review-9001-v4"',
  manual_review_since_ms: 1_700_000_000_000,
  reason: 'Provider terminal usage exceeded the reserved amount.',
  can_accept: true,
  can_reject: true,
  blockers: [
    'terminal_billing_operation_missing_or_invalid',
    'submission_send_lease_active',
  ],
  financial_consequences: {
    current_charge: 101,
    accept_additional_charge: 23,
    accept_final_charge: 124,
    reject_refund: 5,
    reject_final_charge: 99,
    reject_write_off: 25,
  },
  attempts: [
    {
      attempt_index: 1,
      state: 'authorized',
      channel_id: 8,
      credential_id: 15,
      channel_version: 'generation-4',
      authorized_ms: 1_700_000_000_000,
      send_deadline_ms: 1_700_000_300_000,
    },
  ],
}

describe('manual billing review details', () => {
  test('renders all six server-provided financial values without deriving them', () => {
    const html = renderToStaticMarkup(
      createElement(ManualBillingReviewFinancialOutcomes, { review })
    )

    for (const value of ['101', '23', '124', '5', '99', '25']) {
      assert.match(html, new RegExp(`>${value}<`))
    }
    assert.equal((html.match(/data-outcome=/g) ?? []).length, 6)
    assert.match(html, /additional charge/i)
    assert.match(html, /writes off/i)
  })

  test('shows only audit identifiers and stable blocker descriptions', () => {
    const html = renderToStaticMarkup(
      createElement(ManualBillingReviewCaseDetails, { review })
    )

    assert.match(html, /Channel #8/)
    assert.match(html, /Credential #15/)
    assert.match(html, /generation-4/)
    assert.match(html, /Authorized at/)
    assert.match(html, /Send lease deadline/)
    assert.match(html, /submission send lease is still active/i)
    assert.match(html, /terminal billing operation is missing or invalid/i)
    assert.doesNotMatch(html, /api[_ -]?key|access[_ -]?token|password/i)
  })

  test('presents a negative accepted handoff delta as a charge adjustment', () => {
    const html = renderToStaticMarkup(
      createElement(ManualBillingReviewFinancialOutcomes, {
        review: {
          ...review,
          review_kind: 'accepted_handoff',
          can_reject: false,
          financial_consequences: {
            current_charge: 101,
            accept_additional_charge: -21,
            accept_final_charge: 80,
            reject_refund: 0,
            reject_final_charge: 101,
            reject_write_off: 0,
          },
        },
      })
    )

    assert.match(html, /Charge adjustment if accepted/)
    assert.match(html, />-21</)
    assert.match(
      html,
      /negative accepted adjustment reduces the current charge/i
    )
  })
})
