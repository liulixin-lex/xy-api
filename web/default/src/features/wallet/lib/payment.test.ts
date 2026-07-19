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

import { formatPaymentDecimalAmount, formatPaymentMinorAmount } from './billing'
import {
  generatePresetAmounts,
  getPaymentMethodProvider,
  getSafePaymentUrl,
  isSafePaymentQrContent,
  mergePresetAmounts,
} from './payment'

const originalWindow = globalThis.window

function withWindowLocation(origin: string, callback: () => void): void {
  const location = new URL(origin)
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: { location },
  })
  try {
    callback()
  } finally {
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: originalWindow,
    })
  }
}

describe('payment security helpers', () => {
  test('accepts HTTPS and rejects executable or credentialed redirects', () => {
    withWindowLocation('https://console.example.com', () => {
      assert.equal(
        getSafePaymentUrl('https://checkout.example.com/pay')?.href,
        'https://checkout.example.com/pay'
      )
      assert.equal(
        getSafePaymentUrl('/wallet/payment-return')?.href,
        'https://console.example.com/wallet/payment-return'
      )
      assert.equal(getSafePaymentUrl('javascript:alert(1)'), null)
      assert.equal(getSafePaymentUrl('https://user:pass@example.com/pay'), null)
      assert.equal(getSafePaymentUrl('http://checkout.example.com/pay'), null)
    })
  })

  test('permits HTTP only for a loopback development origin', () => {
    withWindowLocation('http://localhost:3000', () => {
      assert.equal(
        getSafePaymentUrl('http://127.0.0.1:8080/pay')?.hostname,
        '127.0.0.1'
      )
      assert.equal(getSafePaymentUrl('http://example.com/pay'), null)
    })
  })

  test('routes methods by explicit provider with legacy-compatible inference', () => {
    assert.equal(getPaymentMethodProvider('custom', 'xorpay'), 'xorpay')
    assert.equal(getPaymentMethodProvider('xorpay_native'), 'xorpay')
    assert.equal(getPaymentMethodProvider('stripe'), 'stripe')
    assert.equal(getPaymentMethodProvider('custom_epay'), 'epay')
  })

  test('allows only the XORPay QR destinations accepted by the server', () => {
    assert.equal(isSafePaymentQrContent('weixin://wxpay/example'), true)
    assert.equal(isSafePaymentQrContent('https://qr.alipay.com/example'), true)
    assert.equal(
      isSafePaymentQrContent('alipays://platformapi/startapp'),
      false
    )
    assert.equal(isSafePaymentQrContent('weixin://example'), false)
    assert.equal(isSafePaymentQrContent('https://xorpay.com/qr/example'), false)
    assert.equal(
      isSafePaymentQrContent('https://qr.alipay.com.evil.example/'),
      false
    )
    assert.equal(
      isSafePaymentQrContent('https://user:pass@qr.alipay.com/example'),
      false
    )
    assert.equal(
      isSafePaymentQrContent('https://qr.alipay.com:8443/example'),
      false
    )
    assert.equal(isSafePaymentQrContent('javascript:alert(1)'), false)
    assert.equal(isSafePaymentQrContent('data:text/html,test'), false)
  })

  test('keeps preset top-ups within the server billing boundary', () => {
    assert.deepEqual(generatePresetAmounts(1000), [
      { value: 1000 },
      { value: 5000 },
      { value: 10000 },
    ])
    assert.deepEqual(
      mergePresetAmounts([0, 1, 1.5, 10000, 10001], { 1: 0.9 }),
      [
        { value: 1, discount: 0.9 },
        { value: 10000, discount: 1 },
      ]
    )
  })
})

describe('payment currency formatting', () => {
  test('uses each ISO 4217 currency minor-unit exponent', () => {
    assert.equal(
      formatPaymentMinorAmount(1234, 'JPY'),
      new Intl.NumberFormat(undefined, {
        style: 'currency',
        currency: 'JPY',
      }).format(1234)
    )
    assert.equal(
      formatPaymentMinorAmount(1234, 'KWD'),
      new Intl.NumberFormat(undefined, {
        style: 'currency',
        currency: 'KWD',
      }).format(1.234)
    )
  })

  test('uses Stripe two-decimal minor units for ISK and UGX', () => {
    for (const currency of ['ISK', 'UGX']) {
      assert.equal(
        formatPaymentMinorAmount(123400, currency, 'stripe'),
        new Intl.NumberFormat(undefined, {
          style: 'currency',
          currency,
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        }).format(1234)
      )
      assert.equal(
        formatPaymentDecimalAmount(1234, currency, 'stripe'),
        new Intl.NumberFormat(undefined, {
          style: 'currency',
          currency,
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        }).format(1234)
      )
      assert.equal(
        formatPaymentMinorAmount(1234, currency, 'epay'),
        new Intl.NumberFormat(undefined, {
          style: 'currency',
          currency,
        }).format(1234)
      )
    }
  })

  test('keeps Stripe HUF and TWD on their two-decimal API exponent', () => {
    for (const currency of ['HUF', 'TWD']) {
      assert.equal(
        formatPaymentMinorAmount(1234, currency, 'stripe'),
        new Intl.NumberFormat(undefined, {
          style: 'currency',
          currency,
        }).format(12.34)
      )
    }
  })
})
