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
import { InformationCircleIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useId, useState } from 'react'
import { Controller, useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from '@/components/ui/input-group'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import type { SmartRoutingSettingField } from '../../types'
import {
  displayRuntimeSettingValue,
  runtimeSettingLabels,
  type RuntimeSettingsFormValues,
} from './lib/runtime-settings'
import { useRuntimeSettingsFormUI } from './use-form-ui'

type RuntimeFieldBaseProps = {
  name: SmartRoutingSettingField
  disabled?: boolean
  inactiveReason?: string
  description?: string
}

function RuntimeFieldHeading(props: RuntimeFieldBaseProps & { id: string }) {
  const { t } = useTranslation()
  const ui = useRuntimeSettingsFormUI()
  const [infoOpen, setInfoOpen] = useState(false)
  const label = t(runtimeSettingLabels[props.name])
  const overridden = !Object.is(
    ui.server.settings[props.name],
    ui.server.stored_settings[props.name]
  )
  const conflicted = ui.conflicts.has(props.name)

  return (
    <div className='flex min-w-0 flex-wrap items-center gap-1.5'>
      <FieldLabel htmlFor={props.id}>{label}</FieldLabel>
      <Tooltip open={infoOpen} onOpenChange={setInfoOpen}>
        <TooltipTrigger
          closeOnClick={false}
          render={
            <button
              type='button'
              className='text-muted-foreground hover:text-foreground focus-visible:ring-ring inline-flex size-7 items-center justify-center rounded-md outline-none focus-visible:ring-2'
              aria-label={t('More information about {{label}}', { label })}
              onClick={() => setInfoOpen(true)}
            />
          }
        >
          <HugeiconsIcon icon={InformationCircleIcon} aria-hidden='true' />
        </TooltipTrigger>
        <TooltipContent className='max-w-xs text-pretty'>
          {props.description ??
            t(
              'Stored value used by the routing runtime. The server normalizes it before activation.'
            )}
        </TooltipContent>
      </Tooltip>
      {overridden ? (
        <Badge variant='secondary'>{t('Deployment override')}</Badge>
      ) : null}
      {conflicted ? (
        <Badge variant='destructive'>{t('Conflicting change')}</Badge>
      ) : null}
    </div>
  )
}

function RuntimeFieldNotes(props: RuntimeFieldBaseProps) {
  const { t } = useTranslation()
  const ui = useRuntimeSettingsFormUI()
  const overridden = !Object.is(
    ui.server.settings[props.name],
    ui.server.stored_settings[props.name]
  )
  return (
    <>
      {props.inactiveReason ? (
        <FieldDescription>{props.inactiveReason}</FieldDescription>
      ) : null}
      {overridden ? (
        <FieldDescription>
          {t('Effective value: {{value}}', {
            value: displayRuntimeSettingValue(
              props.name,
              ui.server.settings[props.name],
              t
            ),
          })}
        </FieldDescription>
      ) : null}
    </>
  )
}

export function RuntimeNumberField(
  props: RuntimeFieldBaseProps & {
    unit?: string
    min?: number
    max?: number
    step?: number
  }
) {
  const { t } = useTranslation()
  const form = useFormContext<RuntimeSettingsFormValues>()
  const ui = useRuntimeSettingsFormUI()
  const id = useId()
  const error = form.formState.errors[props.name]
  const disabled = ui.readOnly || props.disabled
  const invalid = error != null || ui.conflicts.has(props.name)

  return (
    <Field
      data-invalid={invalid || undefined}
      data-disabled={disabled || undefined}
    >
      <RuntimeFieldHeading {...props} id={id} />
      <InputGroup>
        <InputGroupInput
          id={id}
          type='number'
          min={props.min}
          max={props.max}
          step={props.step ?? 1}
          disabled={disabled}
          aria-invalid={invalid || undefined}
          {...form.register(props.name, { valueAsNumber: true })}
        />
        {props.unit ? (
          <InputGroupAddon align='inline-end'>{t(props.unit)}</InputGroupAddon>
        ) : null}
      </InputGroup>
      <RuntimeFieldNotes {...props} />
      <FieldError>
        {error?.message ? t(String(error.message)) : null}
      </FieldError>
    </Field>
  )
}

export function RuntimeTextField(props: RuntimeFieldBaseProps) {
  const { t } = useTranslation()
  const form = useFormContext<RuntimeSettingsFormValues>()
  const ui = useRuntimeSettingsFormUI()
  const id = useId()
  const error = form.formState.errors[props.name]
  const disabled = ui.readOnly || props.disabled
  const invalid = error != null || ui.conflicts.has(props.name)

  return (
    <Field
      data-invalid={invalid || undefined}
      data-disabled={disabled || undefined}
    >
      <RuntimeFieldHeading {...props} id={id} />
      <InputGroup>
        <InputGroupInput
          id={id}
          disabled={disabled}
          aria-invalid={invalid || undefined}
          {...form.register(props.name)}
        />
      </InputGroup>
      <RuntimeFieldNotes {...props} />
      <FieldError>
        {error?.message ? t(String(error.message)) : null}
      </FieldError>
    </Field>
  )
}

export function RuntimeSelectField(
  props: RuntimeFieldBaseProps & {
    options: ReadonlyArray<{ value: string; label: string }>
    onValueChange?: (value: string) => void
  }
) {
  const { t } = useTranslation()
  const form = useFormContext<RuntimeSettingsFormValues>()
  const ui = useRuntimeSettingsFormUI()
  const id = useId()
  const error = form.formState.errors[props.name]
  const disabled = ui.readOnly || props.disabled
  const invalid = error != null || ui.conflicts.has(props.name)
  const registration = form.register(props.name)

  return (
    <Field
      data-invalid={invalid || undefined}
      data-disabled={disabled || undefined}
    >
      <RuntimeFieldHeading {...props} id={id} />
      <NativeSelect
        id={id}
        className='w-full'
        disabled={disabled}
        aria-invalid={invalid || undefined}
        {...registration}
        onChange={(event) => {
          void registration.onChange(event)
          props.onValueChange?.(event.target.value)
        }}
      >
        {props.options.map((option) => (
          <NativeSelectOption key={option.value} value={option.value}>
            {t(option.label)}
          </NativeSelectOption>
        ))}
      </NativeSelect>
      <RuntimeFieldNotes {...props} />
      <FieldError>
        {error?.message ? t(String(error.message)) : null}
      </FieldError>
    </Field>
  )
}

export function RuntimeSwitchField(props: RuntimeFieldBaseProps) {
  const { t } = useTranslation()
  const form = useFormContext<RuntimeSettingsFormValues>()
  const ui = useRuntimeSettingsFormUI()
  const id = useId()
  const error = form.formState.errors[props.name]
  const disabled = ui.readOnly || props.disabled
  const invalid = error != null || ui.conflicts.has(props.name)

  return (
    <Field
      orientation='horizontal'
      data-invalid={invalid || undefined}
      data-disabled={disabled || undefined}
      className='min-h-11 rounded-lg border px-3 py-2.5'
    >
      <div className='min-w-0 flex-1'>
        <RuntimeFieldHeading {...props} id={id} />
        <RuntimeFieldNotes {...props} />
        <FieldError>
          {error?.message ? t(String(error.message)) : null}
        </FieldError>
      </div>
      <Controller
        control={form.control}
        name={props.name}
        render={({ field }) => (
          <Switch
            id={id}
            checked={Boolean(field.value)}
            onCheckedChange={field.onChange}
            disabled={disabled}
            aria-invalid={invalid || undefined}
            aria-label={t(runtimeSettingLabels[props.name])}
          />
        )}
      />
    </Field>
  )
}

export function RuntimeSettingsSection(props: {
  id: string
  title: string
  description: string
  children: React.ReactNode
}) {
  const { t } = useTranslation()
  return (
    <section className='space-y-4 border-t pt-5' aria-labelledby={props.id}>
      <div className='max-w-3xl'>
        <h2 id={props.id} className='text-base font-semibold'>
          {t(props.title)}
        </h2>
        <p className='text-muted-foreground mt-1 text-sm text-pretty'>
          {t(props.description)}
        </p>
      </div>
      <FieldGroup className='grid gap-4 md:grid-cols-2'>
        {props.children}
      </FieldGroup>
    </section>
  )
}
