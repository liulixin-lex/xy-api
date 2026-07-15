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
import {
  COST_BINDING_GROUP_DOM_LIMIT,
  costBindingGroupUsesSubscription,
} from '../lib/cost-binding'
import type { RoutingCostBindingGroupMetadata } from '../types'

export function CostSourceGroupDatalist(props: {
  id: string
  groups: string[]
  groupMeta?: Record<string, RoutingCostBindingGroupMetadata>
  claudeCodeOnlyLabel?: string
  subscriptionLabel?: string
  walletBalanceNotUsedLabel?: string
}) {
  return (
    <datalist id={props.id}>
      {props.groups.slice(0, COST_BINDING_GROUP_DOM_LIMIT).map((group) => {
        const metadata = props.groupMeta?.[group]
        const labels: string[] = []
        if (metadata?.claude_code_only && props.claudeCodeOnlyLabel) {
          labels.push(props.claudeCodeOnlyLabel)
        }
        if (costBindingGroupUsesSubscription(metadata)) {
          if (props.subscriptionLabel) labels.push(props.subscriptionLabel)
          if (props.walletBalanceNotUsedLabel) {
            labels.push(props.walletBalanceNotUsedLabel)
          }
        }
        return (
          <option
            key={group}
            value={group}
            label={labels.length > 0 ? labels.join(' · ') : undefined}
          />
        )
      })}
    </datalist>
  )
}
