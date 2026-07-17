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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Checkbox } from '@/components/ui/checkbox'
import {
  Field,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { ChannelRoutingStatusBadge } from '../../components/status-badge'
import type { PolicyPoolDocument } from '../../types'

function profileLabel(
  profile: PolicyPoolDocument['policy_profile'],
  translate: (key: string) => string
): string {
  const labels: Record<PolicyPoolDocument['policy_profile'], string> = {
    balanced: 'Balanced',
    reliability_first: 'Reliability first',
    cost_aware: 'Cost aware',
    enterprise_slo: 'Enterprise SLO',
    custom: 'Custom',
  }
  return translate(labels[profile])
}

export function PolicyPoolSettings(props: {
  schemaVersion: number
  pool: PolicyPoolDocument
  pools: PolicyPoolDocument[]
  readOnly: boolean
  onUpdate: (patch: Partial<PolicyPoolDocument>) => void
  onProfileChange: (profile: PolicyPoolDocument['policy_profile']) => void
}) {
  const { t } = useTranslation()
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})

  useEffect(() => {
    setFieldErrors({})
  }, [props.pool.pool_id])

  const setError = (key: string, message?: string) => {
    setFieldErrors((current) => {
      const next = { ...current }
      if (message) next[key] = message
      else delete next[key]
      return next
    })
  }

  return (
    <section className='space-y-3' aria-labelledby='visual-pool-heading'>
      <div className='flex flex-wrap items-start justify-between gap-2'>
        <div>
          <h3 id='visual-pool-heading' className='text-sm font-semibold'>
            {props.pool.display_name || props.pool.group_name}
          </h3>
          <p className='text-muted-foreground mt-0.5 text-xs'>
            {t('Pool #{{pool}}', { pool: props.pool.pool_id })}
          </p>
        </div>
        <div className='flex flex-wrap gap-1.5'>
          <ChannelRoutingStatusBadge
            status={props.pool.policy_profile}
            label={profileLabel(props.pool.policy_profile, t)}
          />
          <ChannelRoutingStatusBadge status={props.pool.deployment_stage} />
        </div>
      </div>

      <div className='grid gap-3 sm:grid-cols-2'>
        <div className='space-y-1.5'>
          <Label htmlFor={`policy-group-name-${props.pool.pool_id}`}>
            {t('Group')}
          </Label>
          <Input
            key={`${props.pool.pool_id}:${props.pool.group_name}`}
            id={`policy-group-name-${props.pool.pool_id}`}
            defaultValue={props.pool.group_name}
            maxLength={64}
            disabled={props.readOnly}
            aria-invalid={fieldErrors.groupName ? true : undefined}
            onBlur={(event) => {
              const input = event.currentTarget
              const value = input.value.trim()
              if (!value) {
                const message = t('Group is required.')
                input.setCustomValidity(message)
                setError('groupName', message)
                return
              }
              if ([...value].length > 64) {
                const message = t('Group must be 64 characters or fewer.')
                input.setCustomValidity(message)
                setError('groupName', message)
                return
              }
              if (
                props.pools.some(
                  (pool) =>
                    pool.pool_id !== props.pool.pool_id &&
                    pool.group_name === value
                )
              ) {
                const message = t('Group must be unique across policy pools.')
                input.setCustomValidity(message)
                setError('groupName', message)
                return
              }
              input.setCustomValidity('')
              setError('groupName')
              if (value !== props.pool.group_name) {
                props.onUpdate({ group_name: value })
              }
            }}
          />
          {fieldErrors.groupName ? (
            <p className='text-destructive text-xs' role='alert'>
              {fieldErrors.groupName}
            </p>
          ) : null}
        </div>
        <div className='space-y-1.5'>
          <Label htmlFor={`policy-display-name-${props.pool.pool_id}`}>
            {t('Display name')}
          </Label>
          <Input
            key={`${props.pool.pool_id}:${props.pool.display_name}`}
            id={`policy-display-name-${props.pool.pool_id}`}
            defaultValue={props.pool.display_name}
            maxLength={128}
            disabled={props.readOnly}
            aria-invalid={fieldErrors.displayName ? true : undefined}
            onBlur={(event) => {
              const input = event.currentTarget
              const value = input.value.trim()
              if ([...value].length > 128) {
                const message = t(
                  'Display name must be 128 characters or fewer.'
                )
                input.setCustomValidity(message)
                setError('displayName', message)
                return
              }
              input.setCustomValidity('')
              setError('displayName')
              if (value !== props.pool.display_name) {
                props.onUpdate({ display_name: value })
              }
            }}
          />
          {fieldErrors.displayName ? (
            <p className='text-destructive text-xs' role='alert'>
              {fieldErrors.displayName}
            </p>
          ) : null}
        </div>
        <div className='space-y-1.5'>
          <Label htmlFor={`policy-stage-${props.pool.pool_id}`}>
            {t('Stage')}
          </Label>
          <NativeSelect
            id={`policy-stage-${props.pool.pool_id}`}
            value={props.pool.deployment_stage}
            disabled={props.readOnly}
            onChange={(event) =>
              props.onUpdate({
                deployment_stage: event.target
                  .value as PolicyPoolDocument['deployment_stage'],
              })
            }
          >
            {(['observe', 'shadow', 'canary', 'active'] as const).map(
              (stage) => (
                <NativeSelectOption key={stage} value={stage}>
                  {t(
                    stage === 'active'
                      ? 'Active'
                      : stage[0].toUpperCase() + stage.slice(1)
                  )}
                </NativeSelectOption>
              )
            )}
          </NativeSelect>
        </div>
        <div className='space-y-1.5'>
          <Label htmlFor={`policy-profile-${props.pool.pool_id}`}>
            {t('Policy profile')}
          </Label>
          <NativeSelect
            id={`policy-profile-${props.pool.pool_id}`}
            value={props.pool.policy_profile}
            disabled={props.readOnly}
            onChange={(event) =>
              props.onProfileChange(
                event.target.value as PolicyPoolDocument['policy_profile']
              )
            }
          >
            {(
              [
                'balanced',
                'reliability_first',
                'cost_aware',
                'enterprise_slo',
                'custom',
              ] as const
            ).map((profile) => (
              <NativeSelectOption key={profile} value={profile}>
                {profileLabel(profile, t)}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
      </div>

      {props.schemaVersion === 2 ? (
        <FieldSet className='rounded-lg border p-3'>
          <FieldLegend variant='label'>{t('Default')}</FieldLegend>
          <FieldGroup className='grid gap-3 sm:grid-cols-3'>
            <Field
              orientation='horizontal'
              data-disabled={props.readOnly ? true : undefined}
            >
              <Checkbox
                id={`policy-default-enabled-${props.pool.pool_id}`}
                checked={props.pool.default_enabled ?? true}
                disabled={props.readOnly}
                onCheckedChange={(checked) =>
                  props.onUpdate({ default_enabled: checked === true })
                }
              />
              <FieldLabel
                htmlFor={`policy-default-enabled-${props.pool.pool_id}`}
              >
                {t('Enabled')}
              </FieldLabel>
            </Field>
            <Field data-disabled={props.readOnly ? true : undefined}>
              <FieldLabel
                htmlFor={`policy-default-priority-${props.pool.pool_id}`}
              >
                {t('Priority')}
              </FieldLabel>
              <Input
                key={`${props.pool.pool_id}:default-priority:${props.pool.default_priority ?? 0}`}
                id={`policy-default-priority-${props.pool.pool_id}`}
                type='number'
                step={1}
                defaultValue={props.pool.default_priority ?? 0}
                disabled={props.readOnly}
                onBlur={(event) => {
                  const value = event.currentTarget.valueAsNumber
                  if (Number.isSafeInteger(value)) {
                    props.onUpdate({ default_priority: value })
                  }
                }}
              />
            </Field>
            <Field data-disabled={props.readOnly ? true : undefined}>
              <FieldLabel
                htmlFor={`policy-default-weight-${props.pool.pool_id}`}
              >
                {t('Weight')}
              </FieldLabel>
              <Input
                key={`${props.pool.pool_id}:default-weight:${props.pool.default_weight ?? 100}`}
                id={`policy-default-weight-${props.pool.pool_id}`}
                type='number'
                min={0}
                step={1}
                defaultValue={props.pool.default_weight ?? 100}
                disabled={props.readOnly}
                onBlur={(event) => {
                  const value = event.currentTarget.valueAsNumber
                  if (Number.isSafeInteger(value) && value >= 0) {
                    props.onUpdate({ default_weight: value })
                  }
                }}
              />
            </Field>
          </FieldGroup>
        </FieldSet>
      ) : null}
    </section>
  )
}
