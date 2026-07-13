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
import { getRouteApi } from '@tanstack/react-router'
import { useCallback } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { ManualBillingReviewsSection } from '@/features/channel-routing/policies/manual-billing-reviews-section'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

const route = getRouteApi('/_authenticated/billing-reviews/')

export function BillingReviewsPage() {
  const { t } = useTranslation()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const user = useAuthStore((state) => state.auth.user)
  const canResolve = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.BILLING_REVIEW,
    ADMIN_PERMISSION_ACTIONS.RESOLVE
  )
  const handleCursorChange = useCallback(
    (cursor: number) => {
      void navigate({ search: { cursor }, replace: true })
    },
    [navigate]
  )

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('Billing review')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='h-full min-h-0 overflow-auto pb-2'>
          <ManualBillingReviewsSection
            cursor={search.cursor ?? 0}
            canResolve={canResolve}
            embedded={false}
            onCursorChange={handleCursorChange}
          />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
