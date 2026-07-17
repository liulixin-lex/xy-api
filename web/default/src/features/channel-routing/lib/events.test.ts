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

import { channelRoutingQueryKeys } from '../api/query-keys'
import {
  channelRoutingEventNames,
  getChannelRoutingEventResources,
  getChannelRoutingReadyCursor,
  getChannelRoutingRetryDelayMs,
  inspectChannelRoutingEventSequence,
  parseChannelRoutingEvent,
} from './events'

const nodeEpochId = '0123456789abcdef0123456789abcdef'

function event(sequence: number, type = 'routing.policy.published') {
  return {
    id: `${nodeEpochId}:${sequence}`,
    sequence,
    node_epoch_id: nodeEpochId,
    type,
    revision: 42,
    created_time_ms: 1_700_000_000_000,
    payload: {},
  }
}

describe('channel routing event contract', () => {
  test('rejects a cursor that disagrees with the envelope sequence', () => {
    assert.equal(
      parseChannelRoutingEvent(
        JSON.stringify({ ...event(2), id: `${nodeEpochId}:1` })
      ),
      null
    )
  })

  test('detects duplicates, gaps, and node epoch changes', () => {
    const first = inspectChannelRoutingEventSequence(null, event(7))
    assert.deepEqual(first, {
      cursor: { nodeEpochId, sequence: 7 },
      duplicate: false,
      gap: false,
    })
    assert.equal(
      inspectChannelRoutingEventSequence(first.cursor, event(7)).duplicate,
      true
    )
    assert.equal(
      inspectChannelRoutingEventSequence(first.cursor, event(9)).gap,
      true
    )
    assert.equal(
      inspectChannelRoutingEventSequence(first.cursor, {
        ...event(1),
        id: 'fedcba9876543210fedcba9876543210:1',
        node_epoch_id: 'fedcba9876543210fedcba9876543210',
      }).gap,
      true
    )
  })

  test('maps every subscribed state event to its affected query resources', () => {
    const expected = new Map<string, string[]>([
      ['routing.ready', []],
      ['routing.reset', ['all']],
      ['reset', ['all']],
      ['routing.policy_draft.changed', ['policy-drafts']],
      ['routing.policy_simulation.completed', ['policy-drafts', 'operations']],
      [
        'routing.policy.published',
        ['overview', 'groups', 'policy-drafts', 'policies', 'operations'],
      ],
      [
        'routing.policy.rolled_back',
        ['overview', 'groups', 'policy-drafts', 'policies', 'operations'],
      ],
      [
        'policy.published',
        ['overview', 'groups', 'policy-drafts', 'policies', 'operations'],
      ],
      [
        'policy.rolled_back',
        ['overview', 'groups', 'policy-drafts', 'policies', 'operations'],
      ],
      [
        'routing.policy.applied',
        [
          'overview',
          'nodes',
          'groups',
          'channels',
          'endpoints',
          'costs',
          'policies',
        ],
      ],
      [
        'routing.runtime_settings.changed',
        ['overview', 'runtime-settings', 'control-audits'],
      ],
      [
        'routing.channel_configuration.changed',
        [
          'overview',
          'channels',
          'costs',
          'channel-configurations',
          'decisions',
          'control-audits',
        ],
      ],
      [
        'routing.pricing.changed',
        [
          'overview',
          'groups',
          'channels',
          'costs',
          'channel-configurations',
          'decisions',
          'control-audits',
        ],
      ],
      [
        'routing.probe.completed',
        ['overview', 'groups', 'channels', 'endpoints', 'probes', 'operations'],
      ],
      [
        'probe.completed',
        ['overview', 'groups', 'channels', 'endpoints', 'probes', 'operations'],
      ],
      ['routing.audit_export.ready', ['audit-exports', 'operations']],
      ['audit_export.ready', ['audit-exports', 'operations']],
      ['routing.error_budget.changed', ['overview', 'groups']],
      ['error_budget.changed', ['overview', 'groups']],
      [
        'routing.breaker.reset',
        ['overview', 'groups', 'channels', 'endpoints', 'probes', 'operations'],
      ],
      [
        'routing.breaker.opened',
        ['overview', 'groups', 'channels', 'endpoints', 'probes'],
      ],
      [
        'routing.breaker.recovered',
        ['overview', 'groups', 'channels', 'endpoints', 'probes'],
      ],
    ])

    assert.deepEqual(
      [...channelRoutingEventNames].sort(),
      [...expected.keys()].sort()
    )
    for (const [eventType, resources] of expected) {
      assert.deepEqual(getChannelRoutingEventResources(eventType), resources)
    }
  })

  test('keeps the cost catalog under the costs event invalidation root', () => {
    assert.deepEqual(
      channelRoutingQueryKeys
        .costCatalogRoot()
        .slice(0, channelRoutingQueryKeys.costsRoot().length),
      channelRoutingQueryKeys.costsRoot()
    )
  })

  test('resumes from the ready cursor and honors bounded retry headers', () => {
    const ready = {
      ...event(0, 'routing.ready'),
      payload: { latest_id: `${nodeEpochId}:41` },
    }
    assert.deepEqual(getChannelRoutingReadyCursor(ready), {
      nodeEpochId,
      sequence: 41,
    })
    assert.equal(
      getChannelRoutingRetryDelayMs({ 'retry-after': ['7'] }, 3_000),
      7_000
    )
    assert.equal(
      getChannelRoutingRetryDelayMs({ 'retry-after': 'invalid' }, 3_000),
      3_000
    )
  })
})
