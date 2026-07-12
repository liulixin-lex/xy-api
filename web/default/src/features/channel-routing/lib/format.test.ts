import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { formatChannelRoutingCost } from './format'

describe('channel routing cost formatting', () => {
  test('preserves meaningful digits for small non-zero costs', () => {
    assert.equal(formatChannelRoutingCost(0.0000045, 'en-US'), '0.0000045')
    assert.equal(formatChannelRoutingCost(0.0000061, 'en-US'), '0.0000061')
    assert.notEqual(formatChannelRoutingCost(0.0000045, 'en-US'), '0')
  })

  test('keeps an explicit known zero distinct from an unknown value', () => {
    assert.equal(formatChannelRoutingCost(0, 'en-US'), '0')
    assert.equal(formatChannelRoutingCost(Number.NaN, 'en-US'), '')
  })
})
