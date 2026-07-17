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
import type { Mutation, MutationMeta } from '@tanstack/react-query'
import { AxiosError } from 'axios'

export function authenticationOrAuthorizationStatus(
  error: unknown
): 401 | 403 | undefined {
  if (!(error instanceof AxiosError)) return undefined
  const status = error.response?.status
  return status === 401 || status === 403 ? status : undefined
}

export function isAuthenticationOrAuthorizationError(error: unknown): boolean {
  return authenticationOrAuthorizationStatus(error) != null
}

export function mutationErrorIsHandledLocally(
  error: unknown,
  meta: MutationMeta | undefined
): boolean {
  return (
    meta?.handleErrorLocally === true &&
    !isAuthenticationOrAuthorizationError(error)
  )
}

export function createMutationCacheErrorHandler(
  handleGlobalError: (error: unknown) => void
) {
  return (
    error: Error,
    _variables: unknown,
    _onMutateResult: unknown,
    mutation: Mutation<unknown, unknown, unknown, unknown>
  ) => {
    if (mutationErrorIsHandledLocally(error, mutation.meta)) return
    handleGlobalError(error)
  }
}
