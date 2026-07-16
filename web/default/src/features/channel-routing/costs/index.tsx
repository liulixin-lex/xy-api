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
  ArrowReloadHorizontalIcon,
  Coins01Icon,
  MultiplicationSignIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
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
import { useIsFetching, useQueryClient } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingPageFrame } from '../components/page-frame'
import { ChannelConfigurationsSection } from './channel-configurations-section'
import { EffectiveCostsSection } from './effective-costs-section'

const route = getRouteApi('/_authenticated/channel-routing/$section')

export function ChannelRoutingCostsPage() {
  const { t } = useTranslation()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const queryClient = useQueryClient()
  const activeTab = search.costTab ?? 'channel-multipliers'
  const activeQueryKey =
    activeTab === 'effective-costs'
      ? channelRoutingQueryKeys.costsRoot()
      : channelRoutingQueryKeys.channelConfigurationsRoot()
  const isFetching =
    useIsFetching({
      queryKey: activeQueryKey,
    }) > 0

  return (
    <ChannelRoutingPageFrame
      activeSection='costs'
      title={t('Upstream costs')}
      actions={
        <Button
          size='icon-sm'
          variant='outline'
          aria-label={t('Refresh')}
          disabled={isFetching}
          onClick={() =>
            void queryClient.invalidateQueries({
              queryKey: activeQueryKey,
              refetchType: 'active',
            })
          }
        >
          <HugeiconsIcon
            icon={ArrowReloadHorizontalIcon}
            strokeWidth={2}
            className={
              isFetching ? 'animate-spin motion-reduce:animate-none' : undefined
            }
            aria-hidden='true'
          />
        </Button>
      }
    >
      <Tabs
        value={activeTab}
        onValueChange={(value) => {
          if (value !== 'channel-multipliers' && value !== 'effective-costs') {
            return
          }
          void navigate({
            search: (previous) => ({
              ...previous,
              page: 1,
              costTab: value,
            }),
            replace: true,
          })
        }}
      >
        <TabsList
          variant='line'
          aria-label={t('Upstream cost views')}
          className='h-auto min-h-11 max-w-full justify-start overflow-x-auto'
        >
          <TabsTrigger
            value='channel-multipliers'
            className='min-h-11 flex-none shrink-0 px-3'
          >
            <HugeiconsIcon
              icon={MultiplicationSignIcon}
              strokeWidth={2}
              aria-hidden='true'
            />
            {t('Channel multipliers')}
          </TabsTrigger>
          <TabsTrigger
            value='effective-costs'
            className='min-h-11 flex-none shrink-0 px-3'
          >
            <HugeiconsIcon
              icon={Coins01Icon}
              strokeWidth={2}
              aria-hidden='true'
            />
            {t('Effective costs')}
          </TabsTrigger>
        </TabsList>
        <TabsContent value='channel-multipliers'>
          {activeTab === 'channel-multipliers' ? (
            <ChannelConfigurationsSection />
          ) : null}
        </TabsContent>
        <TabsContent value='effective-costs'>
          {activeTab === 'effective-costs' ? <EffectiveCostsSection /> : null}
        </TabsContent>
      </Tabs>
    </ChannelRoutingPageFrame>
  )
}
