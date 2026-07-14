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
export const CHANNEL_ROUTING_PAGE_SIZES = [10, 20, 50] as const

export type ChannelRoutingPageSize = (typeof CHANNEL_ROUTING_PAGE_SIZES)[number]

export function isChannelRoutingPageSize(
  value: number
): value is ChannelRoutingPageSize {
  return CHANNEL_ROUTING_PAGE_SIZES.some((size) => size === value)
}
