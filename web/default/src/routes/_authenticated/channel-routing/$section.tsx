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
import { createFileRoute, redirect } from '@tanstack/react-router'
import z from 'zod'

import { ChannelRoutingSectionPage } from '@/features/channel-routing'
import { CHANNEL_ROUTING_CHANNEL_TABS } from '@/features/channel-routing/channels/tabs'
import { isChannelRoutingPageSize } from '@/features/channel-routing/lib/pagination'
import type { ChannelRoutingSection } from '@/features/channel-routing/types'

const sections = new Set<ChannelRoutingSection>([
  'overview',
  'groups',
  'channels',
  'decisions',
  'costs',
  'policies',
])

const triStateSearchSchema = z
  .union([
    z.literal('all'),
    z.boolean(),
    z.enum(['true', 'false']).transform((value) => value === 'true'),
  ])
  .optional()
  .catch('all')

const searchSchema = z.object({
  page: z.number().int().min(1).optional().catch(1),
  pageSize: z
    .number()
    .int()
    .refine(isChannelRoutingPageSize)
    .optional()
    .catch(20),
  search: z.string().max(256).optional().catch(''),
  status: z.number().int().optional().catch(undefined),
  type: z.number().int().optional().catch(undefined),
  channelTab: z
    .enum(CHANNEL_ROUTING_CHANNEL_TABS)
    .optional()
    .catch('physical-channels'),
  group: z.string().max(64).optional().catch(''),
  model: z.string().max(128).optional().catch(''),
  known: triStateSearchSchema,
  costTab: z
    .enum(['channel-multipliers', 'effective-costs'])
    .optional()
    .catch('channel-multipliers'),
  policyTab: z
    .enum(['runtime-settings', 'versioned-policies', 'operations-audits'])
    .optional()
    .catch('runtime-settings'),
  costConfirmed: triStateSearchSchema,
  costSource: z
    .enum(['all', 'manual', 'legacy_migrated', 'defaulted'])
    .optional()
    .catch('all'),
  trafficClass: z
    .enum(['any', 'all', 'claude_code_only'])
    .optional()
    .catch('any'),
  cursor: z.number().int().min(0).optional().catch(0),
  draftCursor: z.number().int().min(0).optional().catch(0),
  limit: z.number().int().min(1).max(100).optional().catch(20),
  requestId: z.string().max(64).optional().catch(''),
  matched: triStateSearchSchema,
  activationId: z.number().int().min(1).optional().catch(undefined),
  cohort: z.enum(['all', 'control', 'canary']).optional().catch('all'),
  fromTime: z.number().int().min(1).optional().catch(undefined),
  toTime: z.number().int().min(1).optional().catch(undefined),
  endpointPage: z.number().int().min(1).optional().catch(1),
  endpointPageSize: z
    .number()
    .int()
    .refine(isChannelRoutingPageSize)
    .optional()
    .catch(20),
  endpointSearch: z.string().max(320).optional().catch(''),
  endpointRegion: z.string().max(64).optional().catch(''),
  probeCursor: z.number().int().min(0).optional().catch(0),
  probeLimit: z
    .number()
    .int()
    .refine(isChannelRoutingPageSize)
    .optional()
    .catch(20),
  probeOutcome: z
    .enum(['all', 'success', 'failure', 'timeout', 'canceled', 'local_error'])
    .optional()
    .catch('all'),
  probeChannelId: z.number().int().min(1).optional().catch(undefined),
  operationCursor: z.number().int().min(0).optional().catch(0),
  operationType: z
    .enum([
      '',
      'canary_auto_rollback',
      'policy_simulation',
      'historical_simulation',
      'policy_publish',
      'policy_manual_rollback',
      'cost_sync',
      'active_probe',
      'audit_export',
      'breaker_reset',
    ])
    .optional()
    .catch(''),
  operationStatus: z
    .enum(['', 'pending', 'running', 'succeeded', 'failed', 'superseded'])
    .optional()
    .catch(''),
})

export const Route = createFileRoute(
  '/_authenticated/channel-routing/$section'
)({
  beforeLoad: ({ params }) => {
    if (!sections.has(params.section as ChannelRoutingSection)) {
      throw redirect({
        to: '/channel-routing/$section',
        params: { section: 'overview' },
      })
    }
  },
  validateSearch: searchSchema,
  component: ChannelRoutingSectionPage,
})
