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
import { captureReferral } from '../api'
import {
  clearReferralStorage,
  saveAffiliateCode,
  saveInviteBatchCode,
} from './storage'

let pendingCapture: Promise<void> | null = null
let pendingCaptureKey = ''

export function captureReferralParamsFromLocation(): Promise<void> {
  if (typeof window === 'undefined') return Promise.resolve()

  const params = new URLSearchParams(window.location.search)
  const aff = params.get('aff')?.trim() ?? ''
  const inviteBatch = params.get('invite_batch')?.trim() ?? ''
  if (!aff || !inviteBatch) return Promise.resolve()

  const key = `${aff}:${inviteBatch}`
  if (pendingCapture && pendingCaptureKey === key) return pendingCapture

  pendingCaptureKey = key
  pendingCapture = captureReferral({ affCode: aff, inviteBatch })
    .then((res) => {
      if (res?.success && res.data?.aff_code && res.data?.invite_batch_code) {
        saveAffiliateCode(res.data.aff_code)
        saveInviteBatchCode(res.data.invite_batch_code)
        return
      }
      clearReferralStorage()
    })
    .catch(() => {
      /* Keep existing attribution if capture fails because the cookie may still be valid. */
    })

  return pendingCapture
}
