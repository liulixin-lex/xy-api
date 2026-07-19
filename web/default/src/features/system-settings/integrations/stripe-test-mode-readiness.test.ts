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

import { resolveStripeTestModeNotice } from './stripe-test-mode-readiness'

describe('Stripe test mode readiness notice', () => {
  test('shows the initial blocked state only for test credentials', () => {
    const initial = {
      credentialLivemode: 'test',
      initialEnabled: false,
      initialBlocked: true,
      initialIsolationRequired: true,
    }
    assert.deepEqual(resolveStripeTestModeNotice(initial), {
      state: 'blocked',
      isolationRequired: true,
    })
    assert.equal(
      resolveStripeTestModeNotice({
        ...initial,
        credentialLivemode: 'live',
      }),
      null
    )
  })

  test('uses fresh gateway readiness after a settings update', () => {
    assert.deepEqual(
      resolveStripeTestModeNotice({
        credentialLivemode: 'test',
        initialEnabled: false,
        initialBlocked: true,
        initialIsolationRequired: false,
        readiness: {
          test_mode_enabled: true,
          test_mode_blocked: false,
          test_mode_isolation_required: true,
        },
      }),
      { state: 'enabled', isolationRequired: true }
    )
  })

  test('does not infer an enabled environment from credential mode alone', () => {
    assert.equal(
      resolveStripeTestModeNotice({
        credentialLivemode: 'test',
        initialEnabled: false,
        initialBlocked: false,
        initialIsolationRequired: true,
      }),
      null
    )
  })
})
