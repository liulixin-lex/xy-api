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
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '@/i18n/languages'

const maximumJavaScriptDateMilliseconds = 8_640_000_000_000_000
const neverTimestampSentinel = 9_000_000_000_000_000_000

export type ChannelRoutingTimestampResolution =
  | { kind: 'date'; milliseconds: number }
  | { kind: 'invalid' }
  | { kind: 'never' }

export function resolveChannelRoutingTimestamp(
  timestamp: number
): ChannelRoutingTimestampResolution {
  if (!Number.isFinite(timestamp)) return { kind: 'invalid' }
  if (timestamp <= 0 || timestamp >= neverTimestampSentinel) {
    return { kind: 'never' }
  }
  const milliseconds = timestamp > 10_000_000_000 ? timestamp : timestamp * 1000
  if (
    !Number.isFinite(milliseconds) ||
    milliseconds > maximumJavaScriptDateMilliseconds
  ) {
    return { kind: 'invalid' }
  }
  return { kind: 'date', milliseconds }
}

export function formatChannelRoutingCost(
  value: number,
  locale: string
): string {
  if (!Number.isFinite(value)) return ''
  return new Intl.NumberFormat(channelRoutingIntlLocale(locale), {
    maximumSignificantDigits: 8,
  }).format(value)
}

export function channelRoutingIntlLocale(locale: string): string {
  return toIntlLocale(locale) ?? 'en'
}

export function useChannelRoutingFormatters() {
  const { i18n, t } = useTranslation()

  return useMemo(() => {
    const locale = channelRoutingIntlLocale(
      i18n.resolvedLanguage ?? i18n.language
    )
    const number = new Intl.NumberFormat(locale, {
      maximumFractionDigits: 2,
    })
    const compact = new Intl.NumberFormat(locale, {
      notation: 'compact',
      maximumFractionDigits: 1,
    })
    const percent = new Intl.NumberFormat(locale, {
      style: 'percent',
      maximumFractionDigits: 1,
    })
    const dateTime = new Intl.DateTimeFormat(locale, {
      dateStyle: 'medium',
      timeStyle: 'short',
    })

    return {
      number: (value: number) =>
        Number.isFinite(value) ? number.format(value) : t('Unknown'),
      cost: (value: number) =>
        Number.isFinite(value)
          ? formatChannelRoutingCost(value, locale)
          : t('Unknown'),
      billingMode: (value: string | undefined) => {
        switch (value?.trim().toLowerCase()) {
          case 'token':
            return t('Token-based')
          case 'per_request':
            return t('Per request')
          case 'tiered_expr':
            return t('Tiered pricing')
          default:
            return value?.trim() || t('Unknown')
        }
      },
      compact: (value: number) =>
        Number.isFinite(value) ? compact.format(value) : t('Unknown'),
      percent: (value: number | undefined) =>
        value != null && Number.isFinite(value)
          ? percent.format(Math.max(0, Math.min(1, value)))
          : t('Unknown'),
      milliseconds: (value: number | undefined) =>
        value != null && Number.isFinite(value)
          ? t('{{value}} ms', { value: number.format(value) })
          : t('Unknown'),
      timestamp: (value: number | undefined) => {
        const resolved = resolveChannelRoutingTimestamp(value ?? 0)
        if (resolved.kind === 'never') return t('Never')
        if (resolved.kind === 'invalid') return t('Unknown')
        return dateTime.format(new Date(resolved.milliseconds))
      },
      shortHash: (value: string | undefined) => {
        if (!value) return t('Unknown')
        return value.length > 16
          ? `${value.slice(0, 8)}…${value.slice(-6)}`
          : value
      },
    }
  }, [i18n.language, i18n.resolvedLanguage, t])
}
