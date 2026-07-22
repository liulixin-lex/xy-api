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
  buildRetainedCredentialDisablePayload,
  buildRetainedCredentialDisablePreviewParams,
  RETAINED_PAYMENT_PROVIDERS,
} from './retained-payment-credential-disable.js';

describe('classic retained payment credential emergency disable contract', () => {
  test('uses the all-active preview for every retained gateway', () => {
    for (const provider of RETAINED_PAYMENT_PROVIDERS) {
      assert.deepEqual(buildRetainedCredentialDisablePreviewParams(provider), {
        provider,
        mode: 'all_active',
      });
    }
  });

  test('disables current credentials without rotation or replacement fields', () => {
    assert.deepEqual(
      buildRetainedCredentialDisablePayload(
        'waffo_pancake',
        '  private key exposure review  ',
        17,
      ),
      {
        options: {},
        disable_current_credentials: ['waffo_pancake'],
        reason: 'private key exposure review',
        expected_version: 17,
      },
    );
  });

  test('rejects unsupported providers, invalid reasons, and stale versions', () => {
    assert.throws(
      () =>
        buildRetainedCredentialDisablePayload(
          'stripe',
          'credential incident under review',
          1,
        ),
      RangeError,
    );
    assert.throws(
      () => buildRetainedCredentialDisablePayload('creem', 'short', 1),
      RangeError,
    );
    assert.throws(
      () =>
        buildRetainedCredentialDisablePayload(
          'waffo',
          'credential incident under review',
          0,
        ),
      RangeError,
    );
  });
});
