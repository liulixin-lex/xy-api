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
  normalizePublicBillingRecord,
  normalizePublicPaymentOrder,
} from '../api'
import { normalizePublicTopupInfo } from '../hooks/use-topup-info'
import type { PaymentMethod } from '../types'
import { formatPaymentDecimalAmount, formatPaymentMinorAmount } from './billing'
import {
  createLegacyPaymentRouteId,
  detectPaymentBrowserEnvironment,
  filterEligibleSubscriptionQuoteMethods,
  filterPaymentMethodsForBrowser,
  generatePresetAmounts,
  getEffectivePaymentStatus,
  getPaymentErrorMessage,
  getPaymentMethodProvider,
  getPublicPaymentChannelLabel,
  getPublicPaymentMethodLabel,
  getSafePaymentContinueUrl,
  getSafeWeChatAuthorizationUrl,
  getSafePaymentUrl,
  isSafePaymentJSAPIParameters,
  isSafePaymentQrContent,
  isPaymentReturnCancelled,
  mergePresetAmounts,
  normalizePublicPaymentMethod,
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
  test('treats only an explicit provider cancellation return as cancelled', () => {
    assert.equal(isPaymentReturnCancelled('?payment_result=cancelled'), true)
    assert.equal(isPaymentReturnCancelled('?payment_result=pending'), false)
    assert.equal(isPaymentReturnCancelled('?payment_result=success'), false)
    assert.equal(
      isPaymentReturnCancelled('?payment_result=cancelled_extra'),
      false
    )
  })

  test('hides pending payment instructions as soon as the local deadline passes', () => {
    assert.equal(
      getEffectivePaymentStatus('preparing', 100, 100_000),
      'expired'
    )
    assert.equal(
      getEffectivePaymentStatus('awaiting_payment', 100, 100_001),
      'expired'
    )
    assert.equal(
      getEffectivePaymentStatus('confirming', 100, 100_001),
      'confirming'
    )
    assert.equal(
      getEffectivePaymentStatus('awaiting_payment', 100, 99_999),
      'awaiting_payment'
    )
  })

  test('selects one public brand route for each browser environment', () => {
    const methods: PaymentMethod[] = [
      {
        route_id: 'wechat_native',
        public_method: 'wechat_pay',
        channel_alias: 'qr',
        checkout_mode: 'quote',
      },
      {
        route_id: 'wechat_jsapi',
        public_method: 'wechat_pay',
        channel_alias: 'wechat_browser',
        checkout_mode: 'quote',
      },
      {
        route_id: 'alipay_qr',
        public_method: 'alipay',
        channel_alias: 'qr',
        checkout_mode: 'quote',
      },
      {
        route_id: 'wechat_native_backup',
        public_method: 'wechat_pay',
        channel_alias: 'qr',
        checkout_mode: 'quote',
      },
      {
        route_id: 'alipay_redirect_backup',
        public_method: 'alipay',
        channel_alias: 'redirect',
        checkout_mode: 'quote',
      },
    ]

    assert.deepEqual(
      filterPaymentMethodsForBrowser(methods, 'desktop').map(
        (method) => method.route_id
      ),
      ['wechat_native', 'alipay_qr']
    )
    assert.deepEqual(
      filterPaymentMethodsForBrowser(methods, 'mobile').map(
        (method) => method.route_id
      ),
      ['wechat_native', 'alipay_qr']
    )
    assert.deepEqual(
      filterPaymentMethodsForBrowser(methods, 'wechat').map(
        (method) => method.route_id
      ),
      ['wechat_jsapi', 'alipay_qr']
    )
    assert.deepEqual(
      filterPaymentMethodsForBrowser([methods[0]], 'wechat').map(
        (method) => method.route_id
      ),
      ['wechat_native']
    )
  })

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
    const allowed = [
      'weixin://wxpay/bizpayurl?pr=safe_token-123_ABC',
      'weixin://wxpay/bizpayurl?p%72=safe%5Ftoken',
      'https://qr.alipay.com/example',
      'http://ipay.yltg.com.cn/pay/safe_token',
      'https://IPAY.YLTG.COM.CN/pay/safe_token',
      `https://ipay.yltg.com.cn/${'a'.repeat(2048)}`,
    ]
    const rejected = [
      'weixin://wxpay/example',
      'weixin://wxpay/bizpayurl',
      'weixin://wxpay/bizpayurl?pr=',
      'weixin://wxpay/bizpayurl?pr=safe.token',
      'weixin://wxpay/bizpayurl?pr=first&pr=second',
      'weixin://wxpay/bizpayurl?pr=safe&next=other',
      `weixin://wxpay/bizpayurl?pr=${'a'.repeat(513)}`,
      'weixin://WXPAY/bizpayurl?pr=safe_token',
      'weixin://user@wxpay/bizpayurl?pr=safe_token',
      'alipays://platformapi/startapp',
      'https://xorpay.com/qr/example',
      'https://qr.alipay.com.evil.example/example',
      'https://user:pass@qr.alipay.com/example',
      'https://qr.alipay.com:8443/example',
      'https://qr.alipay.com/example?token=safe',
      'https://qr.alipay.com//evil.example',
      'https://qr.alipay.com/https://evil.example',
      'https://qr.alipay.com/../redirect',
      'https://qr.alipay.com/%2F%2Fevil.example',
      'https://qr.alipay.com/%2e%2e/redirect',
      'https://ipay.yltg.com.cn:443/pay/example',
      'https://ipay.yltg.com.cn/pay/example?token=safe',
      'https://ipay.yltg.com.cn/pay/example#fragment',
      'https://ipay.yltg.com.cn//pay/example',
      'https://ipay.yltg.com.cn/pay/../example',
      'https://ipay.yltg.com.cn/pay/example/..',
      'https://ipay.yltg.com.cn/pay/%2e%2e/example',
      `https://ipay.yltg.com.cn/${'a'.repeat(2049)}`,
      'https://evil.ipay.yltg.com.cn/pay/example',
      'https://ipay.yltg.com.cn.evil.example/pay',
      ' https://ipay.yltg.com.cn/pay/example',
      'javascript:alert(1)',
      'data:text/html,test',
    ]

    for (const value of allowed) {
      assert.equal(isSafePaymentQrContent(value), true, value)
    }
    for (const value of rejected) {
      assert.equal(isSafePaymentQrContent(value), false, value)
    }
  })

  test('accepts only the same-origin payment continuation endpoint', () => {
    withWindowLocation('https://console.example.com', () => {
      assert.equal(
        getSafePaymentContinueUrl(
          '/api/user/payment/orders/order_123/continue',
          'order_123'
        )?.href,
        'https://console.example.com/api/user/payment/orders/order_123/continue'
      )
      assert.equal(
        getSafePaymentContinueUrl(
          'https://checkout.example.com/api/user/payment/orders/order_123/continue',
          'order_123'
        ),
        null
      )
      assert.equal(
        getSafePaymentContinueUrl(
          '/api/user/payment/orders/order_456/continue',
          'order_123'
        ),
        null
      )
      assert.equal(
        getSafePaymentContinueUrl(
          '/api/user/payment/orders/order_123/continue?next=external',
          'order_123'
        ),
        null
      )
    })
  })

  test('accepts only the same-origin WeChat authorization endpoint', () => {
    withWindowLocation('https://console.example.com', () => {
      assert.equal(
        getSafeWeChatAuthorizationUrl(
          '/api/user/payment/orders/order_123/wechat-authorize',
          'order_123'
        )?.href,
        'https://console.example.com/api/user/payment/orders/order_123/wechat-authorize'
      )
      assert.equal(
        getSafeWeChatAuthorizationUrl(
          'https://gateway.example.com/api/user/payment/orders/order_123/wechat-authorize',
          'order_123'
        ),
        null
      )
      assert.equal(
        getSafeWeChatAuthorizationUrl(
          '/api/user/payment/orders/order_123/wechat-authorize?openid=secret',
          'order_123'
        ),
        null
      )
    })
  })

  test('validates only bounded JSAPI bridge parameters', () => {
    assert.equal(
      isSafePaymentJSAPIParameters({
        app_id: 'wx1234567890ABCDEF',
        timestamp: '1720000000',
        nonce_str: 'safe_nonce',
        package: 'prepay_id=example',
        sign_type: 'MD5',
        pay_sign: '0123456789ABCDEF0123456789ABCDEF',
      }),
      true
    )
    assert.equal(
      isSafePaymentJSAPIParameters({
        app_id: 'wx1234567890ABCDEF',
        timestamp: 'not-a-number',
        nonce_str: 'safe_nonce',
        package: 'prepay_id=example',
        sign_type: 'MD5',
        pay_sign: '0123456789ABCDEF0123456789ABCDEF',
      }),
      false
    )
  })

  test('detects the payment browser environment without inventing app capabilities', () => {
    assert.equal(
      detectPaymentBrowserEnvironment('Mozilla/5.0 MicroMessenger/8.0'),
      'wechat'
    )
    assert.equal(
      detectPaymentBrowserEnvironment('Mozilla/5.0 (iPhone) Mobile'),
      'mobile'
    )
    assert.equal(
      detectPaymentBrowserEnvironment('Mozilla/5.0 (X11; Linux x86_64)'),
      'desktop'
    )
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

describe('public payment presentation', () => {
  test('projects top-up capabilities without retained gateway inventory', () => {
    const info = normalizePublicTopupInfo({
      online_payment_available: true,
      payment_routes: [
        {
          route_id: 'pay_0123456789abcdef01234567',
          public_method: 'online_payment',
          channel_alias: 'product_checkout',
          checkout_mode: 'product',
          provider: 'creem',
          type: 'creem',
        },
      ],
      subscription_payment_routes: [
        {
          route_id: 'pay_abcdef0123456789abcdef01',
          public_method: 'online_payment',
          channel_alias: 'hosted_checkout',
          checkout_mode: 'direct',
          provider: 'waffo_pancake',
        },
      ],
      payment_products: [
        {
          product_id: 'product_0123456789abcdef01234567',
          route_id: 'pay_0123456789abcdef01234567',
          name: 'Starter',
          payment_amount: '9.99',
          currency: 'USD',
          top_up_amount: 1000,
          productId: 'prod_private',
        },
      ],
      payment_route_options: [
        {
          option_id: 'option_0123456789abcdef01234567',
          route_id: 'pay_0123456789abcdef01234567',
          public_label: 'Card',
          payMethodType: 'CREDITCARD',
        },
      ],
      min_topup: 1,
      amount_options: [1, 10],
      discount: { 10: 0.9 },
      enable_creem_topup: true,
      creem_products: '[{"productId":"prod_private"}]',
      waffo_pay_methods: [{ payMethodType: 'CREDITCARD' }],
    })

    assert.ok(info)
    assert.deepEqual(info.payment_routes[0], {
      route_id: 'pay_0123456789abcdef01234567',
      public_method: 'online_payment',
      channel_alias: 'product_checkout',
      checkout_mode: 'product',
      min_topup: 0,
    })
    assert.deepEqual(info.subscription_payment_routes?.[0], {
      route_id: 'pay_abcdef0123456789abcdef01',
      public_method: 'online_payment',
      channel_alias: 'hosted_checkout',
      checkout_mode: 'direct',
      min_topup: 0,
    })
    assert.deepEqual(info.payment_products[0], {
      product_id: 'product_0123456789abcdef01234567',
      route_id: 'pay_0123456789abcdef01234567',
      name: 'Starter',
      payment_amount: '9.99',
      top_up_amount: 1000,
      currency: 'USD',
    })
    assert.deepEqual(info.payment_route_options[0], {
      option_id: 'option_0123456789abcdef01234567',
      route_id: 'pay_0123456789abcdef01234567',
      public_label: 'Card',
    })
    const serialized = JSON.stringify(info)
    for (const internalValue of [
      'provider',
      'payment_method',
      'payMethodType',
      'productId',
      'enable_creem_topup',
      'creem_products',
      'waffo_pay_methods',
      'prod_private',
    ]) {
      assert.equal(serialized.includes(internalValue), false)
    }
  })

  test('strips provider diagnostics from user order and history projections', () => {
    const order = normalizePublicPaymentOrder({
      trade_no: 'pay_public_123',
      order_kind: 'topup',
      route_id: 'route_alipay',
      public_method: 'alipay',
      status_code: 'awaiting_payment',
      expected_amount_minor: 1200,
      currency: 'CNY',
      expires_at: 1_900_000_000,
      provider: 'xorpay',
      payment_provider: 'xorpay',
      payment_method: 'xorpay_alipay',
      upstream_trade_no: 'secret-upstream-id',
      status_reason: 'raw gateway reason',
      credential_generation: 7,
    })
    assert.ok(order)
    for (const internalField of [
      'provider',
      'payment_provider',
      'payment_method',
      'upstream_trade_no',
      'status_reason',
      'credential_generation',
      'format_provider',
    ]) {
      assert.equal(internalField in order, false)
    }

    const history = normalizePublicBillingRecord({
      id: 1,
      trade_no: 'pay_public_123',
      amount: 100,
      money: 12,
      public_method: 'alipay',
      status_code: 'succeeded',
      provider: 'xorpay',
      payment_provider: 'xorpay',
      payment_method: 'xorpay_alipay',
      status_reason: 'raw gateway reason',
      refunded_amount_minor: 1,
    })
    assert.ok(history)
    for (const internalField of [
      'provider',
      'payment_provider',
      'payment_method',
      'status_reason',
      'refunded_amount_minor',
    ]) {
      assert.equal(internalField in history, false)
    }
  })

  test('preserves only the public WeChat checkout instructions', () => {
    const authorization = normalizePublicPaymentOrder({
      trade_no: 'pay_wechat_authorize',
      public_method: 'wechat_pay',
      status_code: 'preparing',
      checkout: {
        flow: 'wechat_authorize',
        continue_url:
          '/api/user/payment/orders/pay_wechat_authorize/wechat-authorize',
        openid: 'must-not-reach-the-client-model',
        provider: 'xorpay',
        expires_at: 1_900_000_000,
      },
    })
    assert.equal(authorization?.checkout?.flow, 'wechat_authorize')
    assert.equal(
      authorization?.checkout?.continue_url,
      '/api/user/payment/orders/pay_wechat_authorize/wechat-authorize'
    )
    assert.equal('openid' in (authorization?.checkout || {}), false)
    assert.equal('provider' in (authorization?.checkout || {}), false)

    const jsapi = normalizePublicPaymentOrder({
      trade_no: 'pay_wechat_jsapi',
      public_method: 'wechat_pay',
      status_code: 'awaiting_payment',
      checkout: {
        flow: 'jsapi',
        expires_at: 1_900_000_000,
        jsapi: {
          app_id: 'wx1234567890ABCDEF',
          timestamp: '1720000000',
          nonce_str: 'safe_nonce',
          package: 'prepay_id=example',
          sign_type: 'MD5',
          pay_sign: '0123456789ABCDEF0123456789ABCDEF',
          openid: 'must-not-reach-the-client-model',
        },
      },
    })
    assert.equal(jsapi?.checkout?.flow, 'jsapi')
    assert.deepEqual(jsapi?.checkout?.jsapi, {
      app_id: 'wx1234567890ABCDEF',
      timestamp: '1720000000',
      nonce_str: 'safe_nonce',
      package: 'prepay_id=example',
      sign_type: 'MD5',
      pay_sign: '0123456789ABCDEF0123456789ABCDEF',
    })
    assert.equal('openid' in (jsapi?.checkout?.jsapi || {}), false)
  })

  const translate = (key: string, options?: Record<string, unknown>) =>
    Object.entries(options || {}).reduce(
      (result, [name, value]) =>
        result.replaceAll(`{{${name}}}`, String(value)),
      key
    )

  test('creates stable opaque route ids for older payment responses', () => {
    const first = createLegacyPaymentRouteId(
      'xorpay',
      'xorpay_alipay',
      'Internal name'
    )
    const repeated = createLegacyPaymentRouteId(
      'xorpay',
      'xorpay_alipay',
      'Internal name'
    )
    const second = createLegacyPaymentRouteId(
      'xorpay',
      'xorpay_native',
      'Internal name'
    )

    assert.equal(first, repeated)
    assert.notEqual(first, second)
    assert.match(first, /^route_[a-f0-9]{8}$/)
    assert.doesNotMatch(first, /xorpay|alipay|native/i)
  })

  test('shows public brands and safe aliases without exposing unknown codes', () => {
    assert.equal(
      getPublicPaymentMethodLabel({ public_method: 'alipay' }, translate),
      'Alipay'
    )
    assert.equal(
      getPublicPaymentMethodLabel(
        { public_method: normalizePublicPaymentMethod('merchant_fast') },
        translate
      ),
      'Online payment'
    )
    assert.equal(
      getPublicPaymentMethodLabel(
        { public_method: normalizePublicPaymentMethod('xorpay') },
        translate
      ),
      'Online payment'
    )
  })

  test('maps WeChat Native and JSAPI aliases to their public capabilities', () => {
    const native: PaymentMethod = {
      route_id: 'route_native',
      public_method: 'wechat_pay',
      checkout_mode: 'quote',
      channel_alias: 'native',
    }
    const jsapi: PaymentMethod = {
      route_id: 'route_jsapi',
      public_method: 'wechat_pay',
      checkout_mode: 'quote',
      channel_alias: 'jsapi',
    }

    assert.equal(
      getPublicPaymentChannelLabel(native, [native], translate),
      'QR payment'
    )
    assert.equal(
      getPublicPaymentChannelLabel(jsapi, [jsapi], translate),
      'WeChat in-app payment'
    )
  })

  test('shows and selects only subscription quote routes declared eligible', () => {
    const methods: PaymentMethod[] = [
      {
        route_id: 'quote_allowed',
        public_method: 'alipay',
        checkout_mode: 'quote',
      },
      {
        route_id: 'quote_unlisted',
        public_method: 'wechat_pay',
        checkout_mode: 'quote',
      },
      {
        route_id: 'direct_allowed',
        public_method: 'online_payment',
        checkout_mode: 'direct',
      },
    ]

    const eligible = filterEligibleSubscriptionQuoteMethods(methods, [
      'quote_allowed',
      'direct_allowed',
    ])
    assert.deepEqual(
      eligible.map((method) => method.route_id),
      ['quote_allowed']
    )
    assert.equal(eligible[0]?.route_id ?? '', 'quote_allowed')

    const unavailable = filterEligibleSubscriptionQuoteMethods(methods, [])
    const unavailableSelection = unavailable[0]?.route_id ?? ''
    assert.deepEqual(unavailable, [])
    assert.equal(unavailableSelection, '')
  })

  test('keeps compatibility channel labels free of provider names', () => {
    const methods: PaymentMethod[] = [
      {
        route_id: 'route_primary',
        public_method: 'wechat_pay',
        checkout_mode: 'quote',
      },
      {
        route_id: 'route_backup',
        public_method: 'wechat_pay',
        checkout_mode: 'quote',
      },
    ]
    assert.equal(
      getPublicPaymentChannelLabel(methods[0], methods, translate),
      'Recommended channel'
    )
    assert.equal(
      getPublicPaymentChannelLabel(methods[1], methods, translate),
      'Backup channel 1'
    )
  })

  test('maps stable error codes to safe user messages', () => {
    assert.equal(
      getPaymentErrorMessage(
        { code: 'payment_daily_limit_exceeded' },
        translate
      ),
      'The daily limit for this payment method has been reached.'
    )
    assert.equal(
      getPaymentErrorMessage(
        {
          code: 'payment_amount_invalid',
          params: { min: 1, max: 10_000 },
        },
        translate
      ),
      'Enter an amount between 1 and 10000.'
    )
    assert.equal(
      getPaymentErrorMessage({ code: 'upstream_secret_error' }, translate),
      'Payment is temporarily unavailable. Try again.'
    )
    assert.equal(
      getPaymentErrorMessage({ code: 'payment_redirect_invalid' }, translate),
      'Payment is temporarily unavailable. Try again.'
    )
    assert.equal(
      getPaymentErrorMessage(
        { code: 'payment_product_unavailable' },
        translate
      ),
      'This payment method is temporarily unavailable'
    )
    assert.equal(
      getPaymentErrorMessage(
        { code: 'payment_compliance_required' },
        translate
      ),
      'Payment is temporarily unavailable. Try again later or contact support.'
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
