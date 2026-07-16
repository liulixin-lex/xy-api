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
  RuntimeSettingsSection,
  RuntimeSwitchField,
  RuntimeTextField,
} from '../runtime-fields'

export function RuntimeAgentSection() {
  const { t } = useTranslation()
  const form = useFormContext<RuntimeSettingsFormValues>()
  const [enabled, agentEnabled] = useWatch({
    control: form.control,
    name: ['enabled', 'agent_enabled'],
  })
  const runtimeReason = !enabled
    ? t('Enable channel routing to apply this setting.')
    : undefined
  let childReason: string | undefined
  if (!enabled) {
    childReason = runtimeReason
  } else if (!agentEnabled) {
    childReason = t('Enable the routing agent to apply this setting.')
  }

  return (
    <RuntimeSettingsSection
      id='runtime-settings-agent'
      title='Agent'
      description='Configure the optional routing agent and whether its recommendations may be applied automatically.'
    >
      <RuntimeSwitchField
        name='agent_enabled'
        disabled={!enabled}
        inactiveReason={runtimeReason}
      />
      <RuntimeSwitchField
        name='agent_auto_apply'
        disabled={!enabled || !agentEnabled}
        inactiveReason={childReason}
      />
      <RuntimeTextField
        name='agent_model'
        disabled={!enabled || !agentEnabled}
        inactiveReason={childReason}
      />
    </RuntimeSettingsSection>
  )
}
