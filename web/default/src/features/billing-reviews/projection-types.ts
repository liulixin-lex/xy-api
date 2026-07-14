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
export type BillingProjectionDataset = 'stats' | 'logs' | 'conflicts'
export type FailedBillingProjectionDataset = Exclude<
  BillingProjectionDataset,
  'conflicts'
>

export type BillingProjectionOutcome = {
  user?: string
  channel?: string
  data_export?: string
  log?: string
}

export type FailedBillingProjection = {
  id: number
  kind: string
  reference_id: number
  user_id?: number
  channel_id?: number
  operation_key_hash: string
  state: string
  disposition?: string
  failure_code: string
  error?: string
  attempts: number
  created_time_ms: number
  updated_time_ms: number
  completed_time_ms: number
  outcome: BillingProjectionOutcome
  requeueable: boolean
  etag: string
}

export type BillingLogSinkConflict = {
  id: number
  projection_id: number
  operation_key_hash: string
  state: string
  version: number
  distinct_receipts: number
  physical_rows: number
  first_detected_ms: number
  last_detected_ms: number
  etag: string
}

export type BillingProjectionPage<T> = {
  items: T[]
  count: number
  has_more: boolean
  next_cursor: number
}

export type BillingProjectionOperationResult = {
  operation_id: number
  action: string
  target_id: number
  state: string
  outcome: string
  completed_time_ms: number
  replayed: boolean
}

export type BillingProjectionOperationApiError = {
  status?: number
  code?: string
  message?: string
}
