/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/
import { api } from '@/lib/api'

import { createPaymentAdminError } from '../payment-admin-errors'

export interface PaymentLimitUsage {
  id: number
  provider: string
  payment_method: string
  currency: string
  currency_exponent: number
  single_limit_minor: string
  daily_limit_minor: string
  timezone: string
  enabled: boolean
  version: number
  day_key: string
  reserved_minor: string
  paid_minor: string
}

export interface PaymentLimitUpdateRequest {
  provider: string
  payment_method: string
  currency: string
  single_limit_minor: string
  daily_limit_minor: string
  timezone: string
  enabled: boolean
}

export interface PaymentLimitMutationResult {
  saved: boolean
  refreshed: boolean
  refresh_error_code?: string
  usage?: PaymentLimitUsage
}

interface ApiResponse<T> {
  success: boolean
  code?: string
  message?: string
  data?: T
}

const requestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
} as const

function unwrap<T>(response: ApiResponse<T>, fallback: string): T {
  if (!response.success || response.data === undefined) {
    throw createPaymentAdminError(response, fallback)
  }
  return response.data
}

export async function listPaymentLimits(): Promise<PaymentLimitUsage[]> {
  const response = await api.get<ApiResponse<PaymentLimitUsage[]>>(
    '/api/option/payment/limits',
    requestConfig
  )
  return unwrap(response.data, 'Failed to load payment limits')
}

export async function updatePaymentLimit(
  request: PaymentLimitUpdateRequest
): Promise<PaymentLimitMutationResult> {
  const response = await api.put<ApiResponse<PaymentLimitMutationResult>>(
    '/api/option/payment/limits',
    request,
    requestConfig
  )
  return unwrap(response.data, 'Failed to save payment limit')
}
