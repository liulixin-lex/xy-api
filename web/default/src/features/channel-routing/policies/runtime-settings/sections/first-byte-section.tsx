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
import {
  RuntimeNumberField,
  RuntimeSettingsSection,
  RuntimeSwitchField,
} from '../runtime-fields'

export function RuntimeFirstByteSection() {
  const { t } = useTranslation()
  const form = useFormContext<RuntimeSettingsFormValues>()
  const [enabled, failoverEnabled] = useWatch({
    control: form.control,
    name: ['enabled', 'first_byte_failover_enabled'],
  })
  const runtimeReason = !enabled
    ? t('Enable channel routing to apply this setting.')
    : undefined
  let childReason: string | undefined
  if (!enabled) {
    childReason = runtimeReason
  } else if (!failoverEnabled) {
    childReason = t('Enable first-byte failover to apply this setting.')
  }

  return (
    <RuntimeSettingsSection
      id='runtime-settings-first-byte'
      title='First-byte switching'
      description='Switch away from a slow upstream before response bytes are committed, using bounded latency thresholds.'
    >
      <RuntimeSwitchField
        name='first_byte_failover_enabled'
        disabled={!enabled}
        inactiveReason={runtimeReason}
      />
      <RuntimeNumberField
        name='first_byte_min_ms'
        unit='milliseconds'
        min={1}
        disabled={!enabled || !failoverEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='first_byte_cap_ms'
        unit='milliseconds'
        min={1}
        disabled={!enabled || !failoverEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='first_byte_p95_multiplier'
        unit='multiplier'
        min={0.01}
        step={0.1}
        disabled={!enabled || !failoverEnabled}
        inactiveReason={childReason}
      />
    </RuntimeSettingsSection>
  )
}
