import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { channelRoutingIntlLocale, formatChannelRoutingCost } from './format'

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

  test('maps internal Chinese locale codes to valid Intl locales', () => {
    assert.equal(channelRoutingIntlLocale('zhCN'), 'zh-CN')
    assert.equal(channelRoutingIntlLocale('zhTW'), 'zh-TW')
    assert.doesNotThrow(() => formatChannelRoutingCost(12.5, 'zhCN'))
    assert.doesNotThrow(() => formatChannelRoutingCost(12.5, 'zhTW'))
  })
})
