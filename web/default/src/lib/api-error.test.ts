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

import { getApiErrorMessage } from './api-error'

describe('getApiErrorMessage', () => {
  test('prefers the backend response message over the generic HTTP error', () => {
    const error = Object.assign(
      new Error('Request failed with status code 409'),
      {
        response: {
          data: {
            message: 'Payment settings changed in another session.',
          },
        },
      }
    )

    assert.equal(
      getApiErrorMessage(error, 'fallback'),
      'Payment settings changed in another session.'
    )
  })

  test('falls back to the Error message and then the supplied copy', () => {
    assert.equal(
      getApiErrorMessage(new Error('network failed'), 'fallback'),
      'network failed'
    )
    assert.equal(getApiErrorMessage({}, 'fallback'), 'fallback')
  })
})
