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
import type {
  PolicyDocument,
  PolicyMemberDocument,
  PolicyPoolDocument,
} from '../types'

const enterpriseCapacityMaximum = 1_000_000_000_000
const enterpriseCapacityDefaults = {
  rpm: 600,
  input_tpm: 1_000_000,
  output_tpm: 250_000,
  total_tpm: 1_250_000,
  inflight: 32,
  cost_nano_usd: 0,
}
const enterpriseCapacityScopes = new Set([
  'auto',
  'account',
  'credential',
  'account_credential',
])
const enterpriseHedgeFields = new Set([
  'enabled',
  'delay_ms',
  'max_extra_cost_multiplier',
  'max_response_bytes',
  'scope',
  'cross_region',
])

function isRecord(value: unknown): value is Record<string, unknown> {
  return value != null && typeof value === 'object' && !Array.isArray(value)
}

function boundedInteger(
  value: unknown,
  minimum: number,
  maximum: number,
  fallback: number
): number {
  return typeof value === 'number' &&
    Number.isSafeInteger(value) &&
    value >= minimum &&
    value <= maximum
    ? value
    : fallback
}

function validEnterpriseHedge(
  value: unknown
): value is Record<string, unknown> {
  if (
    !isRecord(value) ||
    typeof value.enabled !== 'boolean' ||
    Object.keys(value).some((key) => !enterpriseHedgeFields.has(key))
  ) {
    return false
  }
  const delay = value.delay_ms
  if (
    delay != null &&
    (typeof delay !== 'number' ||
      !Number.isSafeInteger(delay) ||
      delay < 25 ||
      delay > 10_000)
  ) {
    return false
  }
  const multiplier = value.max_extra_cost_multiplier
  if (
    multiplier != null &&
    (typeof multiplier !== 'number' ||
      !Number.isFinite(multiplier) ||
      multiplier < 1 ||
      multiplier > 4)
  ) {
    return false
  }
  const responseBytes = value.max_response_bytes
  if (
    responseBytes != null &&
    (typeof responseBytes !== 'number' ||
      !Number.isSafeInteger(responseBytes) ||
      responseBytes < 64 << 10 ||
      responseBytes > 64 << 20)
  ) {
    return false
  }
  return (
    (value.scope == null || value.scope === 'distinct_endpoint_or_account') &&
    (value.cross_region == null || value.cross_region === false)
  )
}

function enterprisePolicyForProfile(
  policy: Record<string, unknown>,
  currentProfile: PolicyPoolDocument['policy_profile'],
  nextProfile: PolicyPoolDocument['policy_profile']
): Record<string, unknown> {
  if (currentProfile === nextProfile) return policy
  const currentEnterprise = isRecord(policy.enterprise) ? policy.enterprise : {}
  const currentCapacity = isRecord(currentEnterprise.capacity)
    ? currentEnterprise.capacity
    : {}

  if (nextProfile === 'enterprise_slo') {
    const capacity = { ...currentCapacity }
    capacity.mode =
      capacity.mode === 'redis_strict' || capacity.mode === 'redis_block'
        ? capacity.mode
        : 'redis_block'
    capacity.scope =
      typeof capacity.scope === 'string' &&
      enterpriseCapacityScopes.has(capacity.scope)
        ? capacity.scope
        : 'auto'
    capacity.rpm = boundedInteger(
      capacity.rpm,
      1,
      enterpriseCapacityMaximum,
      enterpriseCapacityDefaults.rpm
    )
    const inputTPM = boundedInteger(
      capacity.input_tpm,
      1,
      enterpriseCapacityMaximum,
      enterpriseCapacityDefaults.input_tpm
    )
    capacity.input_tpm = inputTPM
    const outputTPM = boundedInteger(
      capacity.output_tpm,
      1,
      enterpriseCapacityMaximum,
      enterpriseCapacityDefaults.output_tpm
    )
    capacity.output_tpm = outputTPM
    capacity.total_tpm = Math.max(
      boundedInteger(
        capacity.total_tpm,
        1,
        enterpriseCapacityMaximum,
        enterpriseCapacityDefaults.total_tpm
      ),
      inputTPM,
      outputTPM
    )
    capacity.inflight = boundedInteger(
      capacity.inflight,
      1,
      enterpriseCapacityMaximum,
      enterpriseCapacityDefaults.inflight
    )
    capacity.cost_nano_usd = boundedInteger(
      capacity.cost_nano_usd,
      0,
      enterpriseCapacityMaximum,
      enterpriseCapacityDefaults.cost_nano_usd
    )
    capacity.lease_ttl_seconds = boundedInteger(
      capacity.lease_ttl_seconds,
      1,
      300,
      60
    )
    const guaranteedBasisPoints = boundedInteger(
      capacity.guaranteed_basis_points,
      0,
      10_000,
      0
    )
    capacity.guaranteed_basis_points = guaranteedBasisPoints
    capacity.maximum_basis_points = Math.max(
      boundedInteger(capacity.maximum_basis_points, 1, 10_000, 10_000),
      guaranteedBasisPoints
    )
    const enterprise: Record<string, unknown> = {
      ...currentEnterprise,
      capacity,
    }
    if ('hedge' in currentEnterprise) {
      enterprise.hedge = validEnterpriseHedge(currentEnterprise.hedge)
        ? currentEnterprise.hedge
        : { enabled: false }
    }
    return { ...policy, enterprise }
  }

  if (currentProfile !== 'enterprise_slo') return policy
  const enterprise = { ...currentEnterprise }
  delete enterprise.hedge
  return {
    ...policy,
    enterprise: {
      ...enterprise,
      capacity: { ...currentCapacity, mode: 'local_soft' },
    },
  }
}

function replaceAt<T>(items: T[], index: number, value: T): T[] {
  if (index < 0 || index >= items.length) return items
  const next = [...items]
  next[index] = value
  return next
}

function withPolicyMemberEffectiveValues(
  pool: PolicyPoolDocument,
  member: PolicyMemberDocument
): PolicyMemberDocument {
  return {
    ...member,
    enabled: member.enabled_override ?? pool.default_enabled ?? true,
    priority: member.priority_override ?? pool.default_priority ?? 0,
    weight: member.weight_override ?? pool.default_weight ?? 100,
  }
}

export function updatePolicyPoolDocument(
  document: PolicyDocument,
  poolIndex: number,
  patch: Partial<PolicyPoolDocument>
): PolicyDocument {
  const pool = document.pools[poolIndex]
  if (!pool) return document
  let nextPool = { ...pool, ...patch }
  if (document.schema_version === 2) {
    nextPool = {
      ...nextPool,
      default_enabled: nextPool.default_enabled ?? true,
      default_priority: nextPool.default_priority ?? 0,
      default_weight: nextPool.default_weight ?? 100,
      members: nextPool.members.map((member) =>
        withPolicyMemberEffectiveValues(nextPool, member)
      ),
    }
  }
  return {
    ...document,
    pools: replaceAt(document.pools, poolIndex, nextPool),
  }
}

export function updatePolicyPoolProfile(
  document: PolicyDocument,
  poolIndex: number,
  profile: PolicyPoolDocument['policy_profile']
): PolicyDocument {
  const pool = document.pools[poolIndex]
  if (!pool || pool.policy_profile === profile) return document
  return updatePolicyPoolDocument(document, poolIndex, {
    policy_profile: profile,
    policy: enterprisePolicyForProfile(
      pool.policy,
      pool.policy_profile,
      profile
    ),
  })
}

export function updatePolicyPoolField(
  document: PolicyDocument,
  poolIndex: number,
  key: string,
  value: unknown
): PolicyDocument {
  const pool = document.pools[poolIndex]
  if (!pool) return document
  return updatePolicyPoolDocument(document, poolIndex, {
    policy: { ...pool.policy, [key]: value },
  })
}

export function updatePolicyPoolNestedField(
  document: PolicyDocument,
  poolIndex: number,
  parentKey: string,
  key: string,
  value: unknown
): PolicyDocument {
  return updatePolicyPoolPath(document, poolIndex, [parentKey, key], value)
}

export function updatePolicyPoolPath(
  document: PolicyDocument,
  poolIndex: number,
  path: string[],
  value: unknown
): PolicyDocument {
  const pool = document.pools[poolIndex]
  if (!pool || path.length === 0) return document
  const update = (
    record: Record<string, unknown>,
    pathIndex: number
  ): Record<string, unknown> => {
    const key = path[pathIndex]
    if (pathIndex === path.length - 1) return { ...record, [key]: value }
    const current = record[key]
    const nested =
      current != null && typeof current === 'object' && !Array.isArray(current)
        ? (current as Record<string, unknown>)
        : {}
    return { ...record, [key]: update(nested, pathIndex + 1) }
  }
  return updatePolicyPoolDocument(document, poolIndex, {
    policy: update(pool.policy, 0),
  })
}

export function updatePolicyMemberDocument(
  document: PolicyDocument,
  poolIndex: number,
  memberIndex: number,
  patch: Partial<PolicyMemberDocument>
): PolicyDocument {
  const pool = document.pools[poolIndex]
  const member = pool?.members[memberIndex]
  if (!pool || !member) return document
  let nextMember = { ...member, ...patch }
  if (document.schema_version === 2) {
    if (patch.enabled !== undefined) {
      nextMember.enabled_override = patch.enabled
    }
    if (patch.priority !== undefined) {
      nextMember.priority_override = patch.priority
    }
    if (patch.weight !== undefined) {
      nextMember.weight_override = patch.weight
    }
    nextMember = withPolicyMemberEffectiveValues(pool, nextMember)
  }
  const members = replaceAt(pool.members, memberIndex, nextMember)
  return updatePolicyPoolDocument(document, poolIndex, { members })
}

export type ParsedCredentialIds =
  | { ok: true; value: number[] }
  | { ok: false; value: number[] }

export function parsePolicyCredentialIds(source: string): ParsedCredentialIds {
  const normalized = source.trim()
  if (!normalized) return { ok: true, value: [] }
  const values: number[] = []
  const seen = new Set<number>()
  for (const token of normalized.split(/[\s,]+/)) {
    if (!/^\d+$/.test(token)) return { ok: false, value: [] }
    const value = Number(token)
    if (!Number.isSafeInteger(value) || value <= 0 || seen.has(value)) {
      return { ok: false, value: [] }
    }
    seen.add(value)
    values.push(value)
  }
  return { ok: true, value: values }
}

export function parsePolicyOverrides(
  source: string
): { ok: true; value: Record<string, unknown> } | { ok: false } {
  try {
    const parsed = JSON.parse(source) as unknown
    if (parsed == null || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return { ok: false }
    }
    return { ok: true, value: parsed as Record<string, unknown> }
  } catch {
    return { ok: false }
  }
}
