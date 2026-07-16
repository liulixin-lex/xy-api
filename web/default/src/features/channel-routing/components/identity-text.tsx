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
import { useEffect, useRef, useState } from 'react'

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

import { channelRoutingIdentityTextIsTruncated } from '../lib/identity-text'

const identityResizeCallbacks = new WeakMap<Element, () => void>()
let identityResizeObserver: ResizeObserver | null = null
let identityResizeTargetCount = 0

function observeIdentityResize(element: Element, callback: () => void) {
  if (typeof ResizeObserver === 'undefined') return () => undefined
  if (!identityResizeObserver) {
    identityResizeObserver = new ResizeObserver((entries) => {
      for (const entry of entries) {
        identityResizeCallbacks.get(entry.target)?.()
      }
    })
  }
  identityResizeCallbacks.set(element, callback)
  identityResizeObserver.observe(element)
  identityResizeTargetCount += 1
  return () => {
    identityResizeCallbacks.delete(element)
    identityResizeObserver?.unobserve(element)
    identityResizeTargetCount = Math.max(0, identityResizeTargetCount - 1)
    if (identityResizeTargetCount === 0) {
      identityResizeObserver?.disconnect()
      identityResizeObserver = null
    }
  }
}

export function ChannelRoutingIdentityText(props: {
  text: string
  className?: string
  breakAll?: boolean
  withinInteractive?: boolean
}) {
  const textRef = useRef<HTMLElement | null>(null)
  const [truncated, setTruncated] = useState(false)
  const className = cn(
    props.withinInteractive
      ? 'line-clamp-2 min-w-0 max-w-full'
      : 'focus-visible:ring-ring line-clamp-2 min-w-0 max-w-full rounded-sm p-0 text-left focus-visible:ring-2 focus-visible:ring-inset focus-visible:outline-none',
    props.breakAll ? 'break-all' : 'break-words',
    props.className
  )

  useEffect(() => {
    if (props.withinInteractive) {
      setTruncated(false)
      return
    }
    const element = textRef.current
    if (!element) return
    const update = () => {
      const next = channelRoutingIdentityTextIsTruncated({
        clientWidth: element.clientWidth,
        scrollWidth: element.scrollWidth,
        clientHeight: element.clientHeight,
        scrollHeight: element.scrollHeight,
      })
      setTruncated((current) => (current === next ? current : next))
    }
    update()
    return observeIdentityResize(element, update)
  }, [props.breakAll, props.className, props.text, props.withinInteractive])

  if (props.withinInteractive) {
    return <span className={className}>{props.text}</span>
  }

  return (
    <Tooltip disabled={!truncated}>
      <TooltipTrigger
        render={
          <span
            ref={(element) => {
              textRef.current = element
            }}
            tabIndex={truncated ? 0 : undefined}
            className={className}
          />
        }
      >
        {props.text}
      </TooltipTrigger>
      <TooltipContent className='max-w-sm break-all'>
        {props.text}
      </TooltipContent>
    </Tooltip>
  )
}
