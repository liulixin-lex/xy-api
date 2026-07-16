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

export function RuntimeRetrySection() {
  const { t } = useTranslation()
  const form = useFormContext<RuntimeSettingsFormValues>()
  const enabled = useWatch({ control: form.control, name: 'enabled' })
  const reason = !enabled
    ? t('Enable channel routing to apply this setting.')
    : undefined

  return (
    <RuntimeSettingsSection
      id='runtime-settings-retry'
      title='Retries and failover'
      description='Bound route switching, retry budgets, cost amplification, deadlines, and status-specific backoff behavior.'
    >
      <RuntimeNumberField
        name='max_switches'
        unit='switches'
        min={0}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='retry_token_capacity'
        unit='tokens'
        min={1}
        max={1_000_000}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='retry_token_refill_per_sec'
        unit='tokens per second'
        min={0.001}
        max={1_000_000}
        step={0.1}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='failover_deadline_ms'
        unit='milliseconds'
        min={1}
        max={600_000}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='retry_extra_cost_multiplier'
        unit='multiplier'
        min={0.01}
        max={16}
        step={0.1}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='backoff_base_ms_5xx'
        unit='milliseconds'
        min={1}
        max={600_000}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='backoff_base_ms_429'
        unit='milliseconds'
        min={1}
        max={600_000}
        disabled={!enabled}
        inactiveReason={reason}
      />
      <RuntimeNumberField
        name='backoff_cap_ms'
        unit='milliseconds'
        min={1}
        max={600_000}
        disabled={!enabled}
        inactiveReason={reason}
      />
    </RuntimeSettingsSection>
  )
}
