import assert from 'node:assert/strict'
import { afterEach, describe, test } from 'node:test'

import { getAffiliateRule, saveAffiliateRule } from './storage'

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

    saveAffiliateRule('first_topup_10')

    assert.equal(getAffiliateRule(), 'first_topup_10')
  })

  test('normalizes legacy first top-up reward rule values', () => {
    installLocalStorage()

    saveAffiliateRule('first_topup')

    assert.equal(getAffiliateRule(), 'first_topup_10')
  })
})
