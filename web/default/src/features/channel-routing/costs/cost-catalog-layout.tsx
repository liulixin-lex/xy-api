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
import type { ReactNode } from 'react'

export function CostCatalogHierarchyLayout(props: {
  pools: ReactNode
  members: ReactNode
  models: ReactNode
  detail: ReactNode
}) {
  return (
    <div className='overflow-hidden rounded-lg border xl:grid xl:grid-cols-[minmax(12rem,0.7fr)_minmax(14rem,0.9fr)_minmax(16rem,1fr)_minmax(20rem,1.25fr)]'>
      {props.pools}
      {props.members}
      {props.models}
      {props.detail}
    </div>
  )
}
