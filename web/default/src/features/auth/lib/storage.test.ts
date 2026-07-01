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
import { afterEach, describe, test } from 'node:test'

import {
  getAffiliateCode,
  getAffiliateRule,
  getInviteBatchCode,
  saveAffiliateCode,
  saveAffiliateRule,
  saveInviteBatchCode,
} from './storage'

const originalWindow = globalThis.window

function installLocalStorage() {
  const values = new Map<string, string>()
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      localStorage: {
        getItem: (key: string) => values.get(key) ?? null,
        removeItem: (key: string) => {
          values.delete(key)
        },
        setItem: (key: string, value: string) => {
          values.set(key, value)
        },
      },
    },
  })
}

afterEach(() => {
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: originalWindow,
  })
})

describe('affiliate rule storage', () => {
  test('keeps canonical first top-up reward rule values', () => {
    installLocalStorage()

    saveAffiliateRule('first_topup')

    assert.equal(getAffiliateRule(), 'first_topup')
  })

  test('normalizes legacy first top-up reward rule values', () => {
    installLocalStorage()

    saveAffiliateRule('first_topup_10')

    assert.equal(getAffiliateRule(), 'first_topup')
  })
})

describe('referral parameter storage', () => {
  test('clears affiliate and invite batch values when saving empty strings', () => {
    installLocalStorage()

    saveAffiliateCode(' aff123 ')
    saveInviteBatchCode(' spring ')
    saveAffiliateCode('')
    saveInviteBatchCode('')

    assert.equal(getAffiliateCode(), '')
    assert.equal(getInviteBatchCode(), '')
  })
})
