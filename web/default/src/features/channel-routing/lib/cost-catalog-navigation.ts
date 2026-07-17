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
export type CostCatalogNavigationState = {
  poolPage: number
  memberPage: number
  modelPage: number
  poolSearch: string
  memberSearch: string
  modelSearch: string
  selectedPoolId?: number
  selectedMemberId?: number
  selectedModelName?: string
}

export type CostCatalogNavigationAction =
  | { type: 'search-pools'; value: string }
  | { type: 'page-pools'; page: number }
  | { type: 'select-pool'; poolId: number }
  | { type: 'search-members'; value: string }
  | { type: 'page-members'; page: number }
  | { type: 'select-member'; memberId: number }
  | { type: 'search-models'; value: string }
  | { type: 'page-models'; page: number }
  | { type: 'select-model'; modelName: string }

export const initialCostCatalogNavigationState: CostCatalogNavigationState = {
  poolPage: 1,
  memberPage: 1,
  modelPage: 1,
  poolSearch: '',
  memberSearch: '',
  modelSearch: '',
}

export function reduceCostCatalogNavigation(
  state: CostCatalogNavigationState,
  action: CostCatalogNavigationAction
): CostCatalogNavigationState {
  switch (action.type) {
    case 'search-pools':
      return {
        ...state,
        poolSearch: action.value,
        poolPage: 1,
        memberPage: 1,
        modelPage: 1,
        selectedPoolId: undefined,
        selectedMemberId: undefined,
        selectedModelName: undefined,
      }
    case 'page-pools':
      return {
        ...state,
        poolPage: action.page,
        memberPage: 1,
        modelPage: 1,
        selectedPoolId: undefined,
        selectedMemberId: undefined,
        selectedModelName: undefined,
      }
    case 'select-pool':
      return {
        ...state,
        memberPage: 1,
        modelPage: 1,
        selectedPoolId: action.poolId,
        selectedMemberId: undefined,
        selectedModelName: undefined,
      }
    case 'search-members':
      return {
        ...state,
        memberSearch: action.value,
        memberPage: 1,
        modelPage: 1,
        selectedMemberId: undefined,
        selectedModelName: undefined,
      }
    case 'page-members':
      return {
        ...state,
        memberPage: action.page,
        modelPage: 1,
        selectedMemberId: undefined,
        selectedModelName: undefined,
      }
    case 'select-member':
      return {
        ...state,
        modelPage: 1,
        selectedMemberId: action.memberId,
        selectedModelName: undefined,
      }
    case 'search-models':
      return {
        ...state,
        modelSearch: action.value,
        modelPage: 1,
        selectedModelName: undefined,
      }
    case 'page-models':
      return {
        ...state,
        modelPage: action.page,
        selectedModelName: undefined,
      }
    case 'select-model':
      return { ...state, selectedModelName: action.modelName }
  }
}
