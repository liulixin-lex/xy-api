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
import { Crown, RefreshCw, Sparkles, Check } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  StatusBadge,
  dotColorMap,
  textColorMap,
} from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Skeleton } from '@/components/ui/skeleton'
import { TitledCard } from '@/components/ui/titled-card'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import {
  getPublicPlans,
  getSelfSubscriptionFull,
  updateBillingPreference,
} from '@/features/subscriptions/api'
import { SubscriptionPurchaseDialog } from '@/features/subscriptions/components/dialogs/subscription-purchase-dialog'
import { formatDuration, formatResetPeriod } from '@/features/subscriptions/lib'
import type {
  PublicPlanRecord,
  PublicUserSubscriptionRecord,
} from '@/features/subscriptions/types'
import { formatQuota } from '@/lib/format'
import { cn } from '@/lib/utils'

import { formatPaymentDecimalAmount } from '../lib/billing'
import { filterPaymentMethodsForBrowser } from '../lib/payment'
import type { TopupInfo } from '../types'

interface SubscriptionPlansCardProps {
  topupInfo: TopupInfo | null
  onAvailabilityChange?: (available: boolean) => void
  userQuota?: number
  onPurchaseSuccess?: () => void | Promise<void>
}

function getBillingPreferenceLabel(
  preference: string,
  t: (key: string) => string
): string {
  switch (preference) {
    case 'subscription_first':
      return t('Access First')
    case 'wallet_first':
      return t('Wallet First')
    case 'subscription_only':
      return t('Access Only')
    case 'wallet_only':
      return t('Wallet Only')
    default:
      return t('Automatic')
  }
}

function getBillingPreferenceDisplayLabel(
  preference: string,
  disabled: boolean,
  t: (key: string, values?: Record<string, string | number>) => string
): string {
  const label = getBillingPreferenceLabel(preference, t)
  if (!disabled) return label
  return t('{{label}} ({{status}})', {
    label,
    status: t('No Active'),
  })
}

export function SubscriptionPlansCard({
  topupInfo,
  onAvailabilityChange,
  userQuota,
  onPurchaseSuccess,
}: SubscriptionPlansCardProps) {
  const { t } = useTranslation()

  const [plans, setPlans] = useState<PublicPlanRecord[]>([])
  const [activeSubscriptions, setActiveSubscriptions] = useState<
    PublicUserSubscriptionRecord[]
  >([])
  const [allSubscriptions, setAllSubscriptions] = useState<
    PublicUserSubscriptionRecord[]
  >([])
  const [billingPreference, setBillingPreference] =
    useState('subscription_first')
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [plansError, setPlansError] = useState<string | null>(null)
  const [subscriptionError, setSubscriptionError] = useState<string | null>(
    null
  )
  const [preferenceSaving, setPreferenceSaving] = useState(false)
  const preferenceSavingRef = useRef(false)

  const [purchaseOpen, setPurchaseOpen] = useState(false)
  const [selectedPlan, setSelectedPlan] = useState<PublicPlanRecord | null>(
    null
  )

  const subscriptionPaymentRoutes = useMemo(
    () =>
      filterPaymentMethodsForBrowser(
        topupInfo?.subscription_payment_routes ||
          topupInfo?.payment_routes ||
          []
      ),
    [topupInfo?.subscription_payment_routes, topupInfo?.payment_routes]
  )

  const fetchPlans = useCallback(async () => {
    try {
      const res = await getPublicPlans()
      if (res.success) {
        setPlans(res.data || [])
        setPlansError(null)
        return true
      }
      setPlansError(t('Failed to load access plans'))
    } catch {
      setPlansError(t('Failed to load access plans'))
    }
    return false
  }, [t])

  const fetchSelfSubscription = useCallback(async () => {
    try {
      const res = await getSelfSubscriptionFull()
      if (res.success && res.data) {
        setBillingPreference(
          res.data.billing_preference || 'subscription_first'
        )
        setActiveSubscriptions(res.data.subscriptions || [])
        setAllSubscriptions(res.data.all_subscriptions || [])
        setSubscriptionError(null)
        return true
      }
      setSubscriptionError(t('Failed to load access status'))
    } catch {
      setSubscriptionError(t('Failed to load access status'))
    }
    return false
  }, [t])

  useEffect(() => {
    const init = async () => {
      setLoading(true)
      await Promise.all([fetchPlans(), fetchSelfSubscription()])
      setLoading(false)
    }
    init()
  }, [fetchPlans, fetchSelfSubscription])

  const handleRefresh = async () => {
    setRefreshing(true)
    try {
      await Promise.all([fetchPlans(), fetchSelfSubscription()])
    } finally {
      setRefreshing(false)
    }
  }

  const handlePurchaseSuccess = useCallback(async () => {
    await Promise.all([fetchSelfSubscription(), onPurchaseSuccess?.()])
  }, [fetchSelfSubscription, onPurchaseSuccess])

  const handlePreferenceChange = async (pref: string) => {
    if (preferenceSavingRef.current || pref === billingPreference) return
    preferenceSavingRef.current = true
    setPreferenceSaving(true)
    const previous = billingPreference
    setBillingPreference(pref)
    try {
      const res = await updateBillingPreference(pref)
      if (res.success) {
        toast.success(t('Updated successfully'))
        const normalized = res.data?.billing_preference || pref
        setBillingPreference(normalized)
      } else {
        toast.error(t('Update failed'))
        setBillingPreference(previous)
      }
    } catch {
      toast.error(t('Request failed'))
      setBillingPreference(previous)
    } finally {
      preferenceSavingRef.current = false
      setPreferenceSaving(false)
    }
  }

  const hasActive = activeSubscriptions.length > 0
  const hasAny = allSubscriptions.length > 0
  const isAvailable =
    loading || plans.length > 0 || hasAny || !!plansError || !!subscriptionError
  const disablePref = !hasActive
  const isSubPref =
    billingPreference === 'subscription_first' ||
    billingPreference === 'subscription_only'
  const displayPref =
    disablePref && isSubPref ? 'wallet_first' : billingPreference

  const planPurchaseCountMap = useMemo(() => {
    const map = new Map<number, number>()
    for (const sub of allSubscriptions) {
      const planId = sub?.subscription?.plan_id
      if (!planId) continue
      map.set(planId, (map.get(planId) || 0) + 1)
    }
    return map
  }, [allSubscriptions])

  useEffect(() => {
    onAvailabilityChange?.(isAvailable)
  }, [isAvailable, onAvailabilityChange])

  const getRemainingDays = (sub: PublicUserSubscriptionRecord) => {
    const endTime = sub?.subscription?.end_time || 0
    if (!endTime) return 0
    const now = Date.now() / 1000
    return Math.max(0, Math.ceil((endTime - now) / 86400))
  }

  const getUsagePercent = (sub: PublicUserSubscriptionRecord) => {
    const total = Number(sub?.subscription?.amount_total || 0)
    const used = Number(sub?.subscription?.amount_used || 0)
    if (total <= 0) return 0
    return Math.round((used / total) * 100)
  }

  if (loading) {
    return (
      <Card data-card-hover='false' className='gap-0 overflow-hidden py-0'>
        <CardHeader className='border-b p-3 !pb-3 sm:p-5 sm:!pb-5'>
          <Skeleton className='h-6 w-32' />
        </CardHeader>
        <CardContent className='space-y-4 p-3 sm:p-5'>
          <Skeleton className='h-20 w-full' />
          <div className='grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3'>
            {Array.from({ length: 3 }, (_, i) => `plan-skeleton-${i}`).map(
              (key) => (
                <Skeleton key={key} className='h-48 w-full' />
              )
            )}
          </div>
        </CardContent>
      </Card>
    )
  }

  if (plans.length === 0 && !hasAny && !plansError && !subscriptionError) {
    return null
  }

  return (
    <>
      <TitledCard
        title={t('Access Plans')}
        description={t(
          'Choose fixed-term access. Purchases are one-time and do not renew automatically.'
        )}
        icon={<Crown className='h-4 w-4' />}
        disableHoverEffect
        contentClassName='space-y-4 sm:space-y-5'
      >
        {(plansError || subscriptionError) && (
          <Alert variant='destructive'>
            <AlertDescription className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
              <span>
                {plansError && subscriptionError
                  ? t('Failed to load access information')
                  : plansError || subscriptionError}
              </span>
              <Button
                type='button'
                size='sm'
                variant='outline'
                disabled={refreshing}
                onClick={() => void handleRefresh()}
              >
                {t('Retry')}
              </Button>
            </AlertDescription>
          </Alert>
        )}
        {/* My access & billing preference */}
        <div className='rounded-xl border p-3 sm:p-4'>
          <div className='flex flex-wrap items-center justify-between gap-2.5 sm:gap-3'>
            <div className='flex min-w-0 flex-wrap items-center gap-2'>
              <span className='text-sm font-medium'>{t('My Access')}</span>
              <span className='flex items-center gap-1.5 text-xs font-medium'>
                <span
                  className={cn(
                    'size-1.5 shrink-0 rounded-full',
                    hasActive ? dotColorMap.success : dotColorMap.neutral
                  )}
                  aria-hidden='true'
                />
                {hasActive ? (
                  <span className={cn(textColorMap.success)}>
                    {activeSubscriptions.length} {t('active')}
                  </span>
                ) : (
                  <span className='text-muted-foreground'>
                    {t('No Active')}
                  </span>
                )}
                {allSubscriptions.length > activeSubscriptions.length && (
                  <>
                    <span className='text-muted-foreground/30'>·</span>
                    <span className='text-muted-foreground'>
                      {allSubscriptions.length - activeSubscriptions.length}{' '}
                      {t('expired')}
                    </span>
                  </>
                )}
              </span>
            </div>
            <div className='flex w-full items-center gap-2 sm:w-auto'>
              <Select
                items={[
                  {
                    value: 'subscription_first',
                    label: getBillingPreferenceDisplayLabel(
                      'subscription_first',
                      disablePref,
                      t
                    ),
                  },
                  {
                    value: 'wallet_first',
                    label: getBillingPreferenceLabel('wallet_first', t),
                  },
                  {
                    value: 'subscription_only',
                    label: getBillingPreferenceDisplayLabel(
                      'subscription_only',
                      disablePref,
                      t
                    ),
                  },
                  {
                    value: 'wallet_only',
                    label: getBillingPreferenceLabel('wallet_only', t),
                  },
                ]}
                value={displayPref}
                onValueChange={(v) => v !== null && handlePreferenceChange(v)}
                disabled={preferenceSaving}
              >
                <SelectTrigger className='h-8 flex-1 text-xs sm:w-[140px] sm:flex-none'>
                  <SelectValue>
                    {getBillingPreferenceLabel(displayPref, t)}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent alignItemWithTrigger={false}>
                  <SelectGroup>
                    <SelectItem
                      value='subscription_first'
                      disabled={disablePref}
                    >
                      {getBillingPreferenceDisplayLabel(
                        'subscription_first',
                        disablePref,
                        t
                      )}
                    </SelectItem>
                    <SelectItem value='wallet_first'>
                      {getBillingPreferenceLabel('wallet_first', t)}
                    </SelectItem>
                    <SelectItem
                      value='subscription_only'
                      disabled={disablePref}
                    >
                      {getBillingPreferenceDisplayLabel(
                        'subscription_only',
                        disablePref,
                        t
                      )}
                    </SelectItem>
                    <SelectItem value='wallet_only'>
                      {getBillingPreferenceLabel('wallet_only', t)}
                    </SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
              <Button
                variant='ghost'
                size='icon'
                className='h-8 w-8'
                onClick={handleRefresh}
                disabled={refreshing || preferenceSaving}
                aria-label={t('Refresh access status')}
              >
                <RefreshCw
                  className={`h-3.5 w-3.5 ${refreshing ? 'animate-spin' : ''}`}
                />
              </Button>
            </div>
          </div>

          {disablePref && isSubPref && (
            <p className='text-muted-foreground mt-2 text-xs'>
              {t(
                'Preference saved as {{pref}}, but no active access. Wallet will be used automatically.',
                {
                  pref:
                    billingPreference === 'subscription_only'
                      ? t('Access Only')
                      : t('Access First'),
                }
              )}
            </p>
          )}

          {hasAny && (
            <>
              <Separator className='my-3' />
              <div className='max-h-64 space-y-3 overflow-y-auto pr-1'>
                {allSubscriptions.map((sub) => {
                  const subscription = sub.subscription
                  const totalAmount = Number(subscription?.amount_total || 0)
                  const usedAmount = Number(subscription?.amount_used || 0)
                  const remainAmount =
                    totalAmount > 0 ? Math.max(0, totalAmount - usedAmount) : 0
                  const planTitle = subscription?.plan_title || ''
                  const remainDays = getRemainingDays(sub)
                  const usagePercent = getUsagePercent(sub)
                  const now = Date.now() / 1000
                  const isExpired = (subscription?.end_time || 0) < now
                  const isCancelled = subscription?.status === 'cancelled'
                  const isActive =
                    subscription?.status === 'active' && !isExpired
                  let statusLabel = t('Expired')
                  let statusVariant: 'success' | 'neutral' = 'neutral'
                  let timestampLabel = t('Expired at')
                  if (isActive) {
                    statusLabel = t('Active')
                    statusVariant = 'success'
                    timestampLabel = t('Until')
                  } else if (isCancelled) {
                    statusLabel = t('Cancelled')
                    timestampLabel = t('Cancelled at')
                  }

                  return (
                    <div
                      key={subscription?.id}
                      className='bg-background rounded-md border p-3 text-xs'
                    >
                      <div className='flex items-center justify-between'>
                        <div className='flex items-center gap-2'>
                          <span className='font-medium'>
                            {planTitle
                              ? t('{{plan}}, Access #{{id}}', {
                                  plan: planTitle,
                                  id: subscription?.id || 0,
                                })
                              : t('Access #{{id}}', {
                                  id: subscription?.id || 0,
                                })}
                          </span>
                          <StatusBadge
                            label={statusLabel}
                            variant={statusVariant}
                            copyable={false}
                          />
                        </div>
                        {isActive && (
                          <span className='text-muted-foreground'>
                            {t('{{count}} days remaining', {
                              count: remainDays,
                            })}
                          </span>
                        )}
                      </div>
                      <div className='text-muted-foreground mt-1.5'>
                        {timestampLabel}{' '}
                        {new Date(
                          (subscription?.end_time || 0) * 1000
                        ).toLocaleString()}
                      </div>
                      {isActive && (subscription?.next_reset_time ?? 0) > 0 && (
                        <div className='text-muted-foreground mt-1'>
                          {t('Next reset')}:{' '}
                          {new Date(
                            (subscription?.next_reset_time ?? 0) * 1000
                          ).toLocaleString()}
                        </div>
                      )}
                      <div className='text-muted-foreground mt-1'>
                        {t('Total Quota')}:{' '}
                        {totalAmount > 0 ? (
                          <Tooltip>
                            <TooltipTrigger
                              render={<span className='cursor-help' />}
                            >
                              {t(
                                '{{used}}/{{total}}. Remaining {{remaining}}',
                                {
                                  used: formatQuota(usedAmount),
                                  total: formatQuota(totalAmount),
                                  remaining: formatQuota(remainAmount),
                                }
                              )}
                            </TooltipTrigger>
                            <TooltipContent>
                              {t(
                                'Raw quota: {{used}}/{{total}}. Remaining {{remaining}}',
                                {
                                  used: usedAmount,
                                  total: totalAmount,
                                  remaining: remainAmount,
                                }
                              )}
                            </TooltipContent>
                          </Tooltip>
                        ) : (
                          t('Unlimited')
                        )}
                        {totalAmount > 0 && (
                          <span className='ml-2'>
                            {t('Used {{percent}}%', {
                              percent: usagePercent,
                            })}
                          </span>
                        )}
                      </div>
                      {totalAmount > 0 && isActive && (
                        <Progress value={usagePercent} className='mt-2 h-1.5' />
                      )}
                    </div>
                  )
                })}
              </div>
            </>
          )}

          {!hasAny && (
            <p className='text-muted-foreground mt-2 text-xs'>
              {t('Purchase fixed-term access to available models')}
            </p>
          )}
        </div>

        {/* Available plans grid */}
        {plans.length > 0 ? (
          <div className='grid grid-cols-1 gap-3 2xl:grid-cols-2 2xl:gap-4'>
            {plans.map((p, index) => {
              const plan = p?.plan
              if (!plan) return null
              const totalAmount = Number(plan.total_amount || 0)
              const price = formatPaymentDecimalAmount(
                Number(plan.price_amount || 0),
                plan.currency || 'USD'
              )
              const isPopular = index === 0 && plans.length > 1
              const limit = Number(plan.max_purchase_per_user || 0)
              const count = planPurchaseCountMap.get(plan.id) || 0
              const reached = limit > 0 && count >= limit

              const benefits = [
                t('Validity period: {{value}}', {
                  value: formatDuration(plan, t),
                }),
                formatResetPeriod(plan, t) !== t('No Reset')
                  ? t('Quota reset: {{value}}', {
                      value: formatResetPeriod(plan, t),
                    })
                  : null,
                totalAmount > 0
                  ? t('Total quota: {{value}}', {
                      value: formatQuota(totalAmount),
                    })
                  : t('Total quota: {{value}}', { value: t('Unlimited') }),
                limit > 0
                  ? t('Purchase limit: {{count}}', { count: limit })
                  : null,
                plan.includes_expanded_access
                  ? t('Includes expanded model access')
                  : null,
              ].filter(Boolean) as string[]

              return (
                <Card
                  key={plan.id}
                  data-card-hover='false'
                  className={cn(isPopular && 'border-primary/70 shadow-sm')}
                >
                  <CardContent className='flex h-full flex-col p-3.5 sm:p-4'>
                    <div className='mb-2 flex items-start justify-between gap-3'>
                      <div className='min-w-0'>
                        <h4 className='truncate font-semibold'>
                          {plan.title || t('Access Plans')}
                        </h4>
                        {plan.subtitle && (
                          <p className='text-muted-foreground truncate text-xs'>
                            {plan.subtitle}
                          </p>
                        )}
                      </div>
                      {isPopular && (
                        <StatusBadge
                          variant='info'
                          copyable={false}
                          className='shrink-0'
                        >
                          <Sparkles className='h-3 w-3' />
                          {t('Recommended')}
                        </StatusBadge>
                      )}
                    </div>

                    <div className='py-2'>
                      <span className='text-primary text-2xl font-bold'>
                        {price}
                      </span>
                    </div>

                    <div className='flex-1 space-y-1.5 pb-3'>
                      {benefits.map((label) => (
                        <div
                          key={label}
                          className='text-muted-foreground flex items-center gap-2 text-xs'
                        >
                          <Check className='text-primary h-3 w-3 shrink-0' />
                          <span>{label}</span>
                        </div>
                      ))}
                    </div>

                    <Separator className='mb-3' />

                    {reached ? (
                      <Tooltip>
                        <TooltipTrigger render={<div />}>
                          <Button variant='outline' className='w-full' disabled>
                            {t('Limit Reached')}
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent>
                          {t('Purchase limit reached ({{count}}/{{limit}})', {
                            count,
                            limit,
                          })}
                        </TooltipContent>
                      </Tooltip>
                    ) : (
                      <Button
                        variant='outline'
                        className='w-full'
                        onClick={() => {
                          setSelectedPlan(p)
                          setPurchaseOpen(true)
                        }}
                      >
                        {t('Purchase Access')}
                      </Button>
                    )}
                  </CardContent>
                </Card>
              )
            })}
          </div>
        ) : (
          <p className='text-muted-foreground py-4 text-center text-sm'>
            {t('No plans available')}
          </p>
        )}
      </TitledCard>

      <SubscriptionPurchaseDialog
        open={purchaseOpen}
        onOpenChange={(open) => {
          setPurchaseOpen(open)
          if (!open) {
            fetchSelfSubscription()
          }
        }}
        plan={selectedPlan}
        paymentRoutes={subscriptionPaymentRoutes}
        userQuota={userQuota}
        onPurchaseSuccess={handlePurchaseSuccess}
        purchaseLimit={
          selectedPlan?.plan?.max_purchase_per_user
            ? Number(selectedPlan.plan.max_purchase_per_user)
            : undefined
        }
        purchaseCount={
          selectedPlan?.plan?.id
            ? planPurchaseCountMap.get(selectedPlan.plan.id)
            : undefined
        }
      />
    </>
  )
}
