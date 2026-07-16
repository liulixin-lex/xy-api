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
const currentAlgorithms = new Set([
  'channel-routing-balanced',
  'channel-routing-canary',
  'channel-routing-shadow',
  'channel-routing-legacy',
])

export type ChannelRoutingAlgorithmKind = 'current' | 'historical' | 'unknown'

export function channelRoutingAlgorithmKind(
  algorithm: string
): ChannelRoutingAlgorithmKind {
  const normalized = algorithm.trim().toLowerCase()
  if (currentAlgorithms.has(normalized)) return 'current'
  if (/^channel-routing-(balanced|canary|shadow|legacy)-v/.test(normalized)) {
    return 'historical'
  }
  return 'unknown'
}
