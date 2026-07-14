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
import { TriangleAlert } from 'lucide-react'
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Checkbox } from '@/components/ui/checkbox'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

import type { CostBindingFormValues } from '../lib/cost-binding'
import type {
  RoutingCostBinding,
  RoutingCostBindingCredentialMasks,
  RoutingCostBindingUpstreamType,
} from '../types'

type CredentialValueName =
  | 'newApiAccessToken'
  | 'gatewayApiKey'
  | 'sub2apiEmail'
  | 'sub2apiPassword'
  | 'sub2apiToken'

type CredentialClearName =
  | 'clearNewApiAccessToken'
  | 'clearGatewayApiKey'
  | 'clearSub2apiEmail'
  | 'clearSub2apiPassword'
  | 'clearSub2apiToken'

const credentialRows: Array<{
  key: keyof RoutingCostBindingCredentialMasks
  label: string
}> = [
  { key: 'new_api_access_token', label: 'New API Access Token' },
  { key: 'gateway_api_key', label: 'Gateway API Key' },
  { key: 'sub2api_email', label: 'Sub2API Email' },
  { key: 'sub2api_password', label: 'Sub2API Password' },
  { key: 'sub2api_token', label: 'Sub2API Token' },
  { key: 'custom_ca_configured', label: 'Custom CA' },
]

export function CostSourceCredentialSummary(props: {
  masks: RoutingCostBindingCredentialMasks
  error?: string
}) {
  const { t } = useTranslation()
  const saved = credentialRows.filter((row) => props.masks[row.key])

  return (
    <div className='space-y-3'>
      {saved.length === 0 && !props.error ? (
        <p className='text-muted-foreground text-sm'>
          {t('No credentials are saved for this cost source.')}
        </p>
      ) : null}
      {saved.length > 0 ? (
        <dl className='divide-y rounded-md border'>
          {saved.map((row) => (
            <div
              key={row.key}
              className='grid min-w-0 gap-1 px-3 py-2.5 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] sm:items-center'
            >
              <dt className='text-muted-foreground text-xs'>{t(row.label)}</dt>
              <dd className='min-w-0 font-mono text-xs break-all sm:text-end'>
                {typeof props.masks[row.key] === 'boolean'
                  ? t('Configured')
                  : props.masks[row.key]}
              </dd>
            </div>
          ))}
        </dl>
      ) : null}
      {props.error ? (
        <CostSourceCredentialRecoveryAlert canEdit={false} />
      ) : null}
    </div>
  )
}

export function CostSourceCredentialRecoveryAlert(props: { canEdit: boolean }) {
  const { t } = useTranslation()
  return (
    <Alert className='border-amber-500/30 bg-amber-500/5' role='alert'>
      <TriangleAlert
        className='text-amber-700 dark:text-amber-300'
        aria-hidden='true'
      />
      <AlertTitle>{t('Credentials need to be re-entered')}</AlertTitle>
      <AlertDescription>
        {props.canEdit
          ? t(
              'Saved credentials could not be read. Re-enter every required credential before saving this cost source.'
            )
          : t(
              'Saved credentials could not be read. An administrator with credential access must re-enter them to restore this cost source.'
            )}
      </AlertDescription>
    </Alert>
  )
}

export function CostSourceCustomCAField(props: { configured: boolean }) {
  const { t } = useTranslation()
  const form = useFormContext<CostBindingFormValues>()
  const clear = Boolean(
    useWatch({ control: form.control, name: 'clearCustomCaPem' })
  )

  return (
    <FormField
      control={form.control}
      name='customCaPem'
      render={({ field }) => (
        <FormItem>
          <FormLabel>{t('Custom CA')}</FormLabel>
          <FormControl>
            <Textarea
              {...field}
              value={field.value}
              rows={6}
              disabled={clear}
              spellCheck={false}
              autoComplete='off'
              className='min-h-32 resize-y font-mono text-xs'
              placeholder={
                props.configured
                  ? t('Leave blank to keep the saved value')
                  : t('Paste a PEM-encoded CA certificate')
              }
            />
          </FormControl>
          <FormDescription>
            {props.configured
              ? t('A custom CA certificate is saved.')
              : t(
                  'System trust remains active when no custom CA is configured.'
                )}
          </FormDescription>
          {props.configured ? (
            <FormField
              control={form.control}
              name='clearCustomCaPem'
              render={({ field: clearField }) => (
                <label className='text-foreground flex min-h-11 cursor-pointer items-center gap-2 text-sm'>
                  <Checkbox
                    checked={clearField.value}
                    onCheckedChange={(checked) =>
                      clearField.onChange(Boolean(checked))
                    }
                  />
                  <span>{t('Clear the saved custom CA')}</span>
                </label>
              )}
            />
          ) : null}
          <FormMessage />
        </FormItem>
      )}
    />
  )
}

function CredentialField(props: {
  valueName: CredentialValueName
  clearName: CredentialClearName
  label: string
  mask?: string
  type?: 'text' | 'password' | 'email'
  autoComplete?: string
}) {
  const { t } = useTranslation()
  const form = useFormContext<CostBindingFormValues>()
  const clear = Boolean(
    useWatch({ control: form.control, name: props.clearName })
  )

  return (
    <FormField
      control={form.control}
      name={props.valueName}
      render={({ field }) => (
        <FormItem>
          <FormLabel>{t(props.label)}</FormLabel>
          <FormControl>
            <Input
              {...field}
              value={field.value}
              type={props.type ?? 'password'}
              autoComplete={props.autoComplete ?? 'new-password'}
              disabled={clear}
              placeholder={
                props.mask
                  ? t('Leave blank to keep the saved value')
                  : t('Optional')
              }
            />
          </FormControl>
          {props.mask ? (
            <>
              <FormDescription>
                <span className='block font-mono text-xs break-all'>
                  {t('Current saved value: {{mask}}', { mask: props.mask })}
                </span>
              </FormDescription>
              <FormField
                control={form.control}
                name={props.clearName}
                render={({ field: clearField }) => (
                  <label className='text-foreground flex min-h-11 cursor-pointer items-center gap-2 text-sm'>
                    <Checkbox
                      checked={clearField.value}
                      onCheckedChange={(checked) =>
                        clearField.onChange(Boolean(checked))
                      }
                    />
                    <span>{t('Clear the saved credential')}</span>
                  </label>
                )}
              />
            </>
          ) : (
            <FormDescription>
              {t('Credentials are encrypted and never shown again.')}
            </FormDescription>
          )}
          <FormMessage />
        </FormItem>
      )}
    />
  )
}

export function CostSourceCredentialFields(props: {
  upstreamType: RoutingCostBindingUpstreamType
  binding: RoutingCostBinding | null
}) {
  return (
    <div className='grid gap-4 sm:grid-cols-2'>
      {props.upstreamType === 'newapi' ? (
        <CredentialField
          valueName='newApiAccessToken'
          clearName='clearNewApiAccessToken'
          label='New API Access Token'
          mask={props.binding?.credential_masks.new_api_access_token}
        />
      ) : null}
      <CredentialField
        valueName='gatewayApiKey'
        clearName='clearGatewayApiKey'
        label='Gateway API Key'
        mask={props.binding?.credential_masks.gateway_api_key}
      />
      {props.upstreamType === 'sub2api' ? (
        <>
          <CredentialField
            valueName='sub2apiEmail'
            clearName='clearSub2apiEmail'
            label='Sub2API Email'
            mask={props.binding?.credential_masks.sub2api_email}
            type='email'
            autoComplete='off'
          />
          <CredentialField
            valueName='sub2apiPassword'
            clearName='clearSub2apiPassword'
            label='Sub2API Password'
            mask={props.binding?.credential_masks.sub2api_password}
          />
          <CredentialField
            valueName='sub2apiToken'
            clearName='clearSub2apiToken'
            label='Sub2API Token'
            mask={props.binding?.credential_masks.sub2api_token}
          />
        </>
      ) : null}
    </div>
  )
}
