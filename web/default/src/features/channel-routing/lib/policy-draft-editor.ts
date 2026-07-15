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

import type { PolicyDraftDetail } from '../types'

export type PolicyDraftDetailUpdate = 'apply' | 'defer' | 'ignore'

export function policyDraftDetailIdentity(
  detail: PolicyDraftDetail | null
): string {
  if (!detail) return ''
  return `${detail.id}:${detail.version}:${detail.server_etag || detail.etag}`
}

export function policyDraftDetailUpdate(
  current: PolicyDraftDetail | null,
  incoming: PolicyDraftDetail,
  dirty: boolean
): PolicyDraftDetailUpdate {
  if (
    policyDraftDetailIdentity(current) === policyDraftDetailIdentity(incoming)
  ) {
    return 'ignore'
  }
  if (!current || current.id !== incoming.id || !dirty) return 'apply'
  return 'defer'
}

export function policyDraftDetailBlocksEditor(input: {
  editing: boolean
  isError: boolean
  hasCachedData: boolean
  hasAuthority: boolean
}): boolean {
  return (
    input.editing &&
    input.isError &&
    !input.hasCachedData &&
    !input.hasAuthority
  )
}
