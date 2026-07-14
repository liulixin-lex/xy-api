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

import type {
  BillingLogSinkConflict,
  FailedBillingProjection,
} from '../projection-types'
import {
  BillingConflictTable,
  FailedProjectionTable,
} from './projection-failures-table'

const projection: FailedBillingProjection = {
  id: 12,
  kind: 'future_projection_kind',
  reference_id: 88,
  operation_key_hash: 'a'.repeat(64),
  state: 'future_projection_state',
  disposition: 'future_disposition',
  failure_code: 'future_failure_code',
  error: 'sanitized '.repeat(80),
  attempts: 4,
  created_time_ms: 1_700_000_000_000,
  updated_time_ms: 1_700_000_100_000,
  completed_time_ms: 0,
  outcome: { user: 'future_outcome' },
  requeueable: true,
  etag: '"billing-stats-projection.12.revision.hash"',
}

const conflict: BillingLogSinkConflict = {
  id: 7,
  projection_id: 19,
  operation_key_hash: 'b'.repeat(64),
  state: 'future_conflict_state',
  version: 2,
  distinct_receipts: 2,
  physical_rows: 3,
  first_detected_ms: 1_700_000_000_000,
  last_detected_ms: 1_700_000_100_000,
  etag: '"billing-log-sink-conflict.7.v2"',
}

describe('billing projection operation tables', () => {
  test('keeps unknown codes visible and provides desktop and mobile layouts', () => {
    const html = renderToStaticMarkup(
      createElement(FailedProjectionTable, {
        items: [projection],
        canRequeue: false,
        onRequeue: () => undefined,
      })
    )

    assert.match(html, /Unknown: future_projection_kind/)
    assert.match(html, /Unknown: future_failure_code/)
    assert.match(html, /Unknown: future_projection_state/)
    assert.match(html, /Unknown: future_disposition/)
    assert.match(html, /Unknown: future_outcome/)
    assert.match(html, /md:block/)
    assert.match(html, /md:hidden/)
    assert.match(html, /Read only/)
    assert.doesNotMatch(html, /payload|credential/i)
  })

  test('shows conflict counts while withholding resolve controls in read-only mode', () => {
    const html = renderToStaticMarkup(
      createElement(BillingConflictTable, {
        items: [conflict],
        canResolve: false,
        onResolve: () => undefined,
      })
    )

    assert.match(html, /2 distinct receipts/)
    assert.match(html, /3 physical rows/)
    assert.match(html, /Unknown: future_conflict_state/)
    assert.match(html, /Read only/)
    assert.doesNotMatch(html, />Resolve and requeue</)
  })
})
