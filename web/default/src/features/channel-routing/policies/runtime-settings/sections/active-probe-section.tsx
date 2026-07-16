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

export function RuntimeActiveProbeSection() {
  const { t } = useTranslation()
  const form = useFormContext<RuntimeSettingsFormValues>()
  const [enabled, probesEnabled] = useWatch({
    control: form.control,
    name: ['enabled', 'active_probe_enabled'],
  })
  const runtimeReason = !enabled
    ? t('Enable channel routing to apply this setting.')
    : undefined
  let childReason: string | undefined
  if (!enabled) {
    childReason = runtimeReason
  } else if (!probesEnabled) {
    childReason = t('Enable active probes to apply this setting.')
  }

  return (
    <RuntimeSettingsSection
      id='runtime-settings-active-probe'
      title='Active probes'
      description='Schedule bounded health probes with separate healthy, degraded, and open-state cadence plus explicit cost controls.'
    >
      <RuntimeSwitchField
        name='active_probe_enabled'
        disabled={!enabled}
        inactiveReason={runtimeReason}
      />
      <RuntimeNumberField
        name='active_probe_healthy_sec'
        unit='seconds'
        min={1}
        max={86_400}
        disabled={!enabled || !probesEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='active_probe_degraded_sec'
        unit='seconds'
        min={1}
        max={86_400}
        disabled={!enabled || !probesEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='active_probe_open_sec'
        unit='seconds'
        min={1}
        max={86_400}
        disabled={!enabled || !probesEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='active_probe_timeout_ms'
        unit='milliseconds'
        min={1}
        max={120_000}
        disabled={!enabled || !probesEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='active_probe_max_targets'
        unit='targets'
        min={1}
        max={4_096}
        disabled={!enabled || !probesEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='active_probe_concurrency'
        unit='requests'
        min={1}
        max={64}
        disabled={!enabled || !probesEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='active_probe_per_host'
        unit='requests per host'
        min={1}
        max={64}
        disabled={!enabled || !probesEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='active_probe_token_budget'
        unit='tokens'
        min={1}
        max={1_000_000_000}
        disabled={!enabled || !probesEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='active_probe_cost_budget_usd'
        unit='US dollars'
        min={0.01}
        max={1_000}
        step={0.01}
        disabled={!enabled || !probesEnabled}
        inactiveReason={childReason}
      />
    </RuntimeSettingsSection>
  )
}
