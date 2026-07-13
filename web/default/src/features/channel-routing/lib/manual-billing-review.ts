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
  ManualBillingProviderStatus,
  ManualBillingReviewAction,
  ManualBillingReviewApiError,
  ManualBillingReviewItem,
  ManualBillingReviewKind,
  ManualBillingReviewPage,
  ManualBillingReviewResolutionRequest,
} from '../billing-review-types'

const providerClockSkewMs = 5 * 60 * 1000

export type ManualBillingReviewValidationMessages = {
  actionRequired: string
  actionUnavailable: string
  upstreamTaskRequired: string
  upstreamTaskTooLong: string
  evidenceRequired: string
  evidenceTooLong: string
  reasonRequired: string
  reasonTooLong: string
  singleLineRequired: string
  checkedTimeRequired: string
  checkedTimeTooEarly: string
  checkedTimeFuture: string
}

export type ManualBillingReviewFormInput = {
  action: ManualBillingReviewAction | null
  rejection_provider_status: 'confirmed_rejected' | 'confirmed_not_found'
  upstream_task_id: string
  provider_checked_at: string
  evidence_reference: string
  reason: string
}

export type ManualBillingReviewFormValues = Omit<
  ManualBillingReviewFormInput,
  'action'
> & {
  action: ManualBillingReviewAction
}

export function getManualBillingReviewKindLabelKey(
  reviewKind: string
): string | null {
  switch (reviewKind) {
    case 'send_outcome':
      return 'Send outcome'
    case 'acceptance_overage':
      return 'Acceptance overage'
    case 'accepted_handoff':
      return 'Accepted handoff'
    case 'terminal_overage':
      return 'Terminal overage'
    default:
      return null
  }
}

export function manualBillingReviewKindIsSupported(
  reviewKind: string
): reviewKind is ManualBillingReviewKind {
  return getManualBillingReviewKindLabelKey(reviewKind) != null
}

export function getManualBillingReviewKindDisplay(
  reviewKind: string,
  translate: (key: string, options?: Record<string, unknown>) => string
): string {
  const labelKey = getManualBillingReviewKindLabelKey(reviewKind)
  if (labelKey) return translate(labelKey)
  const code = reviewKind.trim() || translate('Unknown')
  return `${translate('Unknown review type')}: ${code}`
}

export function manualBillingReviewIsOverage(reviewKind: string): boolean {
  return (
    reviewKind === 'acceptance_overage' || reviewKind === 'terminal_overage'
  )
}

export function getManualBillingReviewEvidenceWindowStart(
  review: ManualBillingReviewItem
): number {
  return review.attempts.reduce(
    (latest, attempt) => Math.max(latest, attempt.authorized_ms),
    Math.max(0, review.manual_review_since_ms)
  )
}

export type ManualBillingReviewConfirmationImpact =
  | { kind: 'overage_accept'; additional: number; final: number }
  | { kind: 'overage_reject'; writeOff: number; final: number }
  | { kind: 'accepted_handoff'; adjustment: number; final: number }
  | { kind: 'accepted'; final: number }
  | { kind: 'rejected'; refund: number; final: number }

export function getManualBillingReviewConfirmationImpact(
  review: ManualBillingReviewItem,
  action: ManualBillingReviewAction
): ManualBillingReviewConfirmationImpact {
  const consequences = review.financial_consequences
  if (manualBillingReviewIsOverage(review.review_kind)) {
    if (action === 'confirmed_accepted') {
      return {
        kind: 'overage_accept',
        additional: consequences.accept_additional_charge,
        final: consequences.accept_final_charge,
      }
    }
    return {
      kind: 'overage_reject',
      writeOff: consequences.reject_write_off,
      final: consequences.reject_final_charge,
    }
  }
  if (
    review.review_kind === 'accepted_handoff' &&
    action === 'confirmed_accepted'
  ) {
    return {
      kind: 'accepted_handoff',
      adjustment: consequences.accept_additional_charge,
      final: consequences.accept_final_charge,
    }
  }
  if (action === 'confirmed_accepted') {
    return { kind: 'accepted', final: consequences.accept_final_charge }
  }
  return {
    kind: 'rejected',
    refund: consequences.reject_refund,
    final: consequences.reject_final_charge,
  }
}

function utf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).length
}

function createEvidenceString(options: {
  requiredMessage?: string
  maxBytes: number
  maxMessage: string
  singleLineMessage: string
}) {
  return z
    .string()
    .superRefine((value, context) => {
      const trimmed = value.trim()
      if (options.requiredMessage && trimmed.length === 0) {
        context.addIssue({ code: 'custom', message: options.requiredMessage })
      }
      if (utf8ByteLength(trimmed) > options.maxBytes) {
        context.addIssue({ code: 'custom', message: options.maxMessage })
      }
      if (
        value.includes('\r') ||
        value.includes('\n') ||
        value.includes('\u0000')
      ) {
        context.addIssue({
          code: 'custom',
          message: options.singleLineMessage,
        })
      }
    })
    .transform((value) => value.trim())
}

export function createManualBillingReviewSchema(
  review: ManualBillingReviewItem,
  messages: ManualBillingReviewValidationMessages,
  now: () => number = Date.now
) {
  const evidenceWindowStart = getManualBillingReviewEvidenceWindowStart(review)
  return z
    .object({
      action: z
        .enum(['confirmed_accepted', 'confirmed_rejected'])
        .nullable()
        .refine((value) => value != null, messages.actionRequired)
        .transform((value) => value as ManualBillingReviewAction),
      rejection_provider_status: z.enum([
        'confirmed_rejected',
        'confirmed_not_found',
      ]),
      upstream_task_id: createEvidenceString({
        maxBytes: 191,
        maxMessage: messages.upstreamTaskTooLong,
        singleLineMessage: messages.singleLineRequired,
      }),
      provider_checked_at: z.string().superRefine((value, context) => {
        const timestamp = new Date(value).getTime()
        if (!value || !Number.isFinite(timestamp) || timestamp <= 0) {
          context.addIssue({
            code: 'custom',
            message: messages.checkedTimeRequired,
          })
          return
        }
        if (evidenceWindowStart > 0 && timestamp < evidenceWindowStart) {
          context.addIssue({
            code: 'custom',
            message: messages.checkedTimeTooEarly,
          })
        }
        if (timestamp > now() + providerClockSkewMs) {
          context.addIssue({
            code: 'custom',
            message: messages.checkedTimeFuture,
          })
        }
      }),
      evidence_reference: createEvidenceString({
        requiredMessage: messages.evidenceRequired,
        maxBytes: 512,
        maxMessage: messages.evidenceTooLong,
        singleLineMessage: messages.singleLineRequired,
      }),
      reason: createEvidenceString({
        requiredMessage: messages.reasonRequired,
        maxBytes: 1024,
        maxMessage: messages.reasonTooLong,
        singleLineMessage: messages.singleLineRequired,
      }),
    })
    .superRefine((value, context) => {
      if (
        !manualBillingReviewKindIsSupported(review.review_kind) ||
        manualBillingReviewHasUnknownBlocker(review)
      ) {
        context.addIssue({
          code: 'custom',
          path: ['action'],
          message: messages.actionUnavailable,
        })
        return
      }
      if (value.action === 'confirmed_accepted' && !review.can_accept) {
        context.addIssue({
          code: 'custom',
          path: ['action'],
          message: messages.actionUnavailable,
        })
      }
      if (
        value.action === 'confirmed_rejected' &&
        (!review.can_reject || review.review_kind === 'accepted_handoff')
      ) {
        context.addIssue({
          code: 'custom',
          path: ['action'],
          message: messages.actionUnavailable,
        })
      }
      if (
        review.review_kind === 'send_outcome' &&
        value.action === 'confirmed_accepted' &&
        value.upstream_task_id.length === 0
      ) {
        context.addIssue({
          code: 'custom',
          path: ['upstream_task_id'],
          message: messages.upstreamTaskRequired,
        })
      }
    })
}

export function getManualBillingReviewProviderStatus(
  reviewKind: string,
  action: ManualBillingReviewAction,
  rejectionStatus: 'confirmed_rejected' | 'confirmed_not_found'
): ManualBillingProviderStatus | null {
  if (reviewKind === 'terminal_overage') return 'terminal_usage_verified'
  if (
    reviewKind === 'acceptance_overage' ||
    reviewKind === 'accepted_handoff'
  ) {
    return 'confirmed_accepted'
  }
  if (reviewKind !== 'send_outcome') return null
  if (action === 'confirmed_accepted') return 'confirmed_accepted'
  return rejectionStatus
}

export function buildManualBillingReviewResolution(
  review: ManualBillingReviewItem,
  values: ManualBillingReviewFormValues
): ManualBillingReviewResolutionRequest {
  const providerStatus = getManualBillingReviewProviderStatus(
    review.review_kind,
    values.action,
    values.rejection_provider_status
  )
  if (!providerStatus) {
    throw new Error('Unsupported manual billing review kind')
  }
  return {
    action: values.action,
    expected_version: review.review_version,
    upstream_task_id: values.upstream_task_id.trim(),
    provider_status: providerStatus,
    provider_checked_ms: new Date(values.provider_checked_at).getTime(),
    evidence_reference: values.evidence_reference.trim(),
    reason: values.reason.trim(),
  }
}

export type ManualBillingReviewConsequenceRow = {
  key: keyof ManualBillingReviewItem['financial_consequences']
  labelKey: string
  value: number
  outcome: 'current' | 'accept' | 'reject'
}

export function getManualBillingReviewConsequenceRows(
  review: ManualBillingReviewItem
): ManualBillingReviewConsequenceRow[] {
  const consequences = review.financial_consequences
  return [
    {
      key: 'current_charge',
      labelKey: 'Current charge',
      value: consequences.current_charge,
      outcome: 'current',
    },
    {
      key: 'accept_additional_charge',
      labelKey:
        consequences.accept_additional_charge < 0
          ? 'Charge adjustment if accepted'
          : 'Additional charge if accepted',
      value: consequences.accept_additional_charge,
      outcome: 'accept',
    },
    {
      key: 'accept_final_charge',
      labelKey: 'Final charge if accepted',
      value: consequences.accept_final_charge,
      outcome: 'accept',
    },
    {
      key: 'reject_refund',
      labelKey: 'Refund if rejected',
      value: consequences.reject_refund,
      outcome: 'reject',
    },
    {
      key: 'reject_final_charge',
      labelKey: 'Final charge if rejected',
      value: consequences.reject_final_charge,
      outcome: 'reject',
    },
    {
      key: 'reject_write_off',
      labelKey: 'Write-off if rejected',
      value: consequences.reject_write_off,
      outcome: 'reject',
    },
  ]
}

export function getManualBillingReviewBlockerLabelKey(
  blocker: string
): string | null {
  switch (blocker) {
    case 'resolve_permission_required':
      return 'Resolve permission is required'
    case 'authorized_attempt_missing_or_ambiguous':
      return 'The authorized attempt is missing or ambiguous'
    case 'acceptance_intent_missing_or_invalid':
      return 'The acceptance intent is missing or invalid'
    case 'submission_send_lease_active':
      return 'The submission send lease is still active'
    case 'acceptance_overage_context_missing_or_invalid':
      return 'The acceptance overage context is missing or invalid'
    case 'accepted_handoff_context_missing_or_invalid':
      return 'The accepted handoff context is missing or invalid'
    case 'terminal_billing_operation_missing_or_invalid':
      return 'The terminal billing operation is missing or invalid'
    case 'unknown_review_kind':
      return 'This billing review type is not supported'
    default:
      return null
  }
}

export function getManualBillingReviewBlockerDisplay(
  blocker: string,
  translate: (key: string, options?: Record<string, unknown>) => string
): string {
  const labelKey = getManualBillingReviewBlockerLabelKey(blocker)
  if (labelKey) return translate(labelKey)
  const code = blocker.trim() || translate('Unknown')
  return `${translate('Unknown')}: ${code}`
}

export function manualBillingReviewHasUnknownBlocker(
  review: Pick<ManualBillingReviewItem, 'blockers'>
): boolean {
  return review.blockers.some(
    (blocker) => getManualBillingReviewBlockerLabelKey(blocker) == null
  )
}

export function getManualBillingReviewNextCursor(
  page: Pick<ManualBillingReviewPage, 'has_more' | 'next_cursor'>,
  currentCursor: number
): number {
  if (!page.has_more) return 0
  const nextCursor = page.next_cursor
  if (
    typeof nextCursor !== 'number' ||
    !Number.isSafeInteger(nextCursor) ||
    nextCursor <= currentCursor
  ) {
    return 0
  }
  return nextCursor
}

export function manualBillingReviewNeedsRefresh(
  error: ManualBillingReviewApiError
): boolean {
  return (
    error.status === 403 ||
    error.status === 409 ||
    error.status === 412 ||
    error.status === 404 ||
    error.code === 'decision_blocked'
  )
}

export function toLocalDateTimeInput(timestamp: number): string {
  const date = new Date(timestamp)
  const offset = date.getTimezoneOffset() * 60_000
  return new Date(timestamp - offset).toISOString().slice(0, 19)
}

let idempotencySequence = 0

export function createManualBillingReviewIdempotencyKey(): string {
  const uuid = globalThis.crypto?.randomUUID?.()
  if (uuid) return `billing-review-${uuid}`
  idempotencySequence += 1
  return `billing-review-${Date.now().toString(36)}-${idempotencySequence.toString(36)}`
}

export class ManualBillingReviewSession {
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

  claimSubmission(
    signature: string,
    keyFactory: () => string = createManualBillingReviewIdempotencyKey
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

  releaseSubmission(generation: number, signature: string): void {
    if (!this.isCurrent(generation)) return
    if (this.inFlightSignature === signature) this.inFlightSignature = null
  }

  completeSubmission(generation: number, signature: string): void {
    if (!this.isCurrent(generation)) return
    if (this.inFlightSignature === signature) this.inFlightSignature = null
    if (this.idempotency?.signature === signature) this.idempotency = null
  }
}
