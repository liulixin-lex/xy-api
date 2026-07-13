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

import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Download, FileJson } from 'lucide-react'
import { useEffect, useMemo, useRef } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import z from 'zod'

import {
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'

import {
  createChannelRoutingAuditExport,
  createChannelRoutingIdempotencyKey,
  downloadChannelRoutingAuditExport,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'

type AuditExportFormValues = {
  fromTime: string
  toTime: string
  limit: number
}

const auditExportMaxRangeMs = 31 * 24 * 60 * 60 * 1_000

function localDateTimeValue(date: Date): string {
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000)
  return local.toISOString().slice(0, 19)
}

export function ChannelRoutingAuditExportSheet(props: {
  open: boolean
  canAuditExport: boolean
  initialFromTime?: number
  initialToTime?: number
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const queryClient = useQueryClient()
  const idempotencyRef = useRef<{ signature: string; key: string } | null>(null)
  const schema = useMemo(
    () =>
      z
        .object({
          fromTime: z.string().min(1, t('Start time is required')),
          toTime: z.string().min(1, t('End time is required')),
          limit: z
            .number({ error: t('Enter a valid number') })
            .int(t('Value must be an integer'))
            .min(
              1,
              t('Value must be between {{min}} and {{max}}', {
                min: 1,
                max: 5000,
              })
            )
            .max(
              5000,
              t('Value must be between {{min}} and {{max}}', {
                min: 1,
                max: 5000,
              })
            ),
        })
        .superRefine((value, context) => {
          const fromTime = new Date(value.fromTime).getTime()
          const toTime = new Date(value.toTime).getTime()
          if (
            !Number.isFinite(fromTime) ||
            !Number.isFinite(toTime) ||
            fromTime <= 0 ||
            toTime < fromTime
          ) {
            context.addIssue({
              code: 'custom',
              path: ['toTime'],
              message: t('Select a valid time range'),
            })
            return
          }
          if (toTime - fromTime > auditExportMaxRangeMs) {
            context.addIssue({
              code: 'custom',
              path: ['toTime'],
              message: t('Audit exports can cover up to 31 days'),
            })
          }
        }),
    [t]
  )
  const form = useForm<AuditExportFormValues>({
    resolver: zodResolver(schema),
    defaultValues: { fromTime: '', toTime: '', limit: 1000 },
  })
  const createExport = useMutation({
    mutationFn: (values: AuditExportFormValues) => {
      if (!props.canAuditExport) throw new Error('Audit export is unavailable')
      const payload = {
        from_time: Math.floor(new Date(values.fromTime).getTime() / 1_000),
        to_time: Math.floor(new Date(values.toTime).getTime() / 1_000),
        limit: values.limit,
      }
      const signature = JSON.stringify(payload)
      if (idempotencyRef.current?.signature !== signature) {
        idempotencyRef.current = {
          signature,
          key: createChannelRoutingIdempotencyKey('audit-export'),
        }
      }
      return createChannelRoutingAuditExport(
        payload,
        idempotencyRef.current.key
      )
    },
    onSuccess: async () => {
      idempotencyRef.current = null
      await queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.operationsRoot(),
      })
      toast.success(t('Audit export ready'))
    },
    onError: () => {
      toast.error(t('Could not create the audit export. Try again.'))
    },
  })
  const downloadExport = useMutation({
    mutationFn: downloadChannelRoutingAuditExport,
    onSuccess: () => toast.success(t('Audit export downloaded')),
    onError: () => toast.error(t('Could not download the audit export.')),
  })
  const resetCreateExport = createExport.reset
  const resetDownloadExport = downloadExport.reset

  useEffect(() => {
    if (!props.open) return
    const now = new Date()
    let fromTime = new Date(now.getTime() - 24 * 60 * 60 * 1_000)
    let toTime = now
    if (
      props.initialFromTime != null &&
      props.initialToTime != null &&
      props.initialFromTime > 0 &&
      props.initialToTime >= props.initialFromTime
    ) {
      fromTime = new Date(props.initialFromTime * 1_000)
      toTime = new Date(props.initialToTime * 1_000)
    }
    form.reset({
      fromTime: localDateTimeValue(fromTime),
      toTime: localDateTimeValue(toTime),
      limit: 1000,
    })
    idempotencyRef.current = null
    resetCreateExport()
    resetDownloadExport()
  }, [
    form,
    props.initialFromTime,
    props.initialToTime,
    props.open,
    resetCreateExport,
    resetDownloadExport,
  ])

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent
        className={sideDrawerContentClassName(
          'max-w-none max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11 sm:!max-w-2xl'
        )}
      >
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle className='flex items-center gap-2'>
            <FileJson className='size-4' aria-hidden='true' />
            {t('Export decision audit')}
          </SheetTitle>
          <SheetDescription>
            {t(
              'Create a privacy-filtered JSON export for a time range of up to 31 days.'
            )}
          </SheetDescription>
        </SheetHeader>

        <Form {...form}>
          <form
            id='channel-routing-audit-export-form'
            className={sideDrawerFormClassName('gap-5')}
            onSubmit={form.handleSubmit((values) =>
              createExport.mutate(values)
            )}
          >
            <div className='grid gap-3 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='fromTime'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Start time')}</FormLabel>
                    <FormControl>
                      <Input type='datetime-local' step={1} {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='toTime'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('End time')}</FormLabel>
                    <FormControl>
                      <Input type='datetime-local' step={1} {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
            <FormField
              control={form.control}
              name='limit'
              render={({ field }) => (
                <FormItem className='max-w-48'>
                  <FormLabel>{t('Maximum records')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={1}
                      max={5000}
                      value={field.value}
                      onChange={(event) =>
                        field.onChange(event.target.valueAsNumber)
                      }
                    />
                  </FormControl>
                  <FormDescription>
                    {t('Exports are limited to 5,000 audit records.')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            {createExport.data ? (
              <section className='space-y-4 border-t pt-4'>
                <div className='flex flex-wrap items-center justify-between gap-3'>
                  <div>
                    <h3 className='text-sm font-semibold'>
                      {t('Audit export ready')}
                    </h3>
                    <p className='text-muted-foreground mt-1 font-mono text-xs break-all'>
                      {createExport.data.export.export_id}
                    </p>
                  </div>
                  <ChannelRoutingStatusBadge status='ready' />
                </div>
                <dl className='grid grid-cols-2 gap-4 text-sm sm:grid-cols-3'>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Records')}
                    </dt>
                    <dd className='mt-1 font-medium tabular-nums'>
                      {format.number(createExport.data.export.record_count)}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('File size')}
                    </dt>
                    <dd className='mt-1 font-medium tabular-nums'>
                      {t('{{count}} bytes', {
                        count: format.number(
                          createExport.data.export.content_bytes
                        ),
                      })}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Expires')}
                    </dt>
                    <dd className='mt-1 font-medium'>
                      {format.timestamp(
                        createExport.data.export.expires_time_ms
                      )}
                    </dd>
                  </div>
                </dl>
                <Button
                  type='button'
                  variant='outline'
                  disabled={downloadExport.isPending}
                  onClick={() =>
                    downloadExport.mutate(createExport.data.export.export_id)
                  }
                >
                  <Download aria-hidden='true' />
                  {downloadExport.isPending
                    ? t('Downloading')
                    : t('Download JSON')}
                </Button>
              </section>
            ) : null}
          </form>
        </Form>

        <SheetFooter className={sideDrawerFooterClassName()}>
          <Button
            type='submit'
            form='channel-routing-audit-export-form'
            disabled={!props.canAuditExport || createExport.isPending}
          >
            <FileJson aria-hidden='true' />
            {createExport.isPending ? t('Creating export') : t('Create export')}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
