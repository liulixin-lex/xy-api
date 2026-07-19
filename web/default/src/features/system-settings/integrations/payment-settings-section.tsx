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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Code2, Eye, ShieldAlert } from 'lucide-react'
import * as React from 'react'
import { useForm, type Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import { RiskAcknowledgementDialog } from '@/components/risk-acknowledgement-dialog'
import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
} from '@/components/ui/alert'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import {
  SecureVerificationDialog,
  type StartVerificationOptions,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { getApiErrorMessage } from '@/lib/api-error'
import { cn } from '@/lib/utils'

import {
  confirmPaymentCompliance,
  updatePaymentSettings,
  type PaymentGatewayReadiness,
} from '../api'
import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import {
  buildEmergencyCredentialReplacement,
  EMERGENCY_CREDENTIAL_REVOCATION_REASON_MAX_LENGTH,
  isEmergencyCredentialRevocationReasonValid,
  normalizeEmergencyCredentialRevocationReason,
  resolveEmergencyCredentialRevocationMode,
  type EmergencyCredentialReplacement,
  type EmergencyCredentialRevocationMode,
  type RevocablePaymentProvider,
} from '../payment-credential-revocation'
import { safeNumberFieldProps } from '../utils/numeric-field'
import { AmountDiscountVisualEditor } from './amount-discount-visual-editor'
import { AmountOptionsVisualEditor } from './amount-options-visual-editor'
import { CreemProductsVisualEditor } from './creem-products-visual-editor'
import { PaymentMethodsVisualEditor } from './payment-methods-visual-editor'
import { resolveStripeTestModeNotice } from './stripe-test-mode-readiness'
import {
  formatJsonForEditor,
  getJsonError,
  normalizeJsonForComparison,
  removeTrailingSlash,
} from './utils'
import { saveWaffoPancakeConfig } from './waffo-pancake-api'
import {
  WaffoPancakeSettingsSection,
  type WaffoPancakeBinding,
  type WaffoPancakeSettingsValues,
} from './waffo-pancake-settings-section'
import {
  type PayMethod,
  WaffoSettingsSection,
  type WaffoSettingsValues,
} from './waffo-settings-section'

function isSecureHttpUrl(
  value: string,
  requireOrigin: boolean,
  allowLocalHttp = false
) {
  const trimmed = value.trim()
  if (!trimmed) return true

  try {
    const url = new URL(trimmed)
    const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)
    const isHttpProtocol =
      url.protocol === 'https:' ||
      (allowLocalHttp && url.protocol === 'http:' && loopback)
    const hasNoPath = url.pathname === '' || url.pathname === '/'
    return (
      isHttpProtocol &&
      (!requireOrigin || hasNoPath) &&
      !url.username &&
      !url.password &&
      !url.search &&
      !url.hash
    )
  } catch {
    return false
  }
}

function inferPaymentProvider(type: string): string {
  if (type === 'stripe') return 'stripe'
  if (type.startsWith('xorpay_')) return 'xorpay'
  if (type === 'waffo_pancake') return 'waffo_pancake'
  return 'epay'
}

function validatePaymentMethodsJson(value: string): string | null {
  const error = getJsonError(value, (parsed) => Array.isArray(parsed))
  if (error) return error
  if (!value.trim()) return null

  const parsed = JSON.parse(value) as unknown[]
  if (parsed.length > 20) return 'No more than 20 payment methods are allowed'
  const types = new Set<string>()
  for (const item of parsed) {
    if (!item || typeof item !== 'object') return 'Invalid payment method entry'
    const method = item as Record<string, unknown>
    if (typeof method.name !== 'string' || !method.name.trim()) {
      return 'Each payment method requires a name and type'
    }
    if (method.name.trim().length > 128) {
      return 'Payment method name is too long'
    }
    if (
      typeof method.type !== 'string' ||
      !/^[A-Za-z0-9_-]{1,64}$/.test(method.type.trim())
    ) {
      return 'Use 1 to 64 letters, numbers, underscores, or hyphens.'
    }
    const paymentType = method.type.trim()
    if (types.has(paymentType)) return 'Payment type keys must be unique'
    types.add(paymentType)

    const provider =
      typeof method.provider === 'string'
        ? method.provider
        : inferPaymentProvider(paymentType)
    if (!['epay', 'stripe', 'xorpay', 'waffo_pancake'].includes(provider)) {
      return 'Unsupported payment provider'
    }
    const providerMatchesType =
      provider === 'epay' ||
      (provider === 'stripe' && paymentType === 'stripe') ||
      (provider === 'xorpay' &&
        (paymentType === 'xorpay_native' || paymentType === 'xorpay_alipay')) ||
      (provider === 'waffo_pancake' && paymentType === 'waffo_pancake')
    if (!providerMatchesType) {
      return 'The payment type key does not match the selected provider.'
    }
    if (
      (typeof method.icon === 'string' && method.icon.length > 64) ||
      (typeof method.color === 'string' && method.color.length > 64)
    ) {
      return 'Payment method metadata is too long'
    }
    if (method.min_topup !== undefined) {
      const minTopup = Number(method.min_topup)
      if (
        !Number.isSafeInteger(minTopup) ||
        minTopup < 1 ||
        minTopup > 10_000
      ) {
        return 'Minimum top-up must be a positive whole number between 1 and 10000'
      }
    }
  }
  return null
}

function normalizePaymentMethodsJson(value: string): string {
  if (!value.trim()) return '[]'
  const parsed = JSON.parse(value) as Array<Record<string, unknown>>
  return JSON.stringify(
    parsed.map((method) => ({
      ...method,
      name: String(method.name || '').trim(),
      type: String(method.type || '').trim(),
      provider:
        typeof method.provider === 'string'
          ? method.provider.trim()
          : inferPaymentProvider(String(method.type || '').trim()),
    }))
  )
}

const paymentSchema = z.object({
  PayAddress: z
    .string()
    .refine(
      (value) => isSecureHttpUrl(value, false),
      'Use an HTTPS payment endpoint.'
    ),
  EpayId: z.string(),
  EpayKey: z.string(),
  Price: z.coerce.number().positive(),
  MinTopUp: z.coerce.number().int().min(1).max(10_000),
  CustomCallbackAddress: z
    .string()
    .trim()
    .min(
      1,
      'A dedicated payment callback base address is required for managed payment gateways.'
    )
    .refine(
      (value) => isSecureHttpUrl(value, true, true),
      'Enter only a top-level callback domain, for example https://api.example.com, without any path.'
    ),
  TopupGroupRatio: z.string().superRefine((value, ctx) => {
    const error = getJsonError(value, (parsed) => {
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        return false
      }
      const ratios = Object.entries(parsed)
      return (
        ratios.length >= 1 &&
        ratios.length <= 100 &&
        ratios.every(
          ([group, ratio]) =>
            group.trim().length >= 1 &&
            group.length <= 64 &&
            typeof ratio === 'number' &&
            Number.isFinite(ratio) &&
            ratio > 0 &&
            ratio <= 1_000
        )
      )
    })
    if (error) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        message: error,
      })
    }
  }),
  PayMethods: z.string().superRefine((value, ctx) => {
    const error = validatePaymentMethodsJson(value)
    if (error) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        message: error,
      })
    }
  }),
  AmountOptions: z.string().superRefine((value, ctx) => {
    const error = getJsonError(value, (parsed) => Array.isArray(parsed))
    if (error) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        message: error,
      })
    }
  }),
  AmountDiscount: z.string().superRefine((value, ctx) => {
    const error = getJsonError(
      value,
      (parsed) =>
        !!parsed && typeof parsed === 'object' && !Array.isArray(parsed)
    )
    if (error) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        message: error,
      })
    }
  }),
  StripeApiSecret: z.string(),
  StripeWebhookSecret: z.string(),
  StripePriceId: z.string(),
  StripeAccountId: z.string(),
  StripeCurrency: z
    .string()
    .regex(/^[A-Za-z]{3}$/, 'Enter a three-letter ISO 4217 currency code.'),
  StripeUnitPrice: z.coerce.number().positive(),
  StripeMinTopUp: z.coerce.number().int().min(1).max(10_000),
  StripePromotionCodesEnabled: z.boolean(),
  XorPayAid: z.string(),
  XorPayAppSecret: z.string(),
  XorPayUnitPrice: z.coerce.number().positive(),
  XorPayMinTopUp: z.coerce.number().int().min(1).max(10_000),
  XorPayEnabledMethods: z.string().superRefine((value, ctx) => {
    const error = getJsonError(
      value,
      (parsed) =>
        Array.isArray(parsed) &&
        parsed.every((method) => method === 'native' || method === 'alipay') &&
        new Set(parsed).size === parsed.length
    )
    if (error) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, message: error })
    }
  }),
  CreemApiKey: z.string(),
  CreemWebhookSecret: z.string(),
  CreemTestMode: z.boolean(),
  CreemProducts: z.string().superRefine((value, ctx) => {
    const error = getJsonError(value, (parsed) => Array.isArray(parsed))
    if (error) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        message: error,
      })
    }
  }),
  WaffoEnabled: z.boolean(),
  WaffoApiKey: z.string(),
  WaffoPrivateKey: z.string(),
  WaffoPublicCert: z.string(),
  WaffoSandboxPublicCert: z.string(),
  WaffoSandboxApiKey: z.string(),
  WaffoSandboxPrivateKey: z.string(),
  WaffoSandbox: z.boolean(),
  WaffoMerchantId: z.string(),
  WaffoCurrency: z.string(),
  WaffoUnitPrice: z.coerce.number().min(0),
  WaffoMinTopUp: z.coerce.number().min(1),
  WaffoNotifyUrl: z.string(),
  WaffoReturnUrl: z.string(),
  WaffoPancakeMerchantID: z.string(),
  WaffoPancakePrivateKey: z.string(),
  WaffoPancakeReturnURL: z.string(),
})

type PaymentFormValues = z.infer<typeof paymentSchema>
type ClearablePaymentSecret =
  | 'EpayKey'
  | 'StripeApiSecret'
  | 'StripeWebhookSecret'
  | 'XorPayAppSecret'

const noEmergencyCredentialReplacement: EmergencyCredentialReplacement = {
  state: 'none',
  options: {},
}

function getRevocablePaymentProviderLabel(
  provider: RevocablePaymentProvider,
  t: (key: string) => string
): string {
  switch (provider) {
    case 'epay':
      return 'Epay'
    case 'stripe':
      return t('Stripe webhook')
    case 'xorpay':
      return 'XORPay'
  }
}

function getEmergencyCredentialRevocationDescription(
  provider: RevocablePaymentProvider,
  mode: EmergencyCredentialRevocationMode,
  t: (key: string, options?: Record<string, string>) => string
): string {
  if (mode === 'replace' && provider !== 'stripe') {
    return t(
      'The entered {{provider}} identifier and secret will be saved atomically. The current and previous credential generations are revoked immediately, and unfinished orders using them move to manual review.',
      { provider: getRevocablePaymentProviderLabel(provider, t) }
    )
  }
  if (mode === 'replace') {
    return t(
      'Emergency action: all previously accepted Stripe webhook signing secrets stop validating immediately, and every unfinished Stripe order moves to manual review. If a new whsec is entered in this form, it is saved atomically; otherwise Stripe webhooks are disabled. Clearing or normally rotating a secret does not perform this emergency revocation.'
    )
  }
  if (mode === 'stripe_disable') {
    return t(
      'Emergency action: all Stripe webhook signing secrets stop validating immediately, Stripe webhooks are disabled, and every unfinished Stripe order moves to manual review. To replace the webhook secret atomically instead, confirm compliance and use the Stripe settings form. Clearing or normally rotating a secret does not perform this emergency revocation.'
    )
  }
  return t(
    'No replacement credentials are entered. This only revokes the active previous {{provider}} credential; the current credential stays unchanged. Unfinished orders bound to the previous credential move to manual review.',
    { provider: getRevocablePaymentProviderLabel(provider, t) }
  )
}

function getEmergencyCredentialRevocationTitle(
  provider: RevocablePaymentProvider,
  mode: EmergencyCredentialRevocationMode,
  t: (key: string, options?: Record<string, string>) => string
): string {
  if (mode === 'replace') {
    return t('Emergency replace {{provider}} credentials?', {
      provider: getRevocablePaymentProviderLabel(provider, t),
    })
  }
  if (mode === 'stripe_disable') {
    return t('Disable Stripe webhooks immediately?')
  }
  return t('Revoke previous {{provider}} credential now?', {
    provider: getRevocablePaymentProviderLabel(provider, t),
  })
}

function getEmergencyCredentialRevocationConfirmLabel(
  mode: EmergencyCredentialRevocationMode,
  t: (key: string) => string
): string {
  if (mode === 'replace') return t('Replace and revoke')
  if (mode === 'stripe_disable') return t('Disable and revoke')
  return t('Revoke immediately')
}

function getClearableSecretLabel(
  key: ClearablePaymentSecret,
  t: (key: string) => string
): string {
  switch (key) {
    case 'EpayKey':
      return t('Epay secret key')
    case 'StripeApiSecret':
      return t('Stripe API secret')
    case 'StripeWebhookSecret':
      return t('Stripe webhook secret')
    case 'XorPayAppSecret':
      return t('XORPay app secret')
  }
}

function getPreviousCredentialActive(
  readiness: PaymentGatewayReadiness | null,
  provider: RevocablePaymentProvider,
  fallback: boolean
): boolean {
  const providerReadiness = readiness?.[provider]
  if (!providerReadiness || typeof providerReadiness !== 'object') {
    return fallback
  }
  const active = Reflect.get(providerReadiness, 'previous_credential_active')
  return typeof active === 'boolean' ? active : fallback
}

function EmergencyCredentialRevocationAction(props: {
  provider: RevocablePaymentProvider
  replacement: EmergencyCredentialReplacement
  previousCredentialActive: boolean
  disabled: boolean
  onRequest: (request: PendingCredentialRevocation) => void
}) {
  const { t } = useTranslation()
  const mode = resolveEmergencyCredentialRevocationMode(
    props.provider,
    props.replacement.state,
    props.previousCredentialActive
  )
  const partialReplacement = props.replacement.state === 'partial'
  const previousCredentialUnavailable =
    !mode && props.replacement.state === 'none'
  let description = mode
    ? getEmergencyCredentialRevocationDescription(props.provider, mode, t)
    : ''
  if (partialReplacement) {
    description = t(
      'The replacement credential pair is incomplete. Enter both the identifier and secret, or restore the saved identifier before using this emergency action.'
    )
  } else if (previousCredentialUnavailable) {
    description = t(
      'No active previous {{provider}} credential is available to revoke. Enter a complete replacement identifier and secret to perform an emergency replacement.',
      { provider: getRevocablePaymentProviderLabel(props.provider, t) }
    )
  }

  let actionLabel = t('Revoke previous credential now')
  if (mode === 'replace') {
    actionLabel = t('Emergency replace credentials')
  } else if (mode === 'stripe_disable') {
    actionLabel = t('Disable Stripe webhooks now')
  } else if (previousCredentialUnavailable) {
    actionLabel = t('No previous credential to revoke')
  }

  return (
    <div className='border-destructive/30 bg-destructive/5 flex h-full min-w-0 flex-col gap-4 rounded-lg border p-4'>
      <div className='flex min-w-0 gap-3'>
        <ShieldAlert className='text-destructive mt-0.5 h-4 w-4 shrink-0' />
        <div className='grid gap-1'>
          <p className='text-destructive text-sm font-medium'>
            {t('Emergency credential revocation')}
          </p>
          <p className='text-muted-foreground max-w-[72ch] text-sm'>
            {description}
          </p>
        </div>
      </div>
      <Button
        type='button'
        variant='destructive'
        size='sm'
        className='mt-auto w-full shrink-0 whitespace-nowrap sm:w-auto sm:self-end'
        disabled={
          props.disabled || partialReplacement || previousCredentialUnavailable
        }
        onClick={() => {
          if (!mode) return
          props.onRequest({
            provider: props.provider,
            mode,
            options: props.replacement.options,
          })
        }}
      >
        {actionLabel}
      </Button>
    </div>
  )
}
type WaffoFormFieldValues = Omit<WaffoSettingsValues, 'WaffoPayMethods'>
type PaymentBaseFormValues = Omit<
  PaymentFormValues,
  keyof WaffoFormFieldValues | keyof WaffoPancakeSettingsValues
>

const CURRENT_COMPLIANCE_TERMS_VERSION = 'v1'
const paymentTabContentClassName = 'mt-6 min-w-0'

type PaymentComplianceDefaults = {
  confirmed: boolean
  termsVersion: string
  confirmedAt: number
  confirmedBy: number
}

type PendingCredentialRevocation = {
  provider: RevocablePaymentProvider
  mode: EmergencyCredentialRevocationMode
  options: Record<string, string>
}

type PaymentSettingsSectionProps = {
  configVersion: number
  stripeCredentialAccountId: string
  stripeCredentialLivemode: string
  epayPreviousCredentialActive: boolean
  stripePreviousCredentialActive: boolean
  stripeTestModeEnabled: boolean
  stripeTestModeBlocked: boolean
  stripeTestModeIsolationRequired: boolean
  xorPayPreviousCredentialActive: boolean
  defaultValues: PaymentBaseFormValues
  waffoDefaultValues: WaffoSettingsValues
  waffoPancakeDefaultValues: WaffoPancakeSettingsValues
  waffoPancakeProvisionedStoreID?: string
  waffoPancakeProvisionedProductID?: string
  complianceDefaults: PaymentComplianceDefaults
}

function parseWaffoPayMethods(value: string): PayMethod[] {
  try {
    const parsed = JSON.parse(value || '[]')
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
}

function hasHttpStatus(error: unknown, status: number): boolean {
  if (!error || typeof error !== 'object') return false
  return (
    (error as { response?: { status?: unknown } }).response?.status === status
  )
}

export function PaymentSettingsSection({
  configVersion,
  stripeCredentialAccountId,
  stripeCredentialLivemode,
  epayPreviousCredentialActive,
  stripePreviousCredentialActive,
  stripeTestModeEnabled,
  stripeTestModeBlocked,
  stripeTestModeIsolationRequired,
  xorPayPreviousCredentialActive,
  defaultValues,
  waffoDefaultValues,
  waffoPancakeDefaultValues,
  waffoPancakeProvisionedStoreID,
  waffoPancakeProvisionedProductID,
  complianceDefaults,
}: PaymentSettingsSectionProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const {
    open: verificationOpen,
    methods: verificationMethods,
    state: verificationState,
    executeVerification,
    cancel: cancelVerification,
    setCode: setVerificationCode,
    switchMethod: switchVerificationMethod,
    withVerification,
  } = useSecureVerification()
  const normalizedConfigVersion =
    Number.isSafeInteger(configVersion) && configVersion > 0 ? configVersion : 1
  const configVersionRef = React.useRef(normalizedConfigVersion)
  const submitInFlightRef = React.useRef(false)
  const initialFormValues = React.useMemo<PaymentFormValues>(
    () => ({
      ...defaultValues,
      ...waffoDefaultValues,
      ...waffoPancakeDefaultValues,
    }),
    [defaultValues, waffoDefaultValues, waffoPancakeDefaultValues]
  )
  const initialRef = React.useRef(initialFormValues)
  const defaultsSignature = React.useMemo(
    () => JSON.stringify(initialFormValues),
    [initialFormValues]
  )

  const [payMethodsVisualMode, setPayMethodsVisualMode] = React.useState(true)
  const [amountOptionsVisualMode, setAmountOptionsVisualMode] =
    React.useState(true)
  const [amountDiscountVisualMode, setAmountDiscountVisualMode] =
    React.useState(true)
  const [creemProductsVisualMode, setCreemProductsVisualMode] =
    React.useState(true)
  const [showComplianceDialog, setShowComplianceDialog] = React.useState(false)
  const [pendingSecretClear, setPendingSecretClear] =
    React.useState<ClearablePaymentSecret | null>(null)
  const [pendingCredentialRevocation, setPendingCredentialRevocation] =
    React.useState<PendingCredentialRevocation | null>(null)
  const [credentialRevocationReason, setCredentialRevocationReason] =
    React.useState('')
  const [gatewayReadiness, setGatewayReadiness] =
    React.useState<PaymentGatewayReadiness | null>(null)
  const [waffoPayMethods, setWaffoPayMethods] = React.useState<PayMethod[]>(
    () => parseWaffoPayMethods(waffoDefaultValues.WaffoPayMethods)
  )
  const [waffoPancakeSelection, setWaffoPancakeSelection] =
    React.useState<WaffoPancakeBinding>({
      storeID: waffoPancakeProvisionedStoreID ?? '',
      productID: waffoPancakeProvisionedProductID ?? '',
    })
  const [waffoPancakeSavedBinding, setWaffoPancakeSavedBinding] =
    React.useState<WaffoPancakeBinding>({
      storeID: waffoPancakeProvisionedStoreID ?? '',
      productID: waffoPancakeProvisionedProductID ?? '',
    })

  React.useEffect(() => {
    setWaffoPayMethods(parseWaffoPayMethods(waffoDefaultValues.WaffoPayMethods))
  }, [waffoDefaultValues.WaffoPayMethods])

  React.useEffect(() => {
    const nextBinding = {
      storeID: waffoPancakeProvisionedStoreID ?? '',
      productID: waffoPancakeProvisionedProductID ?? '',
    }
    setWaffoPancakeSelection(nextBinding)
    setWaffoPancakeSavedBinding(nextBinding)
  }, [waffoPancakeProvisionedProductID, waffoPancakeProvisionedStoreID])

  const complianceStatements = React.useMemo(
    () => [
      t(
        'You have legally obtained authorization for the connected model APIs, accounts, keys, and quotas.'
      ),
      t(
        'You commit to using upstream APIs, accounts, keys, quotas, and service capabilities only within the scope of lawful authorization obtained from upstream service providers, model service providers, or relevant rights holders, and will not conduct unauthorized resale, trafficking, distribution, or other non-compliant commercialization.'
      ),
      t(
        'If you provide generative AI services to the public in mainland China, you will fulfill legal obligations including filing, security assessment, content safety, complaint handling, generated content labeling, log retention, and personal information protection.'
      ),
      t(
        'You commit not to use this system to implement, assist with, or indirectly implement acts that violate applicable laws and regulations, regulatory requirements, platform rules, public interests, or the lawful rights and interests of third parties.'
      ),
      t(
        'You understand and independently bear legal responsibility arising from deployment, operation, and charging behavior.'
      ),
      t(
        'You understand this compliance reminder is only for risk notice and does not constitute legal advice, a compliance review conclusion, or a guarantee of the legality of your use of this system; you should consult professional legal or compliance advisors based on your actual business scenario.'
      ),
    ],
    [t]
  )

  const complianceRequiredText = t(
    'I have read and understood the above compliance reminder, acknowledge the related legal risks, and confirm that I bear legal responsibility arising from deployment, operation, and charging behavior.'
  )
  const complianceRequiredTextParts = React.useMemo(
    () => [
      {
        type: 'input' as const,
        text: t('I have read and understood the above compliance reminder'),
      },
      { type: 'static' as const, text: t('，') },
      {
        type: 'input' as const,
        text: t('acknowledge the related legal risks'),
      },
      { type: 'static' as const, text: t('，and ') },
      {
        type: 'input' as const,
        text: t(
          'confirm that I bear legal responsibility arising from deployment'
        ),
      },
      { type: 'static' as const, text: t('、') },
      {
        type: 'input' as const,
        text: t('operation and charging behavior'),
      },
    ],
    [t]
  )

  const complianceConfirmed =
    complianceDefaults.confirmed &&
    complianceDefaults.termsVersion === CURRENT_COMPLIANCE_TERMS_VERSION

  const confirmComplianceMutation = useMutation({
    mutationFn: () =>
      withVerification(
        async () => {
          const data = await confirmPaymentCompliance()
          if (!data.success) {
            throw new Error(data.message || t('Failed to confirm compliance'))
          }
          toast.success(t('Compliance confirmed successfully'))
          setShowComplianceDialog(false)
          await queryClient.invalidateQueries({ queryKey: ['system-options'] })
          return data
        },
        {
          preferredMethod: 'passkey',
          title: t('Verify compliance confirmation'),
          description: t(
            'Confirm your identity before enabling payment and other financial features.'
          ),
        }
      ),
    onError: (error) => {
      toast.error(getApiErrorMessage(error, t('Failed to confirm compliance')))
    },
  })

  const paymentSettingsMutation = useMutation({
    mutationFn: updatePaymentSettings,
  })

  React.useEffect(() => {
    configVersionRef.current = normalizedConfigVersion
  }, [normalizedConfigVersion])

  const mutatePaymentSettings = async (
    request: {
      options: Record<string, string | number | boolean>
      clearSecrets?: string[]
      revokePreviousCredentials?: RevocablePaymentProvider[]
      reason?: string
    },
    onResult: (
      result: Awaited<ReturnType<typeof updatePaymentSettings>>
    ) => void | Promise<void>,
    verificationOptions?: StartVerificationOptions
  ) => {
    const expectedVersion = configVersionRef.current
    return withVerification(
      async () => {
        try {
          const result = await paymentSettingsMutation.mutateAsync({
            ...request,
            expectedVersion,
          })
          const nextVersion = result.data?.version
          if (
            result.success &&
            Number.isSafeInteger(nextVersion) &&
            (nextVersion ?? 0) > 0
          ) {
            configVersionRef.current = nextVersion as number
          }
          await onResult(result)
          return result
        } catch (error) {
          if (hasHttpStatus(error, 409)) {
            await queryClient.invalidateQueries({
              queryKey: ['system-options'],
            })
          }
          throw error
        }
      },
      {
        preferredMethod: 'passkey',
        title:
          verificationOptions?.title ?? t('Verify payment settings update'),
        description:
          verificationOptions?.description ??
          t(
            'Confirm your identity before changing payment credentials or gateway configuration.'
          ),
      }
    )
  }

  const paymentSettingsPending =
    paymentSettingsMutation.isPending || verificationOpen
  const normalizedCredentialRevocationReason =
    normalizeEmergencyCredentialRevocationReason(credentialRevocationReason)
  const credentialRevocationReasonValid =
    isEmergencyCredentialRevocationReasonValid(credentialRevocationReason)
  const credentialRevocationConfirmLabel = pendingCredentialRevocation
    ? getEmergencyCredentialRevocationConfirmLabel(
        pendingCredentialRevocation.mode,
        t
      )
    : t('Revoke immediately')

  const requestEmergencyCredentialRevocation = (
    request: PendingCredentialRevocation
  ) => {
    setCredentialRevocationReason('')
    setPendingCredentialRevocation(request)
  }

  const form = useForm<PaymentFormValues>({
    resolver: zodResolver(paymentSchema) as Resolver<PaymentFormValues>,
    mode: 'onChange', // Enable real-time validation
    defaultValues: {
      ...initialFormValues,
      PayMethods: formatJsonForEditor(initialFormValues.PayMethods),
      TopupGroupRatio: formatJsonForEditor(initialFormValues.TopupGroupRatio),
      AmountOptions: formatJsonForEditor(initialFormValues.AmountOptions),
      AmountDiscount: formatJsonForEditor(initialFormValues.AmountDiscount),
      CreemProducts: formatJsonForEditor(initialFormValues.CreemProducts),
    },
  })

  const { isSubmitting } = form.formState

  const setPaymentValue = React.useCallback(
    (
      key: keyof PaymentFormValues,
      value: PaymentFormValues[keyof PaymentFormValues]
    ) => {
      form.setValue(
        key as Parameters<typeof form.setValue>[0],
        value as Parameters<typeof form.setValue>[1],
        {
          shouldDirty: true,
          shouldValidate: true,
        }
      )
    },
    [form]
  )

  const clearPaymentSecret = async () => {
    const key = pendingSecretClear
    if (!key || paymentSettingsPending) return
    try {
      await mutatePaymentSettings(
        {
          options: {},
          clearSecrets: [key],
        },
        async (result) => {
          if (!result.success) {
            toast.error(result.message || t('Failed to clear saved credential'))
            return
          }
          form.resetField(key, { defaultValue: '' })
          initialRef.current[key] = ''
          setGatewayReadiness(result.data?.readiness ?? null)
          await queryClient.invalidateQueries({ queryKey: ['system-options'] })
          setPendingSecretClear(null)
          toast.success(t('Saved credential cleared'))
        }
      )
    } catch (error) {
      toast.error(
        getApiErrorMessage(error, t('Failed to clear saved credential'))
      )
    }
  }

  const revokePreviousPaymentCredential = async () => {
    const request = pendingCredentialRevocation
    if (
      !request ||
      paymentSettingsPending ||
      !credentialRevocationReasonValid
    ) {
      return
    }
    const provider = request.provider
    const providerLabel = getRevocablePaymentProviderLabel(provider, t)

    try {
      await mutatePaymentSettings(
        {
          options: request.options,
          revokePreviousCredentials: [provider],
          reason: normalizedCredentialRevocationReason,
        },
        async (result) => {
          if (!result.success) {
            toast.error(
              result.message || t('Failed to revoke previous credential')
            )
            return
          }
          if (request.mode === 'replace') {
            if (provider === 'epay') {
              form.resetField('EpayKey', { defaultValue: '' })
              initialRef.current.EpayKey = ''
            } else if (provider === 'stripe') {
              form.resetField('StripeWebhookSecret', { defaultValue: '' })
              initialRef.current.StripeWebhookSecret = ''
            } else {
              form.resetField('XorPayAppSecret', { defaultValue: '' })
              initialRef.current.XorPayAppSecret = ''
            }
          } else if (provider === 'stripe') {
            form.resetField('StripeWebhookSecret', { defaultValue: '' })
            initialRef.current.StripeWebhookSecret = ''
          }
          setGatewayReadiness(result.data?.readiness ?? null)
          await queryClient.invalidateQueries({ queryKey: ['system-options'] })
          setPendingCredentialRevocation(null)
          setCredentialRevocationReason('')
          if (request.mode === 'replace') {
            toast.success(
              t(
                '{{provider}} credentials replaced and compromised generations revoked',
                { provider: providerLabel }
              )
            )
          } else if (request.mode === 'stripe_disable') {
            toast.success(
              t('Stripe webhooks disabled and signing credentials revoked')
            )
          } else {
            toast.success(
              t('Previous {{provider}} credential revoked', {
                provider: providerLabel,
              })
            )
          }
        },
        {
          title: t('Verify emergency credential revocation'),
          description: t(
            'Confirm your identity before immediately invalidating a previous payment credential.'
          ),
        }
      )
    } catch (error) {
      toast.error(
        getApiErrorMessage(error, t('Failed to revoke previous credential'))
      )
    }
  }

  const setWaffoValue = React.useCallback(
    <K extends keyof WaffoFormFieldValues>(
      key: K,
      value: WaffoFormFieldValues[K]
    ) => {
      setPaymentValue(
        key as keyof PaymentFormValues,
        value as PaymentFormValues[keyof PaymentFormValues]
      )
    },
    [setPaymentValue]
  )

  const setWaffoPancakeValue = React.useCallback(
    <K extends keyof WaffoPancakeSettingsValues>(
      key: K,
      value: WaffoPancakeSettingsValues[K]
    ) => {
      setPaymentValue(
        key as keyof PaymentFormValues,
        value as PaymentFormValues[keyof PaymentFormValues]
      )
    },
    [setPaymentValue]
  )

  const setXorPayMethodEnabled = React.useCallback(
    (method: 'native' | 'alipay', enabled: boolean) => {
      let current: string[] = []
      try {
        const parsed = JSON.parse(form.getValues('XorPayEnabledMethods'))
        if (Array.isArray(parsed)) {
          current = parsed.filter(
            (value): value is 'native' | 'alipay' =>
              value === 'native' || value === 'alipay'
          )
        }
      } catch {
        current = []
      }
      const next = new Set(current)
      if (enabled) next.add(method)
      else next.delete(method)
      setPaymentValue('XorPayEnabledMethods', JSON.stringify([...next]))
    },
    [form, setPaymentValue]
  )

  React.useEffect(() => {
    const parsedDefaults = JSON.parse(defaultsSignature) as PaymentFormValues
    initialRef.current = parsedDefaults
    form.reset({
      ...parsedDefaults,
      PayMethods: formatJsonForEditor(parsedDefaults.PayMethods),
      TopupGroupRatio: formatJsonForEditor(parsedDefaults.TopupGroupRatio),
      AmountOptions: formatJsonForEditor(parsedDefaults.AmountOptions),
      AmountDiscount: formatJsonForEditor(parsedDefaults.AmountDiscount),
      CreemProducts: formatJsonForEditor(parsedDefaults.CreemProducts),
    })
  }, [defaultsSignature, form])

  const submitPaymentSettings = async (values: PaymentFormValues) => {
    const sanitized = {
      PayAddress: removeTrailingSlash(values.PayAddress),
      EpayId: values.EpayId.trim(),
      EpayKey: values.EpayKey.trim(),
      Price: values.Price,
      MinTopUp: values.MinTopUp,
      CustomCallbackAddress: removeTrailingSlash(values.CustomCallbackAddress),
      TopupGroupRatio: values.TopupGroupRatio.trim() || '{}',
      PayMethods: normalizePaymentMethodsJson(values.PayMethods),
      AmountOptions: values.AmountOptions.trim(),
      AmountDiscount: values.AmountDiscount.trim(),
      StripeApiSecret: values.StripeApiSecret.trim(),
      StripeWebhookSecret: values.StripeWebhookSecret.trim(),
      StripePriceId: values.StripePriceId.trim(),
      StripeAccountId: values.StripeAccountId.trim(),
      StripeCurrency: values.StripeCurrency.trim().toUpperCase(),
      StripeUnitPrice: values.StripeUnitPrice,
      StripeMinTopUp: values.StripeMinTopUp,
      StripePromotionCodesEnabled: values.StripePromotionCodesEnabled,
      XorPayAid: values.XorPayAid.trim(),
      XorPayAppSecret: values.XorPayAppSecret.trim(),
      XorPayUnitPrice: values.XorPayUnitPrice,
      XorPayMinTopUp: values.XorPayMinTopUp,
      XorPayEnabledMethods: values.XorPayEnabledMethods.trim() || '[]',
      CreemApiKey: values.CreemApiKey.trim(),
      CreemWebhookSecret: values.CreemWebhookSecret.trim(),
      CreemTestMode: values.CreemTestMode,
      CreemProducts: values.CreemProducts.trim(),
      WaffoEnabled: values.WaffoEnabled,
      WaffoSandbox: values.WaffoSandbox,
      WaffoMerchantId: values.WaffoMerchantId.trim(),
      WaffoCurrency: values.WaffoCurrency.trim() || 'USD',
      WaffoUnitPrice: values.WaffoUnitPrice,
      WaffoMinTopUp: values.WaffoMinTopUp,
      WaffoNotifyUrl: values.WaffoNotifyUrl.trim(),
      WaffoReturnUrl: values.WaffoReturnUrl.trim(),
      WaffoPublicCert: values.WaffoPublicCert.trim(),
      WaffoSandboxPublicCert: values.WaffoSandboxPublicCert.trim(),
      WaffoApiKey: values.WaffoApiKey.trim(),
      WaffoPrivateKey: values.WaffoPrivateKey.trim(),
      WaffoSandboxApiKey: values.WaffoSandboxApiKey.trim(),
      WaffoSandboxPrivateKey: values.WaffoSandboxPrivateKey.trim(),
      WaffoPayMethods: JSON.stringify(waffoPayMethods),
      WaffoPancakeMerchantID: values.WaffoPancakeMerchantID.trim(),
      WaffoPancakePrivateKey: values.WaffoPancakePrivateKey.trim(),
      WaffoPancakeReturnURL: removeTrailingSlash(
        values.WaffoPancakeReturnURL.trim()
      ),
    }

    const initial = {
      PayAddress: removeTrailingSlash(initialRef.current.PayAddress),
      EpayId: initialRef.current.EpayId.trim(),
      EpayKey: initialRef.current.EpayKey.trim(),
      Price: initialRef.current.Price,
      MinTopUp: initialRef.current.MinTopUp,
      CustomCallbackAddress: removeTrailingSlash(
        initialRef.current.CustomCallbackAddress
      ),
      TopupGroupRatio: initialRef.current.TopupGroupRatio.trim() || '{}',
      PayMethods: normalizePaymentMethodsJson(initialRef.current.PayMethods),
      AmountOptions: initialRef.current.AmountOptions.trim(),
      AmountDiscount: initialRef.current.AmountDiscount.trim(),
      StripeApiSecret: initialRef.current.StripeApiSecret.trim(),
      StripeWebhookSecret: initialRef.current.StripeWebhookSecret.trim(),
      StripePriceId: initialRef.current.StripePriceId.trim(),
      StripeAccountId: initialRef.current.StripeAccountId.trim(),
      StripeCurrency: initialRef.current.StripeCurrency.trim().toUpperCase(),
      StripeUnitPrice: initialRef.current.StripeUnitPrice,
      StripeMinTopUp: initialRef.current.StripeMinTopUp,
      StripePromotionCodesEnabled:
        initialRef.current.StripePromotionCodesEnabled,
      XorPayAid: initialRef.current.XorPayAid.trim(),
      XorPayAppSecret: initialRef.current.XorPayAppSecret.trim(),
      XorPayUnitPrice: initialRef.current.XorPayUnitPrice,
      XorPayMinTopUp: initialRef.current.XorPayMinTopUp,
      XorPayEnabledMethods:
        initialRef.current.XorPayEnabledMethods.trim() || '[]',
      CreemApiKey: initialRef.current.CreemApiKey.trim(),
      CreemWebhookSecret: initialRef.current.CreemWebhookSecret.trim(),
      CreemTestMode: initialRef.current.CreemTestMode,
      CreemProducts: initialRef.current.CreemProducts.trim(),
      WaffoEnabled: initialRef.current.WaffoEnabled,
      WaffoSandbox: initialRef.current.WaffoSandbox,
      WaffoMerchantId: initialRef.current.WaffoMerchantId.trim(),
      WaffoCurrency: initialRef.current.WaffoCurrency.trim() || 'USD',
      WaffoUnitPrice: initialRef.current.WaffoUnitPrice,
      WaffoMinTopUp: initialRef.current.WaffoMinTopUp,
      WaffoNotifyUrl: initialRef.current.WaffoNotifyUrl.trim(),
      WaffoReturnUrl: initialRef.current.WaffoReturnUrl.trim(),
      WaffoPublicCert: initialRef.current.WaffoPublicCert.trim(),
      WaffoSandboxPublicCert: initialRef.current.WaffoSandboxPublicCert.trim(),
      WaffoApiKey: initialRef.current.WaffoApiKey.trim(),
      WaffoPrivateKey: initialRef.current.WaffoPrivateKey.trim(),
      WaffoSandboxApiKey: initialRef.current.WaffoSandboxApiKey.trim(),
      WaffoSandboxPrivateKey: initialRef.current.WaffoSandboxPrivateKey.trim(),
      WaffoPayMethods: JSON.stringify(
        parseWaffoPayMethods(waffoDefaultValues.WaffoPayMethods)
      ),
      WaffoPancakeMerchantID: initialRef.current.WaffoPancakeMerchantID.trim(),
      WaffoPancakePrivateKey: initialRef.current.WaffoPancakePrivateKey.trim(),
      WaffoPancakeReturnURL: removeTrailingSlash(
        initialRef.current.WaffoPancakeReturnURL.trim()
      ),
    }

    const updates: Array<{ key: string; value: string | number | boolean }> = []

    if (sanitized.PayAddress !== initial.PayAddress) {
      updates.push({ key: 'PayAddress', value: sanitized.PayAddress })
    }

    if (sanitized.EpayId !== initial.EpayId) {
      updates.push({ key: 'EpayId', value: sanitized.EpayId })
    }

    if (sanitized.EpayKey && sanitized.EpayKey !== initial.EpayKey) {
      updates.push({ key: 'EpayKey', value: sanitized.EpayKey })
    }

    if (sanitized.Price !== initial.Price) {
      updates.push({ key: 'Price', value: sanitized.Price })
    }

    if (sanitized.MinTopUp !== initial.MinTopUp) {
      updates.push({ key: 'MinTopUp', value: sanitized.MinTopUp })
    }

    if (sanitized.CustomCallbackAddress !== initial.CustomCallbackAddress) {
      updates.push({
        key: 'CustomCallbackAddress',
        value: sanitized.CustomCallbackAddress,
      })
    }

    if (
      normalizeJsonForComparison(sanitized.TopupGroupRatio) !==
      normalizeJsonForComparison(initial.TopupGroupRatio)
    ) {
      updates.push({
        key: 'TopupGroupRatio',
        value: sanitized.TopupGroupRatio,
      })
    }

    if (
      normalizeJsonForComparison(sanitized.PayMethods) !==
      normalizeJsonForComparison(initial.PayMethods)
    ) {
      updates.push({ key: 'PayMethods', value: sanitized.PayMethods })
    }

    if (
      normalizeJsonForComparison(sanitized.AmountOptions) !==
      normalizeJsonForComparison(initial.AmountOptions)
    ) {
      updates.push({
        key: 'payment_setting.amount_options',
        value: sanitized.AmountOptions,
      })
    }

    if (
      normalizeJsonForComparison(sanitized.AmountDiscount) !==
      normalizeJsonForComparison(initial.AmountDiscount)
    ) {
      updates.push({
        key: 'payment_setting.amount_discount',
        value: sanitized.AmountDiscount,
      })
    }

    if (
      sanitized.StripeApiSecret &&
      sanitized.StripeApiSecret !== initial.StripeApiSecret
    ) {
      updates.push({ key: 'StripeApiSecret', value: sanitized.StripeApiSecret })
    }

    if (
      sanitized.StripeWebhookSecret &&
      sanitized.StripeWebhookSecret !== initial.StripeWebhookSecret
    ) {
      updates.push({
        key: 'StripeWebhookSecret',
        value: sanitized.StripeWebhookSecret,
      })
    }

    if (sanitized.StripePriceId !== initial.StripePriceId) {
      updates.push({ key: 'StripePriceId', value: sanitized.StripePriceId })
    }

    if (sanitized.StripeAccountId !== initial.StripeAccountId) {
      updates.push({
        key: 'StripeAccountId',
        value: sanitized.StripeAccountId,
      })
    }

    if (sanitized.StripeCurrency !== initial.StripeCurrency) {
      updates.push({
        key: 'StripeCurrency',
        value: sanitized.StripeCurrency,
      })
    }

    if (sanitized.StripeUnitPrice !== initial.StripeUnitPrice) {
      updates.push({ key: 'StripeUnitPrice', value: sanitized.StripeUnitPrice })
    }

    if (sanitized.StripeMinTopUp !== initial.StripeMinTopUp) {
      updates.push({ key: 'StripeMinTopUp', value: sanitized.StripeMinTopUp })
    }

    if (sanitized.XorPayAid !== initial.XorPayAid) {
      updates.push({ key: 'XorPayAid', value: sanitized.XorPayAid })
    }

    if (
      sanitized.XorPayAppSecret &&
      sanitized.XorPayAppSecret !== initial.XorPayAppSecret
    ) {
      updates.push({
        key: 'XorPayAppSecret',
        value: sanitized.XorPayAppSecret,
      })
    }

    if (sanitized.XorPayUnitPrice !== initial.XorPayUnitPrice) {
      updates.push({
        key: 'XorPayUnitPrice',
        value: sanitized.XorPayUnitPrice,
      })
    }

    if (sanitized.XorPayMinTopUp !== initial.XorPayMinTopUp) {
      updates.push({
        key: 'XorPayMinTopUp',
        value: sanitized.XorPayMinTopUp,
      })
    }

    if (
      normalizeJsonForComparison(sanitized.XorPayEnabledMethods) !==
      normalizeJsonForComparison(initial.XorPayEnabledMethods)
    ) {
      updates.push({
        key: 'XorPayEnabledMethods',
        value: sanitized.XorPayEnabledMethods,
      })
    }

    if (
      sanitized.CreemApiKey &&
      sanitized.CreemApiKey !== initial.CreemApiKey
    ) {
      updates.push({ key: 'CreemApiKey', value: sanitized.CreemApiKey })
    }

    if (
      sanitized.CreemWebhookSecret &&
      sanitized.CreemWebhookSecret !== initial.CreemWebhookSecret
    ) {
      updates.push({
        key: 'CreemWebhookSecret',
        value: sanitized.CreemWebhookSecret,
      })
    }

    if (sanitized.CreemTestMode !== initial.CreemTestMode) {
      updates.push({ key: 'CreemTestMode', value: sanitized.CreemTestMode })
    }

    if (
      normalizeJsonForComparison(sanitized.CreemProducts) !==
      normalizeJsonForComparison(initial.CreemProducts)
    ) {
      updates.push({ key: 'CreemProducts', value: sanitized.CreemProducts })
    }

    if (sanitized.WaffoEnabled !== initial.WaffoEnabled) {
      updates.push({ key: 'WaffoEnabled', value: sanitized.WaffoEnabled })
    }

    if (sanitized.WaffoSandbox !== initial.WaffoSandbox) {
      updates.push({ key: 'WaffoSandbox', value: sanitized.WaffoSandbox })
    }

    if (sanitized.WaffoMerchantId !== initial.WaffoMerchantId) {
      updates.push({ key: 'WaffoMerchantId', value: sanitized.WaffoMerchantId })
    }

    if (sanitized.WaffoCurrency !== initial.WaffoCurrency) {
      updates.push({ key: 'WaffoCurrency', value: sanitized.WaffoCurrency })
    }

    if (sanitized.WaffoUnitPrice !== initial.WaffoUnitPrice) {
      updates.push({ key: 'WaffoUnitPrice', value: sanitized.WaffoUnitPrice })
    }

    if (sanitized.WaffoMinTopUp !== initial.WaffoMinTopUp) {
      updates.push({ key: 'WaffoMinTopUp', value: sanitized.WaffoMinTopUp })
    }

    if (sanitized.WaffoNotifyUrl !== initial.WaffoNotifyUrl) {
      updates.push({ key: 'WaffoNotifyUrl', value: sanitized.WaffoNotifyUrl })
    }

    if (sanitized.WaffoReturnUrl !== initial.WaffoReturnUrl) {
      updates.push({ key: 'WaffoReturnUrl', value: sanitized.WaffoReturnUrl })
    }

    if (sanitized.WaffoPublicCert !== initial.WaffoPublicCert) {
      updates.push({ key: 'WaffoPublicCert', value: sanitized.WaffoPublicCert })
    }

    if (sanitized.WaffoSandboxPublicCert !== initial.WaffoSandboxPublicCert) {
      updates.push({
        key: 'WaffoSandboxPublicCert',
        value: sanitized.WaffoSandboxPublicCert,
      })
    }

    if (sanitized.WaffoApiKey) {
      updates.push({ key: 'WaffoApiKey', value: sanitized.WaffoApiKey })
    }

    if (sanitized.WaffoPrivateKey) {
      updates.push({ key: 'WaffoPrivateKey', value: sanitized.WaffoPrivateKey })
    }

    if (sanitized.WaffoSandboxApiKey) {
      updates.push({
        key: 'WaffoSandboxApiKey',
        value: sanitized.WaffoSandboxApiKey,
      })
    }

    if (sanitized.WaffoSandboxPrivateKey) {
      updates.push({
        key: 'WaffoSandboxPrivateKey',
        value: sanitized.WaffoSandboxPrivateKey,
      })
    }

    if (
      normalizeJsonForComparison(sanitized.WaffoPayMethods) !==
      normalizeJsonForComparison(initial.WaffoPayMethods)
    ) {
      updates.push({ key: 'WaffoPayMethods', value: sanitized.WaffoPayMethods })
    }

    const hasStripeSettingUpdate = updates.some((update) =>
      update.key.startsWith('Stripe')
    )
    if (
      (!stripeCredentialAccountId.trim() || !stripeCredentialLivemode.trim()) &&
      sanitized.StripePriceId !== '' &&
      !hasStripeSettingUpdate
    ) {
      // Existing Stripe credentials are write-only. Re-submit a current public
      // Stripe setting once so the server can resolve /v1/account and persist
      // the credential account and environment without accepting either value
      // from clients.
      updates.push({ key: 'StripePriceId', value: sanitized.StripePriceId })
    }

    const hasWaffoPancakeChanges =
      sanitized.WaffoPancakeMerchantID !== initial.WaffoPancakeMerchantID ||
      sanitized.WaffoPancakePrivateKey.length > 0 ||
      sanitized.WaffoPancakeReturnURL !== initial.WaffoPancakeReturnURL ||
      waffoPancakeSelection.storeID !== waffoPancakeSavedBinding.storeID ||
      waffoPancakeSelection.productID !== waffoPancakeSavedBinding.productID

    if (updates.length === 0 && !hasWaffoPancakeChanges) {
      toast.info(t('No changes to save'))
      return
    }

    if (hasWaffoPancakeChanges && !sanitized.WaffoPancakeMerchantID) {
      toast.error(t('Merchant ID is required'))
      return
    }

    if (
      hasWaffoPancakeChanges &&
      (!waffoPancakeSelection.storeID || !waffoPancakeSelection.productID)
    ) {
      toast.error(t('Pick or create both a store and a product before saving.'))
      return
    }

    const saveWaffoPancakeChanges = async () => {
      if (!hasWaffoPancakeChanges) return
      try {
        const body = await saveWaffoPancakeConfig({
          merchantID: sanitized.WaffoPancakeMerchantID,
          privateKey: sanitized.WaffoPancakePrivateKey,
          returnURL: sanitized.WaffoPancakeReturnURL,
          storeID: waffoPancakeSelection.storeID,
          productID: waffoPancakeSelection.productID,
        })

        if (
          body?.message === 'success' &&
          typeof body.data === 'object' &&
          body.data
        ) {
          const saved = body.data as { product_id: string; store_id: string }
          const savedBinding = {
            storeID: saved.store_id,
            productID: saved.product_id,
          }
          setWaffoPancakeSavedBinding(savedBinding)
          setWaffoPancakeSelection(savedBinding)
          queryClient.invalidateQueries({ queryKey: ['system-options'] })
          toast.success(t('Waffo Pancake settings saved'))
          return
        }

        const reason = typeof body?.data === 'string' ? body.data : undefined
        toast.error(
          reason
            ? `${t('Waffo Pancake save failed')}: ${reason}`
            : t('Waffo Pancake save failed')
        )
      } catch (error) {
        toast.error(
          `${t('Waffo Pancake save failed')}: ${getApiErrorMessage(
            error,
            t('Request failed')
          )}`
        )
      }
    }

    if (updates.length > 0) {
      const options = Object.fromEntries(
        updates.map((update) => [update.key, update.value])
      )
      try {
        await mutatePaymentSettings({ options }, async (result) => {
          if (!result.success) {
            toast.error(result.message || t('Failed to update setting'))
            return
          }
          const savedSecretKeys = updates
            .map((update) => update.key)
            .filter(
              (
                key
              ): key is
                | 'EpayKey'
                | 'StripeApiSecret'
                | 'StripeWebhookSecret'
                | 'XorPayAppSecret' =>
                key === 'EpayKey' ||
                key === 'StripeApiSecret' ||
                key === 'StripeWebhookSecret' ||
                key === 'XorPayAppSecret'
            )
          for (const key of savedSecretKeys) {
            form.resetField(key, { defaultValue: '' })
            initialRef.current[key] = ''
          }
          setGatewayReadiness(result.data?.readiness ?? null)
          await queryClient.invalidateQueries({ queryKey: ['system-options'] })
          toast.success(t('Setting updated successfully'))
          await saveWaffoPancakeChanges()
        })
      } catch (error) {
        toast.error(getApiErrorMessage(error, t('Failed to update setting')))
      }
      return
    }

    await saveWaffoPancakeChanges()
  }

  const onSubmit = async (values: PaymentFormValues) => {
    if (submitInFlightRef.current) return

    submitInFlightRef.current = true
    try {
      await submitPaymentSettings(values)
    } finally {
      submitInFlightRef.current = false
    }
  }

  const currentFormValues = form.watch()
  const epayEmergencyReplacement = buildEmergencyCredentialReplacement('epay', {
    identifier: currentFormValues.EpayId,
    savedIdentifier: initialRef.current.EpayId,
    secret: currentFormValues.EpayKey,
  })
  const stripeEmergencyReplacement = buildEmergencyCredentialReplacement(
    'stripe',
    { secret: currentFormValues.StripeWebhookSecret }
  )
  const xorPayEmergencyReplacement = buildEmergencyCredentialReplacement(
    'xorpay',
    {
      identifier: currentFormValues.XorPayAid,
      savedIdentifier: initialRef.current.XorPayAid,
      secret: currentFormValues.XorPayAppSecret,
    }
  )
  const epayPreviousCredentialAvailable = getPreviousCredentialActive(
    gatewayReadiness,
    'epay',
    epayPreviousCredentialActive
  )
  const stripePreviousCredentialAvailable = getPreviousCredentialActive(
    gatewayReadiness,
    'stripe',
    stripePreviousCredentialActive
  )
  const xorPayPreviousCredentialAvailable = getPreviousCredentialActive(
    gatewayReadiness,
    'xorpay',
    xorPayPreviousCredentialActive
  )
  const callbackBaseAddress = removeTrailingSlash(
    currentFormValues.CustomCallbackAddress
  )
  const stripeWebhookUrl = callbackBaseAddress
    ? `${callbackBaseAddress}/api/stripe/webhook`
    : '<CallbackAddress>/api/stripe/webhook'
  const epayNotifyUrl = callbackBaseAddress
    ? `${callbackBaseAddress}/api/payment/epay/notify`
    : '<CallbackAddress>/api/payment/epay/notify'
  const xorPayNotifyUrl = callbackBaseAddress
    ? `${callbackBaseAddress}/api/xorpay/notify`
    : '<CallbackAddress>/api/xorpay/notify'
  const creemWebhookUrl = callbackBaseAddress
    ? `${callbackBaseAddress}/api/creem/webhook`
    : '<CallbackAddress>/api/creem/webhook'
  let xorPayEnabledMethods: string[] = []
  try {
    const parsed = JSON.parse(currentFormValues.XorPayEnabledMethods || '[]')
    if (Array.isArray(parsed)) xorPayEnabledMethods = parsed
  } catch {
    xorPayEnabledMethods = []
  }
  const xorPayReadiness = gatewayReadiness?.xorpay
  const xorPayReady =
    typeof xorPayReadiness === 'boolean'
      ? xorPayReadiness
      : !!(
          xorPayReadiness &&
          typeof xorPayReadiness === 'object' &&
          ('ready' in xorPayReadiness
            ? xorPayReadiness.ready
            : 'enabled' in xorPayReadiness && xorPayReadiness.enabled)
        )
  const stripeReadiness = gatewayReadiness?.stripe
  let resolvedStripeCredentialAccountId = stripeCredentialAccountId.trim()
  let resolvedStripeCredentialLivemode = stripeCredentialLivemode
    .trim()
    .toLowerCase()
  if (stripeReadiness && typeof stripeReadiness === 'object') {
    const accountId = Reflect.get(stripeReadiness, 'credential_account_id')
    if (typeof accountId === 'string' && accountId.trim()) {
      resolvedStripeCredentialAccountId = accountId.trim()
    }
    const livemode = Reflect.get(stripeReadiness, 'credential_livemode')
    if (livemode === 'test' || livemode === 'live') {
      resolvedStripeCredentialLivemode = livemode
    }
  }
  let stripeCredentialEnvironmentLabel = ''
  if (resolvedStripeCredentialLivemode === 'live') {
    stripeCredentialEnvironmentLabel = t('Live Mode')
  } else if (resolvedStripeCredentialLivemode === 'test') {
    stripeCredentialEnvironmentLabel = t('Test Mode')
  }
  const stripeTestModeNotice = resolveStripeTestModeNotice({
    credentialLivemode: resolvedStripeCredentialLivemode,
    initialEnabled: stripeTestModeEnabled,
    initialBlocked: stripeTestModeBlocked,
    initialIsolationRequired: stripeTestModeIsolationRequired,
    readiness: stripeReadiness,
  })
  const waffoValues: WaffoSettingsValues = {
    WaffoEnabled: currentFormValues.WaffoEnabled,
    WaffoApiKey: currentFormValues.WaffoApiKey,
    WaffoPrivateKey: currentFormValues.WaffoPrivateKey,
    WaffoPublicCert: currentFormValues.WaffoPublicCert,
    WaffoSandboxPublicCert: currentFormValues.WaffoSandboxPublicCert,
    WaffoSandboxApiKey: currentFormValues.WaffoSandboxApiKey,
    WaffoSandboxPrivateKey: currentFormValues.WaffoSandboxPrivateKey,
    WaffoSandbox: currentFormValues.WaffoSandbox,
    WaffoMerchantId: currentFormValues.WaffoMerchantId,
    WaffoCurrency: currentFormValues.WaffoCurrency,
    WaffoUnitPrice: currentFormValues.WaffoUnitPrice,
    WaffoMinTopUp: currentFormValues.WaffoMinTopUp,
    WaffoNotifyUrl: currentFormValues.WaffoNotifyUrl,
    WaffoReturnUrl: currentFormValues.WaffoReturnUrl,
    WaffoPayMethods: JSON.stringify(waffoPayMethods),
  }
  const waffoPancakeValues: WaffoPancakeSettingsValues = {
    WaffoPancakeMerchantID: currentFormValues.WaffoPancakeMerchantID,
    WaffoPancakePrivateKey: currentFormValues.WaffoPancakePrivateKey,
    WaffoPancakeReturnURL: currentFormValues.WaffoPancakeReturnURL,
  }

  return (
    <SettingsSection title={t('Payment Gateway')}>
      {!complianceConfirmed ? (
        <Alert variant='destructive' className='mb-6'>
          <ShieldAlert className='h-4 w-4' />
          <AlertTitle>{t('Compliance confirmation required')}</AlertTitle>
          <AlertDescription>
            <div className='space-y-3'>
              <p>
                {t(
                  'Payment, redemption codes, subscription plans, and invitation rewards are locked until the root administrator confirms the compliance terms.'
                )}
              </p>
              <ol className='list-decimal space-y-1 pl-5'>
                {complianceStatements.map((statement) => (
                  <li key={statement}>{statement}</li>
                ))}
              </ol>
            </div>
          </AlertDescription>
          <AlertAction>
            <Button
              type='button'
              size='sm'
              variant='destructive'
              onClick={() => setShowComplianceDialog(true)}
            >
              {t('Confirm compliance')}
            </Button>
          </AlertAction>
        </Alert>
      ) : (
        <Alert className='mb-6'>
          <AlertTitle>{t('Compliance confirmed')}</AlertTitle>
          <AlertDescription>
            {t('Confirmed at {{time}} by user #{{userId}}', {
              time: complianceDefaults.confirmedAt
                ? new Date(
                    complianceDefaults.confirmedAt * 1000
                  ).toLocaleString()
                : '-',
              userId: complianceDefaults.confirmedBy || '-',
            })}
          </AlertDescription>
        </Alert>
      )}

      {!complianceConfirmed && (
        <div className='mb-6 grid gap-3 lg:grid-cols-3'>
          <EmergencyCredentialRevocationAction
            provider='epay'
            replacement={noEmergencyCredentialReplacement}
            previousCredentialActive={epayPreviousCredentialAvailable}
            disabled={paymentSettingsPending}
            onRequest={requestEmergencyCredentialRevocation}
          />
          <EmergencyCredentialRevocationAction
            provider='stripe'
            replacement={noEmergencyCredentialReplacement}
            previousCredentialActive={stripePreviousCredentialAvailable}
            disabled={paymentSettingsPending}
            onRequest={requestEmergencyCredentialRevocation}
          />
          <EmergencyCredentialRevocationAction
            provider='xorpay'
            replacement={noEmergencyCredentialReplacement}
            previousCredentialActive={xorPayPreviousCredentialAvailable}
            disabled={paymentSettingsPending}
            onRequest={requestEmergencyCredentialRevocation}
          />
        </div>
      )}

      <RiskAcknowledgementDialog
        open={showComplianceDialog}
        onOpenChange={setShowComplianceDialog}
        title={t('Confirm compliance terms')}
        description={t(
          'This confirmation unlocks payment, redemption code, subscription plan, and invitation reward features. Please read the statements carefully.'
        )}
        items={complianceStatements}
        requiredText={complianceRequiredText}
        requiredTextParts={complianceRequiredTextParts}
        inputPrompt={t('Please type the following text to confirm:')}
        inputPlaceholder={t('Type the confirmation text here')}
        mismatchHint={t('The entered text does not match the required text.')}
        confirmText={t('Confirm and enable')}
        isLoading={confirmComplianceMutation.isPending || verificationOpen}
        onConfirm={() => confirmComplianceMutation.mutate()}
      />

      <SecureVerificationDialog
        open={verificationOpen}
        onOpenChange={(open) => {
          if (!open) cancelVerification()
        }}
        methods={verificationMethods}
        state={verificationState}
        onVerify={(method, code) => {
          void executeVerification(method, code).catch(() => {
            // useSecureVerification already reports the failure to the user.
          })
        }}
        onCancel={cancelVerification}
        onCodeChange={setVerificationCode}
        onMethodChange={switchVerificationMethod}
      />

      <AlertDialog
        open={pendingSecretClear !== null}
        onOpenChange={(open) => {
          if (!open && !paymentSettingsPending) {
            setPendingSecretClear(null)
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('Clear saved credential?')}</AlertDialogTitle>
            <AlertDialogDescription>
              {pendingSecretClear
                ? t(
                    'This permanently removes the saved {{credential}}. New payments or callbacks that depend on it may stop working immediately.',
                    {
                      credential: getClearableSecretLabel(
                        pendingSecretClear,
                        t
                      ),
                    }
                  )
                : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={paymentSettingsPending}>
              {t('Cancel')}
            </AlertDialogCancel>
            <AlertDialogAction
              variant='destructive'
              disabled={paymentSettingsPending}
              onClick={(event) => {
                event.preventDefault()
                void clearPaymentSecret()
              }}
            >
              {paymentSettingsPending
                ? t('Clearing...')
                : t('Clear saved credential')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={pendingCredentialRevocation !== null}
        onOpenChange={(open) => {
          if (!open && !paymentSettingsPending) {
            setPendingCredentialRevocation(null)
            setCredentialRevocationReason('')
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {pendingCredentialRevocation
                ? getEmergencyCredentialRevocationTitle(
                    pendingCredentialRevocation.provider,
                    pendingCredentialRevocation.mode,
                    t
                  )
                : ''}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {pendingCredentialRevocation
                ? getEmergencyCredentialRevocationDescription(
                    pendingCredentialRevocation.provider,
                    pendingCredentialRevocation.mode,
                    t
                  )
                : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className='grid gap-2'>
            <Label htmlFor='emergency-credential-revocation-reason'>
              {t('Emergency revocation reason')}
            </Label>
            <Textarea
              id='emergency-credential-revocation-reason'
              value={credentialRevocationReason}
              maxLength={EMERGENCY_CREDENTIAL_REVOCATION_REASON_MAX_LENGTH}
              rows={4}
              disabled={paymentSettingsPending}
              aria-invalid={
                credentialRevocationReason.length > 0 &&
                !credentialRevocationReasonValid
              }
              placeholder={t(
                'Describe the credential leak, compromise, or emergency rotation'
              )}
              onChange={(event) =>
                setCredentialRevocationReason(event.target.value)
              }
            />
            <div className='flex flex-wrap items-start justify-between gap-2 text-xs'>
              <p
                className={cn(
                  'text-muted-foreground',
                  credentialRevocationReason.length > 0 &&
                    !credentialRevocationReasonValid &&
                    'text-destructive'
                )}
              >
                {credentialRevocationReason.length > 0 &&
                !credentialRevocationReasonValid
                  ? t('Reason must be between 8 and 512 characters')
                  : t(
                      'Enter 8 to 512 characters explaining the credential incident and response.'
                    )}
              </p>
              <span className='text-muted-foreground tabular-nums'>
                {credentialRevocationReason.length} /{' '}
                {EMERGENCY_CREDENTIAL_REVOCATION_REASON_MAX_LENGTH}
              </span>
            </div>
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={paymentSettingsPending}>
              {t('Cancel')}
            </AlertDialogCancel>
            <AlertDialogAction
              variant='destructive'
              disabled={
                paymentSettingsPending || !credentialRevocationReasonValid
              }
              onClick={(event) => {
                event.preventDefault()
                void revokePreviousPaymentCredential()
              }}
            >
              {paymentSettingsPending
                ? t('Revoking...')
                : credentialRevocationConfirmLabel}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <Form {...form}>
        <SettingsForm
          onSubmit={form.handleSubmit(onSubmit)}
          className='gap-y-8'
          data-no-autosubmit='true'
        >
          <fieldset
            disabled={!complianceConfirmed}
            className={cn(
              'min-w-0 space-y-8 border-0 p-0',
              !complianceConfirmed && 'opacity-40'
            )}
          >
            <SettingsPageFormActions
              onSave={form.handleSubmit(onSubmit)}
              isSaving={paymentSettingsPending || isSubmitting}
              saveLabel='Save all settings'
            />
            <Tabs defaultValue='general' className='min-w-0'>
              <div className='overflow-x-auto pb-1'>
                <TabsList className='grid min-w-[51rem] grid-cols-7'>
                  <TabsTrigger value='general'>{t('General')}</TabsTrigger>
                  <TabsTrigger value='epay'>Epay</TabsTrigger>
                  <TabsTrigger value='stripe'>{t('Stripe')}</TabsTrigger>
                  <TabsTrigger value='xorpay'>XORPay</TabsTrigger>
                  <TabsTrigger value='creem'>Creem</TabsTrigger>
                  <TabsTrigger value='waffo-pancake'>Waffo Pancake</TabsTrigger>
                  <TabsTrigger value='waffo'>Waffo</TabsTrigger>
                </TabsList>
              </div>

              <TabsContent
                value='general'
                className={paymentTabContentClassName}
              >
                <div className='space-y-4'>
                  <div>
                    <h3 className='text-lg font-medium'>
                      {t('General Settings')}
                    </h3>
                    <p className='text-muted-foreground text-sm'>
                      {t('Shared configuration for all payment gateways')}
                    </p>
                  </div>

                  <Alert>
                    <ShieldAlert className='h-4 w-4' />
                    <AlertTitle>
                      {t('Payment callback security boundary')}
                    </AlertTitle>
                    <AlertDescription>
                      {t(
                        'New Epay, Stripe, and XORPay payments use only the managed callback base below for callback and return URLs. The general ServerAddress setting is not trusted for these payment flows.'
                      )}
                    </AlertDescription>
                  </Alert>

                  <div className='grid gap-6 md:grid-cols-2 md:items-start'>
                    <FormField
                      control={form.control}
                      name='CustomCallbackAddress'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>
                            {t('Secure payment callback base')}{' '}
                            <span
                              className='text-destructive'
                              aria-hidden='true'
                            >
                              *
                            </span>
                          </FormLabel>
                          <FormControl>
                            <Input
                              placeholder={t('https://gateway.example.com')}
                              aria-required='true'
                              {...field}
                              onChange={(event) =>
                                field.onChange(event.target.value)
                              }
                            />
                          </FormControl>
                          <FormDescription>
                            {t(
                              'Required dedicated HTTPS origin for payment callbacks and returns. Enter only the origin, without a path, query, credentials, or fragment.'
                            )}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <FormField
                      control={form.control}
                      name='TopupGroupRatio'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Top-up group ratios')}</FormLabel>
                          <FormControl>
                            <Textarea
                              rows={5}
                              placeholder='{ "default": 1, "vip": 1.2 }'
                              {...field}
                            />
                          </FormControl>
                          <FormDescription>
                            {t(
                              'Payment-owned JSON map of positive recharge multipliers by user group. Changes require payment gateway permission and identity verification.'
                            )}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  </div>

                  <div className='grid gap-6 md:grid-cols-2'>
                    <FormField
                      control={form.control}
                      name='Price'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>
                            {t('Price (local currency / USD)')}
                          </FormLabel>
                          <FormControl>
                            <Input
                              type='number'
                              step='0.01'
                              min={0.01}
                              {...safeNumberFieldProps(field)}
                            />
                          </FormControl>
                          <FormDescription>
                            {t(
                              'How much to charge for each US dollar of balance (Epay)'
                            )}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <FormField
                      control={form.control}
                      name='MinTopUp'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Minimum top-up (USD)')}</FormLabel>
                          <FormControl>
                            <Input
                              type='number'
                              step={1}
                              min={1}
                              {...safeNumberFieldProps(field)}
                            />
                          </FormControl>
                          <FormDescription>
                            {t('Smallest USD amount users can recharge (Epay)')}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  </div>

                  <FormField
                    control={form.control}
                    name='PayMethods'
                    render={({ field }) => (
                      <FormItem>
                        <div className='mb-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
                          <FormLabel>{t('Payment methods')}</FormLabel>
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            onClick={() =>
                              setPayMethodsVisualMode(!payMethodsVisualMode)
                            }
                            className='w-full sm:w-auto'
                          >
                            {payMethodsVisualMode ? (
                              <>
                                <Code2 className='mr-2 h-3 w-3' />
                                {t('JSON Editor')}
                              </>
                            ) : (
                              <>
                                <Eye className='mr-2 h-3 w-3' />
                                {t('Visual Editor')}
                              </>
                            )}
                          </Button>
                        </div>
                        <FormControl>
                          {payMethodsVisualMode ? (
                            <PaymentMethodsVisualEditor
                              value={field.value}
                              onChange={field.onChange}
                            />
                          ) : (
                            <Textarea
                              rows={4}
                              placeholder={t(
                                '[{"name":"支付宝","type":"alipay","icon":"SiAlipay"}]'
                              )}
                              {...field}
                              onChange={(event) =>
                                field.onChange(event.target.value)
                              }
                            />
                          )}
                        </FormControl>
                        <FormDescription>
                          {t(
                            'Each payment method has an explicit provider. Method keys are sent only to that provider.'
                          )}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />

                  <div className='grid gap-6 md:grid-cols-2 md:items-start'>
                    <FormField
                      control={form.control}
                      name='AmountOptions'
                      render={({ field }) => (
                        <FormItem>
                          <div className='mb-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
                            <FormLabel>{t('Top-up amount options')}</FormLabel>
                            <Button
                              type='button'
                              variant='outline'
                              size='sm'
                              onClick={() =>
                                setAmountOptionsVisualMode(
                                  !amountOptionsVisualMode
                                )
                              }
                              className='w-full sm:w-auto'
                            >
                              {amountOptionsVisualMode ? (
                                <>
                                  <Code2 className='mr-2 h-3 w-3' />
                                  {t('JSON Editor')}
                                </>
                              ) : (
                                <>
                                  <Eye className='mr-2 h-3 w-3' />
                                  {t('Visual Editor')}
                                </>
                              )}
                            </Button>
                          </div>
                          <FormControl>
                            {amountOptionsVisualMode ? (
                              <AmountOptionsVisualEditor
                                value={field.value}
                                onChange={field.onChange}
                              />
                            ) : (
                              <Textarea
                                rows={4}
                                placeholder='[10, 20, 50, 100]'
                                {...field}
                                onChange={(event) =>
                                  field.onChange(event.target.value)
                                }
                              />
                            )}
                          </FormControl>
                          <FormDescription>
                            {t('Preset recharge amounts (JSON array)')}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <FormField
                      control={form.control}
                      name='AmountDiscount'
                      render={({ field }) => (
                        <FormItem>
                          <div className='mb-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
                            <FormLabel>{t('Amount discount')}</FormLabel>
                            <Button
                              type='button'
                              variant='outline'
                              size='sm'
                              onClick={() =>
                                setAmountDiscountVisualMode(
                                  !amountDiscountVisualMode
                                )
                              }
                              className='w-full sm:w-auto'
                            >
                              {amountDiscountVisualMode ? (
                                <>
                                  <Code2 className='mr-2 h-3 w-3' />
                                  {t('JSON Editor')}
                                </>
                              ) : (
                                <>
                                  <Eye className='mr-2 h-3 w-3' />
                                  {t('Visual Editor')}
                                </>
                              )}
                            </Button>
                          </div>
                          <FormControl>
                            {amountDiscountVisualMode ? (
                              <AmountDiscountVisualEditor
                                value={field.value}
                                onChange={field.onChange}
                              />
                            ) : (
                              <Textarea
                                rows={4}
                                placeholder='{"100":0.95,"200":0.9}'
                                {...field}
                                onChange={(event) =>
                                  field.onChange(event.target.value)
                                }
                              />
                            )}
                          </FormControl>
                          <FormDescription>
                            {t('Discount map by recharge amount (JSON object)')}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  </div>
                </div>
              </TabsContent>

              <TabsContent value='epay' className={paymentTabContentClassName}>
                <div className='space-y-4'>
                  <div>
                    <h3 className='text-lg font-medium'>{t('Epay Gateway')}</h3>
                    <p className='text-muted-foreground text-sm'>
                      {t('Configuration for Epay payment integration')}
                    </p>
                  </div>

                  <Alert>
                    <ShieldAlert className='h-4 w-4' />
                    <AlertTitle>{t('Epay safety reminder')}</AlertTitle>
                    <AlertDescription>
                      {t(
                        'Epay is a payment protocol, not a specific official website. Verify the provider yourself and do not trust random third-party Epay deployments.'
                      )}
                    </AlertDescription>
                  </Alert>

                  <div className='max-w-xl'>
                    <FormField
                      control={form.control}
                      name='PayAddress'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Epay endpoint')}</FormLabel>
                          <FormControl>
                            <Input
                              placeholder={t('https://pay.example.com')}
                              {...field}
                              onChange={(event) =>
                                field.onChange(event.target.value)
                              }
                            />
                          </FormControl>
                          <FormDescription>
                            {t('Base address provided by your Epay service')}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  </div>

                  <div className='grid gap-6 md:grid-cols-2'>
                    <FormField
                      control={form.control}
                      name='EpayId'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Epay merchant ID')}</FormLabel>
                          <FormControl>
                            <Input
                              placeholder='10001'
                              autoComplete='off'
                              {...field}
                              onChange={(event) =>
                                field.onChange(event.target.value)
                              }
                            />
                          </FormControl>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <FormField
                      control={form.control}
                      name='EpayKey'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Epay secret key')}</FormLabel>
                          <FormControl>
                            <Input
                              type='password'
                              placeholder={t('Enter new key to update')}
                              autoComplete='new-password'
                              {...field}
                              onChange={(event) =>
                                field.onChange(event.target.value)
                              }
                            />
                          </FormControl>
                          <div className='flex flex-wrap items-center justify-between gap-2'>
                            <FormDescription>
                              {t('Leave blank unless rotating the secret')}
                            </FormDescription>
                            <Button
                              type='button'
                              variant='destructive'
                              size='xs'
                              onClick={() => setPendingSecretClear('EpayKey')}
                            >
                              {t('Clear saved credential')}
                            </Button>
                          </div>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  </div>

                  <div className='bg-muted/40 rounded-lg border p-4 text-sm'>
                    <p className='font-medium'>{t('Callback URL')}</p>
                    <code className='text-muted-foreground mt-2 block text-xs break-all'>
                      {epayNotifyUrl}
                    </code>
                  </div>

                  <EmergencyCredentialRevocationAction
                    provider='epay'
                    replacement={epayEmergencyReplacement}
                    previousCredentialActive={epayPreviousCredentialAvailable}
                    disabled={paymentSettingsPending}
                    onRequest={requestEmergencyCredentialRevocation}
                  />
                </div>
              </TabsContent>

              <TabsContent
                value='stripe'
                className={paymentTabContentClassName}
              >
                <div className='space-y-4'>
                  <div>
                    <h3 className='text-lg font-medium'>
                      {t('Stripe Gateway')}
                    </h3>
                    <p className='text-muted-foreground text-sm'>
                      {t('Configuration for Stripe payment integration')}
                    </p>
                  </div>

                  <div className='rounded-md bg-blue-50 p-4 text-sm text-blue-900 dark:bg-blue-950 dark:text-blue-100'>
                    <p className='mb-2 font-medium'>
                      {t('Webhook Configuration:')}
                    </p>
                    <ul className='list-inside list-disc space-y-1'>
                      <li>
                        {t('Webhook URL:')}{' '}
                        <code className='rounded bg-blue-100 px-1 py-0.5 text-xs dark:bg-blue-900'>
                          {stripeWebhookUrl}
                        </code>
                      </li>
                      <li>
                        {t('Required events:')}{' '}
                        <code className='rounded bg-blue-100 px-1 py-0.5 text-xs dark:bg-blue-900'>
                          {t(
                            'checkout.session.completed, checkout.session.async_payment_succeeded, checkout.session.async_payment_failed, checkout.session.expired, charge.refunded, charge.dispute.created, charge.dispute.closed'
                          )}
                        </code>
                      </li>
                      <li>
                        {t('Configure at:')}{' '}
                        <a
                          href='https://dashboard.stripe.com/developers'
                          target='_blank'
                          rel='noreferrer'
                          className='underline hover:no-underline'
                        >
                          {t('Stripe Dashboard')}
                        </a>
                      </li>
                    </ul>
                  </div>

                  <div className='grid gap-2 rounded-lg border p-3'>
                    <Label htmlFor='stripe-credential-environment'>
                      {t('Verified credential environment')}
                    </Label>
                    <Input
                      id='stripe-credential-environment'
                      value={stripeCredentialEnvironmentLabel}
                      placeholder={t('Not verified yet')}
                      readOnly
                      aria-readonly='true'
                    />
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'The server binds this environment to the active Stripe API credential. Test and live mode cannot be switched after Stripe payment history exists.'
                      )}
                    </p>
                  </div>

                  {stripeTestModeNotice?.state === 'blocked' && (
                    <Alert variant='destructive'>
                      <ShieldAlert aria-hidden='true' />
                      <AlertTitle>
                        {t('Stripe test mode is blocked')}
                      </AlertTitle>
                      <AlertDescription>
                        {t(
                          'This saved Stripe credential is in test mode, but PAYMENT_STRIPE_TEST_MODE_ENABLED is disabled. New Stripe payments and quota credits remain blocked. Set PAYMENT_STRIPE_TEST_MODE_ENABLED=true only in an isolated non-production environment, then restart the service.'
                        )}
                      </AlertDescription>
                    </Alert>
                  )}

                  {stripeTestModeNotice?.state === 'enabled' && (
                    <Alert className='border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-50'>
                      <ShieldAlert
                        className='text-amber-600 dark:text-amber-300'
                        aria-hidden='true'
                      />
                      <AlertTitle>
                        {t('Stripe test mode can credit accounts')}
                      </AlertTitle>
                      <AlertDescription className='text-amber-800 dark:text-amber-100'>
                        {stripeTestModeNotice.isolationRequired
                          ? t(
                              'Stripe test mode is enabled. Test cards can complete checkout and credit real users in this database. Use a fully isolated database and isolated user accounts, never production data.'
                            )
                          : t(
                              'Stripe test mode is enabled. Test cards can complete checkout and credit users in this database.'
                            )}
                      </AlertDescription>
                    </Alert>
                  )}

                  <div className='grid gap-6 md:grid-cols-2 xl:grid-cols-3'>
                    <FormField
                      control={form.control}
                      name='StripeApiSecret'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('API secret')}</FormLabel>
                          <FormControl>
                            <Input
                              type='password'
                              placeholder={t('sk_xxx or rk_xxx')}
                              autoComplete='new-password'
                              {...field}
                              onChange={(event) =>
                                field.onChange(event.target.value)
                              }
                            />
                          </FormControl>
                          <div className='flex flex-wrap items-center justify-between gap-2'>
                            <FormDescription>
                              {t(
                                'Stripe API key (leave blank unless updating)'
                              )}
                            </FormDescription>
                            <Button
                              type='button'
                              variant='destructive'
                              size='xs'
                              onClick={() =>
                                setPendingSecretClear('StripeApiSecret')
                              }
                            >
                              {t('Clear saved credential')}
                            </Button>
                          </div>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <FormField
                      control={form.control}
                      name='StripeWebhookSecret'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Webhook secret')}</FormLabel>
                          <FormControl>
                            <Input
                              type='password'
                              placeholder={t('whsec_xxx')}
                              autoComplete='new-password'
                              {...field}
                              onChange={(event) =>
                                field.onChange(event.target.value)
                              }
                            />
                          </FormControl>
                          <div className='flex flex-wrap items-center justify-between gap-2'>
                            <FormDescription>
                              {t(
                                'Webhook signing secret (leave blank unless updating)'
                              )}
                            </FormDescription>
                            <Button
                              type='button'
                              variant='destructive'
                              size='xs'
                              onClick={() =>
                                setPendingSecretClear('StripeWebhookSecret')
                              }
                            >
                              {t('Clear saved credential')}
                            </Button>
                          </div>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <FormField
                      control={form.control}
                      name='StripePriceId'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Price ID')}</FormLabel>
                          <FormControl>
                            <Input
                              placeholder={t('price_xxx')}
                              autoComplete='off'
                              {...field}
                              onChange={(event) =>
                                field.onChange(event.target.value)
                              }
                            />
                          </FormControl>
                          <div className='flex flex-wrap items-center justify-between gap-2'>
                            <FormDescription>
                              {t('Stripe product price ID')}
                            </FormDescription>
                            {field.value ? (
                              <Button
                                type='button'
                                variant='outline'
                                size='xs'
                                onClick={() => field.onChange('')}
                              >
                                {t('Clear')}
                              </Button>
                            ) : null}
                          </div>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <FormField
                      control={form.control}
                      name='StripeAccountId'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Connected Account ID')}</FormLabel>
                          <FormControl>
                            <Input
                              placeholder='acct_...'
                              autoComplete='off'
                              {...field}
                              onChange={(event) =>
                                field.onChange(event.target.value)
                              }
                            />
                          </FormControl>
                          <FormDescription>
                            {t(
                              'Optional Stripe Connect account. Leave blank for the platform account.'
                            )}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <div className='grid content-start gap-2'>
                      <Label htmlFor='stripe-credential-account-id'>
                        {t('Verified credential account')}
                      </Label>
                      <Input
                        id='stripe-credential-account-id'
                        value={resolvedStripeCredentialAccountId}
                        placeholder={t('Not verified yet')}
                        readOnly
                        aria-readonly='true'
                        className='font-mono'
                      />
                      <p className='text-muted-foreground text-xs'>
                        {resolvedStripeCredentialAccountId
                          ? t(
                              'Resolved by the server from the active Stripe API credential. It cannot be edited here.'
                            )
                          : t(
                              'Save the Stripe settings to verify the active API credential and bind its account identity.'
                            )}
                      </p>
                    </div>

                    <FormField
                      control={form.control}
                      name='StripeCurrency'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Stripe currency')}</FormLabel>
                          <FormControl>
                            <Input
                              maxLength={3}
                              autoComplete='off'
                              placeholder='USD'
                              {...field}
                              onChange={(event) =>
                                field.onChange(event.target.value.toUpperCase())
                              }
                            />
                          </FormControl>
                          <FormDescription>
                            {t(
                              'Must match the currency of the configured Stripe Price ID.'
                            )}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  </div>

                  <div className='grid gap-6 md:grid-cols-3'>
                    <FormField
                      control={form.control}
                      name='StripeUnitPrice'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>
                            {t('Unit price (local currency / USD)')}
                          </FormLabel>
                          <FormControl>
                            <Input
                              type='number'
                              step='0.01'
                              min={0.01}
                              {...safeNumberFieldProps(field)}
                            />
                          </FormControl>
                          <FormDescription>
                            {t('e.g., 8 means 8 local currency per USD')}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <FormField
                      control={form.control}
                      name='StripeMinTopUp'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Minimum top-up (USD)')}</FormLabel>
                          <FormControl>
                            <Input
                              type='number'
                              step={1}
                              min={1}
                              {...safeNumberFieldProps(field)}
                            />
                          </FormControl>
                          <FormDescription>
                            {t('Minimum recharge amount in USD')}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <SettingsSwitchItem>
                      <SettingsSwitchContent>
                        <FormLabel>
                          {t('Stripe promotion codes (compatibility setting)')}
                        </FormLabel>
                        <FormDescription>
                          {t(
                            'To keep server quotes consistent with webhook amounts, unified payments do not support Stripe promotion codes.'
                          )}
                        </FormDescription>
                      </SettingsSwitchContent>
                      <Switch
                        checked={false}
                        disabled
                        aria-label={t('Stripe promotion codes are unavailable')}
                      />
                    </SettingsSwitchItem>
                  </div>

                  <EmergencyCredentialRevocationAction
                    provider='stripe'
                    replacement={stripeEmergencyReplacement}
                    previousCredentialActive={stripePreviousCredentialAvailable}
                    disabled={paymentSettingsPending}
                    onRequest={requestEmergencyCredentialRevocation}
                  />
                </div>
              </TabsContent>

              <TabsContent
                value='xorpay'
                className={paymentTabContentClassName}
              >
                <div className='space-y-5'>
                  <div>
                    <h3 className='text-lg font-medium'>XORPay</h3>
                    <p className='text-muted-foreground text-sm'>
                      {t(
                        'Configuration for XORPay one-time payment integration'
                      )}
                    </p>
                  </div>

                  {xorPayReadiness !== undefined && (
                    <Alert variant={xorPayReady ? 'default' : 'destructive'}>
                      <AlertTitle>
                        {xorPayReady
                          ? t('Gateway ready')
                          : t('Gateway not ready')}
                      </AlertTitle>
                      <AlertDescription>
                        {t(
                          'Readiness is verified by the server after the settings are saved.'
                        )}
                      </AlertDescription>
                    </Alert>
                  )}

                  <Alert>
                    <ShieldAlert className='h-4 w-4' />
                    <AlertTitle>{t('XORPay security boundary')}</AlertTitle>
                    <AlertDescription>
                      {t(
                        'The API host is fixed by the server. The app secret is never returned to the browser, and an empty secret keeps the existing value.'
                      )}
                    </AlertDescription>
                  </Alert>

                  <div className='grid gap-6 md:grid-cols-2'>
                    <FormField
                      control={form.control}
                      name='XorPayAid'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('XORPay AID')}</FormLabel>
                          <FormControl>
                            <Input
                              autoComplete='off'
                              placeholder={t('Merchant AID')}
                              {...field}
                            />
                          </FormControl>
                          <FormDescription>
                            {t('Merchant account identifier issued by XORPay')}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <FormField
                      control={form.control}
                      name='XorPayAppSecret'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('XORPay app secret')}</FormLabel>
                          <FormControl>
                            <Input
                              type='password'
                              autoComplete='new-password'
                              placeholder={t('Enter new secret to update')}
                              {...field}
                            />
                          </FormControl>
                          <div className='flex flex-wrap items-center justify-between gap-2'>
                            <FormDescription>
                              {t('Leave blank unless rotating the secret')}
                            </FormDescription>
                            <Button
                              type='button'
                              variant='destructive'
                              size='xs'
                              onClick={() =>
                                setPendingSecretClear('XorPayAppSecret')
                              }
                            >
                              {t('Clear saved credential')}
                            </Button>
                          </div>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  </div>

                  <div className='grid gap-6 md:grid-cols-2'>
                    <FormField
                      control={form.control}
                      name='XorPayUnitPrice'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>
                            {t('Unit price (local currency / USD)')}
                          </FormLabel>
                          <FormControl>
                            <Input
                              type='number'
                              step='0.01'
                              min={0.01}
                              {...safeNumberFieldProps(field)}
                            />
                          </FormControl>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <FormField
                      control={form.control}
                      name='XorPayMinTopUp'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Minimum top-up (USD)')}</FormLabel>
                          <FormControl>
                            <Input
                              type='number'
                              step={1}
                              min={1}
                              {...safeNumberFieldProps(field)}
                            />
                          </FormControl>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  </div>

                  <FormField
                    control={form.control}
                    name='XorPayEnabledMethods'
                    render={() => (
                      <FormItem>
                        <FormLabel>{t('Enabled payment methods')}</FormLabel>
                        <div className='grid gap-3 sm:grid-cols-2'>
                          <label className='flex items-center gap-3 rounded-lg border p-3 text-sm'>
                            <Checkbox
                              checked={xorPayEnabledMethods.includes('native')}
                              onCheckedChange={(checked) =>
                                setXorPayMethodEnabled(
                                  'native',
                                  checked === true
                                )
                              }
                            />
                            <span>{t('XORPay WeChat Pay')}</span>
                          </label>
                          <label className='flex items-center gap-3 rounded-lg border p-3 text-sm'>
                            <Checkbox
                              checked={xorPayEnabledMethods.includes('alipay')}
                              onCheckedChange={(checked) =>
                                setXorPayMethodEnabled(
                                  'alipay',
                                  checked === true
                                )
                              }
                            />
                            <span>{t('XORPay Alipay')}</span>
                          </label>
                        </div>
                        <FormDescription>
                          {t(
                            'Only native QR and Alipay face-to-face payments are enabled in this version.'
                          )}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />

                  <div className='bg-muted/40 rounded-lg border p-4 text-sm'>
                    <p className='font-medium'>{t('Callback URL')}</p>
                    <code className='text-muted-foreground mt-2 block text-xs break-all'>
                      {xorPayNotifyUrl}
                    </code>
                  </div>

                  <EmergencyCredentialRevocationAction
                    provider='xorpay'
                    replacement={xorPayEmergencyReplacement}
                    previousCredentialActive={xorPayPreviousCredentialAvailable}
                    disabled={paymentSettingsPending}
                    onRequest={requestEmergencyCredentialRevocation}
                  />
                </div>
              </TabsContent>

              <TabsContent value='creem' className={paymentTabContentClassName}>
                <div className='space-y-4'>
                  <div>
                    <h3 className='text-lg font-medium'>
                      {t('Creem Gateway')}
                    </h3>
                    <p className='text-muted-foreground text-sm'>
                      {t('Configuration for Creem payment integration')}
                    </p>
                  </div>

                  <div className='rounded-md bg-blue-50 p-4 text-sm text-blue-900 dark:bg-blue-950 dark:text-blue-100'>
                    <p className='mb-2 font-medium'>
                      {t('Webhook Configuration:')}
                    </p>
                    <ul className='list-inside list-disc space-y-1'>
                      <li>
                        {t('Webhook URL:')}{' '}
                        <code className='rounded bg-blue-100 px-1 py-0.5 text-xs dark:bg-blue-900'>
                          {creemWebhookUrl}
                        </code>
                      </li>
                      <li>{t('Configure in your Creem dashboard')}</li>
                    </ul>
                  </div>

                  <div className='grid gap-6 md:grid-cols-2'>
                    <FormField
                      control={form.control}
                      name='CreemApiKey'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('API Key')}</FormLabel>
                          <FormControl>
                            <Input
                              type='password'
                              placeholder={t('Enter Creem API key')}
                              autoComplete='new-password'
                              {...field}
                              onChange={(event) =>
                                field.onChange(event.target.value)
                              }
                            />
                          </FormControl>
                          <FormDescription>
                            {t('Creem API key (leave blank unless updating)')}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />

                    <FormField
                      control={form.control}
                      name='CreemWebhookSecret'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Webhook Secret')}</FormLabel>
                          <FormControl>
                            <Input
                              type='password'
                              placeholder={t('Enter webhook secret')}
                              autoComplete='new-password'
                              {...field}
                              onChange={(event) =>
                                field.onChange(event.target.value)
                              }
                            />
                          </FormControl>
                          <FormDescription>
                            {t(
                              'Webhook signing secret (leave blank unless updating)'
                            )}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  </div>

                  <FormField
                    control={form.control}
                    name='CreemTestMode'
                    render={({ field }) => (
                      <SettingsSwitchItem>
                        <SettingsSwitchContent>
                          <FormLabel>{t('Test Mode')}</FormLabel>
                          <FormDescription>
                            {t('Enable test mode for Creem payments')}
                          </FormDescription>
                        </SettingsSwitchContent>
                        <FormControl>
                          <Switch
                            checked={field.value}
                            onCheckedChange={field.onChange}
                          />
                        </FormControl>
                      </SettingsSwitchItem>
                    )}
                  />

                  <FormField
                    control={form.control}
                    name='CreemProducts'
                    render={({ field }) => (
                      <FormItem>
                        <div className='mb-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
                          <FormLabel>{t('Products')}</FormLabel>
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            onClick={() =>
                              setCreemProductsVisualMode(
                                !creemProductsVisualMode
                              )
                            }
                            className='w-full sm:w-auto'
                          >
                            {creemProductsVisualMode ? (
                              <>
                                <Code2 className='mr-2 h-3 w-3' />
                                {t('JSON Editor')}
                              </>
                            ) : (
                              <>
                                <Eye className='mr-2 h-3 w-3' />
                                {t('Visual Editor')}
                              </>
                            )}
                          </Button>
                        </div>
                        <FormControl>
                          {creemProductsVisualMode ? (
                            <CreemProductsVisualEditor
                              value={field.value}
                              onChange={field.onChange}
                            />
                          ) : (
                            <Textarea
                              rows={4}
                              placeholder='[{"name":"Basic","productId":"prod_xxx","price":10,"quota":500000,"currency":"USD"}]'
                              {...field}
                              onChange={(event) =>
                                field.onChange(event.target.value)
                              }
                            />
                          )}
                        </FormControl>
                        <FormDescription>
                          {t('Configure Creem products. Provide a JSON array.')}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                </div>
              </TabsContent>

              <TabsContent
                value='waffo-pancake'
                className={paymentTabContentClassName}
              >
                <WaffoPancakeSettingsSection
                  defaultValues={waffoPancakeDefaultValues}
                  values={waffoPancakeValues}
                  onValueChange={setWaffoPancakeValue}
                  selectedBinding={waffoPancakeSelection}
                  savedBinding={waffoPancakeSavedBinding}
                  onSelectedBindingChange={setWaffoPancakeSelection}
                />
              </TabsContent>

              <TabsContent value='waffo' className={paymentTabContentClassName}>
                <WaffoSettingsSection
                  values={waffoValues}
                  onValueChange={setWaffoValue}
                  payMethods={waffoPayMethods}
                  onPayMethodsChange={setWaffoPayMethods}
                />
              </TabsContent>
            </Tabs>
          </fieldset>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
