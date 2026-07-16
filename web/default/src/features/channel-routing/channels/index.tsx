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
  Router01Icon,
  ServerStack01Icon,
  TestTube01Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { getRouteApi } from '@tanstack/react-router'
import { lazy, Suspense } from 'react'
import { useTranslation } from 'react-i18next'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { ChannelRoutingPageFrame } from '../components/page-frame'
import { ChannelRoutingLoadingState } from '../components/page-state'
import {
  CHANNEL_ROUTING_CHANNEL_TABS,
  type ChannelRoutingChannelTab,
} from './tabs'

const route = getRouteApi('/_authenticated/channel-routing/$section')

const loadPhysicalChannels = () => import('./physical-channels-tab')
const loadEndpointBreakers = () => import('./endpoint-breakers-tab')
const loadActiveProbes = () => import('./active-probes-tab')

const PhysicalChannels = lazy(() =>
  loadPhysicalChannels().then((module) => ({
    default: module.PhysicalChannelsTab,
  }))
)
const EndpointBreakers = lazy(() =>
  loadEndpointBreakers().then((module) => ({
    default: module.EndpointBreakersTab,
  }))
)
const ActiveProbes = lazy(() =>
  loadActiveProbes().then((module) => ({
    default: module.ActiveProbesTab,
  }))
)

function isChannelTab(value: string): value is ChannelRoutingChannelTab {
  return CHANNEL_ROUTING_CHANNEL_TABS.some((tab) => tab === value)
}

export function ChannelRoutingChannelsPage() {
  const { t } = useTranslation()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const activeTab = search.channelTab ?? 'physical-channels'

  return (
    <ChannelRoutingPageFrame
      activeSection='channels'
      title={t('Channel health')}
    >
      <Tabs
        value={activeTab}
        onValueChange={(value) => {
          if (!isChannelTab(value)) return
          void navigate({
            search: (previous) => ({ ...previous, channelTab: value }),
            replace: true,
          })
        }}
      >
        <TabsList
          variant='line'
          aria-label={t('Channel health views')}
          className='h-auto min-h-11 max-w-full justify-start overflow-x-auto'
        >
          <TabsTrigger
            value='physical-channels'
            className='min-h-11 flex-none shrink-0 px-3'
            onMouseEnter={() => void loadPhysicalChannels()}
            onFocus={() => void loadPhysicalChannels()}
          >
            <HugeiconsIcon icon={ServerStack01Icon} aria-hidden='true' />
            {t('Physical channels')}
          </TabsTrigger>
          <TabsTrigger
            value='endpoint-breakers'
            className='min-h-11 flex-none shrink-0 px-3'
            onMouseEnter={() => void loadEndpointBreakers()}
            onFocus={() => void loadEndpointBreakers()}
          >
            <HugeiconsIcon icon={Router01Icon} aria-hidden='true' />
            {t('Endpoints and breakers')}
          </TabsTrigger>
          <TabsTrigger
            value='active-probes'
            className='min-h-11 flex-none shrink-0 px-3'
            onMouseEnter={() => void loadActiveProbes()}
            onFocus={() => void loadActiveProbes()}
          >
            <HugeiconsIcon icon={TestTube01Icon} aria-hidden='true' />
            {t('Active probe records')}
          </TabsTrigger>
        </TabsList>

        <TabsContent value='physical-channels' className='pt-3'>
          {activeTab === 'physical-channels' ? (
            <Suspense fallback={<ChannelRoutingLoadingState rows={8} />}>
              <PhysicalChannels />
            </Suspense>
          ) : null}
        </TabsContent>
        <TabsContent value='endpoint-breakers' className='pt-3'>
          {activeTab === 'endpoint-breakers' ? (
            <Suspense fallback={<ChannelRoutingLoadingState rows={8} />}>
              <EndpointBreakers />
            </Suspense>
          ) : null}
        </TabsContent>
        <TabsContent value='active-probes' className='pt-3'>
          {activeTab === 'active-probes' ? (
            <Suspense fallback={<ChannelRoutingLoadingState rows={8} />}>
              <ActiveProbes />
            </Suspense>
          ) : null}
        </TabsContent>
      </Tabs>
    </ChannelRoutingPageFrame>
  )
}
