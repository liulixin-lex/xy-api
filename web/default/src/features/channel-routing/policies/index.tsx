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
import {
  Audit02Icon,
  PolicyIcon,
  Settings02Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { getRouteApi } from '@tanstack/react-router'
import { lazy, Suspense, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { ChannelRoutingPageFrame } from '../components/page-frame'
import { ChannelRoutingLoadingState } from '../components/page-state'

const route = getRouteApi('/_authenticated/channel-routing/$section')

const RuntimeSettings = lazy(() =>
  import('./runtime-settings').then((module) => ({
    default: module.ChannelRoutingRuntimeSettings,
  }))
)
const VersionedPolicies = lazy(() =>
  import('./versioned-policies').then((module) => ({
    default: module.ChannelRoutingVersionedPolicies,
  }))
)
const OperationsAudits = lazy(() =>
  import('./operations-audits').then((module) => ({
    default: module.ChannelRoutingOperationsAudits,
  }))
)

export function ChannelRoutingPoliciesPage() {
  const { i18n, t } = useTranslation()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const activeTab = search.policyTab ?? 'runtime-settings'
  const tabsViewportRef = useRef<HTMLDivElement>(null)
  const language = i18n.resolvedLanguage || i18n.language

  useEffect(() => {
    const viewport = tabsViewportRef.current
    if (!viewport) return

    let animationFrame = 0
    const revealActiveTab = () => {
      window.cancelAnimationFrame(animationFrame)
      animationFrame = window.requestAnimationFrame(() => {
        const activeTabElement = viewport.querySelector<HTMLElement>(
          '[role="tab"][aria-selected="true"]'
        )
        const tabsList = viewport.firstElementChild
        if (!activeTabElement || !tabsList) return

        const viewportRect = viewport.getBoundingClientRect()
        const activeTabRect = activeTabElement.getBoundingClientRect()
        const tabsListStyle = window.getComputedStyle(tabsList)
        const inlineStartPadding = Number.parseFloat(
          tabsListStyle.paddingInlineStart
        )
        viewport.scrollTo({
          behavior: 'auto',
          left:
            viewport.scrollLeft +
            activeTabRect.left -
            viewportRect.left -
            (Number.isFinite(inlineStartPadding) ? inlineStartPadding : 0),
        })
      })
    }

    revealActiveTab()
    if (typeof ResizeObserver === 'undefined') {
      return () => window.cancelAnimationFrame(animationFrame)
    }

    const resizeObserver = new ResizeObserver(revealActiveTab)
    resizeObserver.observe(viewport)
    const tabsList = viewport.firstElementChild
    if (tabsList) resizeObserver.observe(tabsList)

    return () => {
      resizeObserver.disconnect()
      window.cancelAnimationFrame(animationFrame)
    }
  }, [activeTab, language])

  return (
    <ChannelRoutingPageFrame
      activeSection='policies'
      title={t('Routing policies')}
    >
      <Tabs
        value={activeTab}
        onValueChange={(value) => {
          if (
            value !== 'runtime-settings' &&
            value !== 'versioned-policies' &&
            value !== 'operations-audits'
          ) {
            return
          }
          void navigate({
            search: (previous) => ({ ...previous, policyTab: value }),
            replace: true,
          })
        }}
      >
        <div ref={tabsViewportRef} className='overflow-x-auto pb-1'>
          <TabsList
            variant='line'
            aria-label={t('Routing policy views')}
            className='h-auto min-h-11 min-w-max justify-start'
          >
            <TabsTrigger
              value='runtime-settings'
              className='min-h-11 flex-none shrink-0 px-3'
            >
              <HugeiconsIcon icon={Settings02Icon} aria-hidden='true' />
              {t('Runtime settings')}
            </TabsTrigger>
            <TabsTrigger
              value='versioned-policies'
              className='min-h-11 flex-none shrink-0 px-3'
            >
              <HugeiconsIcon icon={PolicyIcon} aria-hidden='true' />
              {t('Versioned policies')}
            </TabsTrigger>
            <TabsTrigger
              value='operations-audits'
              className='min-h-11 flex-none shrink-0 px-3'
            >
              <HugeiconsIcon icon={Audit02Icon} aria-hidden='true' />
              {t('Operations and control audits')}
            </TabsTrigger>
            <span
              aria-hidden='true'
              className='w-[calc(100dvw-10rem)] max-w-[28rem] min-w-8 shrink-0 lg:hidden'
            />
          </TabsList>
        </div>

        <TabsContent value='runtime-settings' className='pt-3'>
          {activeTab === 'runtime-settings' ? (
            <Suspense fallback={<ChannelRoutingLoadingState rows={8} />}>
              <RuntimeSettings />
            </Suspense>
          ) : null}
        </TabsContent>
        <TabsContent value='versioned-policies' className='pt-3'>
          {activeTab === 'versioned-policies' ? (
            <Suspense fallback={<ChannelRoutingLoadingState rows={8} />}>
              <VersionedPolicies />
            </Suspense>
          ) : null}
        </TabsContent>
        <TabsContent value='operations-audits' className='pt-3'>
          {activeTab === 'operations-audits' ? (
            <Suspense fallback={<ChannelRoutingLoadingState rows={8} />}>
              <OperationsAudits />
            </Suspense>
          ) : null}
        </TabsContent>
      </Tabs>
    </ChannelRoutingPageFrame>
  )
}
