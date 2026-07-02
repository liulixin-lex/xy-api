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
export type RewardActivity = {
  activity_detail: string
  type: 'continuous' | 'first_topup'
  percent: number
}

export interface InvitedUser {
  id: number
  username: string
  display_name: string
  created_at: number
  invite_reward_rule: 'continuous' | 'first_topup'
  invite_reward_percent: number
  first_topup_reward_percent: number
  continuous_reward_percent: number
  activity_rules: RewardActivity[]
  contribution_quota: number
  pending_reward_quota: number
  available_reward_quota: number
  transferred_reward_quota: number
  canceled_reward_quota: number
}

export interface InviteLinkBatch {
  id: number
  name: string
  code: string
  base_link: string
  first_topup_reward_percent: number
  continuous_reward_percent: number
  activity_rules: RewardActivity[]
  start_time: number
  end_time: number
  description_mode: 'preset' | 'custom'
  preset_description: string
  custom_description: string
  is_active: boolean
  is_valid?: boolean
  usage_count?: number
  created_at: number
  updated_at: number
}

export interface ReferralRewardDashboard {
  active_batch?: InviteLinkBatch
  invite_link: string
  pending_reward_quota: number
  available_reward_quota: number
  transferred_reward_quota: number
  canceled_reward_quota: number
  invited_user_count: number
  invited_users: InvitedUser[]
}
