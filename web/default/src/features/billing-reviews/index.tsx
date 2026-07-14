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
import { useCallback, useEffect } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Tabs, TabsContent, TabsTrigger } from '@/components/ui/tabs'
import { ChannelRoutingScrollableTabsList } from '@/features/channel-routing/components/scrollable-tabs-list'
import { ManualBillingReviewsSection } from '@/features/channel-routing/policies/manual-billing-reviews-section'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { getSelf } from '@/lib/api'
import { useAuthStore, type AuthUser } from '@/stores/auth-store'

import { ProjectionFailuresSection } from './components/projection-failures-section'
import type { BillingProjectionDataset } from './projection-types'

const route = getRouteApi('/_authenticated/billing-reviews/')

export function BillingReviewsPage() {
  const { t } = useTranslation()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const user = useAuthStore((state) => state.auth.user)
  const setUser = useAuthStore((state) => state.auth.setUser)
  const canReadReviews = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.BILLING_REVIEW,
    ADMIN_PERMISSION_ACTIONS.READ
  )
  const canResolveReviews = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.BILLING_REVIEW,
    ADMIN_PERMISSION_ACTIONS.RESOLVE
  )
  const canReadProjections = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.BILLING_PROJECTION_OPS,
    ADMIN_PERMISSION_ACTIONS.READ
  )
  const canRequeueProjections = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.BILLING_PROJECTION_OPS,
    ADMIN_PERMISSION_ACTIONS.REQUEUE
  )
  const canResolveProjectionConflicts = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.BILLING_PROJECTION_OPS,
    ADMIN_PERMISSION_ACTIONS.RESOLVE
  )

  let activeTab = search.tab
  if (!activeTab || (activeTab === 'reviews' && !canReadReviews)) {
    activeTab = canReadReviews ? 'reviews' : 'projections'
  }
  if (activeTab === 'projections' && !canReadProjections) {
    activeTab = 'reviews'
  }
  const projectionKind = search.projectionKind ?? 'stats'

  const refreshPermissions = useCallback(async () => {
    const response = await getSelf()
    if (response?.success && response.data) {
      setUser(response.data as AuthUser)
    }
  }, [setUser])

  useEffect(() => {
    if (!canReadReviews && !canReadProjections) {
      void navigate({ to: '/403', replace: true })
    }
  }, [canReadProjections, canReadReviews, navigate])

  const handleReviewCursorChange = useCallback(
    (cursor: number) => {
      void navigate({ search: { ...search, cursor }, replace: true })
    },
    [navigate, search]
  )
  const handleProjectionCursorChange = useCallback(
    (projectionCursor: number) => {
      void navigate({
        search: { ...search, projectionCursor },
        replace: true,
      })
    },
    [navigate, search]
  )

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>
        {t('Billing operations')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='flex h-full min-h-0 flex-col gap-3 overflow-hidden pb-2 max-lg:[&_[data-slot=button]]:min-h-11 max-lg:[&_[data-slot=button]]:min-w-11'>
          <Tabs
            value={activeTab}
            onValueChange={(value) => {
              if (value !== 'reviews' && value !== 'projections') return
              void navigate({
                search: { ...search, tab: value },
                replace: true,
              })
            }}
            className='min-h-0 flex-1'
          >
            <ChannelRoutingScrollableTabsList activeValue={activeTab}>
              {canReadReviews ? (
                <TabsTrigger value='reviews'>{t('Manual reviews')}</TabsTrigger>
              ) : null}
              {canReadProjections ? (
                <TabsTrigger value='projections'>
                  {t('Projection failures')}
                </TabsTrigger>
              ) : null}
            </ChannelRoutingScrollableTabsList>

            {canReadReviews ? (
              <TabsContent
                value='reviews'
                className='min-h-0 overflow-auto pt-2'
              >
                <ManualBillingReviewsSection
                  cursor={search.cursor ?? 0}
                  canResolve={canResolveReviews}
                  embedded={false}
                  onCursorChange={handleReviewCursorChange}
                />
              </TabsContent>
            ) : null}

            {canReadProjections ? (
              <TabsContent
                value='projections'
                className='min-h-0 overflow-auto pt-2'
              >
                <Tabs
                  value={projectionKind}
                  onValueChange={(value) => {
                    if (
                      value !== 'stats' &&
                      value !== 'logs' &&
                      value !== 'conflicts'
                    ) {
                      return
                    }
                    void navigate({
                      search: {
                        ...search,
                        projectionKind: value,
                        projectionCursor: 0,
                      },
                      replace: true,
                    })
                  }}
                >
                  <ChannelRoutingScrollableTabsList
                    activeValue={projectionKind}
                  >
                    <TabsTrigger value='stats'>
                      {t('Stats projections')}
                    </TabsTrigger>
                    <TabsTrigger value='logs'>
                      {t('Log projections')}
                    </TabsTrigger>
                    <TabsTrigger value='conflicts'>
                      {t('Sink conflicts')}
                    </TabsTrigger>
                  </ChannelRoutingScrollableTabsList>
                  <TabsContent
                    key={projectionKind}
                    value={projectionKind}
                    className='pt-2'
                  >
                    <ProjectionFailuresSection
                      dataset={projectionKind as BillingProjectionDataset}
                      cursor={search.projectionCursor ?? 0}
                      canRequeue={canRequeueProjections}
                      canResolve={canResolveProjectionConflicts}
                      onCursorChange={handleProjectionCursorChange}
                      onPermissionRevoked={refreshPermissions}
                    />
                  </TabsContent>
                </Tabs>
              </TabsContent>
            ) : null}
          </Tabs>
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
