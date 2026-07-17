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
import { AxiosError } from 'axios'
import i18next from 'i18next'
import { toast } from 'sonner'

export function handleServerError(error: unknown) {
  // eslint-disable-next-line no-console
  console.log(error)

  let errMsg = i18next.t('Something went wrong!')

  if (
    error &&
    typeof error === 'object' &&
    'status' in error &&
    Number(error.status) === 204
  ) {
    errMsg = i18next.t('Content not found.')
  }

  if (error instanceof AxiosError) {
    const status = error.response?.status
    const data = error.response?.data
    if (status === 304) {
      errMsg = i18next.t('Content not modified!')
    } else if (status === 401) {
      errMsg = i18next.t('Session expired!')
    } else if (status === 403) {
      errMsg = i18next.t('Access Forbidden')
    } else if (typeof data === 'string' && data.trim()) {
      errMsg = data.trim()
    } else if (data && typeof data === 'object') {
      const payload = data as { title?: unknown; message?: unknown }
      if (typeof payload.title === 'string' && payload.title.trim()) {
        errMsg = payload.title.trim()
      } else if (
        typeof payload.message === 'string' &&
        payload.message.trim()
      ) {
        errMsg = payload.message.trim()
      }
    }
  }

  toast.error(errMsg)
}
