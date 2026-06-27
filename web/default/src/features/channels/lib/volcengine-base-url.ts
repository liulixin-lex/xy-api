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
export const VOLCENGINE_DEFAULT_BASE_URL = 'https://ark.cn-beijing.volces.com'
export const VOLCENGINE_AP_SOUTHEAST_BASE_URL =
  'https://ark.ap-southeast.bytepluses.com'
export const VOLCENGINE_DOUBAO_CODING_PLAN = 'doubao-coding-plan'

export const VOLCENGINE_BASE_URL_OPTIONS = [
  {
    value: VOLCENGINE_DEFAULT_BASE_URL,
  },
  {
    value: VOLCENGINE_AP_SOUTHEAST_BASE_URL,
  },
  {
    value: VOLCENGINE_DOUBAO_CODING_PLAN,
  },
] as const

export function getVolcEngineBaseUrlSelectValue(
  value: string | null | undefined
) {
  return value || VOLCENGINE_DEFAULT_BASE_URL
}
