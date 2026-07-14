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
import { api } from '@/lib/api'

import type {
  ApiResponse,
  RoutingAgentRecommendation,
  RoutingBinding,
  RoutingBindingActionResult,
  RoutingBindingRequest,
  RoutingBreaker,
  RoutingCostSnapshot,
  RoutingMetric,
  RoutingSyncResponse,
  SmartRoutingSettings,
} from './types'

export async function getSmartRoutingSettings() {
  const res = await api.get<ApiResponse<SmartRoutingSettings>>(
    '/api/smart-routing/settings',
    { skipBusinessError: true }
  )
  if (res.data.data) {
    res.data.data.server_etag = res.headers.etag || ''
  }
  return res.data
}

export async function updateSmartRoutingSettings(
  request: SmartRoutingSettings
) {
  const res = await api.put<ApiResponse<SmartRoutingSettings>>(
    '/api/smart-routing/settings',
    request,
    {
      skipBusinessError: true,
      headers: { 'If-Match': request.server_etag || '' },
    }
  )
  if (res.data.data) {
    res.data.data.server_etag = res.headers.etag || ''
  }
  return res.data
}

export async function listSmartRoutingBindings() {
  const res = await api.get<ApiResponse<RoutingBinding[]>>(
    '/api/smart-routing/bindings',
    { skipBusinessError: true }
  )
  return res.data
}

export async function createSmartRoutingBinding(
  request: RoutingBindingRequest
) {
  const res = await api.post<ApiResponse<RoutingBinding>>(
    '/api/smart-routing/bindings',
    request,
    { skipBusinessError: true }
  )
  return res.data
}

export async function updateSmartRoutingBinding(
  channelId: number,
  request: RoutingBindingRequest,
  etag: string
) {
  const res = await api.put<ApiResponse<RoutingBinding>>(
    `/api/smart-routing/bindings/${channelId}`,
    request,
    { skipBusinessError: true, headers: { 'If-Match': etag } }
  )
  return res.data
}

export async function deleteSmartRoutingBinding(binding: RoutingBinding) {
  const res = await api.delete<ApiResponse<{ channel_id: number }>>(
    `/api/smart-routing/bindings/${binding.channel_id}`,
    {
      skipBusinessError: true,
      headers: { 'If-Match': binding.etag },
    }
  )
  return res.data
}

export async function testSmartRoutingBinding(
  channelId: number | 'new',
  request?: RoutingBindingRequest
) {
  const res = await api.post<ApiResponse<RoutingBindingActionResult>>(
    `/api/smart-routing/bindings/${channelId}/test`,
    request,
    { skipBusinessError: true }
  )
  return res.data
}

export async function loadSmartRoutingBindingGroups(
  channelId: number | 'new',
  request?: RoutingBindingRequest
) {
  const res = await api.post<ApiResponse<RoutingBindingActionResult>>(
    `/api/smart-routing/bindings/${channelId}/groups`,
    request,
    { skipBusinessError: true }
  )
  return res.data
}

export async function listSmartRoutingMetrics(limit = 100) {
  const res = await api.get<ApiResponse<RoutingMetric[]>>(
    '/api/smart-routing/metrics',
    { params: { limit }, skipBusinessError: true }
  )
  return res.data
}

export async function listSmartRoutingSnapshots(limit = 100) {
  const res = await api.get<ApiResponse<RoutingCostSnapshot[]>>(
    '/api/smart-routing/snapshots',
    { params: { limit }, skipBusinessError: true }
  )
  return res.data
}

export async function listSmartRoutingBreakers(limit = 100) {
  const res = await api.get<ApiResponse<RoutingBreaker[]>>(
    '/api/smart-routing/breakers',
    { params: { limit }, skipBusinessError: true }
  )
  return res.data
}

export async function resetSmartRoutingBreaker(id: number) {
  const res = await api.post<ApiResponse<null>>(
    `/api/smart-routing/breakers/${id}/reset`,
    undefined,
    { skipBusinessError: true }
  )
  return res.data
}

export async function enqueueSmartRoutingSync() {
  const res = await api.post<ApiResponse<RoutingSyncResponse>>(
    '/api/smart-routing/sync',
    undefined,
    { skipBusinessError: true }
  )
  return res.data
}

export async function listSmartRoutingAgentRecommendations(limit = 100) {
  const res = await api.get<ApiResponse<RoutingAgentRecommendation[]>>(
    '/api/smart-routing/agent/recommendations',
    { params: { limit }, skipBusinessError: true }
  )
  return res.data
}

export async function approveSmartRoutingAgentRecommendation(id: number) {
  const res = await api.post<ApiResponse<null>>(
    `/api/smart-routing/agent/recommendations/${id}/approve`,
    undefined,
    { skipBusinessError: true }
  )
  return res.data
}

export async function rejectSmartRoutingAgentRecommendation(id: number) {
  const res = await api.post<ApiResponse<null>>(
    `/api/smart-routing/agent/recommendations/${id}/reject`,
    undefined,
    { skipBusinessError: true }
  )
  return res.data
}
