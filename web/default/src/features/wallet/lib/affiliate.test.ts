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
        'https://example.com/sign-up?aff=abc123&aff_rule=first_topup_10'
      )
    })
  })
})
