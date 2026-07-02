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
import type { RewardActivity } from '../types'

export type RewardRateSource = {
  activity_rules?: RewardActivity[]
  first_topup_reward_percent?: number
  continuous_reward_percent?: number
}

type InvitedSequenceSource = {
  id: number
  created_at: number
}

type PendingRewardSource = {
  pending_reward_quota?: number
  available_reward_quota?: number
  transferred_reward_quota?: number
}

const rewardTypeOrder: Record<RewardActivity['type'], number> = {
  first_topup: 0,
  continuous: 1,
}

export function getRewardActivities(
  source?: RewardRateSource
): RewardActivity[] {
  const rules = (source?.activity_rules ?? [])
    .filter((rule) => rule.activity_detail.trim() && rule.percent > 0)
    .sort(
      (left, right) =>
        rewardTypeOrder[left.type] - rewardTypeOrder[right.type] ||
        right.percent - left.percent
    )
  if (rules.length > 0) return rules

  const fallback: RewardActivity[] = []
  if ((source?.first_topup_reward_percent ?? 0) > 0) {
    fallback.push({
      activity_detail: 'One-time Referral',
      type: 'first_topup',
      percent: source?.first_topup_reward_percent ?? 0,
    })
  }
  if ((source?.continuous_reward_percent ?? 0) > 0) {
    fallback.push({
      activity_detail: 'Continuous Referral',
      type: 'continuous',
      percent: source?.continuous_reward_percent ?? 0,
    })
  }
  return fallback
}

export function formatRewardRateSummary(
  source: RewardRateSource | undefined,
  t: (key: string) => string
) {
  return getRewardActivities(source)
    .map((activity) => {
      const label =
        activity.type === 'first_topup' ? t('First Top-up') : t('Continuous')
      return `${label}${activity.percent}%`
    })
    .join('+')
}

export function getRewardRateSortValue(source?: RewardRateSource) {
  return getRewardActivities(source).reduce(
    (total, activity) => total + activity.percent,
    0
  )
}

export function buildInviteSequenceMap(rows: InvitedSequenceSource[]) {
  const sortedRows = [...rows].sort((left, right) => {
    if (left.created_at !== right.created_at) {
      return left.created_at - right.created_at
    }
    return left.id - right.id
  })
  return new Map(sortedRows.map((row, index) => [row.id, index + 1]))
}

export function getPendingRewardQuotaSortValue(source: PendingRewardSource) {
  return source.pending_reward_quota ?? 0
}
