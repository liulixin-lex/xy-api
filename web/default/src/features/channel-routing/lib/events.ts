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

export const channelRoutingEventNames = [
  'routing.ready',
  'routing.reset',
  'routing.policy_draft.changed',
  'routing.policy_simulation.completed',
  'routing.policy.published',
  'routing.policy.rolled_back',
  'routing.policy.applied',
  'routing.cost_sync.queued',
  'routing.cost_sync.completed',
  'routing.probe.completed',
  'routing.audit_export.ready',
  'routing.error_budget.changed',
  'routing.breaker.reset',
  'reset',
  'policy.published',
  'policy.rolled_back',
  'cost_sync.completed',
  'probe.completed',
  'audit_export.ready',
  'error_budget.changed',
] as const

export type ChannelRoutingEventResource =
  | 'all'
  | 'overview'
  | 'nodes'
  | 'groups'
  | 'channels'
  | 'endpoints'
  | 'costs'
  | 'probes'
  | 'decisions'
  | 'policy-drafts'
  | 'policies'
  | 'operations'
  | 'audit-exports'

export type ChannelRoutingEventEnvelope = {
  id: string
  sequence: number
  node_epoch_id: string
  type: string
  revision?: number
  created_time_ms: number
  payload: Record<string, unknown>
}

export type ChannelRoutingEventCursor = {
  nodeEpochId: string
  sequence: number
}

const eventCursorPattern = /^([0-9a-f]{32}):(0|[1-9][0-9]*)$/

export function parseChannelRoutingEvent(
  data: string
): ChannelRoutingEventEnvelope | null {
  try {
    const value = JSON.parse(data) as Partial<ChannelRoutingEventEnvelope>
    if (
      typeof value.id !== 'string' ||
      typeof value.sequence !== 'number' ||
      !Number.isSafeInteger(value.sequence) ||
      value.sequence < 0 ||
      typeof value.node_epoch_id !== 'string' ||
      !/^[0-9a-f]{32}$/.test(value.node_epoch_id) ||
      typeof value.type !== 'string' ||
      typeof value.created_time_ms !== 'number' ||
      !Number.isFinite(value.created_time_ms) ||
      value.created_time_ms <= 0 ||
      value.payload == null ||
      typeof value.payload !== 'object' ||
      Array.isArray(value.payload)
    ) {
      return null
    }
    const cursor = parseChannelRoutingEventCursor(value.id)
    if (
      cursor == null ||
      cursor.nodeEpochId !== value.node_epoch_id ||
      cursor.sequence !== value.sequence
    ) {
      return null
    }
    return value as ChannelRoutingEventEnvelope
  } catch {
    return null
  }
}

export function parseChannelRoutingEventCursor(
  value: string
): ChannelRoutingEventCursor | null {
  const match = eventCursorPattern.exec(value)
  if (!match) return null
  const sequence = Number(match[2])
  if (!Number.isSafeInteger(sequence)) return null
  return { nodeEpochId: match[1], sequence }
}

export function getChannelRoutingReadyCursor(
  event: ChannelRoutingEventEnvelope
): ChannelRoutingEventCursor | null {
  if (event.type !== 'routing.ready') return null
  const latestId = event.payload.latest_id
  if (typeof latestId !== 'string') return null
  const cursor = parseChannelRoutingEventCursor(latestId)
  if (!cursor || cursor.nodeEpochId !== event.node_epoch_id) return null
  return cursor
}

export function getChannelRoutingRetryDelayMs(
  headers: Record<string, string | string[] | undefined>,
  fallbackMs: number
): number {
  const rawValue = headers['retry-after']
  const value = Array.isArray(rawValue) ? rawValue[0] : rawValue
  const seconds = Number(value)
  if (!Number.isFinite(seconds) || seconds <= 0) return fallbackMs
  return Math.min(300_000, Math.max(1_000, Math.round(seconds * 1_000)))
}

export function inspectChannelRoutingEventSequence(
  previous: ChannelRoutingEventCursor | null,
  event: ChannelRoutingEventEnvelope
): {
  cursor: ChannelRoutingEventCursor | null
  duplicate: boolean
  gap: boolean
} {
  if (event.type === 'routing.ready') {
    return { cursor: previous, duplicate: false, gap: false }
  }
  const cursor = {
    nodeEpochId: event.node_epoch_id,
    sequence: event.sequence,
  }
  if (!previous) return { cursor, duplicate: false, gap: false }
  if (previous.nodeEpochId !== cursor.nodeEpochId) {
    return { cursor, duplicate: false, gap: true }
  }
  if (cursor.sequence <= previous.sequence) {
    return { cursor: previous, duplicate: true, gap: false }
  }
  return {
    cursor,
    duplicate: false,
    gap: cursor.sequence !== previous.sequence + 1,
  }
}

export function getChannelRoutingEventResources(
  eventType: string
): ChannelRoutingEventResource[] {
  switch (eventType) {
    case 'routing.reset':
    case 'reset':
      return ['all']
    case 'routing.policy_draft.changed':
      return ['policy-drafts']
    case 'routing.policy_simulation.completed':
      return ['policy-drafts', 'operations']
    case 'routing.policy.published':
    case 'routing.policy.rolled_back':
    case 'policy.published':
    case 'policy.rolled_back':
      return ['overview', 'groups', 'policy-drafts', 'policies', 'operations']
    case 'routing.policy.applied':
      return [
        'overview',
        'nodes',
        'groups',
        'channels',
        'endpoints',
        'costs',
        'policies',
      ]
    case 'routing.cost_sync.queued':
      return ['operations']
    case 'routing.cost_sync.completed':
    case 'cost_sync.completed':
      return ['overview', 'channels', 'costs', 'operations']
    case 'routing.probe.completed':
    case 'probe.completed':
      return ['probes', 'endpoints']
    case 'routing.audit_export.ready':
    case 'audit_export.ready':
      return ['audit-exports', 'operations']
    case 'routing.error_budget.changed':
    case 'error_budget.changed':
      return ['overview', 'groups']
    case 'routing.breaker.reset':
      return ['overview', 'groups', 'endpoints', 'probes', 'operations']
    default:
      return []
  }
}
