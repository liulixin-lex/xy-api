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

import { generateReferralLink } from './affiliate'

const originalWindow = globalThis.window

function withWindowOrigin(origin: string, callback: () => void) {
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: { location: { origin } },
  })
  try {
    callback()
  } finally {
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: originalWindow,
    })
  }
}

describe('affiliate link helpers', () => {
  test('generates a referral link from a batch base link', () => {
    withWindowOrigin('https://example.com', () => {
      assert.equal(
        generateReferralLink('/sign-up?invite_batch=spring', 'abc123'),
        'https://example.com/sign-up?invite_batch=spring&aff=abc123'
      )
    })
  })

  test('keeps existing batch query parameters when adding affiliate code', () => {
    withWindowOrigin('https://example.com', () => {
      assert.equal(
        generateReferralLink(
          'https://example.com/sign-up?invite_batch=summer&utm=mail',
          'abc123'
        ),
        'https://example.com/sign-up?invite_batch=summer&utm=mail&aff=abc123'
      )
    })
  })
})
