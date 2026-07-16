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

export function RuntimeBreakerSection() {
  const { t } = useTranslation()
  const form = useFormContext<RuntimeSettingsFormValues>()
  const enabled = useWatch({ control: form.control, name: 'enabled' })
  const reason = !enabled
    ? t('Enable channel routing to apply this setting.')
    : undefined

  return (
    <RuntimeSettingsSection
      id='runtime-settings-breaker'
      title='Circuit breaking and recovery'
      description='Define when unhealthy capacity is ejected, how much capacity may be removed, and how recovery probes are admitted.'
    >
      <RuntimeNumberField
        name='consecutive_5xx'
        unit='errors'
        min={1}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='failure_rate_pct'
        unit='percent'
        min={1}
        max={100}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='base_cooldown_sec'
        unit='seconds'
        min={1}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='max_cooldown_sec'
        unit='seconds'
        min={1}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='max_ejected_pct'
        unit='percent'
        min={0}
        max={100}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='half_open_probes'
        unit='probes'
        min={1}
        disabled={!enabled}
        inactiveReason={reason}
      />
    </RuntimeSettingsSection>
  )
}
