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
import z from 'zod'

import type {
  RoutingCostBinding,
  RoutingCostBindingActionResult,
  RoutingCostBindingCredentials,
  RoutingCostBindingGroupMetadata,
  RoutingCostBindingRequest,
  RoutingCostBindingUpstreamType,
} from '../types'

type Translate = (key: string, options?: Record<string, unknown>) => string

export type CostBindingFormValues = {
  channelId: string
  upstreamType: 'newapi' | 'sub2api'
  baseUrl: string
  upstreamGroup: string
  servesClaudeCode: boolean
  egressAllowedPrivateCidrs: string
  newApiUserId: string
  enabled: boolean
  newApiAccessToken: string
  gatewayApiKey: string
  sub2apiEmail: string
  sub2apiPassword: string
  sub2apiToken: string
  customCaPem: string
  clearNewApiAccessToken: boolean
  clearGatewayApiKey: boolean
  clearSub2apiEmail: boolean
  clearSub2apiPassword: boolean
  clearSub2apiToken: boolean
  clearCustomCaPem: boolean
}

export const emptyCostBindingFormValues: CostBindingFormValues = {
  channelId: '',
  upstreamType: 'newapi',
  baseUrl: '',
  upstreamGroup: 'default',
  servesClaudeCode: false,
  egressAllowedPrivateCidrs: '',
  newApiUserId: '',
  enabled: true,
  newApiAccessToken: '',
  gatewayApiKey: '',
  sub2apiEmail: '',
  sub2apiPassword: '',
  sub2apiToken: '',
  customCaPem: '',
  clearNewApiAccessToken: false,
  clearGatewayApiKey: false,
  clearSub2apiEmail: false,
  clearSub2apiPassword: false,
  clearSub2apiToken: false,
  clearCustomCaPem: false,
}

function isPositiveInteger(value: string): boolean {
  if (!/^\d+$/.test(value)) return false
  const number = Number(value)
  return Number.isSafeInteger(number) && number > 0
}

function isValidRoutingCostBaseUrl(value: string): boolean {
  try {
    if (value.includes('?') || value.includes('#')) return false
    const url = new URL(value)
    if (url.protocol !== 'https:' || !url.hostname) return false
    if (url.username || url.password) return false
    return true
  } catch {
    return false
  }
}

export function parseCostBindingEgressCidrs(value: string): string[] {
  return value
    .split(/\r?\n/)
    .map((cidr) => cidr.trim())
    .filter(Boolean)
}

function isAllowedPrivateCidr(value: string): boolean {
  const parts = value.split('/')
  if (parts.length !== 2 || !/^\d+$/.test(parts[1] ?? '')) return false
  const address = parts[0] ?? ''
  const prefixLength = Number(parts[1])

  if (address.includes(':')) {
    if (prefixLength < 7 || prefixLength > 128 || /^::ffff:/i.test(address)) {
      return false
    }
    try {
      const parsed = new URL(`http://[${address}]/`)
      if (!parsed.hostname) return false
    } catch {
      return false
    }
    const firstHextet = Number.parseInt(address.split(':')[0] ?? '', 16)
    return Number.isInteger(firstHextet) && (firstHextet & 0xfe00) === 0xfc00
  }

  if (prefixLength < 0 || prefixLength > 32) return false
  const octets = address.split('.')
  if (
    octets.length !== 4 ||
    octets.some(
      (octet) => !/^(0|[1-9]\d{0,2})$/.test(octet) || Number(octet) > 255
    )
  ) {
    return false
  }
  const numbers = octets.map(Number)
  if (numbers[0] === 10) return prefixLength >= 8
  if (numbers[0] === 172 && numbers[1] >= 16 && numbers[1] <= 31) {
    return prefixLength >= 12
  }
  return numbers[0] === 192 && numbers[1] === 168 && prefixLength >= 16
}

export function createCostBindingSchema(t: Translate) {
  const schema = z.object({
    channelId: z
      .string()
      .trim()
      .min(1, t('Channel is required'))
      .refine(isPositiveInteger, t('Enter a valid positive channel ID')),
    upstreamType: z.enum(['newapi', 'sub2api']),
    baseUrl: z
      .string()
      .trim()
      .min(1, t('Base URL is required'))
      .max(512, t('Base URL must be 512 characters or fewer'))
      .refine(
        isValidRoutingCostBaseUrl,
        t(
          'Enter a valid HTTPS URL without credentials, query parameters, or a fragment.'
        )
      ),
    upstreamGroup: z
      .string()
      .trim()
      .min(1, t('Upstream group is required'))
      .max(128, t('Upstream group must be 128 characters or fewer')),
    servesClaudeCode: z.boolean(),
    egressAllowedPrivateCidrs: z
      .string()
      .max(2080, t('Private egress policy is too large'))
      .refine((value) => {
        const cidrs = parseCostBindingEgressCidrs(value)
        return (
          cidrs.length <= 32 &&
          cidrs.every((cidr) => cidr.length <= 64 && isAllowedPrivateCidr(cidr))
        )
      }, t('Enter up to 32 valid private CIDR ranges')),
    newApiUserId: z
      .string()
      .trim()
      .refine(
        (value) => value === '' || isPositiveInteger(value),
        t('Enter a valid positive user ID')
      ),
    enabled: z.boolean(),
    newApiAccessToken: z
      .string()
      .max(4096, t('Credential must be 4,096 characters or fewer')),
    gatewayApiKey: z
      .string()
      .max(4096, t('Credential must be 4,096 characters or fewer')),
    sub2apiEmail: z
      .string()
      .max(320, t('Email must be 320 characters or fewer')),
    sub2apiPassword: z
      .string()
      .max(4096, t('Credential must be 4,096 characters or fewer')),
    sub2apiToken: z
      .string()
      .max(4096, t('Credential must be 4,096 characters or fewer')),
    customCaPem: z
      .string()
      .max(98_304, t('Custom CA must be 96 KiB or smaller')),
    clearNewApiAccessToken: z.boolean(),
    clearGatewayApiKey: z.boolean(),
    clearSub2apiEmail: z.boolean(),
    clearSub2apiPassword: z.boolean(),
    clearSub2apiToken: z.boolean(),
    clearCustomCaPem: z.boolean(),
  })

  return schema.superRefine((values, context) => {
    if (
      values.enabled &&
      values.upstreamType === 'newapi' &&
      values.newApiUserId.trim() === ''
    ) {
      context.addIssue({
        code: 'custom',
        path: ['newApiUserId'],
        message: t('New API user ID is required'),
      })
    }
  })
}

export function costBindingFormValues(
  binding?: RoutingCostBinding | null
): CostBindingFormValues {
  if (!binding) return { ...emptyCostBindingFormValues }
  return {
    ...emptyCostBindingFormValues,
    channelId: String(binding.channel_id),
    upstreamType: binding.upstream_type,
    baseUrl: binding.base_url,
    upstreamGroup: binding.upstream_group,
    servesClaudeCode: binding.serves_claude_code,
    egressAllowedPrivateCidrs: (
      binding.egress_allowed_private_cidrs ?? []
    ).join('\n'),
    newApiUserId:
      binding.new_api_user_id == null ? '' : String(binding.new_api_user_id),
    enabled: binding.enabled,
  }
}

function setCredential(
  credentials: RoutingCostBindingCredentials,
  key: keyof RoutingCostBindingCredentials,
  value: string,
  clear: boolean,
  trim = true
) {
  if (clear) {
    credentials[key] = ''
    return
  }
  const normalized = trim ? value.trim() : value
  if (normalized !== '') credentials[key] = normalized
}

export function costBindingRequest(
  values: CostBindingFormValues
): RoutingCostBindingRequest {
  const credentials: RoutingCostBindingCredentials = {}
  const newApiUserId = values.newApiUserId.trim()

  if (values.upstreamType === 'newapi') {
    setCredential(
      credentials,
      'new_api_access_token',
      values.newApiAccessToken,
      values.clearNewApiAccessToken
    )
  } else {
    setCredential(
      credentials,
      'sub2api_email',
      values.sub2apiEmail,
      values.clearSub2apiEmail
    )
    setCredential(
      credentials,
      'sub2api_password',
      values.sub2apiPassword,
      values.clearSub2apiPassword,
      false
    )
    setCredential(
      credentials,
      'sub2api_token',
      values.sub2apiToken,
      values.clearSub2apiToken
    )
  }

  // Gateway authentication and custom trust are shared by both providers.
  setCredential(
    credentials,
    'gateway_api_key',
    values.gatewayApiKey,
    values.clearGatewayApiKey
  )
  setCredential(
    credentials,
    'custom_ca_pem',
    values.customCaPem,
    values.clearCustomCaPem
  )

  return {
    channel_id: Number(values.channelId),
    upstream_type: values.upstreamType,
    base_url: values.baseUrl.trim(),
    upstream_group: values.upstreamGroup.trim(),
    serves_claude_code:
      values.upstreamType === 'sub2api' && values.servesClaudeCode,
    egress_allowed_private_cidrs: parseCostBindingEgressCidrs(
      values.egressAllowedPrivateCidrs
    ),
    new_api_user_id:
      values.upstreamType === 'newapi' && newApiUserId !== ''
        ? Number(newApiUserId)
        : undefined,
    enabled: values.enabled,
    credentials,
  }
}

export function costBindingUpdateRequest(
  binding: RoutingCostBinding,
  patch: Partial<Pick<RoutingCostBindingRequest, 'enabled'>> = {}
): RoutingCostBindingRequest {
  return {
    channel_id: binding.channel_id,
    upstream_type: binding.upstream_type,
    base_url: binding.base_url,
    upstream_group: binding.upstream_group,
    serves_claude_code: binding.serves_claude_code,
    egress_allowed_private_cidrs: [
      ...(binding.egress_allowed_private_cidrs ?? []),
    ],
    new_api_user_id: binding.new_api_user_id,
    enabled: patch.enabled ?? binding.enabled,
    credentials: {},
  }
}

export function costBindingCredentialCount(
  binding: Pick<RoutingCostBinding, 'credential_masks'>
): number {
  return Object.values(binding.credential_masks).filter(Boolean).length
}

export const COST_BINDING_GROUP_DOM_LIMIT = 100

export function costBindingGroupUsesSubscription(
  metadata: RoutingCostBindingGroupMetadata | null | undefined
): boolean {
  return metadata?.subscription_type?.trim().toLowerCase() === 'subscription'
}

export function costBindingGroupMetadataForValue(
  groupMeta: Record<string, RoutingCostBindingGroupMetadata> | undefined,
  groupValue: string
): RoutingCostBindingGroupMetadata | undefined {
  const value = groupValue.trim()
  if (!value || groupMeta == null) return undefined
  if (Object.hasOwn(groupMeta, value)) return groupMeta[value]
  return Object.values(groupMeta).find(
    (metadata) => metadata.id === value || metadata.name === value
  )
}

export function boundedCostBindingGroups(
  result: RoutingCostBindingActionResult
): RoutingCostBindingActionResult {
  const rawGroups = Array.isArray(result.groups) ? result.groups : []
  const groups: string[] = []
  const seen = new Set<string>()
  const scanLimit = Math.min(rawGroups.length, COST_BINDING_GROUP_DOM_LIMIT * 4)

  for (let index = 0; index < scanLimit; index += 1) {
    const raw = rawGroups[index]
    if (typeof raw !== 'string') continue
    const group = raw.trim()
    if (!group || group.length > 128 || seen.has(group)) continue
    seen.add(group)
    groups.push(group)
    if (groups.length >= COST_BINDING_GROUP_DOM_LIMIT) break
  }

  const reportedTotal = Number.isSafeInteger(result.groups_total)
    ? Math.max(0, result.groups_total ?? 0)
    : rawGroups.length
  const groupsTotal = Math.max(groups.length, reportedTotal, rawGroups.length)
  const groupsTruncated =
    Boolean(result.groups_truncated) ||
    groupsTotal > groups.length ||
    rawGroups.length > groups.length
  const groupMeta: Record<string, RoutingCostBindingGroupMetadata> = {}

  for (const group of groups) {
    if (result.group_meta == null || !Object.hasOwn(result.group_meta, group)) {
      continue
    }
    const rawMetadata = result.group_meta[group]
    if (rawMetadata == null || typeof rawMetadata !== 'object') continue
    const id = typeof rawMetadata.id === 'string' ? rawMetadata.id.trim() : ''
    const name =
      typeof rawMetadata.name === 'string' ? rawMetadata.name.trim() : ''
    const platform =
      typeof rawMetadata.platform === 'string'
        ? rawMetadata.platform.trim()
        : ''
    const subscriptionType =
      typeof rawMetadata.subscription_type === 'string'
        ? rawMetadata.subscription_type.trim()
        : ''
    if (
      !id ||
      id.length > 128 ||
      !name ||
      name.length > 128 ||
      platform.length > 128 ||
      subscriptionType.length > 128
    ) {
      continue
    }
    groupMeta[group] = {
      id,
      name,
      ...(platform ? { platform } : {}),
      ...(subscriptionType ? { subscription_type: subscriptionType } : {}),
      claude_code_only: rawMetadata.claude_code_only === true,
    }
  }

  return {
    ...result,
    groups,
    group_meta: Object.keys(groupMeta).length > 0 ? groupMeta : undefined,
    groups_total: groupsTotal,
    groups_truncated: groupsTruncated,
  }
}

export function costBindingRequiresClaudeCodeDeclaration(input: {
  result: RoutingCostBindingActionResult | null
  upstreamType: RoutingCostBindingUpstreamType
  upstreamGroup: string
  servesClaudeCode: boolean
}): boolean {
  if (
    input.upstreamType !== 'sub2api' ||
    input.servesClaudeCode ||
    input.result?.group_meta == null
  ) {
    return false
  }
  return (
    costBindingGroupMetadataForValue(
      input.result.group_meta,
      input.upstreamGroup
    )?.claude_code_only === true
  )
}
