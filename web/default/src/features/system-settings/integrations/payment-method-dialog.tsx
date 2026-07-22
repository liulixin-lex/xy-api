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
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { Dialog } from '@/components/dialog'
import { ReactIconByName } from '@/components/react-icon-by-name'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { ComboboxInput } from '@/components/ui/combobox-input'
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

const createPaymentMethodDialogSchema = (t: (key: string) => string) =>
  z
    .object({
      name: z
        .string()
        .trim()
        .min(1, t('Payment method name is required'))
        .max(128, t('Payment method name is too long')),
      type: z
        .string()
        .trim()
        .min(1, t('Payment type key is required'))
        .regex(
          /^[A-Za-z0-9_-]{1,64}$/,
          t('Use 1 to 64 letters, numbers, underscores, or hyphens.')
        ),
      provider: z.enum(['epay', 'stripe', 'xorpay', 'waffo_pancake']),
      icon: z.string().trim().max(64, t('Icon name is too long')).optional(),
      min_topup: z
        .string()
        .trim()
        .optional()
        .refine((value) => {
          if (!value) return true
          const amount = Number(value)
          return Number.isSafeInteger(amount) && amount >= 1 && amount <= 10_000
        }, t('Minimum top-up must be a positive whole number between 1 and 10000')),
    })
    .superRefine((value, ctx) => {
      const valid =
        value.provider === 'epay' ||
        (value.provider === 'stripe' && value.type === 'stripe') ||
        (value.provider === 'xorpay' &&
          (value.type === 'xorpay_native' ||
            value.type === 'xorpay_alipay' ||
            value.type === 'xorpay_jsapi')) ||
        (value.provider === 'waffo_pancake' && value.type === 'waffo_pancake')
      if (!valid) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['type'],
          message: t(
            'The payment type key does not match the selected provider.'
          ),
        })
      }
    })

type PaymentMethodDialogFormValues = z.infer<
  ReturnType<typeof createPaymentMethodDialogSchema>
>

const PAYMENT_METHOD_FORM_ID = 'payment-method-form'

export type PaymentMethodData = {
  name: string
  type: string
  provider: 'epay' | 'stripe' | 'xorpay' | 'waffo_pancake'
  icon?: string
  min_topup?: string
  color?: string
  route_id?: string
  public_method?: string
  channel_alias?: string
  flow?: string
  [key: string]: unknown
}

type PaymentMethodDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSave: (data: PaymentMethodData) => boolean | void
  editData?: PaymentMethodData | null
}

const PAYMENT_TYPE_ICON_NAMES: Record<string, string> = {
  alipay: 'SiAlipay',
  stripe: 'SiStripe',
  waffo_pancake: 'LuCreditCard',
  wxpay: 'SiWechat',
  xorpay_alipay: 'SiAlipay',
  xorpay_jsapi: 'SiWechat',
  xorpay_native: 'SiWechat',
}

const getDefaultIconName = (type: string) => PAYMENT_TYPE_ICON_NAMES[type] ?? ''

export function PaymentMethodDialog({
  open,
  onOpenChange,
  onSave,
  editData,
}: PaymentMethodDialogProps) {
  const { t } = useTranslation()
  const isEditMode = !!editData
  const paymentMethodDialogSchema = createPaymentMethodDialogSchema(t)
  const paymentTypeOptions = [
    {
      iconName: 'SiAlipay',
      label: `${t('Alipay')} (Epay: alipay)`,
      name: t('Alipay'),
      provider: 'epay' as const,
      value: 'alipay',
    },
    {
      iconName: 'SiWechat',
      label: `${t('WeChat Pay')} (Epay: wxpay)`,
      name: t('WeChat Pay'),
      provider: 'epay' as const,
      value: 'wxpay',
    },
    {
      iconName: 'SiStripe',
      label: `${t('Stripe')} (stripe)`,
      name: t('Stripe'),
      provider: 'stripe' as const,
      value: 'stripe',
    },
    {
      iconName: 'SiWechat',
      label: `${t('XORPay WeChat Native')} (xorpay_native)`,
      name: t('WeChat Pay'),
      provider: 'xorpay' as const,
      value: 'xorpay_native',
    },
    {
      iconName: 'SiWechat',
      label: `${t('XORPay WeChat in-app (JSAPI)')} (xorpay_jsapi)`,
      name: t('WeChat Pay'),
      provider: 'xorpay' as const,
      value: 'xorpay_jsapi',
    },
    {
      iconName: 'SiAlipay',
      label: `${t('XORPay Alipay')} (xorpay_alipay)`,
      name: t('Alipay'),
      provider: 'xorpay' as const,
      value: 'xorpay_alipay',
    },
    {
      iconName: 'LuCreditCard',
      label: 'Waffo Pancake (waffo_pancake)',
      name: 'Waffo Pancake',
      provider: 'waffo_pancake' as const,
      value: 'waffo_pancake',
    },
  ]
  const getPaymentTypeOption = (value: string) =>
    paymentTypeOptions.find((option) => option.value === value)
  const providerOptions = [
    { label: 'Epay', value: 'epay' },
    { label: 'Stripe', value: 'stripe' },
    { label: 'XORPay', value: 'xorpay' },
    { label: 'Waffo Pancake', value: 'waffo_pancake' },
  ]

  const form = useForm<PaymentMethodDialogFormValues>({
    resolver: zodResolver(paymentMethodDialogSchema),
    defaultValues: {
      name: '',
      type: '',
      provider: 'epay',
      icon: '',
      min_topup: '',
    },
  })

  const iconValue = form.watch('icon')

  useEffect(() => {
    if (editData) {
      form.reset({
        name: editData.name,
        type: editData.type,
        provider: editData.provider,
        icon: editData.icon ?? getDefaultIconName(editData.type),
        min_topup: editData.min_topup ?? '',
      })
    } else {
      form.reset({
        name: '',
        type: '',
        provider: 'epay',
        icon: '',
        min_topup: '',
      })
    }
  }, [editData, form, open])

  const handleSubmit = (values: PaymentMethodDialogFormValues) => {
    const data: PaymentMethodData = {
      name: values.name.trim(),
      type: values.type.trim(),
      provider: values.provider,
    }
    if (values.icon && values.icon.trim() !== '') {
      data.icon = values.icon.trim()
    }
    if (values.min_topup && values.min_topup.trim() !== '') {
      data.min_topup = values.min_topup
    }
    if (editData?.color) data.color = editData.color
    if (onSave(data) === false) return
    form.reset()
    onOpenChange(false)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={isEditMode ? t('Edit payment method') : t('Add payment method')}
      description={t('Configure a payment method for user recharge options.')}
      contentClassName='sm:max-w-[500px]'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form={PAYMENT_METHOD_FORM_ID}>
            {isEditMode ? t('Update') : t('Add')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id={PAYMENT_METHOD_FORM_ID}
          onSubmit={form.handleSubmit(handleSubmit)}
          className='space-y-4'
        >
          <FormField
            control={form.control}
            name='name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Name')}</FormLabel>
                <FormControl>
                  <Input placeholder={t('e.g., Alipay, WeChat')} {...field} />
                </FormControl>
                <FormDescription>
                  {t('Display name for this payment method.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='type'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Payment type key')}</FormLabel>
                <FormControl>
                  <Combobox
                    options={paymentTypeOptions}
                    value={field.value}
                    onValueChange={(value) => {
                      if (value === null) return
                      const currentIcon = form.getValues('icon')?.trim()
                      const currentName = form.getValues('name')?.trim()
                      const previousOption = getPaymentTypeOption(field.value)
                      const nextOption = getPaymentTypeOption(value)

                      field.onChange(value)
                      if (nextOption?.provider) {
                        form.setValue('provider', nextOption.provider, {
                          shouldDirty: true,
                          shouldValidate: true,
                        })
                      }
                      if (
                        nextOption?.iconName &&
                        (!currentIcon ||
                          currentIcon === previousOption?.iconName)
                      ) {
                        form.setValue('icon', nextOption.iconName, {
                          shouldDirty: true,
                        })
                      }
                      if (
                        nextOption?.name &&
                        (!currentName || currentName === previousOption?.name)
                      ) {
                        form.setValue('name', nextOption.name, {
                          shouldDirty: true,
                        })
                      }
                    }}
                    placeholder={t('Select or enter payment type key')}
                    searchPlaceholder={t('Search payment type keys...')}
                    allowCustomValue
                  />
                </FormControl>
                <FormDescription className='leading-relaxed'>
                  {t(
                    'The method key is sent only to the explicitly selected payment provider.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='provider'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Payment Provider')}</FormLabel>
                <FormControl>
                  <ComboboxInput
                    options={providerOptions}
                    value={field.value}
                    onValueChange={field.onChange}
                    placeholder={t('Search payment providers...')}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Explicit provider routing prevents custom methods from being sent to the wrong gateway.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='icon'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Icon')}</FormLabel>
                <FormControl>
                  <div className='flex items-center gap-2'>
                    <Input
                      placeholder={t('e.g., SiAlipay')}
                      {...field}
                      className='flex-1'
                    />
                    {iconValue && (
                      <ReactIconByName
                        name={iconValue}
                        className='text-muted-foreground size-5 shrink-0'
                        title={iconValue}
                      />
                    )}
                  </div>
                </FormControl>
                <FormDescription>
                  {t(
                    'Enter a react-icons component name. Invalid names show no icon.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='min_topup'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Minimum top-up (optional)')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={1}
                    max={10_000}
                    step={1}
                    placeholder={t('e.g., 50')}
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t('Optional minimum recharge amount for this method.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}
