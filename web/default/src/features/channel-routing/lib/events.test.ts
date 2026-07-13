import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
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

  test('keeps probe events scoped to health evidence queries', () => {
    assert.deepEqual(
      getChannelRoutingEventResources('routing.probe.completed'),
      ['probes', 'endpoints']
    )
    assert.deepEqual(getChannelRoutingEventResources('routing.reset'), ['all'])
    assert.deepEqual(
      getChannelRoutingEventResources('routing.cost_sync.queued'),
      ['operations']
    )
    assert.deepEqual(
      getChannelRoutingEventResources('routing.error_budget.changed'),
      ['overview', 'groups']
    )
    assert.deepEqual(getChannelRoutingEventResources('routing.breaker.reset'), [
      'overview',
      'groups',
      'endpoints',
      'probes',
      'operations',
    ])
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
