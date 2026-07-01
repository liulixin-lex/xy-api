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

import { generateAffiliateLink } from './affiliate'

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
  test('generates the default continuous referral link without rule query', () => {
    withWindowOrigin('https://example.com', () => {
      assert.equal(
        generateAffiliateLink('abc123'),
        'https://example.com/sign-up?aff=abc123'
      )
    })
  })

  test('generates a first top-up referral link with the rule query', () => {
    withWindowOrigin('https://example.com', () => {
      assert.equal(
        generateAffiliateLink('abc123', 'first_topup'),
        'https://example.com/sign-up?aff=abc123&aff_rule=first_topup'
      )
    })
  })
})
