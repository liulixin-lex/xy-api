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
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import type { RuntimeSettingsFormValues } from '../lib/runtime-settings'
import { RuntimeNumberField, RuntimeSettingsSection } from '../runtime-fields'

export function RuntimeRefreshRetentionSection() {
  const { t } = useTranslation()
  const form = useFormContext<RuntimeSettingsFormValues>()
  const enabled = useWatch({ control: form.control, name: 'enabled' })
  const reason = !enabled
    ? t('Enable channel routing to apply this setting.')
    : undefined

  return (
    <RuntimeSettingsSection
      id='runtime-settings-refresh-retention'
      title='Refresh and retention'
      description='Control snapshot freshness, balance reserve, cache refresh, metric aggregation, persistence, and retention windows.'
    >
      <RuntimeNumberField
        name='snapshot_live_sec'
        unit='seconds'
        min={1}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='snapshot_stale_sec'
        unit='seconds'
        min={1}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='balance_margin_usd'
        unit='US dollars'
        min={0}
        step={0.01}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='sync_interval_min'
        unit='minutes'
        min={1}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='hotcache_refresh_sec'
        unit='seconds'
        min={1}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='metric_bucket_sec'
        unit='seconds'
        min={1}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='flush_interval_min'
        unit='minutes'
        min={1}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='retention_days'
        unit='days'
        min={1}
        disabled={!enabled}
        inactiveReason={reason}
      />
    </RuntimeSettingsSection>
  )
}
