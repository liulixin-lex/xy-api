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
  createPaymentAdminError,
  getPaymentAdminErrorCode,
  getPaymentAdminErrorMessage,
  getRetainedCredentialDisableErrorMessage,
  getSubscriptionPlanAdminErrorMessage,
} from './payment-admin-errors.js';

const t = (key) => `translated:${key}`;

describe('classic payment administrator errors', () => {
  test('translates stable codes and exposes them only as administrator diagnostics', () => {
    const error = createPaymentAdminError(
      { code: 'payment_limit_timezone_locked' },
      'fallback',
    );
    assert.equal(
      getPaymentAdminErrorCode(error),
      'payment_limit_timezone_locked',
    );
    const message = getPaymentAdminErrorMessage(error, t, 'fallback');
    assert.match(message, /^translated:/);
    assert.match(message, /payment_limit_timezone_locked/);
    assert.doesNotMatch(message, /fallback/);
  });

  test('does not expose a raw backend message when a stable code exists', () => {
    const error = {
      response: {
        data: {
          code: 'payment_settings_stripe_verification_failed',
          message: 'sk_live account mismatch and raw Stripe response',
        },
      },
    };
    const message = getPaymentAdminErrorMessage(error, t, 'fallback');
    assert.match(message, /payment_settings_stripe_verification_failed/);
    assert.doesNotMatch(message, /sk_live|raw Stripe response/);
  });

  test('translates invalid Stripe custom Checkout host policy safely', () => {
    const error = {
      response: {
        data: {
          code: 'payment_settings_stripe_checkout_hosts_invalid',
          message: 'rejected raw host and internal parser detail',
        },
      },
    };
    const message = getPaymentAdminErrorMessage(error, t, 'fallback');
    assert.match(message, /payment_settings_stripe_checkout_hosts_invalid/);
    assert.match(message, /exact custom Stripe Checkout hostnames/);
    assert.doesNotMatch(message, /raw host|parser detail/);
  });

  test('does not describe retained emergency disable as credential rotation', () => {
    const message = getRetainedCredentialDisableErrorMessage(
      { code: 'payment_settings_rotation_blocked' },
      t,
      'fallback',
    );
    assert.match(message, /active current credential/);
    assert.doesNotMatch(message.split(' (')[0], /rotation|previous credential/);
  });

  test('uses safe subscription-plan copy for stable and unknown errors', () => {
    const stableError = {
      response: {
        data: {
          code: 'subscription_plan_save_failed',
          message: 'raw database constraint and merchant details',
        },
      },
    };
    const stableMessage = getSubscriptionPlanAdminErrorMessage(stableError, t);
    assert.equal(
      stableMessage,
      'translated:The subscription plan could not be saved. No changes were applied.',
    );
    assert.doesNotMatch(stableMessage, /database constraint|merchant details/);

    const unknownMessage = getSubscriptionPlanAdminErrorMessage(
      new Error('upstream secret and SQL detail'),
      t,
    );
    assert.equal(
      unknownMessage,
      'translated:The subscription plan could not be saved. Try again.',
    );
    assert.doesNotMatch(unknownMessage, /upstream secret|SQL detail/);
  });
});
