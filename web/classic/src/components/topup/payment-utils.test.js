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
  detectPaymentBrowserEnvironment,
  filterEligibleSubscriptionQuoteMethods,
  filterPaymentMethodsForBrowser,
  getPaymentQuoteRoutePayload,
  getPaymentRouteId,
  getPublicPaymentMethodLabel,
  getSafePaymentContinueUrl,
  getSafeWeChatAuthorizationUrl,
  getSafeUserPaymentError,
  isSafeQrContent,
  isSafeJSAPIParameters,
  isPaymentReturnCancelled,
  normalizePaymentMethod,
  normalizePublicPaymentOrder,
  normalizePublicPaymentStatus,
  normalizePublicTopupInfo,
  normalizePublicTopupRecord,
} from './payment-utils.js';

const t = (key, values = {}) =>
  Object.entries(values).reduce(
    (result, [name, value]) => result.replace(`{{${name}}}`, String(value)),
    key,
  );

describe('classic public payment presentation', () => {
  test('accepts only trusted XORPay and Alipay QR destinations', () => {
    const allowed = [
      'weixin://wxpay/bizpayurl?pr=safe_token-123_ABC',
      'weixin://wxpay/bizpayurl?p%72=safe%5Ftoken',
      'https://qr.alipay.com/example',
      'http://ipay.yltg.com.cn/pay/safe_token',
      'https://IPAY.YLTG.COM.CN/pay/safe_token',
      `https://ipay.yltg.com.cn/${'a'.repeat(2048)}`,
    ];
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
      'javascript:alert(1)',
      'https://evil.ipay.yltg.com.cn/pay/example',
      'https://ipay.yltg.com.cn.evil.example/pay/example',
      'https://user:pass@ipay.yltg.com.cn/pay/example',
      'https://ipay.yltg.com.cn:8443/pay/example',
      'https://ipay.yltg.com.cn:443/pay/example',
      'https://ipay.yltg.com.cn/pay/example#fragment',
      'https://ipay.yltg.com.cn/pay/example?token=safe',
      'https://ipay.yltg.com.cn//pay/example',
      'https://ipay.yltg.com.cn/pay/../example',
      'https://ipay.yltg.com.cn/pay/example/..',
      'https://ipay.yltg.com.cn/pay/%2e%2e/example',
      `https://ipay.yltg.com.cn/${'a'.repeat(2049)}`,
      'https://qr.alipay.com.evil.example/example',
      'https://user:pass@qr.alipay.com/example',
      'https://qr.alipay.com:8443/example',
      'https://qr.alipay.com/example?token=safe',
      'https://qr.alipay.com//evil.example',
      'https://qr.alipay.com/https://evil.example',
      'https://qr.alipay.com/../redirect',
      'https://qr.alipay.com/%2F%2Fevil.example',
      'https://qr.alipay.com/%2e%2e/redirect',
      ' https://ipay.yltg.com.cn/pay/example',
    ];

    for (const value of allowed) assert.equal(isSafeQrContent(value), true);
    for (const value of rejected) assert.equal(isSafeQrContent(value), false);
  });

  test('treats only an explicit provider cancellation return as cancelled', () => {
    assert.equal(isPaymentReturnCancelled('?payment_result=cancelled'), true);
    assert.equal(isPaymentReturnCancelled('?payment_result=pending'), false);
    assert.equal(isPaymentReturnCancelled('?payment_result=success'), false);
    assert.equal(
      isPaymentReturnCancelled('?payment_result=cancelled_extra'),
      false,
    );
  });

  test('selects one public brand route for each browser environment', () => {
    const methods = [
      {
        route_id: 'wechat_native',
        public_method: 'wechat_pay',
        channel_alias: 'qr',
      },
      {
        route_id: 'wechat_jsapi',
        public_method: 'wechat_pay',
        channel_alias: 'wechat_browser',
      },
      {
        route_id: 'alipay_qr',
        public_method: 'alipay',
        channel_alias: 'qr',
      },
      {
        route_id: 'wechat_native_backup',
        public_method: 'wechat_pay',
        channel_alias: 'qr',
      },
      {
        route_id: 'alipay_redirect_backup',
        public_method: 'alipay',
        channel_alias: 'redirect',
      },
    ];

    assert.deepEqual(
      filterPaymentMethodsForBrowser(methods, 'desktop').map(
        (method) => method.route_id,
      ),
      ['wechat_native', 'alipay_qr'],
    );
    assert.deepEqual(
      filterPaymentMethodsForBrowser(methods, 'mobile').map(
        (method) => method.route_id,
      ),
      ['wechat_native', 'alipay_qr'],
    );
    assert.deepEqual(
      filterPaymentMethodsForBrowser(methods, 'wechat').map(
        (method) => method.route_id,
      ),
      ['wechat_jsapi', 'alipay_qr'],
    );
    assert.deepEqual(
      filterPaymentMethodsForBrowser([methods[0]], 'wechat').map(
        (method) => method.route_id,
      ),
      ['wechat_native'],
    );
  });

  test('shows and selects only subscription quote routes declared eligible', () => {
    const methods = [
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
    ];

    const eligible = filterEligibleSubscriptionQuoteMethods(methods, [
      'quote_allowed',
      'direct_allowed',
    ]);
    assert.deepEqual(
      eligible.map((method) => method.route_id),
      ['quote_allowed'],
    );
    assert.equal(eligible[0]?.route_id || '', 'quote_allowed');

    const unavailable = filterEligibleSubscriptionQuoteMethods(methods, []);
    assert.deepEqual(unavailable, []);
    assert.equal(unavailable[0]?.route_id || '', '');
  });

  test('prefers server route ids and keeps legacy fallbacks opaque', () => {
    const serverMethod = normalizePaymentMethod({
      route_id: 'alipay_primary',
      public_method: 'alipay',
      channel_alias: 'qr',
    });
    assert.equal(serverMethod.route_id, 'alipay_primary');
    assert.deepEqual(getPaymentQuoteRoutePayload(serverMethod), {
      route_id: 'alipay_primary',
    });

    const legacyMethod = normalizePaymentMethod({
      provider: 'xorpay',
      type: 'xorpay_alipay',
    });
    assert.match(getPaymentRouteId(legacyMethod), /^pay_[a-z0-9]+$/);
    assert.doesNotMatch(getPaymentRouteId(legacyMethod), /xorpay|alipay/i);
    assert.deepEqual(getPaymentQuoteRoutePayload(legacyMethod), {
      route_id: legacyMethod.route_id,
    });
  });

  test('whitelists public order, history, and top-up capability state', () => {
    const order = normalizePublicPaymentOrder({
      trade_no: 'PO_PUBLIC',
      route_id: 'pay_0123456789abcdef01234567',
      public_method: 'alipay',
      payment_amount: '9.99',
      currency: 'CNY',
      status_code: 'awaiting_payment',
      provider: 'xorpay',
      payment_method: 'xorpay_alipay',
    });
    const record = normalizePublicTopupRecord({
      id: 1,
      trade_no: 'PO_PUBLIC',
      amount: 100,
      payment_amount: '9.99',
      currency: 'CNY',
      status_code: 'succeeded',
      provider: 'xorpay',
    });
    const info = normalizePublicTopupInfo({
      payment_routes: [
        {
          route_id: 'pay_0123456789abcdef01234567',
          public_method: 'online_payment',
          checkout_mode: 'product',
          provider: 'creem',
        },
      ],
      subscription_payment_routes: [
        {
          route_id: 'pay_abcdef0123456789abcdef01',
          public_method: 'online_payment',
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
          top_up_amount: 100,
          currency: 'USD',
          productId: 'private_product',
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
    });
    assert.equal(
      info.subscription_payment_routes[0].route_id,
      'pay_abcdef0123456789abcdef01',
    );
    for (const value of [order, record, info]) {
      const serialized = JSON.stringify(value);
      assert.doesNotMatch(
        serialized,
        /provider|payment_method|payMethodType|productId|private_product|xorpay|creem/i,
      );
    }
  });

  test('keeps compatibility labels free of provider names', () => {
    const methods = [
      {
        route_id: 'alipay_primary',
        public_method: 'alipay',
        channel_alias: 'qr',
      },
      {
        route_id: 'alipay_backup',
        public_method: 'alipay',
        channel_alias: 'redirect',
      },
    ];
    assert.equal(
      getPublicPaymentMethodLabel(methods[0], t, methods),
      '支付宝（扫码支付）',
    );
    assert.equal(
      getPublicPaymentMethodLabel(methods[1], t, methods),
      '支付宝（网页支付）',
    );
  });

  test('maps legacy provider method labels to generic online payment copy', () => {
    const providerNames = [
      'creem',
      'waffo',
      'waffo_pancake',
      'stripe',
      'epay',
      'xorpay',
    ];
    for (const [index, providerName] of providerNames.entries()) {
      const order = normalizePublicPaymentOrder({
        trade_no: 'PO_LEGACY',
        public_method: providerName,
        payment_amount: '9.99',
        currency: 'USD',
        status_code: 'awaiting_payment',
      });
      const record = normalizePublicTopupRecord({
        id: 1,
        trade_no: 'PO_LEGACY',
        public_method: providerName,
        payment_amount: '9.99',
        currency: 'USD',
        status_code: 'succeeded',
      });
      const info = normalizePublicTopupInfo({
        payment_routes: [
          {
            route_id: `pay_${String(index).padStart(24, '0')}`,
            public_method: providerName,
          },
        ],
      });

      assert.equal(order.public_method, 'payment');
      assert.equal(record.public_method, 'payment');
      assert.equal(info.payment_routes[0].public_method, 'payment');
      assert.equal(getPublicPaymentMethodLabel(order, t), '在线支付');
      assert.doesNotMatch(
        JSON.stringify({ order, record, info }),
        new RegExp(providerName, 'i'),
      );
    }
  });

  test('never reflects upstream error text into user messages', () => {
    const error = {
      response: {
        status: 503,
        data: { message: 'xorpay secret invalid: aid=123' },
      },
    };
    const message = getSafeUserPaymentError(error, t);
    assert.equal(message, '支付服务暂时不可用，请稍后重试');
    assert.doesNotMatch(message, /xorpay|secret|aid/i);

    const compatibilityError = {
      response: {
        status: 400,
        data: {
          message: 'payment_product_unavailable',
          data: 'payment_product_unavailable',
        },
      },
    };
    assert.equal(
      getSafeUserPaymentError(compatibilityError, t),
      '当前支付方式暂时不可用，请重新选择',
    );

    const complianceError = {
      response: {
        status: 200,
        data: {
          message: 'payment_compliance_required',
          code: 'payment_compliance_required',
        },
      },
    };
    assert.equal(
      getSafeUserPaymentError(complianceError, t),
      'Payment is temporarily unavailable. Try again later or contact support.',
    );
  });

  test('normalizes new and legacy order states to the public contract', () => {
    assert.equal(
      normalizePublicPaymentStatus({ status_code: 'awaiting_payment' }),
      'awaiting_payment',
    );
    assert.equal(
      normalizePublicPaymentStatus({ status: 'paid' }),
      'confirming',
    );
    assert.equal(
      normalizePublicPaymentStatus({ status: 'fulfilled' }),
      'succeeded',
    );
    assert.equal(
      normalizePublicPaymentStatus({ status: 'manual_review' }),
      'temporarily_unavailable',
    );
  });

  test('accepts only the authenticated same-origin continuation endpoint', () => {
    const originalWindow = globalThis.window;
    globalThis.window = {
      location: { origin: 'https://pay.example.com' },
    };
    try {
      assert.equal(
        getSafePaymentContinueUrl(
          '/api/user/payment/orders/PO_123/continue',
          'PO_123',
        )?.href,
        'https://pay.example.com/api/user/payment/orders/PO_123/continue',
      );
      assert.equal(
        getSafePaymentContinueUrl('https://gateway.example.com/pay', 'PO_123'),
        null,
      );
      assert.equal(
        getSafePaymentContinueUrl(
          '/api/user/payment/orders/PO_123/continue?next=provider',
          'PO_123',
        ),
        null,
      );
    } finally {
      globalThis.window = originalWindow;
    }
  });

  test('accepts only the authenticated same-origin WeChat authorization endpoint', () => {
    const originalWindow = globalThis.window;
    globalThis.window = {
      location: { origin: 'https://pay.example.com' },
    };
    try {
      assert.equal(
        getSafeWeChatAuthorizationUrl(
          '/api/user/payment/orders/PO_123/wechat-authorize',
          'PO_123',
        )?.href,
        'https://pay.example.com/api/user/payment/orders/PO_123/wechat-authorize',
      );
      assert.equal(
        getSafeWeChatAuthorizationUrl(
          'https://gateway.example.com/api/user/payment/orders/PO_123/wechat-authorize',
          'PO_123',
        ),
        null,
      );
    } finally {
      globalThis.window = originalWindow;
    }
  });

  test('validates bounded JSAPI bridge parameters', () => {
    assert.equal(
      isSafeJSAPIParameters({
        app_id: 'wx1234567890ABCDEF',
        timestamp: '1720000000',
        nonce_str: 'safe_nonce',
        package: 'prepay_id=example',
        sign_type: 'MD5',
        pay_sign: '0123456789ABCDEF0123456789ABCDEF',
      }),
      true,
    );
    assert.equal(
      isSafeJSAPIParameters({
        app_id: 'wx1234567890ABCDEF',
        timestamp: '1720000000',
        nonce_str: 'safe_nonce',
        package: 'https://example.com',
        sign_type: 'MD5',
        pay_sign: '0123456789ABCDEF0123456789ABCDEF',
      }),
      false,
    );
  });

  test('distinguishes desktop, mobile, and WeChat browser environments', () => {
    assert.equal(detectPaymentBrowserEnvironment('Mozilla/5.0'), 'desktop');
    assert.equal(
      detectPaymentBrowserEnvironment('Mozilla/5.0 (iPhone) Mobile'),
      'mobile',
    );
    assert.equal(
      detectPaymentBrowserEnvironment('Mozilla/5.0 MicroMessenger Mobile'),
      'wechat',
    );
  });
});
