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

import {
  buildInviteSequenceMap,
  formatRewardRateSummary,
  getPendingRewardQuotaSortValue,
  getRewardActivities,
} from './reward-display.ts'

const translate = (key: string) => {
  const translations: Record<string, string> = {
    'First Top-up': '首充',
    Continuous: '持续',
  }
  return translations[key] ?? key
}

describe('referral reward display helpers', () => {
  test('formats bound reward ratios as compact activity labels', () => {
    assert.equal(
      formatRewardRateSummary(
        {
          activity_rules: [
            {
              activity_detail: 'Campaign',
              type: 'first_topup',
              percent: 30,
            },
            {
              activity_detail: 'Ongoing',
              type: 'continuous',
              percent: 5,
            },
          ],
        },
        translate
      ),
      '首充30%+持续5%'
    )
  })

  test('keeps one ratio when only one reward rule is bound', () => {
    assert.equal(
      formatRewardRateSummary(
        {
          first_topup_reward_percent: 30,
          continuous_reward_percent: 0,
        },
        translate
      ),
      '首充30%'
    )
    assert.deepEqual(
      getRewardActivities({
        first_topup_reward_percent: 0,
        continuous_reward_percent: 5,
      }),
      [
        {
          activity_detail: 'Continuous Referral',
          type: 'continuous',
          percent: 5,
        },
      ]
    )
  })

  test('numbers invited users by first invitation order', () => {
    const sequenceMap = buildInviteSequenceMap([
      { id: 30, created_at: 300 },
      { id: 10, created_at: 100 },
      { id: 20, created_at: 200 },
    ])

    assert.equal(sequenceMap.get(10), 1)
    assert.equal(sequenceMap.get(20), 2)
    assert.equal(sequenceMap.get(30), 3)
  })

  test('sorts contribution rewards by pending rewards', () => {
    assert.equal(
      getPendingRewardQuotaSortValue({
        pending_reward_quota: 12,
        available_reward_quota: 999,
        transferred_reward_quota: 999,
      }),
      12
    )
  })
})
