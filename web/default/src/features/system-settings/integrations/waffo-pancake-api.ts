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

import type { PaymentGatewayReadiness } from '../api'

// Catalog / pair admin endpoints. Match
// controller/topup_waffo_pancake.go: empty body creds make the backend
// fall back to persisted OptionMap values, so returning admins don't
// have to re-paste the private key (stripped from GET /api/option/).

export interface CatalogProduct {
  id: string
  name: string
  status: string
}

export interface CatalogStore {
  id: string
  name: string
  status: string
  prodEnabled: boolean
  onetimeProducts: CatalogProduct[]
}

export interface PairResult {
  store_id: string
  store_name: string
  product_id: string
  product_name: string
}

export interface PairFailureParams {
  orphan_store?: boolean
  store_id?: string
  store_name?: string
}

interface BackendBody<T> {
  success: boolean
  code?: string
  params?: PairFailureParams
  data?: T
}

export type CatalogResponse = BackendBody<{ stores: CatalogStore[] }>
export type PairResponse = BackendBody<PairResult>
export type SaveResponse = BackendBody<{
  store_id: string
  product_id: string
  test_mode: boolean
  environment?: 'test' | 'prod' | string
  unit_price: number
  min_top_up: number
  version: number
  readiness?: PaymentGatewayReadiness
}>

export interface WaffoPancakeSaveInput {
  merchantID: string
  privateKey: string
  returnURL: string
  storeID: string
  productID: string
  unitPrice: number
  minTopUp: number
  testMode: boolean
  expectedVersion: number
}

const requestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
} as const

export async function listWaffoPancakeCatalog(
  merchantID: string,
  privateKey: string
): Promise<CatalogResponse> {
  const res = await api.post<CatalogResponse>(
    '/api/option/waffo-pancake/catalog',
    { merchant_id: merchantID, private_key: privateKey },
    requestConfig
  )
  return res.data
}

export async function createWaffoPancakePair(params: {
  merchantID: string
  privateKey: string
  returnURL: string
}): Promise<PairResponse> {
  const res = await api.post<PairResponse>(
    '/api/option/waffo-pancake/pair',
    {
      merchant_id: params.merchantID,
      private_key: params.privateKey,
      return_url: params.returnURL,
    },
    requestConfig
  )
  return res.data
}

export function buildWaffoPancakeSavePayload(input: WaffoPancakeSaveInput) {
  return {
    merchant_id: input.merchantID,
    private_key: input.privateKey,
    return_url: input.returnURL,
    store_id: input.storeID,
    product_id: input.productID,
    unit_price: input.unitPrice,
    min_top_up: input.minTopUp,
    test_mode: input.testMode,
    expected_version: input.expectedVersion,
  }
}

export async function saveWaffoPancakeSettings(
  input: WaffoPancakeSaveInput
): Promise<SaveResponse> {
  const res = await api.post<SaveResponse>(
    '/api/option/waffo-pancake/save',
    buildWaffoPancakeSavePayload(input),
    requestConfig
  )
  return res.data
}
