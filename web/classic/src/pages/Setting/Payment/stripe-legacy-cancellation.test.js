/*
Copyright (C) 2025 QuantumNous

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

import assert from 'node:assert/strict';
import { describe, test } from 'node:test';

import {
  canScheduleStripeSubscriptionCancellation,
  isStripeCancellationReasonValid,
} from './stripe-legacy-cancellation.js';

describe('classic Stripe legacy subscription cancellation controls', () => {
  test('allows only a current non-terminal snapshot without a pending cancellation', () => {
    const current = {
      id: 42,
      cancel_at_period_end: false,
      ended_at: 0,
      expected_updated_at: 1721200000,
      status: 'active',
    };
    assert.equal(canScheduleStripeSubscriptionCancellation(current), true);
    assert.equal(
      canScheduleStripeSubscriptionCancellation({
        ...current,
        cancel_at_period_end: true,
      }),
      false,
    );
    assert.equal(
      canScheduleStripeSubscriptionCancellation({
        ...current,
        status: 'incomplete_expired',
      }),
      false,
    );
    assert.equal(
      canScheduleStripeSubscriptionCancellation({ ...current, ended_at: 1 }),
      false,
    );
    assert.equal(
      canScheduleStripeSubscriptionCancellation({
        ...current,
        expected_updated_at: 0,
      }),
      false,
    );
    assert.equal(
      canScheduleStripeSubscriptionCancellation({ ...current, id: 0 }),
      false,
    );
    assert.equal(
      canScheduleStripeSubscriptionCancellation({ ...current, status: '' }),
      false,
    );
  });

  test('requires an audited reason between 8 and 512 trimmed characters', () => {
    assert.equal(isStripeCancellationReasonValid('too few'), false);
    assert.equal(isStripeCancellationReasonValid(' stop renewal '), true);
    assert.equal(isStripeCancellationReasonValid('x'.repeat(512)), true);
    assert.equal(isStripeCancellationReasonValid('x'.repeat(513)), false);
    assert.equal(isStripeCancellationReasonValid('停止续费'), true);
    assert.equal(isStripeCancellationReasonValid('界'.repeat(170)), true);
    assert.equal(isStripeCancellationReasonValid('界'.repeat(171)), false);
  });
});
