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

import { buildLegacySubscriptionResolutionRequest } from './legacy-subscription-resolution'
import type { PaymentEvent } from './types'

const event = {
  id: 23,
  attempts: 4,
  available_actions: ['resolve_legacy_subscription'],
} as PaymentEvent

describe('legacy subscription terminal resolution form', () => {
  test('builds only the external refund contract', () => {
    assert.deepEqual(
      buildLegacySubscriptionResolutionRequest(
        event,
        ' refund-subscription-23 ',
        ' Provider confirms that the full payment was refunded. '
      ),
      {
        event_id: 23,
        expected_event_attempts: 4,
        resolution: 'external_refund',
        provider_refund_reference: 'refund-subscription-23',
        reason: 'Provider confirms that the full payment was refunded.',
      }
    )
  })

  test('requires server authorization, provider reference, and evidence', () => {
    assert.equal(
      buildLegacySubscriptionResolutionRequest(
        { ...event, available_actions: [] },
        'refund-subscription-23',
        'Provider confirms that the full payment was refunded.'
      ),
      null
    )
    assert.equal(
      buildLegacySubscriptionResolutionRequest(
        event,
        ' ',
        'Provider confirms that the full payment was refunded.'
      ),
      null
    )
    assert.equal(
      buildLegacySubscriptionResolutionRequest(
        event,
        'refund-subscription-23',
        'short'
      ),
      null
    )
  })
})
