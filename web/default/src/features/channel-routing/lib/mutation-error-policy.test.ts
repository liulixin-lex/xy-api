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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { MutationCache, QueryClient } from '@tanstack/react-query'
import { AxiosError, AxiosHeaders } from 'axios'

import { createMutationCacheErrorHandler } from '@/lib/query-error-policy'

function axiosStatusError(status: number): AxiosError {
  return new AxiosError(
    `HTTP ${status}`,
    'ERR_BAD_RESPONSE',
    undefined,
    undefined,
    {
      data: {},
      status,
      statusText: String(status),
      headers: {},
      config: { headers: new AxiosHeaders() },
    }
  )
}

async function runFailedMutation(options: {
  error: Error
  handleErrorLocally?: boolean
  localOnError?: boolean
}): Promise<{ globallyHandled: Error[]; locallyHandled: Error[] }> {
  const globallyHandled: Error[] = []
  const locallyHandled: Error[] = []
  const queryClient = new QueryClient({
    mutationCache: new MutationCache({
      onError: createMutationCacheErrorHandler((error) => {
        globallyHandled.push(error as Error)
      }),
    }),
    defaultOptions: {
      mutations: {
        retry: false,
      },
    },
  })
  const mutation = queryClient.getMutationCache().build(queryClient, {
    mutationFn: async () => {
      throw options.error
    },
    meta: { handleErrorLocally: options.handleErrorLocally },
    ...(options.localOnError
      ? {
          onError: (error: Error) => {
            locallyHandled.push(error)
          },
        }
      : {}),
  })

  await assert.rejects(mutation.execute(undefined))
  return { globallyHandled, locallyHandled }
}

describe('routing mutation error notification policy', () => {
  test('keeps 401 and 403 in the global handler alongside a local onError', async () => {
    for (const status of [401, 403]) {
      const error = axiosStatusError(status)
      const result = await runFailedMutation({
        error,
        handleErrorLocally: true,
        localOnError: true,
      })

      assert.deepEqual(result.locallyHandled, [error])
      assert.deepEqual(result.globallyHandled, [error])
    }
  })

  test('suppresses the global handler for a locally handled non-auth error', async () => {
    const error = new Error('inline error')
    const result = await runFailedMutation({
      error,
      handleErrorLocally: true,
      localOnError: true,
    })

    assert.deepEqual(result.locallyHandled, [error])
    assert.deepEqual(result.globallyHandled, [])
  })

  test('globally handles mutations without the local error contract', async () => {
    const error = new Error('unhandled error')
    const result = await runFailedMutation({ error })

    assert.deepEqual(result.locallyHandled, [])
    assert.deepEqual(result.globallyHandled, [error])
  })
})
