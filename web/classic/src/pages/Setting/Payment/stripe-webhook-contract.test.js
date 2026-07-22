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

import { resolveStripeWebhookContract } from './stripe-webhook-contract.js';

describe('resolveStripeWebhookContract', () => {
  test('uses the Stripe SDK contract reported by the server', () => {
    assert.deepEqual(
      resolveStripeWebhookContract(' 2026-06-24.dahlia ', '24'),
      {
        apiVersion: '2026-06-24.dahlia',
        overlapHours: 24,
      },
    );
  });

  test('keeps the rotation window safe when older servers omit metadata', () => {
    assert.deepEqual(resolveStripeWebhookContract(undefined, 0), {
      apiVersion: '',
      overlapHours: 24,
    });
  });
});
