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

function toMilliseconds(timestamp: number): number {
  if (!Number.isFinite(timestamp) || timestamp <= 0) return 0
  return timestamp > 10_000_000_000 ? timestamp : timestamp * 1000
}

export function useBillingOperationsFormatters() {
  const { i18n, t } = useTranslation()

  return useMemo(() => {
    const locale = toIntlLocale(i18n.resolvedLanguage ?? i18n.language) ?? 'en'
    const number = new Intl.NumberFormat(locale, { maximumFractionDigits: 2 })
    const dateTime = new Intl.DateTimeFormat(locale, {
      dateStyle: 'medium',
      timeStyle: 'short',
    })
    return {
      number: (value: number) =>
        Number.isFinite(value) ? number.format(value) : t('Unknown'),
      timestamp: (value: number | undefined) => {
        const milliseconds = toMilliseconds(value ?? 0)
        return milliseconds > 0
          ? dateTime.format(new Date(milliseconds))
          : t('Never')
      },
    }
  }, [i18n.language, i18n.resolvedLanguage, t])
}
