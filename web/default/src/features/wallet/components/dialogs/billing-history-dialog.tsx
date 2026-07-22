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
import { Search, Copy, Check, ChevronLeft, ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from '@/components/ui/empty'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { formatCurrencyFromUSD } from '@/lib/currency'

import { useBillingHistory } from '../../hooks/use-billing-history'
import {
  getStatusConfig,
  formatPaymentDecimalAmount,
  formatTimestamp,
} from '../../lib/billing'
import { getPublicPaymentRecordMethodName } from '../../lib/payment'

interface BillingHistoryDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function BillingHistoryDialog({
  open,
  onOpenChange,
}: BillingHistoryDialogProps) {
  const { t } = useTranslation()
  const {
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
    refresh,
  } = useBillingHistory({ enabled: open })
  const { copyToClipboard, copiedText } = useCopyToClipboard({ notify: false })

  const totalPages = Math.ceil(total / pageSize)

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('Billing History')}
      description={t('View your topup transaction records and payment history')}
      contentClassName='flex max-h-[calc(100dvh-2rem)] flex-col max-sm:w-screen max-sm:max-w-none max-sm:rounded-none max-sm:p-4 sm:max-w-4xl'
      contentHeight='auto'
      bodyClassName='space-y-3'
    >
      <div className='min-h-0 space-y-3'>
        {/* Search and Filter Bar */}
        <div className='flex items-center gap-2'>
          <div className='relative flex-1'>
            <Label htmlFor='billing-history-search' className='sr-only'>
              {t('Search by order number')}
            </Label>
            <Search
              className='text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2'
              aria-hidden='true'
            />
            <Input
              id='billing-history-search'
              placeholder={t('Search by order number...')}
              value={keyword}
              onChange={(e) => handleSearch(e.target.value)}
              className='h-9 pl-10'
            />
          </div>
          <Select
            items={[
              { value: '10', label: t('10 / page') },
              { value: '20', label: t('20 / page') },
              { value: '50', label: t('50 / page') },
              { value: '100', label: t('100 / page') },
            ]}
            value={pageSize.toString()}
            onValueChange={(value) =>
              value !== null && handlePageSizeChange(Number.parseInt(value))
            }
          >
            <SelectTrigger className='h-9 w-[92px] sm:w-32'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent alignItemWithTrigger={false}>
              <SelectGroup>
                <SelectItem value='10'>{t('10 / page')}</SelectItem>
                <SelectItem value='20'>{t('20 / page')}</SelectItem>
                <SelectItem value='50'>{t('50 / page')}</SelectItem>
                <SelectItem value='100'>{t('100 / page')}</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>

        {error && (
          <Alert variant='destructive'>
            <AlertDescription className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
              <span>{t('Failed to load billing history')}</span>
              <Button
                type='button'
                size='sm'
                variant='outline'
                disabled={loading}
                onClick={() => void refresh()}
              >
                {t('Retry')}
              </Button>
            </AlertDescription>
          </Alert>
        )}

        {/* Records List */}
        <div className='max-h-[min(54vh,520px)] overflow-y-auto pr-1'>
          {(() => {
            if (loading && records.length === 0) {
              return (
                <div className='space-y-3'>
                  {Array.from(
                    { length: 5 },
                    (_, i) => `billing-history-skeleton-${i}`
                  ).map((key) => (
                    <div key={key} className='rounded-lg border p-3 sm:p-4'>
                      <div className='flex items-start justify-between'>
                        <div className='flex-1 space-y-2'>
                          <Skeleton className='h-4 w-48' />
                          <Skeleton className='h-3 w-32' />
                        </div>
                        <Skeleton className='h-5 w-16' />
                      </div>
                      <div className='mt-3 grid grid-cols-2 gap-3 sm:grid-cols-3 sm:gap-4'>
                        <Skeleton className='h-3 w-full' />
                        <Skeleton className='h-3 w-full' />
                        <Skeleton className='h-3 w-full' />
                      </div>
                    </div>
                  ))}
                </div>
              )
            }
            if (error && records.length === 0) return null
            if (records.length === 0) {
              return (
                <Empty className='min-h-40 border-0 py-10'>
                  <EmptyHeader>
                    <EmptyTitle>{t('No billing records found')}</EmptyTitle>
                    <EmptyDescription>
                      {keyword
                        ? t('Try adjusting your search')
                        : t('Your transaction history will appear here')}
                    </EmptyDescription>
                  </EmptyHeader>
                </Empty>
              )
            }
            return (
              <div className='space-y-3'>
                {records.map((record) => {
                  const statusConfig = getStatusConfig(
                    record.status_code || 'temporarily_unavailable'
                  )
                  return (
                    <div
                      key={record.id}
                      className='rounded-lg border p-3 sm:p-4'
                    >
                      {/* Header Row */}
                      <div className='flex items-start justify-between gap-2'>
                        <div className='flex-1 space-y-1'>
                          <div className='flex min-w-0 items-center gap-2'>
                            <code className='text-foreground truncate font-mono text-sm'>
                              {record.trade_no}
                            </code>
                            <Button
                              variant='ghost'
                              size='sm'
                              className='h-5 w-5 p-0'
                              aria-label={t('Copy order number')}
                              onClick={() => copyToClipboard(record.trade_no)}
                            >
                              {copiedText === record.trade_no ? (
                                <Check className='h-3 w-3' />
                              ) : (
                                <Copy className='h-3 w-3' />
                              )}
                            </Button>
                          </div>
                          <div className='text-muted-foreground text-xs'>
                            {formatTimestamp(record.created_at)}
                          </div>
                        </div>
                        <StatusBadge
                          label={t(statusConfig.label)}
                          variant={statusConfig.variant}
                          showDot
                          copyable={false}
                        />
                      </div>

                      {/* Details Grid */}
                      <div className='mt-3 grid grid-cols-2 gap-3 sm:mt-4 sm:grid-cols-3 sm:gap-4'>
                        <div className='space-y-1'>
                          <Label className='text-muted-foreground text-xs'>
                            {t('Payment Method')}
                          </Label>
                          <div className='text-sm font-medium'>
                            {getPublicPaymentRecordMethodName(
                              record.public_method,
                              t
                            )}
                          </div>
                        </div>
                        <div className='space-y-1'>
                          <Label className='text-muted-foreground text-xs'>
                            {t('Amount')}
                          </Label>
                          <div className='text-sm font-semibold'>
                            {formatCurrencyFromUSD(record.amount, {
                              digitsLarge: 2,
                              digitsSmall: 2,
                              abbreviate: false,
                            })}
                          </div>
                        </div>
                        <div className='space-y-1'>
                          <Label className='text-muted-foreground text-xs'>
                            {t('Payment')}
                          </Label>
                          <div className='text-sm font-semibold text-red-600'>
                            {formatPaymentDecimalAmount(
                              record.payment_amount,
                              record.currency || 'CNY'
                            )}
                          </div>
                        </div>
                      </div>
                    </div>
                  )
                })}
              </div>
            )
          })()}
        </div>

        {/* Pagination */}
        {!loading && records.length > 0 && (
          <div className='flex flex-col items-center gap-3 border-t pt-4 sm:flex-row sm:items-center sm:justify-between'>
            <div className='text-muted-foreground text-xs sm:text-sm'>
              {t('Showing {{start}}-{{end}} of {{total}}', {
                start: (page - 1) * pageSize + 1,
                end: Math.min(page * pageSize, total),
                total,
              })}
            </div>
            <div className='flex items-center gap-2'>
              <Button
                variant='outline'
                size='sm'
                onClick={() => handlePageChange(page - 1)}
                disabled={page <= 1}
                className='h-8 w-8 p-0'
                aria-label={t('Previous page')}
              >
                <ChevronLeft className='h-4 w-4' />
              </Button>
              <div className='text-muted-foreground flex items-center gap-1 text-sm'>
                <span className='font-medium'>{page}</span>
                <span>/</span>
                <span>{totalPages}</span>
              </div>
              <Button
                variant='outline'
                size='sm'
                onClick={() => handlePageChange(page + 1)}
                disabled={page >= totalPages}
                className='h-8 w-8 p-0'
                aria-label={t('Next page')}
              >
                <ChevronRight className='h-4 w-4' />
              </Button>
            </div>
          </div>
        )}
      </div>
    </Dialog>
  )
}
