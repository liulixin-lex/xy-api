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
  RoutingCostBinding,
  RoutingCostBindingRequest,
  RoutingCostBindingUpstreamType,
} from '../types'
import {
  costBindingFormValues,
  type CostBindingFormValues,
} from './cost-binding'

export type CostBindingEditorSession = {
  generation: number
  subject: string
  signal: AbortSignal
}

export class CostBindingEditorSessionManager {
  private generation = 0
  private subject = ''
  private controller: AbortController | null = null

  activate(subject: string): CostBindingEditorSession {
    if (
      this.controller == null ||
      this.controller.signal.aborted ||
      this.subject !== subject
    ) {
      this.rotate(subject)
    }
    return this.capture()
  }

  rotate(subject: string): CostBindingEditorSession {
    this.controller?.abort()
    this.generation += 1
    this.subject = subject
    this.controller = new AbortController()
    return this.capture()
  }

  deactivate(): void {
    this.controller?.abort()
    this.controller = null
    this.subject = ''
    this.generation += 1
  }

  isCurrent(session: CostBindingEditorSession, subject: string): boolean {
    return (
      this.controller != null &&
      !session.signal.aborted &&
      session.generation === this.generation &&
      session.subject === this.subject &&
      session.subject === subject
    )
  }

  private capture(): CostBindingEditorSession {
    if (this.controller == null) {
      throw new Error('Cost binding editor session is inactive')
    }
    return {
      generation: this.generation,
      subject: this.subject,
      signal: this.controller.signal,
    }
  }
}

const previewCredentialKeys = [
  'new_api_access_token',
  'gateway_api_key',
  'sub2api_email',
  'sub2api_password',
  'sub2api_token',
  'custom_ca_pem',
] as const

export function costBindingAccountPreviewMatchesRequest(
  previous: RoutingCostBindingRequest,
  current: RoutingCostBindingRequest
): boolean {
  if (
    previous.upstream_type !== current.upstream_type ||
    previous.base_url !== current.base_url ||
    previous.new_api_user_id !== current.new_api_user_id ||
    previous.egress_allowed_private_cidrs.length !==
      current.egress_allowed_private_cidrs.length
  ) {
    return false
  }

  for (
    let index = 0;
    index < previous.egress_allowed_private_cidrs.length;
    index += 1
  ) {
    if (
      previous.egress_allowed_private_cidrs[index] !==
      current.egress_allowed_private_cidrs[index]
    ) {
      return false
    }
  }

  for (const key of previewCredentialKeys) {
    if (previous.credentials[key] !== current.credentials[key]) return false
  }
  return true
}

export function costBindingTestPreviewMatchesRequest(
  previous: RoutingCostBindingRequest,
  current: RoutingCostBindingRequest
): boolean {
  return (
    costBindingAccountPreviewMatchesRequest(previous, current) &&
    previous.upstream_group === current.upstream_group &&
    previous.serves_claude_code === current.serves_claude_code
  )
}

type CostBindingDirtyFields = Partial<
  Record<keyof CostBindingFormValues, boolean>
>

export type CostBindingConflictMerge = {
  values: CostBindingFormValues
  serverChangedLabels: string[]
  overlappingLabels: string[]
}

const editableFields = [
  ['upstreamType', 'Upstream type'],
  ['baseUrl', 'Base URL'],
  ['upstreamGroup', 'Upstream group'],
  ['servesClaudeCode', 'Serves Claude Code'],
  ['egressAllowedPrivateCidrs', 'Private egress CIDRs'],
  ['newApiUserId', 'New API user ID'],
  ['enabled', 'Cost sync enabled'],
] as const satisfies ReadonlyArray<
  readonly [keyof CostBindingFormValues, string]
>

const credentialFields = [
  {
    value: 'newApiAccessToken',
    clear: 'clearNewApiAccessToken',
    mask: 'new_api_access_token',
    label: 'New API Access Token',
  },
  {
    value: 'gatewayApiKey',
    clear: 'clearGatewayApiKey',
    mask: 'gateway_api_key',
    label: 'Gateway API Key',
  },
  {
    value: 'sub2apiEmail',
    clear: 'clearSub2apiEmail',
    mask: 'sub2api_email',
    label: 'Sub2API Email',
  },
  {
    value: 'sub2apiPassword',
    clear: 'clearSub2apiPassword',
    mask: 'sub2api_password',
    label: 'Sub2API Password',
  },
  {
    value: 'sub2apiToken',
    clear: 'clearSub2apiToken',
    mask: 'sub2api_token',
    label: 'Sub2API JWT',
  },
  {
    value: 'customCaPem',
    clear: 'clearCustomCaPem',
    mask: 'custom_ca_configured',
    label: 'Custom CA',
  },
] as const

function preserveDirtyField<K extends keyof CostBindingFormValues>(
  merged: CostBindingFormValues,
  draft: CostBindingFormValues,
  dirtyFields: CostBindingDirtyFields,
  field: K
): void {
  if (dirtyFields[field]) merged[field] = draft[field]
}

export function mergeCostBindingConflictDraft(input: {
  baseline: RoutingCostBinding
  latest: RoutingCostBinding
  draft: CostBindingFormValues
  dirtyFields: CostBindingDirtyFields
}): CostBindingConflictMerge {
  const baselineValues = costBindingFormValues(input.baseline)
  const latestValues = costBindingFormValues(input.latest)
  const values = { ...latestValues }
  const serverChangedLabels: string[] = []
  const overlappingLabels: string[] = []

  for (const [field, label] of editableFields) {
    const serverChanged = baselineValues[field] !== latestValues[field]
    if (serverChanged) serverChangedLabels.push(label)
    if (serverChanged && input.dirtyFields[field]) {
      overlappingLabels.push(label)
    }
    preserveDirtyField(values, input.draft, input.dirtyFields, field)
  }

  for (const credential of credentialFields) {
    const serverChanged =
      input.baseline.credential_masks[credential.mask] !==
      input.latest.credential_masks[credential.mask]
    const locallyChanged =
      Boolean(input.dirtyFields[credential.value]) ||
      Boolean(input.dirtyFields[credential.clear])

    if (serverChanged) serverChangedLabels.push(credential.label)
    if (serverChanged && locallyChanged) {
      overlappingLabels.push(credential.label)
    }
    if (locallyChanged) {
      values[credential.value] = input.draft[credential.value]
      values[credential.clear] = input.draft[credential.clear]
    }
  }

  return {
    values,
    serverChangedLabels: [...new Set(serverChangedLabels)],
    overlappingLabels: [...new Set(overlappingLabels)],
  }
}

const serverFieldNames = {
  channel_id: 'channelId',
  upstream_type: 'upstreamType',
  base_url: 'baseUrl',
  upstream_group: 'upstreamGroup',
  serves_claude_code: 'servesClaudeCode',
  egress_allowed_private_cidrs: 'egressAllowedPrivateCidrs',
  new_api_user_id: 'newApiUserId',
  enabled: 'enabled',
  new_api_access_token: 'newApiAccessToken',
  gateway_api_key: 'gatewayApiKey',
  sub2api_email: 'sub2apiEmail',
  sub2api_password: 'sub2apiPassword',
  sub2api_token: 'sub2apiToken',
  custom_ca_pem: 'customCaPem',
} as const satisfies Record<string, keyof CostBindingFormValues>

const serverReasonKeys: Record<string, string> = {
  required: 'This field is required.',
  invalid: 'Enter a valid value.',
  invalid_format: 'Enter a valid value.',
  invalid_json: 'Enter a valid value.',
  invalid_type: 'Enter a valid value.',
  unsupported: 'This value is not supported.',
  unknown_field: 'This value is not supported.',
  too_long: 'This value is too long.',
  out_of_range: 'This value is outside the allowed range.',
  credential_required: 'Enter the required credential.',
  management_auth_required: 'Enter the required credential.',
  serving_auth_required:
    'Gateway API Key is required to verify which models the channel can serve.',
  insecure_scheme:
    'HTTPS is required. Do not place tokens or passwords in the URL.',
  credentials_not_allowed:
    'HTTPS is required. Do not place tokens or passwords in the URL.',
  sensitive_query_not_allowed:
    'HTTPS is required. Do not place tokens or passwords in the URL.',
  query_or_fragment_not_allowed:
    'Enter a Base URL without query parameters or a fragment.',
  unsafe_target: 'This target is blocked by the network trust policy.',
}

export function costBindingServerFieldError(
  failure: { field?: string; reason?: string },
  t: (key: string) => string,
  upstreamType?: RoutingCostBindingUpstreamType
): { name: keyof CostBindingFormValues; message: string } | null {
  const rawField = failure.field
    ?.trim()
    .replace(/^credentials\./, '')
    .replace(/^credentials\[/, '')
    .replace(/\]$/, '')
  if (!rawField) return null

  const reason = failure.reason
    ?.trim()
    .toLowerCase()
    .replaceAll(/[\s-]+/g, '_')

  if (rawField === 'credentials') {
    if (reason !== 'management_auth_required') return null
    if (upstreamType === 'newapi') {
      return {
        name: 'newApiAccessToken',
        message: t(
          'New API account access requires both an Access Token and user ID. A separate Gateway API Key verifies which models the channel can serve.'
        ),
      }
    }
    if (upstreamType === 'sub2api') {
      return {
        name: 'sub2apiToken',
        message: t(
          'Sub2API account access requires a JWT or both email and password. Gateway API Key is kept separate and does not authorize account balance access.'
        ),
      }
    }
    return null
  }

  const name = serverFieldNames[rawField as keyof typeof serverFieldNames]
  if (!name) return null
  const messageKey =
    (reason ? serverReasonKeys[reason] : undefined) ??
    'The server rejected this value. Review it and try again.'
  return { name, message: t(messageKey) }
}
