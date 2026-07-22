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

import {
  getPaymentMethodIdentity,
  MAX_CONFIGURED_PAYMENT_METHODS,
  mergePaymentMethodEdit,
  normalizePaymentMethod,
  removePaymentMethodByIdentity,
  validatePaymentMethodCollection,
} from './payment-methods-visual-editor-utils'

describe('payment methods visual editor', () => {
  test('preserves public route metadata and unknown compatible fields', () => {
    const normalized = normalizePaymentMethod({
      name: 'WeChat Pay',
      type: 'xorpay_native',
      provider: 'xorpay',
      route_id: 'xorpay_wechat_desktop',
      public_method: 'wechat_pay',
      channel_alias: 'native',
      flow: 'qr',
      future_flag: true,
    })

    assert.ok(normalized)
    assert.equal(normalized.route_id, 'xorpay_wechat_desktop')
    assert.equal(normalized.public_method, 'wechat_pay')
    assert.equal(normalized.channel_alias, 'native')
    assert.equal(normalized.flow, 'qr')
    assert.equal(normalized.future_flag, true)
  })

  test('keeps route metadata for cosmetic edits and clears it for identity changes', () => {
    const existing = normalizePaymentMethod({
      name: 'Primary WeChat',
      type: 'xorpay_native',
      provider: 'xorpay',
      route_id: 'xorpay_wechat_desktop',
      public_method: 'wechat_pay',
      channel_alias: 'native',
      flow: 'qr',
    })
    assert.ok(existing)

    const cosmetic = mergePaymentMethodEdit(existing, {
      ...existing,
      name: 'Desktop WeChat',
    })
    assert.equal(cosmetic.route_id, 'xorpay_wechat_desktop')
    assert.equal(cosmetic.public_method, 'wechat_pay')

    const changed = mergePaymentMethodEdit(existing, {
      name: 'Alipay',
      type: 'alipay',
      provider: 'epay',
    })
    assert.equal(changed.route_id, undefined)
    assert.equal(changed.public_method, undefined)
    assert.equal(changed.channel_alias, undefined)
    assert.equal(changed.flow, undefined)
    assert.notEqual(
      getPaymentMethodIdentity(changed),
      getPaymentMethodIdentity(existing)
    )
  })

  test('matches the backend provider identity and 27-entry compatibility limit', () => {
    const historical = normalizePaymentMethod({
      name: 'Legacy product checkout',
      type: 'creem',
      provider: 'epay',
    })
    const canonical = normalizePaymentMethod({
      name: 'Online payment',
      type: 'creem',
      provider: 'creem',
    })
    assert.ok(historical)
    assert.ok(canonical)
    assert.notEqual(
      getPaymentMethodIdentity(historical),
      getPaymentMethodIdentity(canonical)
    )
    assert.equal(validatePaymentMethodCollection([historical, canonical]), null)
    assert.equal(
      validatePaymentMethodCollection([historical, historical]),
      'duplicate_payment_method'
    )

    const maximum = Array.from(
      { length: MAX_CONFIGURED_PAYMENT_METHODS },
      (_, index) => ({
        type: `custom_${index}`,
        provider: 'epay' as const,
      })
    )
    assert.equal(validatePaymentMethodCollection(maximum), null)
    assert.equal(
      validatePaymentMethodCollection([
        ...maximum,
        {
          type: 'custom_28',
          provider: 'epay',
        },
      ]),
      'too_many_payment_methods'
    )
  })

  test('deletes only the selected provider identity when names and types overlap', () => {
    const historical = {
      name: 'Online payment',
      type: 'creem',
      provider: 'epay',
      legacy_flag: true,
    }
    const canonical = {
      name: 'Online payment',
      type: 'creem',
      provider: 'creem',
      route_id: 'online_primary',
    }
    const invalidEntry = { future_configuration: true }

    assert.deepEqual(
      removePaymentMethodByIdentity(
        [historical, canonical, invalidEntry],
        canonical
      ),
      [historical, invalidEntry]
    )
    assert.deepEqual(
      removePaymentMethodByIdentity(
        [historical, canonical, invalidEntry],
        historical
      ),
      [canonical, invalidEntry]
    )
  })
})
