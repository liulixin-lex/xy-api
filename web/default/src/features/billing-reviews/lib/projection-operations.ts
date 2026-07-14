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
import z from 'zod'

import type {
  BillingProjectionOperationApiError,
  BillingProjectionPage,
} from '../projection-types'

export type BillingProjectionReasonMessages = {
  required: string
  tooLong: string
  singleLine: string
}

function utf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).length
}

export function createBillingProjectionReasonSchema(
  messages: BillingProjectionReasonMessages
) {
  return z.object({
    reason: z
      .string()
      .superRefine((value, context) => {
        const trimmed = value.trim()
        if (!trimmed) {
          context.addIssue({ code: 'custom', message: messages.required })
        }
        if (utf8ByteLength(trimmed) > 1024) {
          context.addIssue({ code: 'custom', message: messages.tooLong })
        }
        if (
          value.includes('\r') ||
          value.includes('\n') ||
          value.includes('\u0000')
        ) {
          context.addIssue({ code: 'custom', message: messages.singleLine })
        }
      })
      .transform((value) => value.trim()),
  })
}

export type BillingProjectionReasonValues = z.infer<
  ReturnType<typeof createBillingProjectionReasonSchema>
>

export function getBillingProjectionNextCursor(
  page: Pick<BillingProjectionPage<unknown>, 'has_more' | 'next_cursor'>,
  currentCursor: number
): number {
  if (!page.has_more) return 0
  const nextCursor = page.next_cursor
  if (
    !Number.isSafeInteger(nextCursor) ||
    nextCursor <= currentCursor ||
    nextCursor <= 0
  ) {
    return 0
  }
  return nextCursor
}

export function billingProjectionOperationNeedsRefresh(
  error: BillingProjectionOperationApiError
): boolean {
  return [403, 404, 409, 412, 422].includes(error.status ?? 0)
}

export function canMutateBillingProjectionPage(state: {
  hasPermission: boolean
  isError: boolean
  isRefetchError: boolean
  isPlaceholderData: boolean
}): boolean {
  return (
    state.hasPermission &&
    !state.isError &&
    !state.isRefetchError &&
    !state.isPlaceholderData
  )
}

export function getBillingProjectionCodeDisplay(
  value: string,
  known: Record<string, string>,
  translate: (key: string, options?: Record<string, unknown>) => string
): string {
  const normalized = value.trim()
  const labelKey = known[normalized]
  if (labelKey) return translate(labelKey)
  return normalized
    ? `${translate('Unknown')}: ${normalized}`
    : translate('Unknown')
}

let projectionOperationSequence = 0

export function createBillingProjectionIdempotencyKey(): string {
  const uuid = globalThis.crypto?.randomUUID?.()
  if (uuid) return `billing-projection-${uuid}`
  projectionOperationSequence += 1
  return `billing-projection-${Date.now().toString(36)}-${projectionOperationSequence.toString(36)}`
}

export class BillingProjectionOperationSession {
  private generation = 0
  private controller: AbortController | null = null
  private inFlightSignature: string | null = null
  private idempotency: { signature: string; key: string } | null = null

  open(): number {
    this.controller?.abort()
    this.controller = new AbortController()
    this.generation += 1
    this.inFlightSignature = null
    this.idempotency = null
    return this.generation
  }

  close(): void {
    this.controller?.abort()
    this.controller = null
    this.generation += 1
    this.inFlightSignature = null
    this.idempotency = null
  }

  isCurrent(generation: number): boolean {
    return this.controller != null && generation === this.generation
  }

  claim(
    signature: string,
    keyFactory: () => string = createBillingProjectionIdempotencyKey
  ): { generation: number; key: string; signal: AbortSignal } | null {
    if (!this.controller || this.inFlightSignature != null) return null
    if (this.idempotency?.signature !== signature) {
      this.idempotency = { signature, key: keyFactory() }
    }
    this.inFlightSignature = signature
    return {
      generation: this.generation,
      key: this.idempotency.key,
      signal: this.controller.signal,
    }
  }

  release(generation: number, signature: string): void {
    if (!this.isCurrent(generation)) return
    if (this.inFlightSignature === signature) this.inFlightSignature = null
  }

  complete(generation: number, signature: string): void {
    if (!this.isCurrent(generation)) return
    if (this.inFlightSignature === signature) this.inFlightSignature = null
    if (this.idempotency?.signature === signature) this.idempotency = null
  }
}
