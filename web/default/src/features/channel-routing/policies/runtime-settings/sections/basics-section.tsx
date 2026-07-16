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
  RuntimeSelectField,
  RuntimeSettingsSection,
  RuntimeSwitchField,
} from '../runtime-fields'

const modeOptions = [
  { value: 'observe', label: 'Observe only' },
  { value: 'shadow', label: 'Shadow routing' },
  { value: 'balanced', label: 'Balanced routing' },
  { value: 'enterprise_slo', label: 'Enterprise SLO' },
] as const

export function RuntimeBasicsSection() {
  const { t } = useTranslation()
  const form = useFormContext<RuntimeSettingsFormValues>()
  const enabled = useWatch({ control: form.control, name: 'enabled' })
  const inactiveReason = !enabled
    ? t('Enable channel routing to apply this setting.')
    : undefined

  return (
    <RuntimeSettingsSection
      id='runtime-settings-basics'
      title='Basics and operating mode'
      description='Choose whether the routing runtime participates in requests and how aggressively it may influence selection.'
    >
      <RuntimeSwitchField name='enabled' />
      <RuntimeSwitchField
        name='request_profile_enabled'
        disabled={!enabled}
        inactiveReason={inactiveReason}
      />
      <RuntimeSelectField
        name='mode'
        options={modeOptions}
        disabled={!enabled}
        inactiveReason={inactiveReason}
        onValueChange={(value) => {
          if (value !== 'enterprise_slo') return
          form.setValue('weight_availability', 0.55, {
            shouldDirty: true,
            shouldValidate: true,
          })
          form.setValue('weight_latency', 0.3, {
            shouldDirty: true,
            shouldValidate: true,
          })
          form.setValue('weight_throughput', 0.1, {
            shouldDirty: true,
            shouldValidate: true,
          })
          form.setValue('weight_cost', 0.05, {
            shouldDirty: true,
            shouldValidate: true,
          })
        }}
      />
    </RuntimeSettingsSection>
  )
}
