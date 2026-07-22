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
import i18next from 'i18next'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { toast } from 'sonner'

import { getApiErrorMessage } from '@/lib/api-error'
import { isVerificationRequiredError } from '@/lib/secure-verification'

import { checkVerificationMethods, verify } from '../api'
import type {
  SecureVerificationState,
  StartVerificationOptions,
  UseSecureVerificationOptions,
  VerificationMethod,
  VerificationMethods,
} from '../types'

type ApiCall = (() => Promise<unknown>) | null

interface InternalState extends SecureVerificationState {
  apiCall: ApiCall
}

const defaultMethods: VerificationMethods = {
  has2FA: false,
  hasPasskey: false,
  passkeySupported: false,
}

const initialState: InternalState = {
  method: null,
  loading: false,
  code: '',
  title: undefined,
  description: undefined,
  apiCall: null,
}

export function useSecureVerification(
  options: UseSecureVerificationOptions = {}
) {
  const { onSuccess, onError, successMessage, autoReset = true } = options

  const [methods, setMethods] = useState<VerificationMethods>(defaultMethods)
  const [state, setState] = useState<InternalState>(initialState)
  const [open, setOpen] = useState(false)
  const pendingApiCallRef = useRef<ApiCall>(null)
  const verificationExecutionRef = useRef(false)

  const fetchVerificationMethods = useCallback(async () => {
    const result = await checkVerificationMethods()
    setMethods(result)
    return result
  }, [])

  useEffect(() => {
    fetchVerificationMethods()
  }, [fetchVerificationMethods])

  const reset = useCallback(() => {
    pendingApiCallRef.current = null
    verificationExecutionRef.current = false
    setState(initialState)
    setOpen(false)
  }, [])

  const startVerification = useCallback(
    async (
      apiCall: () => Promise<unknown>,
      config: StartVerificationOptions = {}
    ) => {
      const { preferredMethod, title, description } = config
      const availableMethods = await fetchVerificationMethods()
      const passkeyAvailable =
        availableMethods.hasPasskey && availableMethods.passkeySupported

      if (!availableMethods.has2FA && !passkeyAvailable) {
        toast.error(
          i18next.t(
            'Please enable Two-factor Authentication or Passkey before proceeding'
          )
        )
        onError?.(
          new Error(
            'No verification methods available. Enable 2FA or Passkey to continue.'
          )
        )
        return false
      }

      let defaultMethod: VerificationMethod | null = null
      if (preferredMethod === '2fa' && availableMethods.has2FA) {
        defaultMethod = '2fa'
      } else if (preferredMethod === 'passkey' && passkeyAvailable) {
        defaultMethod = 'passkey'
      }
      if (!defaultMethod) {
        if (passkeyAvailable) {
          defaultMethod = 'passkey'
        } else if (availableMethods.has2FA) {
          defaultMethod = '2fa'
        }
      }

      pendingApiCallRef.current = apiCall
      setState((prev) => ({
        ...prev,
        apiCall,
        method: defaultMethod,
        title,
        description,
      }))
      setOpen(true)
      return true
    },
    [fetchVerificationMethods, onError]
  )

  const executeVerification = useCallback(
    async (method?: VerificationMethod, code?: string) => {
      if (!pendingApiCallRef.current) {
        toast.error(i18next.t('Verification is not configured properly'))
        return
      }

      const actualMethod = method ?? state.method
      if (!actualMethod) {
        toast.error(i18next.t('Select a verification method first'))
        return
      }
      if (verificationExecutionRef.current) return

      verificationExecutionRef.current = true
      setState((prev) => ({ ...prev, loading: true }))
      let operationClaimed = false

      try {
        await verify(actualMethod, code ?? state.code)
        const apiCall = pendingApiCallRef.current
        if (!apiCall) {
          reset()
          return
        }

        pendingApiCallRef.current = null
        operationClaimed = true
        setState((prev) => ({ ...prev, apiCall: null }))

        const result = await apiCall()

        if (successMessage) {
          toast.success(successMessage)
        }

        onSuccess?.(result, actualMethod)

        if (autoReset) {
          reset()
        } else {
          setOpen(false)
        }

        return result
      } catch (error) {
        if (operationClaimed) reset()
        const message = getApiErrorMessage(
          error,
          i18next.t('Verification failed')
        )
        toast.error(message)
        onError?.(error)
        throw error
      } finally {
        verificationExecutionRef.current = false
        setState((prev) => ({ ...prev, loading: false }))
      }
    },
    [state, successMessage, onSuccess, onError, autoReset, reset]
  )

  const setCode = useCallback((code: string) => {
    setState((prev) => ({ ...prev, code }))
  }, [])

  const switchMethod = useCallback((method: VerificationMethod) => {
    setState((prev) => ({ ...prev, method, code: '' }))
  }, [])

  const cancel = useCallback(() => {
    reset()
  }, [reset])

  const withVerification = useCallback(
    async <T>(
      apiCall: () => Promise<T>,
      config: StartVerificationOptions = {}
    ): Promise<T | null> => {
      try {
        return await apiCall()
      } catch (error) {
        if (isVerificationRequiredError(error)) {
          await startVerification(apiCall, config)
          return null
        }
        throw error
      }
    },
    [startVerification]
  )

  const canUseMethod = useCallback(
    (method: VerificationMethod) => {
      if (method === '2fa') return methods.has2FA
      if (method === 'passkey') {
        return methods.hasPasskey && methods.passkeySupported
      }
      return false
    },
    [methods]
  )

  const recommendedMethod = useMemo<VerificationMethod | null>(() => {
    if (methods.hasPasskey && methods.passkeySupported) return 'passkey'
    if (methods.has2FA) return '2fa'
    return null
  }, [methods])

  return {
    open,
    setOpen,
    methods,
    state,
    startVerification,
    executeVerification,
    cancel,
    reset,
    setCode,
    switchMethod,
    withVerification,
    fetchVerificationMethods,
    canUseMethod,
    recommendedMethod,
    hasAnyMethod: methods.has2FA || methods.hasPasskey,
    isLoading: state.loading,
    currentMethod: state.method,
    code: state.code,
  }
}
