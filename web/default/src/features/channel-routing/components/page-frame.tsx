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
import { Link } from '@tanstack/react-router'
import {
  Activity,
  Coins,
  GitBranch,
  RadioTower,
  ScrollText,
  ShieldCheck,
  WifiOff,
} from 'lucide-react'
import { useEffect, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { cn } from '@/lib/utils'

import { useChannelRoutingRealtimeStatus } from '../shell/realtime-state'
import type { ChannelRoutingSection } from '../types'

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
  const { i18n, t } = useTranslation()
  const realtimeStatus = useChannelRoutingRealtimeStatus()
  const navigationRef = useRef<HTMLElement>(null)
  const language = i18n.resolvedLanguage || i18n.language

  useEffect(() => {
    const navigation = navigationRef.current
    if (!navigation) return

    let animationFrame = 0
    const revealActiveLink = () => {
      window.cancelAnimationFrame(animationFrame)
      animationFrame = window.requestAnimationFrame(() => {
        const activeLink = navigation.querySelector<HTMLElement>(
          '[aria-current="page"]'
        )
        if (!activeLink) return

        const navigationRect = navigation.getBoundingClientRect()
        const activeRect = activeLink.getBoundingClientRect()
        const navigationStyle = window.getComputedStyle(navigation)
        const inlineStartPadding = Number.parseFloat(
          navigationStyle.paddingInlineStart
        )
        navigation.scrollTo({
          behavior: 'auto',
          left:
            navigation.scrollLeft +
            activeRect.left -
            navigationRect.left -
            (Number.isFinite(inlineStartPadding) ? inlineStartPadding : 0),
        })
      })
    }

    revealActiveLink()
    if (typeof ResizeObserver === 'undefined') {
      return () => window.cancelAnimationFrame(animationFrame)
    }

    const resizeObserver = new ResizeObserver(revealActiveLink)
    resizeObserver.observe(navigation)
    const links = navigation.firstElementChild
    if (links) resizeObserver.observe(links)

    return () => {
      resizeObserver.disconnect()
      window.cancelAnimationFrame(animationFrame)
    }
  }, [language, props.activeSection])

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
        <div className='channel-routing-touch-surface flex h-full min-h-0 flex-col gap-3 max-lg:[&_[data-slot=button]]:min-h-11 max-lg:[&_[data-slot=button]]:min-w-11 max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11'>
          <nav
            ref={navigationRef}
            aria-label={t('Channel Routing')}
            className='overflow-x-auto p-1'
          >
            <div className='flex min-w-max items-center gap-1 border-b'>
              {sections.map((section) => {
                const Icon = section.icon
                const active = section.id === props.activeSection
                return (
                  <Link
                    key={section.id}
                    to='/channel-routing/$section'
                    params={{ section: section.id }}
                    aria-current={active ? 'page' : undefined}
                    className={cn(
                      'text-foreground/60 hover:text-foreground focus-visible:ring-ring/50 relative inline-flex min-h-8 items-center justify-center gap-1.5 rounded-md px-1.5 py-0.5 text-sm font-medium whitespace-nowrap transition-colors after:absolute after:inset-x-0 after:-bottom-1 after:h-0.5 after:bg-foreground after:opacity-0 after:transition-opacity focus-visible:ring-[3px] focus-visible:outline-1 max-lg:min-h-11 dark:text-muted-foreground dark:hover:text-foreground',
                      active &&
                        'text-foreground after:opacity-100 dark:text-foreground'
                    )}
                  >
                    <Icon className='size-4' aria-hidden='true' />
                    {t(section.label)}
                  </Link>
                )
              })}
              <span
                aria-hidden='true'
                className='w-[calc(100dvw-10rem)] max-w-[28rem] min-w-8 shrink-0 lg:hidden'
              />
            </div>
          </nav>
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
