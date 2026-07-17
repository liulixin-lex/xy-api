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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import type { TFunction } from 'i18next'

import type { RoutingChannelConfiguration } from '../types'
import {
  channelConfigurationConflictSummary,
  channelConfigurationFormFieldForApiField,
  channelConfigurationFormValues,
  channelConfigurationRequest,
  createChannelConfigurationSchema,
} from './channel-configuration'

const translate = ((key: string) => key) as TFunction

const configuration: RoutingChannelConfiguration = {
  channel_id: 77,
  channel_name: 'Primary upstream',
  upstream_cost_multiplier: 1,
  cost_source: 'legacy_migrated',
  cost_confirmed: false,
  traffic_class: 'all',
  failure_domain_status: 'historical_migrated',
  failure_domain_label: '',
  effective_model_count: 12,
  cost_basis_available: true,
  revision: 4,
  updated_by: 1,
  created_time: 100,
  updated_time: 100,
  etag: '"routing-channel-configuration:77:4"',
}

describe('channel configuration form contract', () => {
  test('accepts explicit zero and preserves the clear flag in the request', () => {
    const schema = createChannelConfigurationSchema(translate)
    const values = {
      ...channelConfigurationFormValues(configuration),
      upstreamCostMultiplier: '0',
      clearFailureDomain: true,
    }

    assert.equal(schema.safeParse(values).success, true)
    assert.deepEqual(channelConfigurationRequest(values), {
      upstream_cost_multiplier: 0,
      traffic_class: 'all',
      failure_domain_label: '',
      clear_failure_domain: true,
    })
  })

  test('rejects non-finite and out-of-range multipliers', () => {
    const schema = createChannelConfigurationSchema(translate)
    const base = channelConfigurationFormValues(configuration)

    for (const upstreamCostMultiplier of [
      'Infinity',
      'NaN',
      '-0.1',
      '1000.1',
    ]) {
      assert.equal(
        schema.safeParse({ ...base, upstreamCostMultiplier }).success,
        false,
        upstreamCostMultiplier
      )
    }
  })

  test('reports a negative multiplier on the multiplier field', () => {
    const schema = createChannelConfigurationSchema(translate)
    const result = schema.safeParse({
      ...channelConfigurationFormValues(configuration),
      upstreamCostMultiplier: '-1',
    })

    assert.equal(result.success, false)
    if (result.success) return
    assert.deepEqual(result.error.issues, [
      {
        code: 'custom',
        path: ['upstreamCostMultiplier'],
        message: 'Channel multiplier must be between 0 and 1000.',
      },
    ])
  })

  test('requires an explicit choice between a label and clearing the domain', () => {
    const schema = createChannelConfigurationSchema(translate)
    const result = schema.safeParse({
      ...channelConfigurationFormValues(configuration),
      failureDomainLabel: 'provider account a',
      clearFailureDomain: true,
    })

    assert.equal(result.success, false)
  })

  test('maps every server validation field to its exact form control', () => {
    assert.deepEqual(
      [
        'upstream_cost_multiplier',
        'traffic_class',
        'failure_domain_label',
        'clear_failure_domain',
        'unknown_field',
      ].map(channelConfigurationFormFieldForApiField),
      [
        'upstreamCostMultiplier',
        'trafficClass',
        'failureDomainLabel',
        'clearFailureDomain',
        null,
      ]
    )
  })

  test('identifies overlapping edits after a stale ETag response', () => {
    const latest = {
      ...configuration,
      upstream_cost_multiplier: 2,
      traffic_class: 'claude_code_only' as const,
      revision: 5,
    }
    const summary = channelConfigurationConflictSummary({
      baseline: configuration,
      latest,
      draft: {
        ...channelConfigurationFormValues(configuration),
        upstreamCostMultiplier: '0.5',
      },
    })

    assert.deepEqual(summary.serverChangedLabels, [
      'Channel multiplier',
      'Traffic class',
    ])
    assert.deepEqual(summary.overlappingLabels, ['Channel multiplier'])
  })
})
