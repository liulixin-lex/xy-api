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

import {
  BillingProjectionOperationSession,
  billingProjectionOperationNeedsRefresh,
  canMutateBillingProjectionPage,
  createBillingProjectionReasonSchema,
  getBillingProjectionCodeDisplay,
  getBillingProjectionNextCursor,
} from './projection-operations'

describe('billing projection operation rules', () => {
  test('accepts only a safe advancing server cursor', () => {
    assert.equal(
      getBillingProjectionNextCursor({ has_more: true, next_cursor: 20 }, 10),
      20
    )
    assert.equal(
      getBillingProjectionNextCursor({ has_more: true, next_cursor: 10 }, 10),
      0
    )
    assert.equal(
      getBillingProjectionNextCursor(
        { has_more: true, next_cursor: Number.MAX_SAFE_INTEGER + 1 },
        10
      ),
      0
    )
    assert.equal(
      getBillingProjectionNextCursor({ has_more: false, next_cursor: 20 }, 10),
      0
    )
  })

  test('enforces the backend UTF-8 byte and single-line reason contract', () => {
    const schema = createBillingProjectionReasonSchema({
      required: 'required',
      tooLong: 'too long',
      singleLine: 'single line',
    })

    assert.deepEqual(schema.parse({ reason: ' verified ' }), {
      reason: 'verified',
    })
    assert.equal(schema.safeParse({ reason: '' }).success, false)
    assert.equal(schema.safeParse({ reason: '界'.repeat(342) }).success, false)
    assert.equal(schema.safeParse({ reason: 'line\ntwo' }).success, false)
  })

  test('fails visibly for unknown server codes', () => {
    assert.equal(
      getBillingProjectionCodeDisplay(
        'completed',
        { completed: 'Completed' },
        (key) => key
      ),
      'Completed'
    )
    assert.equal(
      getBillingProjectionCodeDisplay('future_state', {}, (key) => key),
      'Unknown: future_state'
    )
  })

  test('refreshes authoritative data after terminal mutation errors', () => {
    for (const status of [403, 404, 409, 412, 422]) {
      assert.equal(billingProjectionOperationNeedsRefresh({ status }), true)
    }
    assert.equal(billingProjectionOperationNeedsRefresh({ status: 500 }), false)
  })

  test('allows mutations only on a confirmed authoritative page', () => {
    assert.equal(
      canMutateBillingProjectionPage({
        hasPermission: true,
        isError: false,
        isRefetchError: false,
        isPlaceholderData: false,
      }),
      true
    )
    for (const state of [
      { hasPermission: false },
      { isError: true },
      { isRefetchError: true },
      { isPlaceholderData: true },
    ]) {
      assert.equal(
        canMutateBillingProjectionPage({
          hasPermission: true,
          isError: false,
          isRefetchError: false,
          isPlaceholderData: false,
          ...state,
        }),
        false
      )
    }
  })
})

describe('billing projection operation sessions', () => {
  test('blocks duplicate submits and reuses a key for the same retry', () => {
    const session = new BillingProjectionOperationSession()
    const generation = session.open()
    const first = session.claim('projection:12:etag', () => 'stable-key')
    assert.equal(session.claim('projection:12:etag'), null)

    session.release(generation, 'projection:12:etag')
    const retry = session.claim('projection:12:etag', () => 'different-key')

    assert.equal(first?.key, 'stable-key')
    assert.equal(retry?.key, 'stable-key')
  })

  test('aborts stale requests when a different target opens', () => {
    const session = new BillingProjectionOperationSession()
    const firstGeneration = session.open()
    const first = session.claim('projection:12:etag')
    const secondGeneration = session.open()

    assert.equal(first?.signal.aborted, true)
    assert.equal(session.isCurrent(firstGeneration), false)
    assert.equal(session.isCurrent(secondGeneration), true)
  })
})
