/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/
export type PaymentSettingsTab =
  | 'general'
  | 'epay'
  | 'stripe'
  | 'xorpay'
  | 'creem'
  | 'waffo-pancake'
  | 'waffo'

export type PaymentSettingUpdate = {
  key: string
  value: string | number | boolean
}

export function getPaymentSettingsTabForOptionKey(
  value: string
): PaymentSettingsTab | null {
  const key = value.trim()
  if (!key) return null
  if (
    key === 'ServerAddress' ||
    key === 'TopupGroupRatio' ||
    key === 'Price' ||
    key === 'MinTopUp' ||
    key === 'CustomCallbackAddress' ||
    key === 'PayMethods' ||
    key.startsWith('payment_setting.')
  ) {
    return 'general'
  }
  if (key === 'PayAddress' || key.startsWith('Epay')) return 'epay'
  if (key.startsWith('Stripe')) return 'stripe'
  if (key.startsWith('XorPay')) return 'xorpay'
  if (key.startsWith('Creem')) return 'creem'
  if (key.startsWith('WaffoPancake')) return 'waffo-pancake'
  if (key.startsWith('Waffo')) return 'waffo'
  return null
}

const PAYMENT_OPTION_KEYS_BY_TAB: Record<
  Exclude<PaymentSettingsTab, 'waffo-pancake'>,
  ReadonlySet<string>
> = {
  general: new Set([
    'CustomCallbackAddress',
    'TopupGroupRatio',
    'Price',
    'MinTopUp',
    'PayMethods',
    'payment_setting.amount_options',
    'payment_setting.amount_discount',
  ]),
  epay: new Set(['PayAddress', 'EpayId', 'EpayKey']),
  stripe: new Set([
    'StripeApiSecret',
    'StripeWebhookSecret',
    'StripePriceId',
    'StripeAccountId',
    'StripeCheckoutAllowedHosts',
    'StripeCurrency',
    'StripeUnitPrice',
    'StripeMinTopUp',
  ]),
  xorpay: new Set([
    'XorPayAid',
    'XorPayAppSecret',
    'XorPayUnitPrice',
    'XorPayMinTopUp',
    'XorPayEnabledMethods',
  ]),
  creem: new Set([
    'CreemApiKey',
    'CreemWebhookSecret',
    'CreemTestMode',
    'CreemProducts',
  ]),
  waffo: new Set([
    'WaffoEnabled',
    'WaffoSandbox',
    'WaffoMerchantId',
    'WaffoCurrency',
    'WaffoUnitPrice',
    'WaffoMinTopUp',
    'WaffoNotifyUrl',
    'WaffoReturnUrl',
    'WaffoWebRedirectHosts',
    'WaffoAppRedirectSchemes',
    'WaffoPublicCert',
    'WaffoSandboxPublicCert',
    'WaffoApiKey',
    'WaffoPrivateKey',
    'WaffoSandboxApiKey',
    'WaffoSandboxPrivateKey',
    'WaffoPayMethods',
  ]),
}

export function selectPaymentSettingUpdates(
  section: PaymentSettingsTab,
  updates: readonly PaymentSettingUpdate[]
): PaymentSettingUpdate[] {
  if (section === 'waffo-pancake') return []
  const allowedKeys = PAYMENT_OPTION_KEYS_BY_TAB[section]
  return updates.filter((update) => allowedKeys.has(update.key))
}
