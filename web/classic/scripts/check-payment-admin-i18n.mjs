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

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { resolve } from 'node:path';

const root = process.cwd();
const locales = ['zh-CN', 'zh-TW', 'en', 'fr', 'ja', 'ru', 'vi'];
const themes = [
  {
    name: 'Classic',
    root,
    localeDirectory: resolve(root, 'src/i18n/locales'),
    locales,
    sourceRoots: [
      'src/components/settings/PaymentSetting.jsx',
      'src/components/table/subscriptions',
      'src/components/table/users/modals/UserSubscriptionsModal.jsx',
      'src/components/topup',
      'src/helpers/payment-admin-errors.js',
      'src/helpers/subscription-stripe.js',
      'src/hooks/subscriptions',
      'src/pages/Payment',
      'src/pages/Setting/Payment',
      'src/pages/Subscription',
      'src/pages/TopUp',
    ],
    sourceExtension: /\.(?:js|jsx)$/,
  },
  {
    name: 'Default',
    root: resolve(root, '../default'),
    localeDirectory: resolve(root, '../default/src/i18n/locales'),
    locales: ['zh', 'zh-TW', 'en', 'fr', 'ja', 'ru', 'vi'],
    sourceRoots: [
      'src/features/subscriptions',
      'src/features/system-settings/billing',
      'src/features/system-settings/integrations',
      'src/features/system-settings/payment-admin-errors.ts',
      'src/features/system-settings/retained-payment-credential-disable.ts',
      'src/features/wallet',
      'src/routes/_authenticated/payment',
    ],
    sourceExtension: /\.(?:ts|tsx)$/,
  },
];
const publicThemeParityKeys = [
  ['返回钱包', 'Back to Wallet'],
  ['返回钱包', 'Return to Wallet'],
  ['支付准备中', 'Preparing payment'],
  ['等待支付', 'Waiting for payment'],
  ['确认中', 'Confirming payment'],
  ['支付成功', 'Payment completed'],
  ['已过期', 'Expired'],
  ['暂时不可用', 'Payment temporarily unavailable'],
  ['支付完成', 'Payment completed'],
  ['您的定期权益已生效。', 'Your fixed-term access is ready to use.'],
  ['您的余额已更新。', 'Your balance has been updated.'],
  ['确认支付中', 'Confirming payment'],
  [
    '支付已提交，正在确认结果。您可以保持页面打开，也可以稍后返回查看。',
    'Payment was submitted and is being confirmed. You may keep this page open or return later.',
  ],
  ['支付已过期', 'Payment expired'],
  [
    '此订单已无法继续支付，请返回钱包重新创建。',
    'This payment can no longer be completed. Create a new order from your wallet.',
  ],
  ['重新发起支付', 'Create a new payment'],
  ['支付暂时不可用', 'Payment temporarily unavailable'],
  [
    '当前订单暂时无法继续，请刷新状态或重新发起支付。',
    'We could not prepare this payment. Refresh the status or create a new order.',
  ],
  ['刷新状态', 'Refresh status'],
  ['准备您的支付', 'Preparing your payment'],
  [
    '通常只需几秒。您可以安全离开，稍后凭同一订单编号返回。',
    'This usually takes a few seconds. You can safely leave and return with the same order number.',
  ],
  ['在微信中打开', 'Open in WeChat'],
  [
    '此支付方式需要在微信内完成，请使用微信打开本页面。',
    'This payment is available inside WeChat. Open this page in WeChat to continue.',
  ],
  ['页面链接已复制', 'Page link copied'],
  ['复制页面链接', 'Copy page link'],
  ['继续微信支付', 'Continue with WeChat Pay'],
  [
    '请确认微信账户，以安全准备本次支付。',
    'Confirm your WeChat account to prepare this payment securely.',
  ],
  ['在微信中继续', 'Continue in WeChat'],
  ['支付链接暂不可用', 'Payment link unavailable'],
  ['请刷新订单状态后重试。', 'Refresh the order status before trying again.'],
  [
    '支付已准备完成，请在微信中重新打开本订单继续。',
    'This payment is ready inside WeChat. Reopen this order in WeChat to continue.',
  ],
  ['微信支付', 'WeChat Pay'],
  ['Pay with WeChat', 'Pay with WeChat'],
  [
    '支付已提交，正在等待本站安全确认。',
    'Payment was submitted. Waiting for secure confirmation.',
  ],
  [
    '微信将打开支付面板，最终成功结果仍以本站确认为准。',
    'WeChat will open the payment panel. Final success is confirmed by this site.',
  ],
  ['Opening WeChat Pay', 'Opening WeChat Pay'],
  ['立即支付', 'Pay now'],
  ['支付已取消，您可以重新尝试。', 'Payment was cancelled. You can try again.'],
  [
    '未能打开微信支付，请刷新页面后重试。',
    'WeChat Pay could not open. Refresh the page and try again.',
  ],
  ['继续支付', 'Continue your payment'],
  [
    '请前往安全支付页面完成操作，最终结果仍以本站确认为准。',
    'Continue through the secure payment page. Final confirmation will appear here.',
  ],
  ['前往支付', 'Continue to payment'],
  ['支付二维码暂不可用', 'Payment code unavailable'],
  [
    '当前二维码无法安全使用，请刷新状态后重试。',
    'This payment code is unavailable. Refresh the status before trying again.',
  ],
  ['请使用所选支付应用扫码。', 'Scan the code with the selected payment app.'],
  [
    '请使用手机支付宝扫描二维码。',
    'Use Alipay on your phone to scan this code.',
  ],
  [
    '请打开支付宝完成支付；若未能拉起，可在系统浏览器中打开，或使用另一台设备扫码。',
    'Open Alipay to pay. If it does not open, use a browser or scan the code from another device.',
  ],
  ['请使用手机微信扫描二维码。', 'Use WeChat on your phone to scan this code.'],
  [
    '当前订单需要使用另一台设备展示二维码，再用微信扫一扫。',
    'This order currently requires another device to scan the payment code.',
  ],
  [
    '请在微信中打开本页，或使用另一台设备扫描二维码。',
    'Open this page in WeChat, or use another device to scan the payment code.',
  ],
  ['支付二维码', 'Payment QR code'],
  ['Scan to Pay', 'Scan to Pay'],
  ['打开支付宝', 'Open Alipay'],
  ['在浏览器中打开', 'Open in browser'],
  [
    'Unable to copy. Copy the browser address manually.',
    'Unable to copy. Copy the browser address manually.',
  ],
  ['Loading payment', 'Loading payment'],
  ['无法加载支付订单', 'Unable to load payment'],
  ['请检查网络后重试。', 'Check your connection and try again.'],
  ['重试', 'Retry'],
  ['Not available', 'Not available'],
  ['准备中', 'Preparing'],
  ['权益购买', 'Access purchase'],
  ['安全支付', 'Secure payment'],
  ['Payment method', 'Payment method'],
  ['订单详情', 'Order details'],
  ['订单编号', 'Order Number'],
  ['Payment Method', 'Payment Method'],
  ['支付选项', 'Payment option'],
  ['剩余时间', 'Time remaining'],
  ['{{minutes}} 分 {{seconds}} 秒', '{{minutes}} min {{seconds}} sec'],
  [
    '如需联系客服，请保留此订单编号。',
    'Keep the order number when contacting support about this payment.',
  ],
  [
    '自动状态更新已暂停，请刷新后继续。',
    'Automatic status updates are paused. Refresh to continue.',
  ],
  ['这是 {{site}} 的支付页面。', 'This is a payment page from {{site}}.'],
  ['支付宝', 'Alipay'],
  ['银行卡支付', 'Card payment'],
  ['在线支付', 'Online payment'],
  ['Minimum {{amount}}', 'Minimum {{amount}}'],
  ['Minimum topup amount: {{amount}}', 'Minimum topup amount: {{amount}}'],
  ['{{count}} days remaining', '{{count}} days remaining'],
  ['Validity period: {{value}}', 'Validity period: {{value}}'],
  ['Quota reset: {{value}}', 'Quota reset: {{value}}'],
  ['Total quota: {{value}}', 'Total quota: {{value}}'],
  ['Purchase limit: {{count}}', 'Purchase limit: {{count}}'],
  ['Raw quota: {{amount}}', 'Raw quota: {{amount}}'],
  [
    'Raw quota: {{used}}/{{total}}. Remaining {{remaining}}',
    'Raw quota: {{used}}/{{total}}. Remaining {{remaining}}',
  ],
  [
    '{{used}}/{{total}}. Remaining {{remaining}}',
    '{{used}}/{{total}}. Remaining {{remaining}}',
  ],
  ['Used {{percent}}%', 'Used {{percent}}%'],
  [
    'Purchase limit reached ({{count}}/{{limit}})',
    'Purchase limit reached ({{count}}/{{limit}})',
  ],
  [
    'Redemption successful! Added: {{quota}}',
    'Redemption successful! Added: {{quota}}',
  ],
  ['扫码支付', 'QR payment'],
  ['微信内支付', 'WeChat in-app payment'],
  ['Secure checkout', 'Secure checkout'],
];
const staticTranslationCall = /\bt\(\s*(['"])((?:\\.|(?!\1)[\s\S])*?)\1/g;
const hardcodedJsxText =
  /<[A-Z][^>]*>\s*([A-Za-z][A-Za-z0-9 ()$€¥.,:+/_-]*?)\s*<\//g;
const allowedHardcodedJsxText = new Set(['Creem', 'EUR (€)', 'USD ($)']);
const technicalLiterals = new Set([
  '(1 USD = {{rate}} {{symbol}})',
  'New API &lt;noreply@example.com&gt;',
  'Webhook URL:',
  'Worker URL',
  'XORPay AID',
  'XORPay Alipay',
  'XORPay WeChat Pay',
  'Waffo Pancake MoR',
  'Waffo Pancake Dashboard',
  'WeChat Native',
  'checkout.session.completed, checkout.session.async_payment_succeeded, checkout.session.async_payment_failed, checkout.session.expired, charge.refunded, charge.dispute.created, charge.dispute.closed',
  '[{"name":"Alipay","type":"alipay","icon":"SiAlipay"}]',
  'smtp.example.com',
  'stripe, epay, xorpay...',
]);
const technicalTokens = new Set([
  'AI',
  'API',
  'Alipay',
  'Checkout',
  'Creem',
  'Epay',
  'H5',
  'HTTP',
  'HTTPS',
  'JSAPI',
  'JSON',
  'Native',
  'OAuth',
  'OpenID',
  'QR',
  'Stripe',
  'URL',
  'USD',
  'Waffo',
  'Waffo Pancake',
  'WeChat',
  'WeChat Pay',
  'Webhook',
  'XORPay',
]);
const mixedEnglishResiduePatterns = {
  fr: /\b(?:Store|Product|Compliance|confirmed|successfully|user|Add)\b/i,
  vi: /\b(?:Store|Product|Merchant|Test|Production|Compliance|confirmed|confirmation|successfully|user|Dollar|Add|off|e\.g)\b/i,
  ja: /\b(?:Store|Product|Merchant|Test Mode|Production Mode|Test Key|Production Key|Compliance|Dollar|Add)\b/,
  ru: /\b(?:Store|Product|Merchant|Test Mode|Production Mode|Test Key|Production Key|Compliance|Dollar|Add)\b/,
  'zh-CN':
    /\b(?:Store|Product|Merchant|Test Mode|Production Mode|Test Key|Production Key|Compliance|Dollar|Add)\b/,
  zh: /\b(?:Store|Product|Merchant|Test Mode|Production Mode|Test Key|Production Key|Compliance|Dollar|Add)\b/,
  'zh-TW':
    /\b(?:Store|Product|Merchant|Test Mode|Production Mode|Test Key|Production Key|Compliance|Dollar|Add)\b/,
};
const simplifiedChineseResidue =
  /[规声认间户产业设发续过买费务状误现应与网关证凭据异]/;

const collectSourceFiles = (theme, sourceRoot, sourceFiles) => {
  const absolutePath = resolve(theme.root, sourceRoot);
  if (!statSync(absolutePath).isDirectory()) {
    if (
      theme.sourceExtension.test(sourceRoot) &&
      !/\.(?:test|spec)\.[jt]sx?$/.test(sourceRoot)
    ) {
      sourceFiles.push(absolutePath);
    }
    return;
  }
  for (const entry of readdirSync(absolutePath).sort()) {
    collectSourceFiles(theme, `${sourceRoot}/${entry}`, sourceFiles);
  }
};

const isLikelyUntranslated = ({ locale, englishValue, value }) => {
  if (typeof englishValue !== 'string' || typeof value !== 'string')
    return false;
  if (value !== englishValue) return false;

  const source = englishValue.trim();
  if (!source || technicalLiterals.has(source) || technicalTokens.has(source)) {
    return false;
  }
  if (
    /^(?:https?|weixin|alipays|wxp):\/\//i.test(source) ||
    /^\/[\w./-]+/.test(source) ||
    /^[\w.-]+@[\w.-]+$/.test(source) ||
    /^smtp\./i.test(source) ||
    /^(?:sk|pk|whsec|price|acct|prod)_[\w-]+$/i.test(source) ||
    /^(?:org-|gpt-)/i.test(source) ||
    /^[a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+$/i.test(source) ||
    /^[A-Z0-9_ *./:+-]+$/.test(source) ||
    source.startsWith('{') ||
    source.startsWith('[') ||
    source.includes('&#10;')
  ) {
    return false;
  }

  const copy = source
    .replace(/{{[^{}]+}}/g, ' ')
    .replace(/<[^>]+>/g, ' ')
    .replace(/&[a-z0-9#]+;/gi, ' ')
    .replace(/\s+/g, ' ')
    .trim();
  if (!/[A-Za-z]{3,}/.test(copy)) return false;

  if (locale === 'ja' || locale === 'ru' || locale.startsWith('zh')) {
    return true;
  }
  if (locale === 'fr' || locale === 'vi') {
    return /\b(the|and|or|to|with|please|day|days|remaining|move|show|hide|delete|save|cancel|canceled|open|close|payment|pay|order|retry|failed|success|available|unavailable|enable|disable|enabled|disabled|copy|link|select|product|confirm|refresh|back|continue|create|expire|expires|expired|waiting|preparing|store|end)\b/i.test(
      copy,
    );
  }
  return false;
};

const hasObviousMixedLanguageResidue = ({ locale, value }) => {
  if (typeof value !== 'string') return false;
  const copy = value
    .replace(/{{[^{}]+}}/g, ' ')
    .replace(/<[^>]+>/g, ' ')
    .replace(/https?:\/\/\S+/gi, ' ')
    .replace(/\bMerchant of Record\b/gi, ' ')
    .replace(
      /\b(?:API|HTTP|HTTPS|JSAPI|JSON|OpenID|QR|URL|USD|Webhook|Stripe|XORPay|Epay|Creem|Waffo(?: Pancake)?|WeChat(?: Pay)?|Alipay|Checkout|SuccessURL|ID|MoR|ICP|RSA|OPC|SKU)\b/gi,
      ' ',
    )
    .replace(/\s+/g, ' ')
    .trim();
  const pattern = mixedEnglishResiduePatterns[locale];
  if (pattern?.test(copy)) return true;
  return locale === 'zh-TW' && simplifiedChineseResidue.test(copy);
};

const themeResults = [];
const missingTranslations = [];
const untranslatedValues = [];
const hardcodedVisibleText = [];
for (const theme of themes) {
  const sourceFiles = [];
  for (const sourceRoot of theme.sourceRoots) {
    collectSourceFiles(theme, sourceRoot, sourceFiles);
  }

  const keys = new Set();
  for (const sourceFile of sourceFiles) {
    const source = readFileSync(sourceFile, 'utf8');
    for (const match of source.matchAll(staticTranslationCall)) {
      keys.add(match[2].replace(/\\'/g, "'").replace(/\\"/g, '"'));
    }
    for (const match of source.matchAll(hardcodedJsxText)) {
      const literal = match[1].trim();
      if (!allowedHardcodedJsxText.has(literal)) {
        hardcodedVisibleText.push(
          `${theme.name}: ${sourceFile.replace(`${theme.root}/`, '')}: ${literal}`,
        );
      }
    }
  }

  const translationsByLocale = new Map();
  for (const locale of theme.locales) {
    const content = JSON.parse(
      readFileSync(resolve(theme.localeDirectory, `${locale}.json`), 'utf8'),
    );
    translationsByLocale.set(locale, content.translation || {});
  }

  const englishTranslations = translationsByLocale.get('en');
  for (const locale of theme.locales) {
    const translations = translationsByLocale.get(locale);
    for (const key of keys) {
      if (
        !Object.prototype.hasOwnProperty.call(translations, key) ||
        String(translations[key]).trim() === ''
      ) {
        missingTranslations.push(`${theme.name}/${locale}: ${key}`);
        continue;
      }
      if (
        locale !== 'en' &&
        (isLikelyUntranslated({
          locale,
          englishValue: englishTranslations[key],
          value: translations[key],
        }) ||
          hasObviousMixedLanguageResidue({ locale, value: translations[key] }))
      ) {
        untranslatedValues.push(`${theme.name}/${locale}: ${key}`);
      }
    }
  }

  themeResults.push({ name: theme.name, keyCount: keys.size });
}

if (hardcodedVisibleText.length > 0) {
  console.error(
    `Payment source still contains hardcoded visible JSX text (${hardcodedVisibleText.length} values):`,
  );
  for (const literal of hardcodedVisibleText) console.error(literal);
  process.exit(1);
}

if (missingTranslations.length > 0) {
  console.error(
    `Payment translations are incomplete (${missingTranslations.length} values):`,
  );
  for (const missing of missingTranslations) console.error(missing);
  process.exit(1);
}

if (untranslatedValues.length > 0) {
  console.error(
    `Payment translations still contain obvious English source copy (${untranslatedValues.length} values):`,
  );
  for (const untranslated of untranslatedValues) console.error(untranslated);
  process.exit(1);
}

const parityMismatches = [];
for (const locale of locales) {
  const classicContent = JSON.parse(
    readFileSync(resolve(root, `src/i18n/locales/${locale}.json`), 'utf8'),
  );
  const defaultLocale = locale === 'zh-CN' ? 'zh' : locale;
  const defaultContent = JSON.parse(
    readFileSync(
      resolve(root, `../default/src/i18n/locales/${defaultLocale}.json`),
      'utf8',
    ),
  );
  const classicTranslations = classicContent.translation || {};
  const defaultTranslations = defaultContent.translation || {};
  for (const [classicKey, defaultKey] of publicThemeParityKeys) {
    if (
      String(classicTranslations[classicKey] || '') !==
      String(defaultTranslations[defaultKey] || '')
    ) {
      parityMismatches.push(`${locale}: ${classicKey} <> ${defaultKey}`);
    }
  }
}

if (parityMismatches.length > 0) {
  console.error('Public payment copy differs between Classic and Default:');
  for (const mismatch of parityMismatches) console.error(mismatch);
  process.exit(1);
}

console.log(
  `Payment translations complete: ${themeResults.map(({ name, keyCount }) => `${name} ${keyCount} keys`).join(', ')} across seven locales; obvious English copy scan passed and public payment copy matches across themes.`,
);
