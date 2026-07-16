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
import { useEffect, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { TabsList } from '@/components/ui/tabs'

export function BillingScrollableTabsList(props: {
  activeValue: string
  children: ReactNode
}) {
  const { i18n } = useTranslation()
  const viewportRef = useRef<HTMLDivElement>(null)
  const language = i18n.resolvedLanguage || i18n.language

  useEffect(() => {
    const viewport = viewportRef.current
    if (!viewport) return

    let animationFrame = 0
    const revealActiveTab = () => {
      window.cancelAnimationFrame(animationFrame)
      animationFrame = window.requestAnimationFrame(() => {
        const activeTab = viewport.querySelector<HTMLElement>(
          '[role="tab"][aria-selected="true"]'
        )
        activeTab?.scrollIntoView({
          behavior: 'auto',
          block: 'nearest',
          inline: 'start',
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
  }, [props.activeValue, language])

  return (
    <div ref={viewportRef} className='overflow-x-auto pb-1'>
      <TabsList
        variant='line'
        className='min-w-max justify-start max-lg:min-h-11 max-lg:[&_[data-slot=tabs-trigger]]:min-h-11'
      >
        {props.children}
        <span
          aria-hidden='true'
          className='w-[calc(100dvw-10rem)] max-w-[28rem] min-w-8 shrink-0 lg:hidden'
        />
      </TabsList>
    </div>
  )
}
