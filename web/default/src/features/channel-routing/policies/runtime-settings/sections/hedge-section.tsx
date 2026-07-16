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

export function RuntimeHedgeSection() {
  const { t } = useTranslation()
  const form = useFormContext<RuntimeSettingsFormValues>()
  const [enabled, hedgeEnabled] = useWatch({
    control: form.control,
    name: ['enabled', 'hedge_enabled'],
  })
  const runtimeReason = !enabled
    ? t('Enable channel routing to apply this setting.')
    : undefined
  let childReason: string | undefined
  if (!enabled) {
    childReason = runtimeReason
  } else if (!hedgeEnabled) {
    childReason = t('Enable hedge requests to apply this setting.')
  }

  return (
    <RuntimeSettingsSection
      id='runtime-settings-hedge'
      title='Hedge'
      description='Bound speculative duplicate requests, response buffering, additional traffic, and audit retention.'
    >
      <RuntimeSwitchField
        name='hedge_enabled'
        disabled={!enabled}
        inactiveReason={runtimeReason}
      />
      <RuntimeNumberField
        name='hedge_max_concurrent'
        unit='requests'
        min={1}
        max={128}
        disabled={!enabled || !hedgeEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='hedge_max_response_bytes'
        unit='bytes'
        min={64 << 10}
        max={64 << 20}
        disabled={!enabled || !hedgeEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='hedge_max_buffered_bytes'
        unit='bytes'
        min={128 << 10}
        max={1 << 30}
        disabled={!enabled || !hedgeEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='hedge_ratio_window_sec'
        unit='seconds'
        min={1}
        max={3_600}
        disabled={!enabled || !hedgeEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='hedge_max_extra_basis_points'
        unit='basis points'
        min={1}
        max={10_000}
        disabled={!enabled || !hedgeEnabled}
        inactiveReason={childReason}
      />
      <RuntimeNumberField
        name='hedge_audit_retention_days'
        unit='days'
        min={1}
        max={365}
        disabled={!enabled || !hedgeEnabled}
        inactiveReason={childReason}
      />
    </RuntimeSettingsSection>
  )
}
