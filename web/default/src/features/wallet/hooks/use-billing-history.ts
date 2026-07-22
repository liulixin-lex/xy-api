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
import { useState, useEffect, useCallback, useRef } from 'react'

import { getUserBillingHistory, isApiSuccess } from '../api'
import type { TopupRecord } from '../types'

// ============================================================================
// Billing History Hook
// ============================================================================

interface UseBillingHistoryOptions {
  /** Initial page number */
  initialPage?: number
  /** Initial page size */
  initialPageSize?: number
  /** Fetch only while the history surface is visible. */
  enabled?: boolean
}

export function useBillingHistory(options: UseBillingHistoryOptions = {}) {
  const { initialPage = 1, initialPageSize = 10, enabled = true } = options
  const [records, setRecords] = useState<TopupRecord[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(initialPage)
  const [pageSize, setPageSize] = useState(initialPageSize)
  const [keyword, setKeyword] = useState('')
  const [debouncedKeyword, setDebouncedKeyword] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const requestRef = useRef<AbortController | null>(null)

  useEffect(() => {
    const timer = setTimeout(() => setDebouncedKeyword(keyword.trim()), 300)
    return () => clearTimeout(timer)
  }, [keyword])

  /**
   * Fetch billing history
   */
  const fetchBillingHistory = useCallback(async () => {
    if (!enabled) return
    requestRef.current?.abort()
    const controller = new AbortController()
    requestRef.current = controller
    setLoading(true)
    try {
      const response = await getUserBillingHistory(
        page,
        pageSize,
        debouncedKeyword,
        controller.signal
      )

      if (isApiSuccess(response) && response.data) {
        setRecords(response.data.items || [])
        setTotal(response.data.total || 0)
        setError(null)
      } else {
        const message = i18next.t('Failed to load billing history')
        setError(message)
      }
    } catch (error) {
      if (
        error &&
        typeof error === 'object' &&
        'code' in error &&
        error.code === 'ERR_CANCELED'
      ) {
        return
      }
      const message = i18next.t('Failed to load billing history')
      setError(message)
    } finally {
      if (requestRef.current === controller) {
        requestRef.current = null
        setLoading(false)
      }
    }
  }, [debouncedKeyword, enabled, page, pageSize])

  /**
   * Change page
   */
  const handlePageChange = useCallback((newPage: number) => {
    setPage(newPage)
  }, [])

  /**
   * Change page size
   */
  const handlePageSizeChange = useCallback((newPageSize: number) => {
    setPageSize(newPageSize)
    setPage(1) // Reset to first page when changing page size
  }, [])

  /**
   * Search by keyword
   */
  const handleSearch = useCallback((newKeyword: string) => {
    setKeyword(newKeyword)
    setPage(1) // Reset to first page when searching
  }, [])

  // Fetch data when dependencies change
  useEffect(() => {
    void fetchBillingHistory()
    return () => {
      requestRef.current?.abort()
      requestRef.current = null
    }
  }, [fetchBillingHistory])

  return {
    records,
    total,
    page,
    pageSize,
    keyword,
    loading,
    error,
    handlePageChange,
    handlePageSizeChange,
    handleSearch,
    refresh: fetchBillingHistory,
  }
}
