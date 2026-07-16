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
import { InformationCircleIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

import type { RuntimeSettingsFormValues } from '../lib/runtime-settings'
import { RuntimeNumberField, RuntimeSettingsSection } from '../runtime-fields'

export function RuntimeScoringSection() {
  const { t } = useTranslation()
  const form = useFormContext<RuntimeSettingsFormValues>()
  const enabled = useWatch({ control: form.control, name: 'enabled' })
  const mode = useWatch({ control: form.control, name: 'mode' })
  const availability = useWatch({
    control: form.control,
    name: 'weight_availability',
  })
  const latency = useWatch({ control: form.control, name: 'weight_latency' })
  const throughput = useWatch({
    control: form.control,
    name: 'weight_throughput',
  })
  const cost = useWatch({ control: form.control, name: 'weight_cost' })
  const enterprise = mode === 'enterprise_slo'
  const total = availability + latency + throughput + cost
  const normalized =
    total > 0 && Number.isFinite(total)
      ? [
          availability / total,
          latency / total,
          throughput / total,
          cost / total,
        ]
      : [0, 0, 0, 0]
  const inactiveReason = !enabled
    ? t('Enable channel routing to apply this setting.')
    : undefined
  const weightReason = enterprise
    ? t('Enterprise SLO fixes these weights at 55/30/10/5.')
    : inactiveReason

  return (
    <RuntimeSettingsSection
      id='runtime-settings-scoring'
      title='Scoring and candidates'
      description='Control candidate breadth, evidence requirements, and how the runtime balances availability, latency, throughput, and cost.'
    >
      <Alert className='md:col-span-2'>
        <HugeiconsIcon icon={InformationCircleIcon} aria-hidden='true' />
        <AlertTitle>
          {enterprise
            ? t('Enterprise SLO weights are locked')
            : t('Normalized routing weights')}
        </AlertTitle>
        <AlertDescription>
          {enterprise
            ? t('Availability 55% · latency 30% · throughput 10% · cost 5%')
            : t(
                'Availability {{availability}}% · latency {{latency}}% · throughput {{throughput}}% · cost {{cost}}%',
                {
                  availability: (normalized[0] * 100).toFixed(1),
                  latency: (normalized[1] * 100).toFixed(1),
                  throughput: (normalized[2] * 100).toFixed(1),
                  cost: (normalized[3] * 100).toFixed(1),
                }
              )}
        </AlertDescription>
      </Alert>
      <RuntimeNumberField
        name='weight_availability'
        unit='weight'
        min={0}
        max={1}
        step={0.01}
        disabled={!enabled || enterprise}
        inactiveReason={weightReason}
      />
      <RuntimeNumberField
        name='weight_latency'
        unit='weight'
        min={0}
        max={1}
        step={0.01}
        disabled={!enabled || enterprise}
        inactiveReason={weightReason}
      />
      <RuntimeNumberField
        name='weight_throughput'
        unit='weight'
        min={0}
        max={1}
        step={0.01}
        disabled={!enabled || enterprise}
        inactiveReason={weightReason}
      />
      <RuntimeNumberField
        name='weight_cost'
        unit='weight'
        min={0}
        max={1}
        step={0.01}
        disabled={!enabled || enterprise}
        inactiveReason={weightReason}
      />
      <RuntimeNumberField
        name='availability_floor'
        unit='ratio'
        min={0}
        max={1}
        step={0.01}
        disabled={!enabled}
        inactiveReason={inactiveReason}
      />
      <RuntimeNumberField
        name='min_volume'
        unit='requests'
        min={0}
        disabled={!enabled}
        inactiveReason={inactiveReason}
      />
      <RuntimeNumberField
        name='top_k'
        unit='candidates'
        min={1}
        disabled={!enabled}
        inactiveReason={inactiveReason}
      />
    </RuntimeSettingsSection>
  )
}
