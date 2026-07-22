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
import { useNavigate } from '@tanstack/react-router'
import { useState, useEffect, useCallback, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { SectionPageLayout } from '@/components/layout'
import { useSystemConfig } from '@/hooks/use-system-config'
import { getSelf } from '@/lib/api'

import { AffiliateRewardsCard } from './components/affiliate-rewards-card'
import { BillingHistoryDialog } from './components/dialogs/billing-history-dialog'
import { CreemConfirmDialog } from './components/dialogs/creem-confirm-dialog'
import { PaymentConfirmDialog } from './components/dialogs/payment-confirm-dialog'
import { TransferDialog } from './components/dialogs/transfer-dialog'
import { PaymentResultAlert } from './components/payment-result-alert'
import { RechargeFormCard } from './components/recharge-form-card'
import { SubscriptionPlansCard } from './components/subscription-plans-card'
import { WalletStatsCard } from './components/wallet-stats-card'
import { DEFAULT_DISCOUNT_RATE } from './constants'
import {
  useTopupInfo,
  usePayment,
  useAffiliate,
  useRedemption,
  useCreemPayment,
  useWaffoPayment,
} from './hooks'
import { getDefaultPaymentMethod, getMinTopupAmount } from './lib'
import type {
  UserWalletData,
  PaymentMethod,
  PresetAmount,
  PaymentProduct,
  PaymentRouteOption,
  PaymentOrder,
  PaymentStart,
} from './types'

interface WalletProps {
  initialShowHistory?: boolean
  initialPaymentResult?: string
  initialTradeNo?: string
}

export function Wallet(props: WalletProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [user, setUser] = useState<UserWalletData | null>(null)
  const [userLoading, setUserLoading] = useState(true)
  const [topupAmount, setTopupAmount] = useState(0)
  const [selectedPreset, setSelectedPreset] = useState<number | null>(null)
  const [selectedPaymentMethod, setSelectedPaymentMethod] =
    useState<PaymentMethod>()
  const [paymentLoading, setPaymentLoading] = useState<string | null>(null)
  const [confirmDialogOpen, setConfirmDialogOpen] = useState(false)
  const [transferDialogOpen, setTransferDialogOpen] = useState(false)
  const [billingDialogOpen, setBillingDialogOpen] = useState(false)
  const [redemptionCode, setRedemptionCode] = useState('')
  const [creemDialogOpen, setCreemDialogOpen] = useState(false)
  const [selectedPaymentProduct, setSelectedPaymentProduct] =
    useState<PaymentProduct | null>(null)
  const [showSubscriptionPanel, setShowSubscriptionPanel] = useState(true)
  const [returnTradeNo, setReturnTradeNo] = useState(props.initialTradeNo || '')

  const { currency } = useSystemConfig()
  const {
    topupInfo,
    presetAmounts,
    loading: topupLoading,
    error: topupError,
    refetch: refetchTopupInfo,
  } = useTopupInfo()

  // Calculate effective exchange rate - when display type is USD, use rate of 1
  const effectiveUsdExchangeRate = useMemo(() => {
    return currency?.quotaDisplayType === 'USD'
      ? 1
      : currency?.usdExchangeRate || 1
  }, [currency?.quotaDisplayType, currency?.usdExchangeRate])
  const {
    quote,
    quoteError,
    amount: paymentAmount,
    calculating,
    processing,
    calculatePaymentQuote,
    schedulePaymentQuote,
    processPayment,
  } = usePayment()
  const {
    affiliateLink,
    loading: affiliateLoading,
    transferQuota,
    transferring,
  } = useAffiliate()
  const { redeeming, redeemCode } = useRedemption()
  const { processing: creemProcessing, processCreemPayment } = useCreemPayment()
  const { processWaffoPayment } = useWaffoPayment()

  // Fetch and refresh user data
  const fetchUser = useCallback(async () => {
    try {
      setUserLoading(true)
      const response = await getSelf()
      if (response.success && response.data) {
        setUser(response.data as UserWalletData)
      }
    } catch {
    } finally {
      setUserLoading(false)
    }
  }, [])

  useEffect(() => {
    fetchUser()
  }, [fetchUser])

  useEffect(() => {
    if (props.initialShowHistory) {
      setBillingDialogOpen(true)
      const url = new URL(window.location.href)
      url.searchParams.delete('show_history')
      if (!props.initialTradeNo) {
        url.searchParams.delete('pay')
        url.searchParams.delete('payment_result')
      }
      window.history.replaceState({}, '', `${url.pathname}${url.search}`)
    }
  }, [props.initialShowHistory, props.initialTradeNo])

  useEffect(() => {
    setReturnTradeNo(props.initialTradeNo || '')
  }, [props.initialTradeNo])

  // Initialize topup amount when topup info is loaded
  useEffect(() => {
    if (topupInfo && topupAmount === 0) {
      const minTopup = getMinTopupAmount(topupInfo)
      setTopupAmount(minTopup)

      const defaultMethod = getDefaultPaymentMethod(topupInfo)
      if (defaultMethod) {
        setSelectedPaymentMethod(defaultMethod)
        schedulePaymentQuote(minTopup, defaultMethod)
      }
    }
  }, [topupInfo, topupAmount, schedulePaymentQuote])

  const getCurrentPaymentMethod = useCallback(() => {
    return selectedPaymentMethod || getDefaultPaymentMethod(topupInfo)
  }, [selectedPaymentMethod, topupInfo])

  // Handle preset selection
  const handleSelectPreset = (preset: PresetAmount) => {
    setTopupAmount(preset.value)
    setSelectedPreset(preset.value)
    const method = getCurrentPaymentMethod()
    if (method) schedulePaymentQuote(preset.value, method)
  }

  // Handle topup amount change
  const handleTopupAmountChange = (amount: number) => {
    setTopupAmount(amount)
    setSelectedPreset(null)
    const method = getCurrentPaymentMethod()
    if (method) schedulePaymentQuote(amount, method)
  }

  // Handle payment method selection
  const handlePaymentMethodSelect = async (method: PaymentMethod) => {
    setSelectedPaymentMethod(method)
    setPaymentLoading(method.route_id)

    try {
      // Validate minimum topup
      const minTopup = method.min_topup || getMinTopupAmount(topupInfo)
      if (topupAmount < minTopup) {
        toast.error(t('Minimum topup amount: {{amount}}', { amount: minTopup }))
        return
      }

      const nextQuote = await calculatePaymentQuote(topupAmount, method)
      if (nextQuote) setConfirmDialogOpen(true)
    } finally {
      setPaymentLoading(null)
    }
  }

  // Handle payment confirmation
  const handlePaymentConfirm = async () => {
    if (!selectedPaymentMethod || !quote) return

    const paymentStart = await processPayment(quote, selectedPaymentMethod)
    if (paymentStart) {
      setConfirmDialogOpen(false)
      if (paymentStart.flow === 'qr' || paymentStart.flow === 'pending') {
        if (!paymentStart.trade_no) {
          toast.error(t('Payment is temporarily unavailable. Try again.'))
          return
        }
        await navigate({
          to: '/payment/$tradeNo',
          params: { tradeNo: paymentStart.trade_no },
        })
      }
    }
  }

  const handlePaymentSettled = useCallback(
    async (order: PaymentOrder) => {
      if (order.status_code === 'succeeded') await fetchUser()
    },
    [fetchUser]
  )

  const dismissPaymentResult = useCallback(() => {
    setReturnTradeNo('')
    const url = new URL(window.location.href)
    url.searchParams.delete('payment_result')
    url.searchParams.delete('trade_no')
    window.history.replaceState({}, '', `${url.pathname}${url.search}`)
  }, [])

  // Handle redemption
  const handleRedeem = async () => {
    if (!redemptionCode) return

    const success = await redeemCode(redemptionCode)
    if (success) {
      setRedemptionCode('')
      await fetchUser()
    }
  }

  const handleTransfer = async (amount: number) => {
    const success = await transferQuota(amount)
    if (success) {
      await fetchUser()
    }
    return success
  }

  // Handle Creem product selection
  const handlePaymentProductSelect = (product: PaymentProduct) => {
    setSelectedPaymentProduct(product)
    setCreemDialogOpen(true)
  }

  const openLocalPaymentOrder = async (paymentStart: PaymentStart | null) => {
    if (!paymentStart?.trade_no) {
      if (paymentStart) {
        toast.error(t('Payment is temporarily unavailable. Try again.'))
      }
      return false
    }
    await navigate({
      to: '/payment/$tradeNo',
      params: { tradeNo: paymentStart.trade_no },
    })
    return true
  }

  // Handle Creem payment confirmation
  const handleCreemConfirm = async () => {
    if (!selectedPaymentProduct) return

    const paymentStart = await processCreemPayment(
      selectedPaymentProduct.route_id,
      selectedPaymentProduct.product_id
    )
    if (await openLocalPaymentOrder(paymentStart)) {
      setCreemDialogOpen(false)
      setSelectedPaymentProduct(null)
    }
  }

  const handlePaymentRouteOptionSelect = async (option: PaymentRouteOption) => {
    const loadingKey = option.option_id
    setPaymentLoading(loadingKey)

    try {
      const paymentStart = await processWaffoPayment(
        option.route_id,
        topupAmount,
        option.option_id
      )
      await openLocalPaymentOrder(paymentStart)
    } finally {
      setPaymentLoading(null)
    }
  }

  // Get discount rate for current topup amount
  const getDiscountRate = useCallback(() => {
    return topupInfo?.discount?.[topupAmount] || DEFAULT_DISCOUNT_RATE
  }, [topupInfo, topupAmount])

  const handleSubscriptionAvailabilityChange = useCallback(
    (available: boolean) => {
      setShowSubscriptionPanel(available)
    },
    []
  )

  return (
    <>
      <SectionPageLayout>
        <SectionPageLayout.Title>{t('Wallet')}</SectionPageLayout.Title>
        <SectionPageLayout.Content>
          <div className='mx-auto flex w-full max-w-7xl flex-col gap-4 sm:gap-5'>
            {returnTradeNo && (
              <PaymentResultAlert
                tradeNo={returnTradeNo}
                resultHint={props.initialPaymentResult}
                onDismiss={dismissPaymentResult}
                onSettled={handlePaymentSettled}
                onOpenHistory={() => setBillingDialogOpen(true)}
              />
            )}
            <WalletStatsCard user={user} loading={userLoading} />

            <div
              className={
                showSubscriptionPanel
                  ? 'grid gap-4 xl:grid-cols-[minmax(0,1.05fr)_minmax(360px,0.95fr)] xl:items-start'
                  : 'grid gap-4'
              }
            >
              <div id='wallet-add-funds' className='scroll-mt-4'>
                <RechargeFormCard
                  topupInfo={topupInfo}
                  presetAmounts={presetAmounts}
                  selectedPreset={selectedPreset}
                  onSelectPreset={handleSelectPreset}
                  topupAmount={topupAmount}
                  onTopupAmountChange={handleTopupAmountChange}
                  paymentAmount={paymentAmount}
                  paymentCurrency={quote?.currency}
                  paymentFormatProvider={
                    selectedPaymentMethod?.public_method === 'card'
                      ? 'card'
                      : undefined
                  }
                  calculating={calculating}
                  quoteError={quoteError}
                  onPaymentMethodSelect={handlePaymentMethodSelect}
                  paymentLoading={paymentLoading}
                  redemptionCode={redemptionCode}
                  onRedemptionCodeChange={setRedemptionCode}
                  onRedeem={handleRedeem}
                  redeeming={redeeming}
                  topupLink={topupInfo?.topup_link}
                  loading={topupLoading}
                  loadError={topupError}
                  onRetryLoad={() => void refetchTopupInfo()}
                  usdExchangeRate={effectiveUsdExchangeRate}
                  onOpenBilling={() => setBillingDialogOpen(true)}
                  paymentProducts={topupInfo?.payment_products}
                  onPaymentProductSelect={handlePaymentProductSelect}
                  paymentRouteOptions={topupInfo?.payment_route_options}
                  onPaymentRouteOptionSelect={handlePaymentRouteOptionSelect}
                />
              </div>

              <SubscriptionPlansCard
                topupInfo={topupInfo}
                onAvailabilityChange={handleSubscriptionAvailabilityChange}
                userQuota={user?.quota}
                onPurchaseSuccess={fetchUser}
              />
            </div>

            <AffiliateRewardsCard
              user={user}
              affiliateLink={affiliateLink}
              onTransfer={() => setTransferDialogOpen(true)}
              complianceConfirmed={
                topupInfo?.payment_compliance_confirmed !== false
              }
              loading={affiliateLoading || userLoading}
            />
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <PaymentConfirmDialog
        open={confirmDialogOpen}
        onOpenChange={setConfirmDialogOpen}
        onConfirm={handlePaymentConfirm}
        topupAmount={topupAmount}
        paymentAmount={paymentAmount}
        quote={quote}
        quoteError={quoteError}
        paymentMethod={selectedPaymentMethod}
        calculating={calculating}
        processing={processing}
        discountRate={getDiscountRate()}
        usdExchangeRate={effectiveUsdExchangeRate}
      />

      <TransferDialog
        open={transferDialogOpen}
        onOpenChange={setTransferDialogOpen}
        onConfirm={handleTransfer}
        availableQuota={user?.aff_quota ?? 0}
        transferring={transferring}
      />

      <BillingHistoryDialog
        open={billingDialogOpen}
        onOpenChange={setBillingDialogOpen}
      />

      <CreemConfirmDialog
        open={creemDialogOpen}
        onOpenChange={setCreemDialogOpen}
        onConfirm={handleCreemConfirm}
        product={selectedPaymentProduct}
        processing={creemProcessing}
      />
    </>
  )
}
