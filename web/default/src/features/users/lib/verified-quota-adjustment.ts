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
import type { StartVerificationOptions } from '@/features/auth/secure-verification'

import type { ApiResponse, User } from '../types'

type QuotaAdjustmentResponse = ApiResponse<Partial<User>>

interface VerifiedQuotaAdjustmentOptions {
  runWithVerification: <T>(
    operation: () => Promise<T>,
    options?: StartVerificationOptions
  ) => Promise<T | null>
  request: () => Promise<QuotaAdjustmentResponse>
  onSuccess: (result: QuotaAdjustmentResponse) => void | Promise<void>
  failureMessage: string
  verification: StartVerificationOptions
}

export async function executeVerifiedQuotaAdjustment(
  options: VerifiedQuotaAdjustmentOptions
): Promise<QuotaAdjustmentResponse | null> {
  return options.runWithVerification(async () => {
    const result = await options.request()
    if (!result.success) {
      throw new Error(result.message || options.failureMessage)
    }
    await options.onSuccess(result)
    return result
  }, options.verification)
}
