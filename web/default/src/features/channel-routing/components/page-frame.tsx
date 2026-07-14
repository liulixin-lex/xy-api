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
import {
  Activity,
  Coins,
  GitBranch,
  RadioTower,
  ScrollText,
  ShieldCheck,
  WifiOff,
} from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Tabs, TabsTrigger } from '@/components/ui/tabs'

import { useChannelRoutingRealtimeStatus } from '../shell/realtime-state'
import type { ChannelRoutingSection } from '../types'
import { ChannelRoutingScrollableTabsList } from './scrollable-tabs-list'

const sections: Array<{
  id: ChannelRoutingSection
  label: string
  icon: typeof Activity
}> = [
  { id: 'overview', label: 'Overview', icon: Activity },
  { id: 'groups', label: 'Routing groups', icon: GitBranch },
  { id: 'channels', label: 'Channel health', icon: RadioTower },
  { id: 'decisions', label: 'Decision audit', icon: ScrollText },
  { id: 'costs', label: 'Upstream costs', icon: Coins },
  { id: 'policies', label: 'Policies and changes', icon: ShieldCheck },
]

export function ChannelRoutingPageFrame(props: {
  activeSection: ChannelRoutingSection
  title: ReactNode
  actions?: ReactNode
  breadcrumb?: ReactNode
  children: ReactNode
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const realtimeStatus = useChannelRoutingRealtimeStatus()

  return (
    <SectionPageLayout as='div' fixedContent headingAs='h1'>
      {props.breadcrumb ? (
        <SectionPageLayout.Breadcrumb>
          {props.breadcrumb}
        </SectionPageLayout.Breadcrumb>
      ) : null}
      <SectionPageLayout.Title>
        <span className='block min-w-0 break-words whitespace-normal'>
          {props.title}
        </span>
      </SectionPageLayout.Title>
      {props.actions ? (
        <SectionPageLayout.Actions>
          <div className='max-lg:[&_[data-slot=button]]:min-h-11 max-lg:[&_[data-slot=button]]:min-w-11 max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11'>
            {props.actions}
          </div>
        </SectionPageLayout.Actions>
      ) : null}
      <SectionPageLayout.Content>
        <div className='flex h-full min-h-0 flex-col gap-3 max-lg:[&_[data-slot=button]]:min-h-11 max-lg:[&_[data-slot=button]]:min-w-11 max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11'>
          <Tabs
            value={props.activeSection}
            onValueChange={(value) => {
              void navigate({
                to: '/channel-routing/$section',
                params: { section: value as ChannelRoutingSection },
              })
            }}
          >
            <ChannelRoutingScrollableTabsList activeValue={props.activeSection}>
              {sections.map((section) => {
                const Icon = section.icon
                return (
                  <TabsTrigger
                    key={section.id}
                    value={section.id}
                    className='max-lg:min-h-11'
                  >
                    <Icon aria-hidden='true' />
                    {t(section.label)}
                  </TabsTrigger>
                )
              })}
            </ChannelRoutingScrollableTabsList>
          </Tabs>
          {realtimeStatus === 'polling' ? (
            <div
              role='status'
              aria-live='polite'
              className='bg-muted/40 text-muted-foreground flex items-start gap-2 rounded-md border px-3 py-2 text-xs'
            >
              <WifiOff
                className='mt-0.5 size-3.5 shrink-0'
                aria-hidden='true'
              />
              <span>
                {t(
                  'Live updates are unavailable. Data is refreshing every 15 seconds.'
                )}
              </span>
            </div>
          ) : null}
          <div
            role='region'
            aria-label={t('Channel routing page content')}
            tabIndex={0}
            className='focus-visible:ring-ring min-h-0 flex-1 overflow-auto focus-visible:ring-2 focus-visible:outline-none focus-visible:ring-inset'
          >
            {props.children}
          </div>
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
