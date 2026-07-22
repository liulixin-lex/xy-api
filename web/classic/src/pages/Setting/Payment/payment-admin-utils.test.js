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
  buildStripeCheckoutAllowedHostsUpdate,
  formatPaymentAge,
  getPaymentCurrencyExponent,
  getPaymentLimitUsagePercent,
  paymentMajorToMinor,
  paymentMinorToMajor,
} from './payment-admin-utils';

describe('classic payment administrator amount helpers', () => {
  test('keeps the Stripe custom Checkout allowlist in the Stripe save scope', () => {
    assert.deepEqual(
      buildStripeCheckoutAllowedHostsUpdate(
        ' pay.example.com\ncheckout.example.net ',
      ),
      {
        key: 'StripeCheckoutAllowedHosts',
        value: 'pay.example.com\ncheckout.example.net',
      },
    );
    assert.deepEqual(buildStripeCheckoutAllowedHostsUpdate(''), {
      key: 'StripeCheckoutAllowedHosts',
      value: '',
    });
  });

  test('converts merchant limits without floating-point arithmetic', () => {
    assert.equal(paymentMajorToMinor('1999.99', 2), '199999');
    assert.equal(paymentMajorToMinor('1.234', 2), null);
    assert.equal(paymentMajorToMinor('92233720368547758.08', 2), null);
    assert.equal(paymentMinorToMajor('199999', 2), '1999.99');
    assert.equal(paymentMinorToMajor('1200', 0), '1200');
  });

  test('honors provider currency exponent compatibility', () => {
    assert.equal(getPaymentCurrencyExponent('xorpay', 'CNY'), 2);
    assert.equal(getPaymentCurrencyExponent('stripe', 'ISK'), 2);
    assert.equal(getPaymentCurrencyExponent('epay', 'JPY'), 0);
  });

  test('includes active reservations in daily usage', () => {
    assert.equal(
      getPaymentLimitUsagePercent({
        daily_limit_minor: '10000',
        paid_minor: '2500',
        reserved_minor: '1250',
      }),
      37.5,
    );
    assert.equal(
      getPaymentLimitUsagePercent({
        daily_limit_minor: '0',
        paid_minor: '2500',
        reserved_minor: '1250',
      }),
      0,
    );
  });

  test('formats worker age with locale-aware units', () => {
    assert.match(formatPaymentAge(90, 'en'), /2\s*min/);
    assert.match(formatPaymentAge(7200, 'zh-CN'), /2.*小时/);
    assert.equal(formatPaymentAge(0, 'en'), '—');
  });
});
