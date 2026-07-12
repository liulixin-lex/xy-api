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
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

export function ChannelRoutingIdentityText(props: {
  text: string
  className?: string
  breakAll?: boolean
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span
            className={cn(
              'line-clamp-2 block min-w-0 max-w-full',
              props.breakAll ? 'break-all' : 'break-words',
              props.className
            )}
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
