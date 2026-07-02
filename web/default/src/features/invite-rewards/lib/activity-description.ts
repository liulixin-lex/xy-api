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
import { marked } from 'marked'

const trustedMarkdownOptions = {
  async: false,
  breaks: true,
  gfm: true,
} as const

const shanghaiTimestampFormatter = new Intl.DateTimeFormat('sv-SE', {
  timeZone: 'Asia/Shanghai',
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
})

export function renderTrustedActivityDescription(content: string) {
  return marked.parse(content, trustedMarkdownOptions)
}

export function formatShanghaiTimestamp(timestamp: number) {
  if (!timestamp) return '-'
  return shanghaiTimestampFormatter.format(timestamp * 1000)
}
