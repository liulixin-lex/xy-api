/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { selectPaymentSettingUpdates } from './payment-settings-scope'

const CHANGES = [
  { key: 'CustomCallbackAddress', value: 'https://pay.example.com' },
  { key: 'EpayId', value: '10001' },
  { key: 'StripeCurrency', value: 'USD' },
  { key: 'StripeCheckoutAllowedHosts', value: 'pay.example.com' },
  { key: 'XorPayEnabledMethods', value: '["alipay","jsapi"]' },
  { key: 'WaffoMerchantId', value: 'merchant' },
]

describe('payment settings section save scope', () => {
  test('submits only keys owned by the selected gateway section', () => {
    assert.deepEqual(selectPaymentSettingUpdates('stripe', CHANGES), [
      { key: 'StripeCurrency', value: 'USD' },
      { key: 'StripeCheckoutAllowedHosts', value: 'pay.example.com' },
    ])
    assert.deepEqual(selectPaymentSettingUpdates('xorpay', CHANGES), [
      { key: 'XorPayEnabledMethods', value: '["alipay","jsapi"]' },
    ])
  })

  test('never sends Waffo Pancake through the shared option mutation', () => {
    assert.deepEqual(selectPaymentSettingUpdates('waffo-pancake', CHANGES), [])
  })

  test('does not include unrelated providers in a general settings save', () => {
    assert.deepEqual(selectPaymentSettingUpdates('general', CHANGES), [
      { key: 'CustomCallbackAddress', value: 'https://pay.example.com' },
    ])
  })
})
