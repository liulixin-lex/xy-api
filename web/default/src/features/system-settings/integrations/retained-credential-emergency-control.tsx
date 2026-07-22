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
import { SecurityWarningIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import * as React from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
} from '@/components/ui/alert'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import type { StartVerificationOptions } from '@/features/auth/secure-verification'
import { isVerificationRequiredError } from '@/lib/secure-verification'
import { cn } from '@/lib/utils'

import {
  disableRetainedPaymentCredential,
  getRetainedCredentialDisablePreview,
  type PaymentGatewayReadiness,
} from '../api'
import {
  createPaymentAdminError,
  getRetainedCredentialDisableErrorMessage,
} from '../payment-admin-errors'
import {
  EMERGENCY_CREDENTIAL_REVOCATION_REASON_MAX_LENGTH,
  isEmergencyCredentialRevocationReasonValid,
} from '../payment-credential-revocation'
import type {
  RetainedCredentialDisablePreview,
  RetainedCredentialDisableResponse,
  RetainedPaymentProvider,
} from '../retained-payment-credential-disable'

type DisableResult = RetainedCredentialDisableResponse<{
  readiness?: PaymentGatewayReadiness
  version?: number
}>

type Props = {
  provider: RetainedPaymentProvider
  disabled: boolean
  withVerification: <T>(
    apiCall: () => Promise<T>,
    config?: StartVerificationOptions
  ) => Promise<T | null>
  onCompleted: (
    provider: RetainedPaymentProvider,
    result: DisableResult
  ) => Promise<boolean>
  onStale: () => Promise<unknown>
  onPendingChange?: (pending: boolean) => void
}

function hasHttpStatus(error: unknown, status: number): boolean {
  if (!error || typeof error !== 'object') return false
  return (
    (error as { response?: { status?: unknown } }).response?.status === status
  )
}

export function RetainedCredentialEmergencyControl(props: Props) {
  const { t } = useTranslation()
  const [open, setOpen] = React.useState(false)
  const [preview, setPreview] =
    React.useState<RetainedCredentialDisablePreview | null>(null)
  const [previewLoading, setPreviewLoading] = React.useState(false)
  const [previewError, setPreviewError] = React.useState('')
  const [reason, setReason] = React.useState('')
  const [actionPending, setActionPending] = React.useState(false)

  let providerLabel = t('Creem')
  let emergencyScope = t(
    'This clears the current Creem API key and webhook signing secret in this system. Products, limits, and other non-credential settings remain unchanged.'
  )
  if (props.provider === 'waffo') {
    providerLabel = t('Waffo')
    emergencyScope = t(
      'This disables Waffo and clears the API key, private key, and callback verification certificate for the currently selected environment. Merchant, payment-method, limit, and routing settings remain unchanged.'
    )
  } else if (props.provider === 'waffo_pancake') {
    providerLabel = t('Waffo Pancake')
    emergencyScope = t(
      'This clears the current Waffo Pancake private key and Store binding in this system so checkout and webhook attribution fail closed. Merchant, Product, return, and limit settings remain unchanged.'
    )
  }

  const resetDialog = React.useCallback(() => {
    setPreview(null)
    setPreviewError('')
    setReason('')
  }, [])

  const loadPreview = React.useCallback(async () => {
    setPreview(null)
    setPreviewError('')
    setPreviewLoading(true)
    try {
      const result = await getRetainedCredentialDisablePreview(props.provider)
      if (!result.success || !result.data) {
        const error = createPaymentAdminError(
          result,
          t('The emergency impact preview could not be loaded.')
        )
        setPreviewError(
          getRetainedCredentialDisableErrorMessage(
            error,
            t,
            t('The emergency impact preview could not be loaded.')
          )
        )
        return
      }
      setPreview(result.data)
    } catch (error) {
      setPreviewError(
        getRetainedCredentialDisableErrorMessage(
          error,
          t,
          t('The emergency impact preview could not be loaded.')
        )
      )
    } finally {
      setPreviewLoading(false)
    }
  }, [props.provider, t])

  const openPreview = () => {
    resetDialog()
    setOpen(true)
    void loadPreview()
  }

  const reasonValid = isEmergencyCredentialRevocationReasonValid(reason)
  const controlsDisabled = props.disabled || actionPending

  const disableCurrentCredential = async () => {
    if (!preview || !reasonValid || controlsDisabled) return
    const toastId = `retained-credential-disable-${props.provider}`
    const operation = async (): Promise<DisableResult | null> => {
      setActionPending(true)
      props.onPendingChange?.(true)
      toast.loading(
        t('Disabling {{provider}} current credentials...', {
          provider: providerLabel,
        }),
        { id: toastId }
      )
      try {
        const result = await disableRetainedPaymentCredential({
          provider: props.provider,
          reason,
          expectedVersion: preview.configuration_version,
        })
        if (!result.success) {
          const error = createPaymentAdminError(
            result,
            t('Current credentials could not be disabled.')
          )
          toast.error(
            getRetainedCredentialDisableErrorMessage(
              error,
              t,
              t('Current credentials could not be disabled.')
            ),
            { id: toastId }
          )
          return null
        }

        const refreshed = await props.onCompleted(props.provider, result)
        setOpen(false)
        resetDialog()
        if (refreshed) {
          toast.success(
            t(
              '{{provider}} current credentials disabled; affected orders quarantined',
              { provider: providerLabel }
            ),
            { id: toastId }
          )
        } else {
          toast.warning(
            t(
              '{{provider}} current credentials were disabled, but the latest status could not be refreshed.',
              { provider: providerLabel }
            ),
            { id: toastId }
          )
        }
        return result
      } catch (error) {
        if (isVerificationRequiredError(error)) {
          toast.dismiss(toastId)
          throw error
        }
        if (hasHttpStatus(error, 409)) {
          await props.onStale()
        }
        toast.error(
          getRetainedCredentialDisableErrorMessage(
            error,
            t,
            t('Current credentials could not be disabled.')
          ),
          { id: toastId }
        )
        return null
      } finally {
        setActionPending(false)
        props.onPendingChange?.(false)
      }
    }

    await props.withVerification(operation, {
      preferredMethod: 'passkey',
      title: t('Verify emergency credential disable'),
      description: t(
        'Confirm your identity before disabling current payment credentials and quarantining affected orders.'
      ),
    })
  }

  const impact = preview?.impact
  const impactItems = impact
    ? [
        [t('Total affected orders'), impact.total_affected_orders],
        [
          t('Unfinished orders moving to manual review'),
          impact.total_unfinished_orders,
        ],
        [t('Canonical payment orders'), impact.canonical_affected_orders],
        [t('Legacy pending top-ups'), impact.legacy_pending_topups],
        [
          t('Legacy pending subscriptions'),
          impact.legacy_pending_subscriptions,
        ],
        [t('Unmatched economic events'), impact.unmatched_economic_events],
      ]
    : []

  let impactPreviewContent: React.ReactNode = null
  if (previewLoading) {
    impactPreviewContent = (
      <div
        className='bg-muted/40 text-muted-foreground rounded-md border p-4 text-sm'
        role='status'
        aria-live='polite'
      >
        {t('Loading emergency impact preview...')}
      </div>
    )
  } else if (previewError) {
    impactPreviewContent = (
      <Alert variant='destructive'>
        <AlertTitle>{t('Impact preview unavailable')}</AlertTitle>
        <AlertDescription>{previewError}</AlertDescription>
        <AlertAction>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={controlsDisabled}
            onClick={() => void loadPreview()}
          >
            {t('Retry preview')}
          </Button>
        </AlertAction>
      </Alert>
    )
  } else if (preview && impact) {
    impactPreviewContent = (
      <div className='space-y-3 rounded-md border p-4'>
        <div>
          <p className='text-sm font-medium'>{t('Affected order preview')}</p>
          <p className='text-muted-foreground text-xs'>
            {t('Preview generated at {{time}}', {
              time: new Date(preview.generated_at * 1000).toLocaleString(),
            })}
          </p>
        </div>
        <dl className='grid gap-3 sm:grid-cols-2'>
          {impactItems.map(([label, value]) => (
            <div key={label} className='bg-muted/40 rounded-md p-3'>
              <dt className='text-muted-foreground text-xs'>{label}</dt>
              <dd className='mt-1 text-lg font-semibold tabular-nums'>
                {Number(value).toLocaleString()}
              </dd>
            </div>
          ))}
        </dl>
      </div>
    )
  }

  return (
    <section
      className='border-border mt-6 space-y-3 border-t pt-5'
      aria-label={t('Advanced and emergency')}
    >
      <div className='space-y-1'>
        <h4 className='text-sm font-medium'>{t('Advanced and emergency')}</h4>
        <p className='text-muted-foreground max-w-[76ch] text-sm'>
          {t(
            'You can replace these credentials through normal save only when no dependent orders still require the current credentials.'
          )}
        </p>
      </div>

      <Alert variant='destructive'>
        <HugeiconsIcon
          icon={SecurityWarningIcon}
          strokeWidth={2}
          aria-hidden='true'
        />
        <AlertTitle>{t('Emergency credential disable')}</AlertTitle>
        <AlertDescription>
          {t(
            'If the current credential may be compromised, review the impact before disabling it and quarantining affected orders for manual review.'
          )}
        </AlertDescription>
        <AlertAction>
          <Button
            type='button'
            variant='destructive'
            size='sm'
            className='w-full whitespace-normal sm:w-auto'
            disabled={controlsDisabled}
            onClick={openPreview}
          >
            {t('Review impact and disable')}
          </Button>
        </AlertAction>
      </Alert>

      <AlertDialog
        open={open}
        onOpenChange={(nextOpen) => {
          if (!nextOpen && !controlsDisabled) {
            setOpen(false)
            resetDialog()
          }
        }}
      >
        <AlertDialogContent className='sm:max-w-xl'>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t('Disable {{provider}} current credentials?', {
                provider: providerLabel,
              })}
            </AlertDialogTitle>
            <AlertDialogDescription>{emergencyScope}</AlertDialogDescription>
          </AlertDialogHeader>

          <Alert variant='destructive'>
            <AlertTitle>
              {t('Emergency disable does not replace credentials')}
            </AlertTitle>
            <AlertDescription>
              {t(
                'This emergency action does not accept replacement credentials. It immediately stops using the current credentials and moves affected unfinished orders to manual review.'
              )}
            </AlertDescription>
          </Alert>

          {impactPreviewContent}

          <div className='grid gap-2'>
            <Label
              htmlFor={`retained-credential-disable-reason-${props.provider}`}
            >
              {t('Emergency disable reason')}
            </Label>
            <Textarea
              id={`retained-credential-disable-reason-${props.provider}`}
              value={reason}
              maxLength={EMERGENCY_CREDENTIAL_REVOCATION_REASON_MAX_LENGTH}
              rows={4}
              disabled={controlsDisabled || !preview}
              aria-invalid={reason.length > 0 && !reasonValid}
              placeholder={t(
                'Describe why the current credentials must be disabled and how the incident is being handled'
              )}
              onChange={(event) => setReason(event.target.value)}
            />
            <div className='flex flex-wrap items-start justify-between gap-2 text-xs'>
              <p
                className={cn(
                  'text-muted-foreground',
                  reason.length > 0 && !reasonValid && 'text-destructive'
                )}
              >
                {reason.length > 0 && !reasonValid
                  ? t('Reason must be between 8 and 512 characters')
                  : t(
                      'Enter 8 to 512 characters explaining the credential incident and response.'
                    )}
              </p>
              <span className='text-muted-foreground tabular-nums'>
                {reason.length} /{' '}
                {EMERGENCY_CREDENTIAL_REVOCATION_REASON_MAX_LENGTH}
              </span>
            </div>
          </div>

          <AlertDialogFooter>
            <AlertDialogCancel disabled={controlsDisabled}>
              {t('Cancel')}
            </AlertDialogCancel>
            <AlertDialogAction
              variant='destructive'
              disabled={controlsDisabled || !preview || !reasonValid}
              onClick={(event) => {
                event.preventDefault()
                void disableCurrentCredential()
              }}
            >
              {actionPending
                ? t('Disabling and quarantining...')
                : t('Disable credentials and quarantine orders')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}
