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
import {
  applyEdits,
  findNodeAtLocation,
  format as formatJson,
  getLocation,
  parse,
  parseTree,
  printParseErrorCode,
  type JSONPath,
  type Node as JsonNode,
  type ParseError,
} from 'jsonc-parser'

import type { PolicyDocument } from '../types'

const legacyPolicySchemaVersion = 1
const policySchemaVersion = 2
const maxPolicyBytes = 64 << 20
const maxPolicyPools = 4_096
const maxPolicyMembers = 100_000
const maxMembersPerPool = 4_096
const maxCredentialIdsPerMember = 4_096
const maxCredentialReferences = 1_000_000
const maxReportedIssues = 50
const deploymentStages = new Set(['observe', 'shadow', 'canary', 'active'])
const policyProfiles = new Set([
  'balanced',
  'reliability_first',
  'cost_aware',
  'enterprise_slo',
  'custom',
])
const routingGenerationPattern = /^[0-9a-f]{32}$/

export type PolicyDocumentIssueCode =
  | 'document_too_large'
  | 'duplicate_value'
  | 'expected_array'
  | 'expected_boolean'
  | 'expected_integer'
  | 'expected_nonnegative_integer'
  | 'expected_object'
  | 'expected_positive_integer'
  | 'invalid_deployment_stage'
  | 'invalid_policy_profile'
  | 'invalid_routing_generation'
  | 'json_syntax'
  | 'required_string'
  | 'string_too_long'
  | 'too_many_items'
  | 'unsupported_schema_field'
  | 'unsupported_schema_version'

export type PolicyDocumentIssue = {
  diagnosticId: number
  kind: 'syntax' | 'schema'
  code: PolicyDocumentIssueCode
  path: JSONPath
  offset: number
  length: number
  line: number
  column: number
  detail?: string
  limit?: number
}

export type PolicyDocumentSummary = {
  bytes: number
  poolCount: number
  memberCount: number
  enabledMemberCount: number
  stages: Record<string, number>
  profiles: Record<string, number>
}

export type PolicyDocumentAnalysis = {
  document?: PolicyDocument
  issues: PolicyDocumentIssue[]
  issuesTruncated: boolean
  summary: PolicyDocumentSummary
  valid: boolean
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value != null && typeof value === 'object' && !Array.isArray(value)
}

function isSafeInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value)
}

function incrementCount(counts: Record<string, number>, value: unknown) {
  if (typeof value !== 'string' || value.length === 0) return
  counts[value] = (counts[value] ?? 0) + 1
}

export function policyDocumentPositionAtOffset(
  source: string,
  targetOffset: number
): { line: number; column: number } {
  const offset = Math.max(0, Math.min(source.length, targetOffset))
  let line = 1
  let column = 1
  for (let index = 0; index < offset; index += 1) {
    if (source.charCodeAt(index) === 10) {
      line += 1
      column = 1
    } else {
      column += 1
    }
  }
  return { line, column }
}

export function formatPolicyDocumentPath(path: JSONPath): string {
  let output = '$'
  for (const segment of path) {
    if (typeof segment === 'number') {
      output += `[${segment}]`
    } else if (/^[A-Za-z_$][A-Za-z0-9_$]*$/.test(segment)) {
      output += `.${segment}`
    } else {
      output += `[${JSON.stringify(segment)}]`
    }
  }
  return output
}

function closestNode(root: JsonNode | undefined, path: JSONPath) {
  if (!root) return undefined
  const candidatePath = [...path]
  while (candidatePath.length > 0) {
    const node = findNodeAtLocation(root, candidatePath)
    if (node) return node
    candidatePath.pop()
  }
  return root
}

function createIssue(
  source: string,
  root: JsonNode | undefined,
  issue: Omit<
    PolicyDocumentIssue,
    'diagnosticId' | 'offset' | 'length' | 'line' | 'column'
  > & {
    offset?: number
    length?: number
  },
  diagnosticId: number
): PolicyDocumentIssue {
  const node = closestNode(root, issue.path)
  const offset = issue.offset ?? node?.offset ?? 0
  const length = Math.max(1, issue.length ?? node?.length ?? 1)
  const position = policyDocumentPositionAtOffset(source, offset)
  return {
    ...issue,
    diagnosticId,
    offset,
    length,
    line: position.line,
    column: position.column,
  }
}

export function analyzePolicyDocument(source: string): PolicyDocumentAnalysis {
  const summary: PolicyDocumentSummary = {
    bytes: new TextEncoder().encode(source).byteLength,
    poolCount: 0,
    memberCount: 0,
    enabledMemberCount: 0,
    stages: {},
    profiles: {},
  }
  const parseErrors: ParseError[] = []
  const parseOptions = {
    allowEmptyContent: false,
    allowTrailingComma: false,
    disallowComments: true,
  }
  const value = parse(source, parseErrors, parseOptions) as unknown
  const root = parseTree(source, [], parseOptions)
  const issues: PolicyDocumentIssue[] = []
  let issuesTruncated = false

  const addIssue = (
    issue: Omit<
      PolicyDocumentIssue,
      'diagnosticId' | 'offset' | 'length' | 'line' | 'column'
    > & { offset?: number; length?: number }
  ) => {
    if (issues.length >= maxReportedIssues) {
      issuesTruncated = true
      return
    }
    issues.push(createIssue(source, root, issue, issues.length))
  }

  for (const error of parseErrors) {
    addIssue({
      kind: 'syntax',
      code: 'json_syntax',
      path: getLocation(source, error.offset).path,
      offset: error.offset,
      length: error.length,
      detail: printParseErrorCode(error.error),
    })
  }
  if (parseErrors.length > 0) {
    return {
      issues,
      issuesTruncated,
      summary,
      valid: false,
    }
  }

  if (summary.bytes > maxPolicyBytes) {
    addIssue({
      kind: 'schema',
      code: 'document_too_large',
      path: [],
      limit: maxPolicyBytes,
    })
  }
  if (!isRecord(value)) {
    addIssue({ kind: 'schema', code: 'expected_object', path: [] })
    return {
      issues,
      issuesTruncated,
      summary,
      valid: false,
    }
  }

  const schemaVersion = value.schema_version
  const legacySchema = schemaVersion === legacyPolicySchemaVersion
  const currentSchema = schemaVersion === policySchemaVersion
  if (!legacySchema && !currentSchema) {
    addIssue({
      kind: 'schema',
      code: 'unsupported_schema_version',
      path: ['schema_version'],
    })
  }
  if (!Array.isArray(value.pools)) {
    addIssue({ kind: 'schema', code: 'expected_array', path: ['pools'] })
    return {
      issues,
      issuesTruncated,
      summary,
      valid: false,
    }
  }

  summary.poolCount = value.pools.length
  if (value.pools.length > maxPolicyPools) {
    addIssue({
      kind: 'schema',
      code: 'too_many_items',
      path: ['pools'],
      limit: maxPolicyPools,
    })
  }

  const poolIds = new Set<number>()
  const groupNames = new Set<string>()
  const memberIds = new Set<number>()
  let credentialReferenceCount = 0

  value.pools.forEach((poolValue, poolIndex) => {
    const poolPath: JSONPath = ['pools', poolIndex]
    if (!isRecord(poolValue)) {
      addIssue({ kind: 'schema', code: 'expected_object', path: poolPath })
      return
    }

    const poolIdPath = [...poolPath, 'pool_id']
    if (!isSafeInteger(poolValue.pool_id) || poolValue.pool_id <= 0) {
      addIssue({
        kind: 'schema',
        code: 'expected_positive_integer',
        path: poolIdPath,
      })
    } else if (poolIds.has(poolValue.pool_id)) {
      addIssue({
        kind: 'schema',
        code: 'duplicate_value',
        path: poolIdPath,
      })
    } else {
      poolIds.add(poolValue.pool_id)
    }

    const groupNamePath = [...poolPath, 'group_name']
    if (
      typeof poolValue.group_name !== 'string' ||
      poolValue.group_name.length === 0
    ) {
      addIssue({
        kind: 'schema',
        code: 'required_string',
        path: groupNamePath,
      })
    } else {
      if ([...poolValue.group_name].length > 64) {
        addIssue({
          kind: 'schema',
          code: 'string_too_long',
          path: groupNamePath,
          limit: 64,
        })
      }
      if (groupNames.has(poolValue.group_name)) {
        addIssue({
          kind: 'schema',
          code: 'duplicate_value',
          path: groupNamePath,
        })
      } else {
        groupNames.add(poolValue.group_name)
      }
    }

    const displayNamePath = [...poolPath, 'display_name']
    if (typeof poolValue.display_name !== 'string') {
      addIssue({
        kind: 'schema',
        code: 'required_string',
        path: displayNamePath,
      })
    } else if ([...poolValue.display_name].length > 128) {
      addIssue({
        kind: 'schema',
        code: 'string_too_long',
        path: displayNamePath,
        limit: 128,
      })
    }

    incrementCount(summary.stages, poolValue.deployment_stage)
    if (
      typeof poolValue.deployment_stage !== 'string' ||
      !deploymentStages.has(poolValue.deployment_stage)
    ) {
      addIssue({
        kind: 'schema',
        code: 'invalid_deployment_stage',
        path: [...poolPath, 'deployment_stage'],
      })
    }

    incrementCount(summary.profiles, poolValue.policy_profile)
    if (
      typeof poolValue.policy_profile !== 'string' ||
      !policyProfiles.has(poolValue.policy_profile)
    ) {
      addIssue({
        kind: 'schema',
        code: 'invalid_policy_profile',
        path: [...poolPath, 'policy_profile'],
      })
    }

    let defaultEnabled = true
    let defaultPriority = 0
    let defaultWeight = 100
    if (currentSchema) {
      if (poolValue.default_enabled == null) {
        poolValue.default_enabled = defaultEnabled
      } else if (typeof poolValue.default_enabled !== 'boolean') {
        addIssue({
          kind: 'schema',
          code: 'expected_boolean',
          path: [...poolPath, 'default_enabled'],
        })
      } else {
        defaultEnabled = poolValue.default_enabled
      }

      if (poolValue.default_priority == null) {
        poolValue.default_priority = defaultPriority
      } else if (!isSafeInteger(poolValue.default_priority)) {
        addIssue({
          kind: 'schema',
          code: 'expected_integer',
          path: [...poolPath, 'default_priority'],
        })
      } else {
        defaultPriority = poolValue.default_priority
      }

      if (poolValue.default_weight == null) {
        poolValue.default_weight = defaultWeight
      } else if (
        !isSafeInteger(poolValue.default_weight) ||
        poolValue.default_weight < 0
      ) {
        addIssue({
          kind: 'schema',
          code: 'expected_nonnegative_integer',
          path: [...poolPath, 'default_weight'],
        })
      } else {
        defaultWeight = poolValue.default_weight
      }
    } else if (legacySchema) {
      for (const field of [
        'default_enabled',
        'default_priority',
        'default_weight',
      ]) {
        if (poolValue[field] != null) {
          addIssue({
            kind: 'schema',
            code: 'unsupported_schema_field',
            path: [...poolPath, field],
          })
        }
      }
    }

    if (!isRecord(poolValue.policy)) {
      addIssue({
        kind: 'schema',
        code: 'expected_object',
        path: [...poolPath, 'policy'],
      })
    }
    let members: unknown[]
    if (poolValue.members == null) {
      members = []
      poolValue.members = members
    } else if (!Array.isArray(poolValue.members)) {
      addIssue({
        kind: 'schema',
        code: 'expected_array',
        path: [...poolPath, 'members'],
      })
      return
    } else {
      members = poolValue.members
    }

    summary.memberCount += members.length
    if (members.length > maxMembersPerPool) {
      addIssue({
        kind: 'schema',
        code: 'too_many_items',
        path: [...poolPath, 'members'],
        limit: maxMembersPerPool,
      })
    }
    const channelIds = new Set<number>()

    members.forEach((memberValue, memberIndex) => {
      const memberPath: JSONPath = [...poolPath, 'members', memberIndex]
      if (!isRecord(memberValue)) {
        addIssue({
          kind: 'schema',
          code: 'expected_object',
          path: memberPath,
        })
        return
      }

      const memberIdPath = [...memberPath, 'member_id']
      if (!isSafeInteger(memberValue.member_id) || memberValue.member_id <= 0) {
        addIssue({
          kind: 'schema',
          code: 'expected_positive_integer',
          path: memberIdPath,
        })
      } else if (memberIds.has(memberValue.member_id)) {
        addIssue({
          kind: 'schema',
          code: 'duplicate_value',
          path: memberIdPath,
        })
      } else {
        memberIds.add(memberValue.member_id)
      }

      const channelIdPath = [...memberPath, 'channel_id']
      if (
        !isSafeInteger(memberValue.channel_id) ||
        memberValue.channel_id <= 0
      ) {
        addIssue({
          kind: 'schema',
          code: 'expected_positive_integer',
          path: channelIdPath,
        })
      } else if (channelIds.has(memberValue.channel_id)) {
        addIssue({
          kind: 'schema',
          code: 'duplicate_value',
          path: channelIdPath,
        })
      } else {
        channelIds.add(memberValue.channel_id)
      }

      if (currentSchema) {
        let rawEnabled = false
        let rawPriority = 0
        let rawWeight = 0
        if (memberValue.enabled != null) {
          if (typeof memberValue.enabled !== 'boolean') {
            addIssue({
              kind: 'schema',
              code: 'expected_boolean',
              path: [...memberPath, 'enabled'],
            })
          } else {
            rawEnabled = memberValue.enabled
          }
        }
        if (memberValue.priority != null) {
          if (!isSafeInteger(memberValue.priority)) {
            addIssue({
              kind: 'schema',
              code: 'expected_integer',
              path: [...memberPath, 'priority'],
            })
          } else {
            rawPriority = memberValue.priority
          }
        }
        if (memberValue.weight != null) {
          if (!isSafeInteger(memberValue.weight) || memberValue.weight < 0) {
            addIssue({
              kind: 'schema',
              code: 'expected_nonnegative_integer',
              path: [...memberPath, 'weight'],
            })
          } else {
            rawWeight = memberValue.weight
          }
        }

        let routingGeneration = ''
        if (memberValue.routing_generation == null) {
          delete memberValue.routing_generation
        } else if (
          typeof memberValue.routing_generation !== 'string' ||
          (memberValue.routing_generation.length > 0 &&
            !routingGenerationPattern.test(memberValue.routing_generation))
        ) {
          addIssue({
            kind: 'schema',
            code: 'invalid_routing_generation',
            path: [...memberPath, 'routing_generation'],
          })
        } else {
          routingGeneration = memberValue.routing_generation
          if (routingGeneration.length === 0) {
            delete memberValue.routing_generation
          }
        }

        let enabledOverride: boolean | undefined
        if (memberValue.enabled_override == null) {
          delete memberValue.enabled_override
        } else if (typeof memberValue.enabled_override !== 'boolean') {
          addIssue({
            kind: 'schema',
            code: 'expected_boolean',
            path: [...memberPath, 'enabled_override'],
          })
        } else {
          enabledOverride = memberValue.enabled_override
        }

        let priorityOverride: number | undefined
        if (memberValue.priority_override == null) {
          delete memberValue.priority_override
        } else if (!isSafeInteger(memberValue.priority_override)) {
          addIssue({
            kind: 'schema',
            code: 'expected_integer',
            path: [...memberPath, 'priority_override'],
          })
        } else {
          priorityOverride = memberValue.priority_override
        }

        let weightOverride: number | undefined
        if (memberValue.weight_override == null) {
          delete memberValue.weight_override
        } else if (
          !isSafeInteger(memberValue.weight_override) ||
          memberValue.weight_override < 0
        ) {
          addIssue({
            kind: 'schema',
            code: 'expected_nonnegative_integer',
            path: [...memberPath, 'weight_override'],
          })
        } else {
          weightOverride = memberValue.weight_override
        }

        if (
          routingGeneration.length === 0 &&
          enabledOverride === undefined &&
          priorityOverride === undefined &&
          weightOverride === undefined &&
          (rawEnabled || rawPriority !== 0 || rawWeight !== 0)
        ) {
          enabledOverride = rawEnabled
          priorityOverride = rawPriority
          weightOverride = rawWeight
          memberValue.enabled_override = enabledOverride
          memberValue.priority_override = priorityOverride
          memberValue.weight_override = weightOverride
        }

        memberValue.enabled = enabledOverride ?? defaultEnabled
        memberValue.priority = priorityOverride ?? defaultPriority
        memberValue.weight = weightOverride ?? defaultWeight
        if (memberValue.enabled) summary.enabledMemberCount += 1
      } else {
        if (legacySchema) {
          for (const field of [
            'routing_generation',
            'enabled_override',
            'priority_override',
            'weight_override',
          ]) {
            if (memberValue[field] != null && memberValue[field] !== '') {
              addIssue({
                kind: 'schema',
                code: 'unsupported_schema_field',
                path: [...memberPath, field],
              })
            }
          }
        }
        if (typeof memberValue.enabled !== 'boolean') {
          addIssue({
            kind: 'schema',
            code: 'expected_boolean',
            path: [...memberPath, 'enabled'],
          })
        } else if (memberValue.enabled) {
          summary.enabledMemberCount += 1
        }

        if (!isSafeInteger(memberValue.priority)) {
          addIssue({
            kind: 'schema',
            code: 'expected_integer',
            path: [...memberPath, 'priority'],
          })
        }
        if (!isSafeInteger(memberValue.weight) || memberValue.weight < 0) {
          addIssue({
            kind: 'schema',
            code: 'expected_nonnegative_integer',
            path: [...memberPath, 'weight'],
          })
        }
      }

      const credentialIdsPath = [...memberPath, 'credential_ids']
      if (memberValue.credential_ids == null) {
        memberValue.credential_ids = []
      } else if (!Array.isArray(memberValue.credential_ids)) {
        addIssue({
          kind: 'schema',
          code: 'expected_array',
          path: credentialIdsPath,
        })
      } else {
        credentialReferenceCount += memberValue.credential_ids.length
        if (memberValue.credential_ids.length > maxCredentialIdsPerMember) {
          addIssue({
            kind: 'schema',
            code: 'too_many_items',
            path: credentialIdsPath,
            limit: maxCredentialIdsPerMember,
          })
        }
        const credentialIds = new Set<number>()
        memberValue.credential_ids.forEach((credentialId, credentialIndex) => {
          const credentialIdPath = [...credentialIdsPath, credentialIndex]
          if (!isSafeInteger(credentialId) || credentialId <= 0) {
            addIssue({
              kind: 'schema',
              code: 'expected_positive_integer',
              path: credentialIdPath,
            })
          } else if (credentialIds.has(credentialId)) {
            addIssue({
              kind: 'schema',
              code: 'duplicate_value',
              path: credentialIdPath,
            })
          } else {
            credentialIds.add(credentialId)
          }
        })
      }

      if (memberValue.overrides === undefined) {
        memberValue.overrides = {}
      } else if (!isRecord(memberValue.overrides)) {
        addIssue({
          kind: 'schema',
          code: 'expected_object',
          path: [...memberPath, 'overrides'],
        })
      }
    })
  })

  if (summary.memberCount > maxPolicyMembers) {
    addIssue({
      kind: 'schema',
      code: 'too_many_items',
      path: ['pools'],
      limit: maxPolicyMembers,
    })
  }
  if (credentialReferenceCount > maxCredentialReferences) {
    addIssue({
      kind: 'schema',
      code: 'too_many_items',
      path: ['pools'],
      limit: maxCredentialReferences,
    })
  }

  issues.sort((left, right) => left.offset - right.offset)
  const valid = issues.length === 0
  return {
    document: valid ? (value as PolicyDocument) : undefined,
    issues,
    issuesTruncated,
    summary,
    valid,
  }
}

export function formatPolicyDocumentText(source: string): string | null {
  const analysis = analyzePolicyDocument(source)
  if (analysis.issues.some((issue) => issue.kind === 'syntax')) return null
  return applyEdits(
    source,
    formatJson(source, undefined, {
      eol: '\n',
      insertFinalNewline: true,
      insertSpaces: true,
      tabSize: 2,
    })
  )
}

export function policyDocumentText(document: PolicyDocument): string {
  return `${JSON.stringify(document, null, 2)}\n`
}

export function starterPolicyDocumentText(): string {
  return policyDocumentText({ schema_version: policySchemaVersion, pools: [] })
}
