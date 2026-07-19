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

import { resolveStripeTestModeNotice } from './stripe-test-mode-readiness.js';

describe('classic Stripe test-mode readiness notice', () => {
  test('shows a blocked warning only for a verified test credential', () => {
    const input = {
      credentialLivemode: 'test',
      enabled: false,
      blocked: true,
      isolationRequired: true,
    };
    assert.deepEqual(resolveStripeTestModeNotice(input), {
      state: 'blocked',
      isolationRequired: true,
    });
    assert.equal(
      resolveStripeTestModeNotice({ ...input, credentialLivemode: 'live' }),
      null,
    );
  });

  test('warns that enabled test credentials can credit isolated accounts', () => {
    assert.deepEqual(
      resolveStripeTestModeNotice({
        credentialLivemode: ' test ',
        enabled: true,
        blocked: false,
        isolationRequired: true,
      }),
      { state: 'enabled', isolationRequired: true },
    );
  });

  test('does not infer enablement from credential mode alone', () => {
    assert.equal(
      resolveStripeTestModeNotice({
        credentialLivemode: 'test',
        enabled: false,
        blocked: false,
        isolationRequired: true,
      }),
      null,
    );
  });
});
