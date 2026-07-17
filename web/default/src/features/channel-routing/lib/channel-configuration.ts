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
import type { TFunction } from 'i18next'
import z from 'zod'

import type {
  RoutingChannelConfiguration,
  RoutingChannelConfigurationTrafficClass,
  RoutingChannelConfigurationUpdate,
} from '../types'

export const CHANNEL_MULTIPLIER_MAXIMUM = 1000
export const FAILURE_DOMAIN_LABEL_MAXIMUM = 128

export type ChannelConfigurationFormValues = {
  upstreamCostMultiplier: string
  trafficClass: RoutingChannelConfigurationTrafficClass
  failureDomainLabel: string
  clearFailureDomain: boolean
}

export type ChannelConfigurationConflictSummary = {
  serverChangedLabels: string[]
  overlappingLabels: string[]
}

export function channelConfigurationFormFieldForApiField(
  field: string | undefined
): keyof ChannelConfigurationFormValues | null {
  switch (field) {
    case 'upstream_cost_multiplier':
      return 'upstreamCostMultiplier'
    case 'traffic_class':
      return 'trafficClass'
    case 'failure_domain_label':
      return 'failureDomainLabel'
    case 'clear_failure_domain':
      return 'clearFailureDomain'
    default:
      return null
  }
}

function normalizedFailureDomainLabel(value: string): string {
  return value.trim().replaceAll(/\s+/gu, ' ')
}

function multiplierFromValue(value: string): number {
  return Number(value.trim())
}

export function createChannelConfigurationSchema(t: TFunction) {
  return z
    .object({
      upstreamCostMultiplier: z
        .string()
        .trim()
        .min(1, t('Enter a channel multiplier.'))
        .refine(
          (value) => Number.isFinite(multiplierFromValue(value)),
          t('Enter a finite channel multiplier.')
        )
        .refine((value) => {
          const multiplier = multiplierFromValue(value)
          return (
            Number.isFinite(multiplier) &&
            multiplier >= 0 &&
            multiplier <= CHANNEL_MULTIPLIER_MAXIMUM
          )
        }, t('Channel multiplier must be between 0 and 1000.')),
      trafficClass: z.enum(['all', 'claude_code_only']),
      failureDomainLabel: z
        .string()
        .refine(
          (value) =>
            [...normalizedFailureDomainLabel(value)].length <=
            FAILURE_DOMAIN_LABEL_MAXIMUM,
          t('Failure domain labels can contain at most 128 characters.')
        ),
      clearFailureDomain: z.boolean(),
    })
    .superRefine((values, context) => {
      if (
        values.clearFailureDomain &&
        normalizedFailureDomainLabel(values.failureDomainLabel)
      ) {
        context.addIssue({
          code: 'custom',
          path: ['failureDomainLabel'],
          message: t('Remove the label before clearing the failure domain.'),
        })
      }
    })
}

export function channelConfigurationFormValues(
  configuration: RoutingChannelConfiguration
): ChannelConfigurationFormValues {
  return {
    upstreamCostMultiplier: String(configuration.upstream_cost_multiplier),
    trafficClass: configuration.traffic_class,
    failureDomainLabel: configuration.failure_domain_label,
    clearFailureDomain: false,
  }
}

export function channelConfigurationRequest(
  values: ChannelConfigurationFormValues
): RoutingChannelConfigurationUpdate {
  return {
    upstream_cost_multiplier: multiplierFromValue(
      values.upstreamCostMultiplier
    ),
    traffic_class: values.trafficClass,
    failure_domain_label: values.clearFailureDomain
      ? ''
      : normalizedFailureDomainLabel(values.failureDomainLabel),
    clear_failure_domain: values.clearFailureDomain,
  }
}

export function channelConfigurationConflictSummary(input: {
  baseline: RoutingChannelConfiguration
  latest: RoutingChannelConfiguration
  draft: ChannelConfigurationFormValues
}): ChannelConfigurationConflictSummary {
  const baseline = channelConfigurationFormValues(input.baseline)
  const latest = channelConfigurationFormValues(input.latest)
  const fields = [
    {
      label: 'Channel multiplier',
      baseline: multiplierFromValue(baseline.upstreamCostMultiplier),
      latest: multiplierFromValue(latest.upstreamCostMultiplier),
      draft: multiplierFromValue(input.draft.upstreamCostMultiplier),
    },
    {
      label: 'Traffic class',
      baseline: baseline.trafficClass,
      latest: latest.trafficClass,
      draft: input.draft.trafficClass,
    },
    {
      label: 'Failure domain',
      baseline: `${input.baseline.failure_domain_status}:${normalizedFailureDomainLabel(
        baseline.failureDomainLabel
      )}`,
      latest: `${input.latest.failure_domain_status}:${normalizedFailureDomainLabel(
        latest.failureDomainLabel
      )}`,
      draft: input.draft.clearFailureDomain
        ? 'unconfigured:'
        : `configured:${normalizedFailureDomainLabel(
            input.draft.failureDomainLabel
          )}`,
    },
  ]

  const serverChangedLabels: string[] = []
  const overlappingLabels: string[] = []
  for (const field of fields) {
    if (field.latest === field.baseline) continue
    serverChangedLabels.push(field.label)
    if (field.draft !== field.baseline && field.draft !== field.latest) {
      overlappingLabels.push(field.label)
    }
  }
  return { serverChangedLabels, overlappingLabels }
}
