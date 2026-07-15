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
  group: z.string().max(64).optional().catch(''),
  model: z.string().max(128).optional().catch(''),
  known: triStateSearchSchema,
  costView: z.enum(['snapshots', 'sources']).optional().catch('snapshots'),
  sourcePage: z.number().int().min(1).optional().catch(1),
  sourcePageSize: z
    .number()
    .int()
    .refine(isChannelRoutingPageSize)
    .optional()
    .catch(20),
  sourceSearch: z.string().max(256).optional().catch(''),
  sourceType: z.enum(['all', 'newapi', 'sub2api']).optional().catch('all'),
  sourceEnabled: triStateSearchSchema,
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
  operationCursor: z.number().int().min(0).optional().catch(0),
  billingReviewCursor: z
    .number()
    .int()
    .min(0)
    .max(Number.MAX_SAFE_INTEGER)
    .optional()
    .catch(0),
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
