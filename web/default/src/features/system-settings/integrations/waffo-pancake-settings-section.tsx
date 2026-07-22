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
import * as React from 'react'
import type { SetStateAction } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import type { StartVerificationOptions } from '@/features/auth/secure-verification'

import {
  createPaymentAdminError,
  getPaymentAdminErrorMessage,
} from '../payment-admin-errors'
import { removeTrailingSlash } from './utils'
import {
  type CatalogStore,
  type PairFailureParams,
  type PairResult,
  createWaffoPancakePair,
  listWaffoPancakeCatalog,
} from './waffo-pancake-api'
import { getWaffoPancakePricingError } from './waffo-pancake-pricing'

export type WaffoPancakeSettingsValues = {
  WaffoPancakeMerchantID: string
  WaffoPancakePrivateKey: string
  WaffoPancakeReturnURL: string
  WaffoPancakeUnitPrice: number
  WaffoPancakeMinTopUp: number
  WaffoPancakeTestMode: boolean
}

export interface WaffoPancakeBinding {
  storeID: string
  productID: string
}

interface Props {
  defaultValues: WaffoPancakeSettingsValues
  values: WaffoPancakeSettingsValues
  onValueChange: <K extends keyof WaffoPancakeSettingsValues>(
    key: K,
    value: WaffoPancakeSettingsValues[K]
  ) => void
  selectedBinding: WaffoPancakeBinding
  savedBinding: WaffoPancakeBinding
  onSelectedBindingChange: (value: SetStateAction<WaffoPancakeBinding>) => void
  withVerification: <T>(
    apiCall: () => Promise<T>,
    config?: StartVerificationOptions
  ) => Promise<T | null>
  emergencyControl?: React.ReactNode
}

const PANCAKE_DASHBOARD_URL = 'https://pancake.waffo.ai/merchant/dashboard'
const DEFAULT_NEW_STORE_NAME = 'new-api-store'
const DEFAULT_NEW_PRODUCT_NAME = 'new-api-charge-product'
const DEFAULT_NEW_PAIR_NAME = `${DEFAULT_NEW_STORE_NAME} + ${DEFAULT_NEW_PRODUCT_NAME}`

export function WaffoPancakeSettingsSection({
  defaultValues,
  values,
  onValueChange,
  selectedBinding,
  savedBinding,
  onSelectedBindingChange,
  withVerification,
  emergencyControl,
}: Props) {
  const { t } = useTranslation()

  const [phase, setPhase] = React.useState<'idle' | 'verifying'>('idle')
  const [catalog, setCatalog] = React.useState<CatalogStore[]>([])
  const [creatingPair, setCreatingPair] = React.useState(false)
  const chosenStoreID = selectedBinding.storeID
  const chosenProductID = selectedBinding.productID
  const storeID = savedBinding.storeID
  const productID = savedBinding.productID
  const returnURL = values.WaffoPancakeReturnURL
  const pricingError = getWaffoPancakePricingError(
    values.WaffoPancakeUnitPrice,
    values.WaffoPancakeMinTopUp
  )

  const initialRef = React.useRef(defaultValues)
  const defaultsSignature = React.useMemo(
    () => JSON.stringify(defaultValues),
    [defaultValues]
  )

  const fetchSerialRef = React.useRef(0)

  // Mount-only — never re-sync from props after the first render. The
  // backend strips PrivateKey from GET /api/option/, so a re-sync would
  // wipe whatever the operator just typed.
  const didMountRef = React.useRef(false)
  React.useEffect(() => {
    const parsed = JSON.parse(defaultsSignature) as WaffoPancakeSettingsValues
    initialRef.current = parsed
    if (didMountRef.current) return
    didMountRef.current = true
  }, [defaultsSignature])

  const productsForChosenStore = React.useMemo(() => {
    if (!chosenStoreID) return []
    return catalog.find((s) => s.id === chosenStoreID)?.onetimeProducts ?? []
  }, [catalog, chosenStoreID])

  // Raw-ID fallback items render the trigger before the catalog loads or
  // when the saved entity has been deleted upstream.
  const storeSelectItems = React.useMemo(() => {
    const items = catalog.map((s) => ({
      value: s.id,
      label: `${s.name} (${s.id})`,
    }))
    if (chosenStoreID && !catalog.some((s) => s.id === chosenStoreID)) {
      items.push({ value: chosenStoreID, label: chosenStoreID })
    }
    return items
  }, [catalog, chosenStoreID])
  const productSelectItems = React.useMemo(() => {
    const items = productsForChosenStore.map((p) => ({
      value: p.id,
      label: `${p.name} (${p.id})`,
    }))
    if (
      chosenProductID &&
      !productsForChosenStore.some((p) => p.id === chosenProductID)
    ) {
      items.push({ value: chosenProductID, label: chosenProductID })
    }
    return items
  }, [productsForChosenStore, chosenProductID])

  // Refreshes the catalog after step-up verification. `preselect` overrides
  // the post-load anchor selection; omitting it defaults to the saved binding
  // or the first store with an available product.
  const fetchCatalog = React.useCallback(
    async (
      merchantID: string,
      privateKey: string,
      preselect?: { storeID?: string; productID?: string }
    ) => {
      const serial = ++fetchSerialRef.current
      setPhase('verifying')
      try {
        const body = await listWaffoPancakeCatalog(merchantID, privateKey)
        if (serial !== fetchSerialRef.current) return false
        if (!body.success || !body.data) {
          const error = createPaymentAdminError(
            body,
            t('Credentials verification failed')
          )
          throw new Error(
            getPaymentAdminErrorMessage(
              error,
              t,
              t('Credentials verification failed')
            )
          )
        }
        const stores = body.data.stores ?? []

        setCatalog(stores)
        if (preselect) {
          onSelectedBindingChange({
            storeID: preselect.storeID ?? '',
            productID: preselect.productID ?? '',
          })
        } else {
          const boundStore = stores.find((store) =>
            store.onetimeProducts.some((product) => product.id === productID)
          )
          if (boundStore && productID) {
            onSelectedBindingChange({
              storeID: boundStore.id,
              productID,
            })
          } else {
            const storeWithProducts = stores.find(
              (store) => store.onetimeProducts.length > 0
            )
            if (storeWithProducts) {
              onSelectedBindingChange({
                storeID: storeWithProducts.id,
                productID: storeWithProducts.onetimeProducts[0].id,
              })
            } else {
              onSelectedBindingChange({ storeID: '', productID: '' })
            }
          }
        }
        return true
      } catch (error) {
        if (serial !== fetchSerialRef.current) return false
        throw new Error(
          getPaymentAdminErrorMessage(
            error,
            t,
            t('Credentials verification failed')
          )
        )
      } finally {
        if (serial === fetchSerialRef.current) setPhase('idle')
      }
    },
    [onSelectedBindingChange, productID, t]
  )

  // Returns typed creds when the operator edited either field; otherwise
  // blanks so the backend falls back to persisted creds. Without this,
  // returning admins (saved merchant ID but empty key field) would send
  // a mixed-state body that the backend rejects.
  const readCreds = () => {
    const formMerchant = (values.WaffoPancakeMerchantID || '').trim()
    const formKey = (values.WaffoPancakePrivateKey || '').trim()
    const saved = (defaultValues.WaffoPancakeMerchantID || '').trim()
    const edited = formMerchant !== saved || formKey.length > 0
    if (!edited) return { merchantID: '', privateKey: '' }
    return { merchantID: formMerchant, privateKey: formKey }
  }

  const verifyAndFetchCatalog = async () => {
    if (!credsReady) {
      toast.error(
        t('Fill in both Merchant ID and API Private Key before creating.')
      )
      return
    }
    const { merchantID, privateKey } = readCreds()
    try {
      await withVerification(() => fetchCatalog(merchantID, privateKey), {
        preferredMethod: 'passkey',
        title: t('Verify payment settings update'),
        description: t(
          'Confirm your identity before changing payment credentials or gateway configuration.'
        ),
      })
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t('Credentials verification failed')
      )
    }
  }

  // The minted product's SuccessURL is pinned to the current Return URL
  // field, so we prompt before creating when that field is empty.
  const handleCreatePair = async () => {
    if (!credsReady) {
      toast.error(
        t('Fill in both Merchant ID and API Private Key before creating.')
      )
      return
    }
    const { merchantID, privateKey } = readCreds()
    const trimmedReturn = removeTrailingSlash(returnURL.trim())
    if (!trimmedReturn) {
      if (
        !window.confirm(
          t(
            'Payment return URL is empty. Create the product without a SuccessURL redirect?'
          )
        )
      ) {
        return
      }
    }
    try {
      await withVerification(
        async () => {
          setCreatingPair(true)
          try {
            const body = await createWaffoPancakePair({
              merchantID,
              privateKey,
              returnURL: trimmedReturn,
            })
            if (!body.success || !body.data) {
              const failure = body.params as PairFailureParams | undefined
              if (failure?.orphan_store && failure.store_id) {
                await fetchCatalog(merchantID, privateKey, {
                  storeID: failure.store_id,
                  productID: '',
                })
              }
              const error = createPaymentAdminError(body, t('Creation failed'))
              throw new Error(
                getPaymentAdminErrorMessage(error, t, t('Creation failed'))
              )
            }
            const created = body.data as PairResult
            await fetchCatalog(merchantID, privateKey, {
              storeID: created.store_id,
              productID: created.product_id,
            })
            toast.success(
              `${t('Store + product created')}: ${created.store_id} / ${created.product_id}`
            )
            return body
          } finally {
            setCreatingPair(false)
          }
        },
        {
          preferredMethod: 'passkey',
          title: t('Verify payment settings update'),
          description: t(
            'Confirm your identity before changing payment credentials or gateway configuration.'
          ),
        }
      )
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t('Creation failed'))
    }
  }

  const verifying = phase === 'verifying'

  // "Not edited" = MerchantID unchanged AND PrivateKey field blank, in
  // which case the backend falls back to persisted creds. Otherwise we
  // require both fields filled (mixed states would fail signature check).
  const savedMerchantID = (defaultValues.WaffoPancakeMerchantID || '').trim()
  const watchedMerchantID = values.WaffoPancakeMerchantID || ''
  const watchedPrivateKey = values.WaffoPancakePrivateKey || ''
  const formMerchantID = watchedMerchantID.trim()
  const formPrivateKey = watchedPrivateKey.trim()
  const credsEdited =
    formMerchantID !== savedMerchantID || formPrivateKey.length > 0
  const hasSavedCreds = savedMerchantID.length > 0
  const credsReady = credsEdited
    ? formMerchantID.length > 0 && formPrivateKey.length > 0
    : hasSavedCreds
  const hasCatalog = catalog.length > 0

  let bindStatusMessage: string
  if (!credsReady) {
    bindStatusMessage = t('Fill in the credentials above to begin.')
  } else if (verifying) {
    bindStatusMessage = t(
      'Verifying credentials and pulling stores from your Pancake account...'
    )
  } else if (hasCatalog) {
    bindStatusMessage = t(
      'Create a new pair below or choose an existing one. Save when the selection is ready.'
    )
  } else {
    bindStatusMessage = t(
      'Verify credentials to load stores, or create a new store and product.'
    )
  }

  return (
    <div className='space-y-4 pt-4'>
      <div>
        <h3 className='text-lg font-medium'>{t('Waffo Pancake MoR')}</h3>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Start collecting payments globally without registering a company. Built for indie developers, OPC sole proprietorships, and startups. Waffo Pancake acts as your Merchant of Record, taking on the compliance burden of global payment collection — consumption tax, invoicing, subscription management, refunds, and chargebacks. Solo developers can launch fast and stay focused on product instead of compliance. Onboard in minutes — one prompt to a full integration.'
          )}
        </p>
      </div>
      <div className='grid min-w-0 gap-x-5 gap-y-4 lg:grid-cols-2'>
        {/* Blue box — webhook configuration only. */}
        <div className='rounded-md bg-blue-50 p-4 text-sm text-blue-900 lg:col-span-2 dark:bg-blue-950 dark:text-blue-100'>
          <p className='mb-2 font-medium'>{t('Webhook Configuration:')}</p>
          <ul className='list-inside list-disc space-y-1'>
            <li>
              {t('Webhook URL (Test):')}{' '}
              <code className='rounded bg-blue-100 px-1 py-0.5 text-xs dark:bg-blue-900'>
                {'<ServerAddress>/api/waffo-pancake/webhook/test'}
              </code>
            </li>
            <li>
              {t('Webhook URL (Production):')}{' '}
              <code className='rounded bg-blue-100 px-1 py-0.5 text-xs dark:bg-blue-900'>
                {'<ServerAddress>/api/waffo-pancake/webhook/prod'}
              </code>
            </li>
            <li>
              {t(
                'Register each URL into the matching Test Mode / Production Mode webhook slot in the Pancake dashboard. Separate endpoints prevent test traffic from accidentally crediting production accounts.'
              )}
            </li>
            <li>
              {t('Configure at:')}{' '}
              <a
                href={PANCAKE_DASHBOARD_URL}
                target='_blank'
                rel='noreferrer'
                className='underline hover:no-underline'
              >
                {t('Waffo Pancake Dashboard')}
              </a>
            </li>
          </ul>
        </div>

        <div className='grid gap-1.5'>
          <Label>{t('Merchant ID')}</Label>
          <Input
            placeholder='MER_xxx'
            autoComplete='off'
            value={values.WaffoPancakeMerchantID}
            onChange={(event) =>
              onValueChange('WaffoPancakeMerchantID', event.target.value)
            }
          />
        </div>

        <div className='grid gap-1.5'>
          <Label>{t('API Private Key')}</Label>
          <Textarea
            rows={4}
            placeholder={t('Leave blank to keep the existing key')}
            autoComplete='new-password'
            value={values.WaffoPancakePrivateKey}
            onChange={(event) =>
              onValueChange('WaffoPancakePrivateKey', event.target.value)
            }
            className='font-mono text-xs'
          />
          <p className='text-muted-foreground text-xs'>
            {t('Leave this field blank to keep the saved signing private key.')}
          </p>
        </div>

        <div className='grid gap-1.5'>
          <Label>{t('Payment environment')}</Label>
          <Select
            items={[
              { value: 'production', label: t('Production') },
              { value: 'test', label: t('Test') },
            ]}
            value={values.WaffoPancakeTestMode ? 'test' : 'production'}
            onValueChange={(value) =>
              onValueChange('WaffoPancakeTestMode', value === 'test')
            }
          >
            <SelectTrigger className='w-full'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='production'>{t('Production')}</SelectItem>
              <SelectItem value='test'>{t('Test')}</SelectItem>
            </SelectContent>
          </Select>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Choose the environment that matches the Waffo Pancake merchant account, signing private key, and webhook URL. A mismatch is treated as a payment anomaly and sent to manual review.'
            )}
          </p>
        </div>

        <div className='grid gap-1.5'>
          <Label htmlFor='waffo-pancake-unit-price'>
            {t('USD base price multiplier')}
          </Label>
          <Input
            id='waffo-pancake-unit-price'
            type='number'
            min='0.000001'
            max='1000000'
            step='0.01'
            inputMode='decimal'
            value={
              Number.isFinite(values.WaffoPancakeUnitPrice)
                ? values.WaffoPancakeUnitPrice
                : ''
            }
            onChange={(event) =>
              onValueChange(
                'WaffoPancakeUnitPrice',
                event.target.value === ''
                  ? Number.NaN
                  : Number(event.target.value)
              )
            }
            aria-invalid={pricingError === 'unit_price'}
            aria-describedby='waffo-pancake-unit-price-help'
          />
          <p
            id='waffo-pancake-unit-price-help'
            className={
              pricingError === 'unit_price'
                ? 'text-destructive text-xs'
                : 'text-muted-foreground text-xs'
            }
          >
            {pricingError === 'unit_price'
              ? t(
                  'Enter a multiplier greater than 0 and no more than 1,000,000.'
                )
              : t(
                  'Positive multiplier applied to the USD base price to calculate the settlement amount for wallet top-ups and fixed-term purchases.'
                )}
          </p>
        </div>

        <div className='grid gap-1.5'>
          <Label htmlFor='waffo-pancake-min-top-up'>
            {t('Minimum wallet top-up (USD)')}
          </Label>
          <Input
            id='waffo-pancake-min-top-up'
            type='number'
            min='1'
            max='10000'
            step='1'
            inputMode='numeric'
            value={
              Number.isFinite(values.WaffoPancakeMinTopUp)
                ? values.WaffoPancakeMinTopUp
                : ''
            }
            onChange={(event) =>
              onValueChange(
                'WaffoPancakeMinTopUp',
                event.target.value === ''
                  ? Number.NaN
                  : Number(event.target.value)
              )
            }
            aria-invalid={pricingError === 'min_top_up'}
            aria-describedby='waffo-pancake-min-top-up-help'
          />
          <p
            id='waffo-pancake-min-top-up-help'
            className={
              pricingError === 'min_top_up'
                ? 'text-destructive text-xs'
                : 'text-muted-foreground text-xs'
            }
          >
            {pricingError === 'min_top_up'
              ? t('Enter a whole-dollar minimum between 1 and 10,000.')
              : t(
                  'Smallest wallet top-up amount a user can enter, measured in USD. It does not set a minimum for fixed-term purchases.'
                )}
          </p>
        </div>

        {/*
          Binding section — split into two visually distinct paths:
          (A) "Use existing" pair from the loaded catalog — only rendered when
              the merchant actually has stores, so first-time setup isn't
              cluttered by dead dropdowns.
          (B) "Create a fresh pair" — always available, paired with the
              return URL field that's only meaningful here.
          The two paths are split by an "or" divider so the operator never has
          to wonder which field belongs to which intent.
        */}
        <div className='space-y-4 pt-2 lg:col-span-2'>
          <div>
            <h4 className='font-medium'>
              {t('Bind a Pancake store + product')}
            </h4>
            <p className='text-muted-foreground text-xs'>{bindStatusMessage}</p>
            <Button
              type='button'
              variant='outline'
              className='mt-3 min-h-11'
              onClick={() => void verifyAndFetchCatalog()}
              disabled={verifying || creatingPair || !credsReady}
              aria-busy={verifying}
            >
              {verifying
                ? t('Verifying credentials...')
                : t('Verify credentials and load catalog')}
            </Button>
          </div>

          {/*
              Operator-facing explainer: why only ONE store + product needs
              to be bound at the gateway level, and what each piece is used
              for. Subscriptions reuse the same Store but get their own
              per-plan product, configured in the Subscriptions admin.
            */}
          <div className='rounded-md border border-blue-200 bg-blue-50 p-3 text-xs text-blue-900 dark:border-blue-900/60 dark:bg-blue-950/40 dark:text-blue-100'>
            <p className='mb-1 font-medium'>
              {t('Why only one store + product?')}
            </p>
            <ul className='list-inside list-disc space-y-1'>
              <li>
                {t(
                  'The bound Store is the parent container for every Pancake product new-api creates from this admin — both the wallet top-up product and any subscription-plan products. One store is enough; pin a different one only if you genuinely run separate Pancake catalogs.'
                )}
              </li>
              <li>
                {t(
                  'The bound Product powers wallet top-ups: when a user enters any amount, new-api runs the checkout against this single Pancake product and overrides the price per session — no need to pre-create $1 / $5 / $10 SKUs.'
                )}
              </li>
              <li>
                {t(
                  'Subscription plans do NOT use the bound Product — each plan has its own dedicated Pancake product, set in the Subscriptions admin (or auto-minted via the "+ Create" button there).'
                )}
              </li>
            </ul>
          </div>

          {/* Create section — first, since creating auto-fills the pick-existing dropdowns below. */}
          <div className='space-y-1.5'>
            <Label>{t('Payment return URL')}</Label>
            <div className='flex gap-2'>
              <Input
                placeholder='https://example.com/console/topup'
                value={returnURL}
                onChange={(event) =>
                  onValueChange('WaffoPancakeReturnURL', event.target.value)
                }
                className='flex-1'
              />
              <Button
                type='button'
                variant='outline'
                onClick={handleCreatePair}
                disabled={creatingPair || verifying || !credsReady}
                className='shrink-0'
              >
                {creatingPair
                  ? t('Creating...')
                  : `+ ${t('Create')} ${DEFAULT_NEW_PAIR_NAME}`}
              </Button>
            </div>
            <p className='text-muted-foreground text-xs'>
              {t(
                "Used as SuccessURL on the new product. You'll be prompted to confirm if left blank."
              )}
            </p>
          </div>

          {hasCatalog ? (
            <>
              <div className='relative flex items-center py-1'>
                <div className='flex-1 border-t' />
                <span className='text-muted-foreground px-3 text-[10px] font-medium tracking-[0.2em] uppercase'>
                  {t('or pick existing')}
                </span>
                <div className='flex-1 border-t' />
              </div>

              <div className='grid grid-cols-2 gap-3'>
                <div className='grid gap-1.5'>
                  <Label>{t('Store')}</Label>
                  <Select
                    items={storeSelectItems}
                    value={chosenStoreID}
                    onValueChange={(value) => {
                      // Base UI Select can deliver null on deselect.
                      onSelectedBindingChange({
                        storeID: value ?? '',
                        productID: '',
                      })
                    }}
                  >
                    <SelectTrigger className='w-full'>
                      <SelectValue placeholder={t('Select a store')} />
                    </SelectTrigger>
                    <SelectContent>
                      {storeSelectItems.map((item) => (
                        <SelectItem key={item.value} value={item.value}>
                          {item.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>

                <div className='grid gap-1.5'>
                  <Label>{t('Product')}</Label>
                  <Select
                    items={productSelectItems}
                    value={chosenProductID}
                    onValueChange={(value) =>
                      onSelectedBindingChange((previous) => ({
                        ...previous,
                        productID: value ?? '',
                      }))
                    }
                    disabled={!chosenStoreID || productSelectItems.length === 0}
                  >
                    <SelectTrigger className='w-full'>
                      <SelectValue placeholder={t('Select a product')} />
                    </SelectTrigger>
                    <SelectContent>
                      {productSelectItems.map((item) => (
                        <SelectItem key={item.value} value={item.value}>
                          {item.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
            </>
          ) : null}

          <div className='flex items-center gap-3'>
            {storeID || productID ? (
              <div className='text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 text-xs'>
                {storeID ? (
                  <span>
                    {t('Bound store:')}{' '}
                    <code className='bg-muted rounded px-1 py-0.5'>
                      {storeID}
                    </code>
                  </span>
                ) : null}
                {productID ? (
                  <span>
                    {t('Bound product:')}{' '}
                    <code className='bg-muted rounded px-1 py-0.5'>
                      {productID}
                    </code>
                  </span>
                ) : null}
              </div>
            ) : null}
          </div>
        </div>
      </div>
      {emergencyControl}
    </div>
  )
}
