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
import type { RoutingAttempt, RoutingCostEstimate } from '../types'

export function hasCurrentChannelCostAudit(
  estimate: RoutingCostEstimate
): boolean {
  return (
    Boolean(estimate.pricing_identity) ||
    (estimate.configuration_revision ?? 0) > 0 ||
    estimate.baseline_expected_known != null ||
    estimate.baseline_worst_case_known != null
  )
}

export function hasCurrentRoutingAttemptCostAudit(
  attempt: RoutingAttempt
): boolean {
  return (
    Boolean(attempt.pricing_identity) ||
    (attempt.configuration_revision ?? 0) > 0 ||
    Boolean(attempt.unknown_reason) ||
    attempt.baseline_expected_known === true ||
    attempt.baseline_worst_case_known === true
  )
}

export function routingCostUnknownReasonLabel(
  reason: string,
  translate: (key: string) => string
): string {
  switch (reason) {
    case 'effective_model_missing':
      return translate('The effective model mapping is unavailable.')
    case 'system_pricing_missing':
      return translate('System pricing is unavailable for the effective model.')
    case 'billing_expression_missing':
      return translate('The billing expression is missing.')
    case 'billing_expression_invalid':
      return translate('The billing expression is invalid.')
    case 'system_pricing_invalid':
      return translate('The system pricing configuration is invalid.')
    default:
      return reason
  }
}
