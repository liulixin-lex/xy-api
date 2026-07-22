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
import type { TFunction } from 'i18next'

import { parseCurrencyDisplayType } from '@/lib/currency'
import type { AuthUser } from '@/stores/auth-store'

import { CheckinSettingsSection } from '../general/checkin-settings-section'
import { PricingSection } from '../general/pricing-section'
import { QuotaSettingsSection } from '../general/quota-settings-section'
import { PaymentLimitsSection } from '../integrations/payment-limits-section'
import { PaymentSettingsSection } from '../integrations/payment-settings-section'
import { RatioSettingsCard } from '../models/ratio-settings-card'
import type { BillingSettings } from '../types'
import { createSectionRegistry } from '../utils/section-registry'
import { AffiliateRewardSettingsSection } from './affiliate-reward-settings-section'
import { PaymentOperationsSection } from './payment-operations'
import { canAccessBillingSection } from './permissions'

const getModelDefaults = (settings: BillingSettings) => ({
  ModelPrice: settings.ModelPrice,
  ModelRatio: settings.ModelRatio,
  CacheRatio: settings.CacheRatio,
  CreateCacheRatio: settings.CreateCacheRatio,
  CompletionRatio: settings.CompletionRatio,
  ImageRatio: settings.ImageRatio,
  AudioRatio: settings.AudioRatio,
  AudioCompletionRatio: settings.AudioCompletionRatio,
  ExposeRatioEnabled: settings.ExposeRatioEnabled,
  BillingMode: settings['billing_setting.billing_mode'],
  BillingExpr: settings['billing_setting.billing_expr'],
})

const getGroupDefaults = (settings: BillingSettings) => ({
  GroupRatio: settings.GroupRatio,
  UserUsableGroups: settings.UserUsableGroups,
  GroupGroupRatio: settings.GroupGroupRatio,
  AutoGroups: settings.AutoGroups,
  DefaultUseAutoGroup: settings.DefaultUseAutoGroup,
  GroupSpecialUsableGroup:
    settings['group_ratio_setting.group_special_usable_group'],
})

const BILLING_SECTIONS = [
  {
    id: 'quota',
    titleKey: 'Quota Settings',
    build: (settings: BillingSettings) => (
      <QuotaSettingsSection
        defaultValues={{
          QuotaForNewUser: settings.QuotaForNewUser,
          PreConsumedQuota: settings.PreConsumedQuota,
          QuotaForInviter: settings.QuotaForInviter,
          QuotaForInvitee: settings.QuotaForInvitee,
          TopUpLink: settings.TopUpLink,
          general_setting: {
            docs_link: settings['general_setting.docs_link'],
          },
          quota_setting: {
            enable_free_model_pre_consume:
              settings['quota_setting.enable_free_model_pre_consume'],
          },
        }}
        complianceConfirmed={
          (settings['payment_setting.compliance_confirmed'] ?? false) &&
          settings['payment_setting.compliance_terms_version'] === 'v1'
        }
      />
    ),
  },
  {
    id: 'currency',
    titleKey: 'Currency & Display',
    build: (settings: BillingSettings) => (
      <PricingSection
        defaultValues={{
          QuotaPerUnit: settings.QuotaPerUnit,
          USDExchangeRate: settings.USDExchangeRate,
          DisplayInCurrencyEnabled: settings.DisplayInCurrencyEnabled,
          DisplayTokenStatEnabled: settings.DisplayTokenStatEnabled,
          general_setting: {
            quota_display_type: parseCurrencyDisplayType(
              settings['general_setting.quota_display_type']
            ),
            custom_currency_symbol:
              settings['general_setting.custom_currency_symbol'] ?? '¤',
            custom_currency_exchange_rate:
              settings['general_setting.custom_currency_exchange_rate'] ?? 1,
          },
        }}
      />
    ),
  },
  {
    id: 'model-pricing',
    titleKey: 'Model Pricing',
    build: (settings: BillingSettings) => (
      <RatioSettingsCard
        titleKey='Model Pricing'
        modelDefaults={getModelDefaults(settings)}
        groupDefaults={getGroupDefaults(settings)}
        toolPricesDefault={settings['tool_price_setting.prices']}
        visibleTabs={['models', 'tool-prices', 'upstream-sync']}
      />
    ),
  },
  {
    id: 'group-pricing',
    titleKey: 'Group Pricing',
    build: (settings: BillingSettings) => (
      <RatioSettingsCard
        titleKey='Group Pricing'
        modelDefaults={getModelDefaults(settings)}
        groupDefaults={getGroupDefaults(settings)}
        toolPricesDefault={settings['tool_price_setting.prices']}
        visibleTabs={['groups']}
      />
    ),
  },
  {
    id: 'payment',
    titleKey: 'Payment Gateway',
    build: (settings: BillingSettings) => (
      <PaymentSettingsSection
        configVersion={settings['payment_setting.config_version']}
        stripeCredentialAccountId={settings.StripeCredentialAccountId}
        stripeCredentialLivemode={settings.StripeCredentialLivemode}
        epayPreviousCredentialActive={
          settings['payment_setting.epay_previous_credential_active']
        }
        stripePreviousCredentialActive={
          settings['payment_setting.stripe_previous_credential_active']
        }
        stripeTestModeEnabled={
          settings['payment_setting.stripe_test_mode_enabled']
        }
        stripeTestModeBlocked={
          settings['payment_setting.stripe_test_mode_blocked']
        }
        stripeTestModeIsolationRequired={
          settings['payment_setting.stripe_test_mode_isolation_required']
        }
        stripeWebhookAPIVersion={
          settings['payment_setting.stripe_webhook_api_version']
        }
        stripeWebhookSecretOverlapHours={
          settings['payment_setting.stripe_webhook_secret_overlap_hours']
        }
        xorPayPreviousCredentialActive={
          settings['payment_setting.xorpay_previous_credential_active']
        }
        defaultValues={{
          PayAddress: settings.PayAddress,
          EpayId: settings.EpayId,
          EpayKey: settings.EpayKey,
          Price: settings.Price,
          MinTopUp: settings.MinTopUp,
          CustomCallbackAddress: settings.CustomCallbackAddress,
          TopupGroupRatio: settings.TopupGroupRatio,
          PayMethods: settings.PayMethods,
          AmountOptions: settings['payment_setting.amount_options'],
          AmountDiscount: settings['payment_setting.amount_discount'],
          StripeApiSecret: settings.StripeApiSecret,
          StripeWebhookSecret: settings.StripeWebhookSecret,
          StripePriceId: settings.StripePriceId,
          StripeAccountId: settings.StripeAccountId,
          StripeCheckoutAllowedHosts: settings.StripeCheckoutAllowedHosts,
          StripeCurrency: settings.StripeCurrency,
          StripeUnitPrice: settings.StripeUnitPrice,
          StripeMinTopUp: settings.StripeMinTopUp,
          StripePromotionCodesEnabled: settings.StripePromotionCodesEnabled,
          XorPayAid: settings.XorPayAid,
          XorPayAppSecret: settings.XorPayAppSecret,
          XorPayUnitPrice: settings.XorPayUnitPrice,
          XorPayMinTopUp: settings.XorPayMinTopUp,
          XorPayEnabledMethods: settings.XorPayEnabledMethods,
          CreemApiKey: settings.CreemApiKey,
          CreemWebhookSecret: settings.CreemWebhookSecret,
          CreemTestMode: settings.CreemTestMode,
          CreemProducts: settings.CreemProducts,
        }}
        waffoDefaultValues={{
          WaffoEnabled: settings.WaffoEnabled ?? false,
          WaffoApiKey: settings.WaffoApiKey ?? '',
          WaffoPrivateKey: settings.WaffoPrivateKey ?? '',
          WaffoPublicCert: settings.WaffoPublicCert ?? '',
          WaffoSandboxPublicCert: settings.WaffoSandboxPublicCert ?? '',
          WaffoSandboxApiKey: settings.WaffoSandboxApiKey ?? '',
          WaffoSandboxPrivateKey: settings.WaffoSandboxPrivateKey ?? '',
          WaffoSandbox: settings.WaffoSandbox ?? false,
          WaffoMerchantId: settings.WaffoMerchantId ?? '',
          WaffoCurrency: settings.WaffoCurrency ?? 'USD',
          WaffoUnitPrice: settings.WaffoUnitPrice ?? 1,
          WaffoMinTopUp: settings.WaffoMinTopUp ?? 1,
          WaffoNotifyUrl: settings.WaffoNotifyUrl ?? '',
          WaffoReturnUrl: settings.WaffoReturnUrl ?? '',
          WaffoWebRedirectHosts: settings.WaffoWebRedirectHosts ?? '',
          WaffoAppRedirectSchemes: settings.WaffoAppRedirectSchemes ?? '',
          WaffoPayMethods: settings.WaffoPayMethods ?? '[]',
        }}
        waffoPancakeDefaultValues={{
          WaffoPancakeMerchantID: settings.WaffoPancakeMerchantID ?? '',
          WaffoPancakePrivateKey: settings.WaffoPancakePrivateKey ?? '',
          WaffoPancakeReturnURL: settings.WaffoPancakeReturnURL ?? '',
          WaffoPancakeUnitPrice: settings.WaffoPancakeUnitPrice ?? 1,
          WaffoPancakeMinTopUp: settings.WaffoPancakeMinTopUp ?? 1,
          WaffoPancakeTestMode: settings.WaffoPancakeTestMode ?? false,
        }}
        waffoPancakeProvisionedStoreID={settings.WaffoPancakeStoreID ?? ''}
        waffoPancakeProvisionedProductID={settings.WaffoPancakeProductID ?? ''}
        complianceDefaults={{
          confirmed: settings['payment_setting.compliance_confirmed'] ?? false,
          termsVersion:
            settings['payment_setting.compliance_terms_version'] ?? '',
          confirmedAt: settings['payment_setting.compliance_confirmed_at'] ?? 0,
          confirmedBy: settings['payment_setting.compliance_confirmed_by'] ?? 0,
        }}
      />
    ),
  },
  {
    id: 'payment-limits',
    titleKey: 'Payment Limits',
    build: (_settings: BillingSettings) => <PaymentLimitsSection />,
  },
  {
    id: 'payment-operations',
    titleKey: 'Payment Operations',
    build: (_settings: BillingSettings) => <PaymentOperationsSection />,
  },
  {
    id: 'affiliate-rewards',
    titleKey: 'Referral Reward Settings',
    build: (settings: BillingSettings) => (
      <AffiliateRewardSettingsSection
        defaultValues={{
          continuousPercent:
            settings['payment_setting.affiliate_continuous_percent'],
          firstTopupPercent:
            settings['payment_setting.affiliate_first_topup_percent'],
        }}
      />
    ),
  },
  {
    id: 'checkin',
    titleKey: 'Check-in Rewards',
    build: (settings: BillingSettings) => (
      <CheckinSettingsSection
        defaultValues={{
          enabled: settings['checkin_setting.enabled'],
          minQuota: settings['checkin_setting.min_quota'],
          maxQuota: settings['checkin_setting.max_quota'],
        }}
      />
    ),
  },
] as const

export type BillingSectionId = (typeof BILLING_SECTIONS)[number]['id']

const billingRegistry = createSectionRegistry<
  BillingSectionId,
  BillingSettings
>({
  sections: BILLING_SECTIONS,
  defaultSection: 'quota',
  basePath: '/system-settings/billing',
  urlStyle: 'path',
})

export const BILLING_SECTION_IDS = billingRegistry.sectionIds
export const BILLING_DEFAULT_SECTION = billingRegistry.defaultSection
export function getBillingSectionNavItems(
  t: TFunction,
  user?: AuthUser | null
) {
  return billingRegistry.getSectionNavItems(t).filter((item) => {
    const section = item.url.slice(item.url.lastIndexOf('/') + 1)
    return canAccessBillingSection(section, user)
  })
}
export const getBillingSectionContent = billingRegistry.getSectionContent
export const getBillingSectionMeta = billingRegistry.getSectionMeta
