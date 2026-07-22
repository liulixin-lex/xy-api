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
import { useQuery } from '@tanstack/react-query'
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  SecureVerificationDialog,
  useSecureVerification,
  type StartVerificationOptions,
} from '@/features/auth/secure-verification'

import { listAdminStripeInventory } from './api'
import { BillingReservationPanel } from './billing-reservation-panel'
import { PaymentAuditPanel } from './payment-audit-panel'
import { PaymentOverviewPanel } from './payment-overview-panel'
import { StripeInventoryPanel } from './stripe-inventory-panel'
import {
  PaymentOperationVerificationContext,
  type RunVerifiedPaymentOperation,
} from './verification-context'

export function PaymentOperationsSection() {
  const { t } = useTranslation()
  const [tab, setTab] = useState('overview')
  const stripeInventoryPresenceQuery = useQuery({
    queryKey: ['stripe-legacy-inventory', 'admin', 'presence'],
    queryFn: () =>
      listAdminStripeInventory(
        {
          status: '',
          mappingStatus: '',
          userId: '',
          customerId: '',
          subscriptionId: '',
        },
        1,
        1
      ),
    staleTime: 30_000,
  })
  const hasStripeLegacyInventory =
    (stripeInventoryPresenceQuery.data?.total ?? 0) > 0
  const activeTab =
    tab === 'stripe' && !hasStripeLegacyInventory ? 'overview' : tab
  const {
    open: verificationOpen,
    methods: verificationMethods,
    state: verificationState,
    executeVerification,
    cancel: cancelVerification,
    setCode,
    switchMethod,
    withVerification,
  } = useSecureVerification()
  const runWithVerification = useCallback<RunVerifiedPaymentOperation>(
    async <T,>(
      operation: () => Promise<T>,
      options?: StartVerificationOptions
    ) => (await withVerification(operation, options)) as T | null,
    [withVerification]
  )
  const verificationContext = useMemo(
    () => ({ runWithVerification, verificationOpen }),
    [runWithVerification, verificationOpen]
  )

  return (
    <PaymentOperationVerificationContext.Provider value={verificationContext}>
      <Tabs value={activeTab} onValueChange={setTab} className='gap-4'>
        <div className='overflow-x-auto'>
          <TabsList
            variant='line'
            className='w-max min-w-full justify-start'
            aria-label={t('Payment operations views')}
          >
            <TabsTrigger value='overview'>{t('Payment Overview')}</TabsTrigger>
            <TabsTrigger value='audit'>{t('Payment Audit')}</TabsTrigger>
            <TabsTrigger value='reservations'>
              {t('Billing Reservations')}
            </TabsTrigger>
            {hasStripeLegacyInventory && (
              <TabsTrigger value='stripe'>
                {t('Stripe Legacy Inventory')}
              </TabsTrigger>
            )}
          </TabsList>
        </div>
        <TabsContent value='overview'>
          <PaymentOverviewPanel />
        </TabsContent>
        <TabsContent value='audit'>
          <PaymentAuditPanel />
        </TabsContent>
        <TabsContent value='reservations'>
          <BillingReservationPanel />
        </TabsContent>
        {hasStripeLegacyInventory && (
          <TabsContent value='stripe'>
            <StripeInventoryPanel />
          </TabsContent>
        )}
      </Tabs>

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
        onCodeChange={setCode}
        onMethodChange={switchMethod}
      />
    </PaymentOperationVerificationContext.Provider>
  )
}
