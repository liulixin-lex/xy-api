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
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

export type DataTableManualViewOption<TColumn extends string> = {
  id: TColumn
  label: string
  canHide?: boolean
}

type DataTableManualViewOptionsProps<TColumn extends string> = {
  columns: Array<DataTableManualViewOption<TColumn>>
  hiddenColumns: Partial<Record<TColumn, boolean>>
  onVisibilityChange: (column: TColumn, visible: boolean) => void
}

export function DataTableManualViewOptions<TColumn extends string>(
  props: DataTableManualViewOptionsProps<TColumn>
) {
  const { t } = useTranslation()
  const hideableColumns = props.columns.filter(
    (column) => column.canHide !== false
  )

  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger
        render={
          <Button
            variant='outline'
            className='shrink-0'
            aria-label={t('View')}
          />
        }
      >
        {t('View')}
      </DropdownMenuTrigger>
      <DropdownMenuContent align='end' className='w-[170px]'>
        <DropdownMenuGroup>
          <DropdownMenuLabel>{t('Toggle columns')}</DropdownMenuLabel>
          {hideableColumns.map((column) => (
            <DropdownMenuCheckboxItem
              key={column.id}
              checked={!props.hiddenColumns[column.id]}
              onCheckedChange={(value) =>
                props.onVisibilityChange(column.id, !!value)
              }
            >
              {column.label}
            </DropdownMenuCheckboxItem>
          ))}
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
