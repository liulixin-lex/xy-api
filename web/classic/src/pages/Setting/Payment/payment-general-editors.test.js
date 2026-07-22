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
  getPaymentMethodIdentity,
  parseAmountDiscounts,
  parseAmountOptions,
  parsePaymentMethods,
  parseTopupGroupRatios,
  upsertPaymentMethod,
  validatePaymentMethodDraft,
} from './payment-general-editors.js';

describe('classic payment general visual editor data', () => {
  test('keeps Epay, XORPay, and Stripe methods independent', () => {
    const methods = [
      { name: 'Alipay', type: 'alipay', provider: 'epay' },
      { name: 'Alipay', type: 'xorpay_alipay', provider: 'xorpay' },
      { name: 'Stripe', type: 'stripe', provider: 'stripe' },
    ];
    const parsed = parsePaymentMethods(JSON.stringify(methods));
    assert.equal(parsed.error, null);
    assert.equal(new Set(parsed.items.map(getPaymentMethodIdentity)).size, 3);
    assert.equal(
      validatePaymentMethodDraft(
        { name: 'WeChat Pay', type: 'wxpay', provider: 'epay' },
        parsed.items,
      ),
      null,
    );
  });

  test('uses provider and method type together for duplicate detection', () => {
    const methods = [
      { name: 'Custom', type: 'custom1', provider: 'epay' },
      { name: 'Alipay', type: 'xorpay_alipay', provider: 'xorpay' },
    ];
    assert.equal(
      validatePaymentMethodDraft(
        { name: 'Duplicate', type: 'custom1', provider: 'epay' },
        methods,
      ),
      'duplicate_payment_method',
    );
    assert.equal(
      validatePaymentMethodDraft(
        { name: 'WeChat Pay', type: 'xorpay_native', provider: 'xorpay' },
        methods,
      ),
      null,
    );
  });

  test('preserves historical Epay types and the 27-entry compatibility limit', () => {
    const methods = [
      { name: 'Legacy product checkout', type: 'creem', provider: 'epay' },
      { name: 'Online payment', type: 'creem', provider: 'creem' },
      { name: 'Legacy payment options', type: 'waffo', provider: 'epay' },
      { name: 'Payment options', type: 'waffo', provider: 'waffo' },
    ];
    methods.forEach((method, index) => {
      assert.equal(validatePaymentMethodDraft(method, methods, index), null);
    });

    const maximum = Array.from({ length: 27 }, (_, index) => ({
      name: `Method ${index}`,
      type: `custom_${index}`,
      provider: 'epay',
    }));
    assert.equal(
      validatePaymentMethodDraft(
        { name: 'Method 28', type: 'custom_28', provider: 'epay' },
        maximum.slice(0, 26),
      ),
      null,
    );
    assert.equal(
      validatePaymentMethodDraft(
        { name: 'Method 28', type: 'custom_28', provider: 'epay' },
        maximum,
      ),
      'too_many_payment_methods',
    );
  });

  test('preserves opaque public aliases when editing presentation fields', () => {
    const methods = [
      {
        name: 'Alipay',
        type: 'xorpay_alipay',
        provider: 'xorpay',
        route_id: 'pay_opaque',
        public_method: 'alipay',
        channel_alias: 'qr',
        flow: 'qr',
      },
    ];
    const updated = upsertPaymentMethod(methods, 0, {
      ...methods[0],
      name: 'Alipay primary',
      min_topup: '10',
    });
    assert.equal(updated[0].route_id, 'pay_opaque');
    assert.equal(updated[0].public_method, 'alipay');
    assert.equal(updated[0].min_topup, '10');

    const changedProduct = upsertPaymentMethod(methods, 0, {
      ...methods[0],
      type: 'xorpay_native',
      name: 'WeChat Pay',
      icon: 'SiWechat',
    });
    assert.equal(changedProduct[0].route_id, undefined);
    assert.equal(changedProduct[0].public_method, undefined);
    assert.equal(changedProduct[0].flow, undefined);
  });

  test('accepts only integer amount options and bounded discount rates', () => {
    assert.deepEqual(parseAmountOptions('[100, 20, 100]').amounts, [20, 100]);
    assert.equal(parseAmountOptions('[10.5]').error, 'invalid_amount_option');
    assert.equal(parseAmountDiscounts('{"100":0.95,"200":0.9}').error, null);
    assert.equal(
      parseAmountDiscounts('{"100":1.1}').error,
      'invalid_amount_discount',
    );
  });

  test('validates top-up group ratios without floating-point coercion', () => {
    assert.deepEqual(parseTopupGroupRatios('{"default":1}').ratios, {
      default: 1,
    });
    assert.equal(
      parseTopupGroupRatios('{"default":"1"}').error,
      'invalid_group_ratio',
    );
  });
});
