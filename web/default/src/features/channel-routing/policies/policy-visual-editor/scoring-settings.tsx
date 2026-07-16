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

import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import { useChannelRoutingFormatters } from '../../lib/format'
import type { PolicyPoolDocument } from '../../types'

const policyWeightKeys = [
  'weight_availability',
  'weight_latency',
  'weight_throughput',
  'weight_cost',
]

const profileDefaults: Record<
  PolicyPoolDocument['policy_profile'],
  Record<string, number>
> = {
  balanced: {
    weight_availability: 0.45,
    weight_latency: 0.25,
    weight_throughput: 0.1,
    weight_cost: 0.2,
    availability_target: 0.99,
    availability_floor: 0.95,
  },
  reliability_first: {
    weight_availability: 0.65,
    weight_latency: 0.2,
    weight_throughput: 0.1,
    weight_cost: 0.05,
    availability_target: 0.995,
    availability_floor: 0.98,
  },
  cost_aware: {
    weight_availability: 0.3,
    weight_latency: 0.15,
    weight_throughput: 0.1,
    weight_cost: 0.45,
    availability_target: 0.98,
    availability_floor: 0.9,
  },
  enterprise_slo: {
    weight_availability: 0.55,
    weight_latency: 0.3,
    weight_throughput: 0.1,
    weight_cost: 0.05,
    availability_target: 0.999,
    availability_floor: 0.98,
  },
  custom: {
    weight_availability: 0.45,
    weight_latency: 0.25,
    weight_throughput: 0.1,
    weight_cost: 0.2,
    availability_target: 0.99,
    availability_floor: 0.95,
  },
}

function recordAtPath(
  root: Record<string, unknown>,
  path: string[]
): Record<string, unknown> {
  let current = root
  for (const key of path) {
    const value = current[key]
    if (value == null || typeof value !== 'object' || Array.isArray(value)) {
      return {}
    }
    current = value as Record<string, unknown>
  }
  return current
}

function numberAtPath(
  root: Record<string, unknown>,
  path: string[],
  fallback: number
): number {
  if (path.length === 0) return fallback
  const parent = recordAtPath(root, path.slice(0, -1))
  const value = parent[path.at(-1) ?? '']
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function booleanAtPath(
  root: Record<string, unknown>,
  path: string[],
  fallback: boolean
): boolean {
  if (path.length === 0) return fallback
  const parent = recordAtPath(root, path.slice(0, -1))
  const value = parent[path.at(-1) ?? '']
  return typeof value === 'boolean' ? value : fallback
}

type NumericPolicyField = {
  key: string
  label: string
  path: string[]
  defaultValue: number
  min: number
  max: number
  step: number
}

export function PolicyScoringSettings(props: {
  pool: PolicyPoolDocument
  readOnly: boolean
  onUpdatePath: (path: string[], value: unknown) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
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
  const clearErrors = (keys: string[]) => {
    setFieldErrors((current) => {
      const next = { ...current }
      for (const key of keys) delete next[key]
      return next
    })
  }
  const commitPolicyNumber = (
    field: NumericPolicyField,
    input: HTMLInputElement
  ) => {
    const value = input.valueAsNumber
    const explorationOutOfRange =
      field.key === 'exploration_basis_points' &&
      value !== 0 &&
      (value < 100 || value > 300)
    const integerRequired = field.step === 1 && !Number.isSafeInteger(value)
    if (
      !Number.isFinite(value) ||
      value < field.min ||
      value > field.max ||
      explorationOutOfRange ||
      integerRequired
    ) {
      if (explorationOutOfRange) {
        const message = t(
          'Exploration basis points must be 0, or between 100 and 300.'
        )
        input.setCustomValidity(message)
        setError(field.key, message)
        return
      }
      if (integerRequired) {
        const message = t('Enter a whole number')
        input.setCustomValidity(message)
        setError(field.key, message)
        return
      }
      const message = t('Value must be between {{min}} and {{max}}', {
        min: field.min,
        max: field.max,
      })
      input.setCustomValidity(message)
      setError(field.key, message)
      return
    }
    if (policyWeightKeys.includes(field.key)) {
      const weightTotal = policyWeightKeys.reduce((total, key) => {
        if (key === field.key) return total + value
        return total + numberAtPath(props.pool.policy, [key], defaults[key])
      }, 0)
      if (weightTotal <= 0) {
        const message = t(
          'At least one routing weight must be greater than zero'
        )
        input.setCustomValidity(message)
        setError(field.key, message)
        return
      }
      for (const key of policyWeightKeys) {
        const weightInput = document.querySelector<HTMLInputElement>(
          `[id="policy-${props.pool.pool_id}-${key}"]`
        )
        weightInput?.setCustomValidity('')
      }
      clearErrors(policyWeightKeys)
    } else {
      setError(field.key)
    }
    input.setCustomValidity('')
    props.onUpdatePath(field.path, value)
  }

  const defaults = profileDefaults[props.pool.policy_profile]
  const availabilityTarget = numberAtPath(
    props.pool.policy,
    ['availability_target'],
    defaults.availability_target
  )
  const availabilityFloor = numberAtPath(
    props.pool.policy,
    ['availability_floor'],
    defaults.availability_floor
  )
  const slowStartStateTTL = numberAtPath(
    props.pool.policy,
    ['canary', 'slow_start', 'state_ttl_seconds'],
    24 * 60 * 60
  )
  const numericFields: NumericPolicyField[] = [
    {
      key: 'weight_availability',
      label: t('Availability weight'),
      path: ['weight_availability'],
      defaultValue: defaults.weight_availability,
      min: 0,
      max: Number.MAX_SAFE_INTEGER,
      step: 0.01,
    },
    {
      key: 'weight_latency',
      label: t('Latency weight'),
      path: ['weight_latency'],
      defaultValue: defaults.weight_latency,
      min: 0,
      max: Number.MAX_SAFE_INTEGER,
      step: 0.01,
    },
    {
      key: 'weight_throughput',
      label: t('Throughput weight'),
      path: ['weight_throughput'],
      defaultValue: defaults.weight_throughput,
      min: 0,
      max: Number.MAX_SAFE_INTEGER,
      step: 0.01,
    },
    {
      key: 'weight_cost',
      label: t('Cost weight'),
      path: ['weight_cost'],
      defaultValue: defaults.weight_cost,
      min: 0,
      max: Number.MAX_SAFE_INTEGER,
      step: 0.01,
    },
    {
      key: 'availability_target',
      label: t('Availability target'),
      path: ['availability_target'],
      defaultValue: defaults.availability_target,
      min: Math.max(0.001, availabilityFloor),
      max: 1,
      step: 0.001,
    },
    {
      key: 'availability_floor',
      label: t('Availability floor'),
      path: ['availability_floor'],
      defaultValue: defaults.availability_floor,
      min: 0,
      max: Math.min(1, availabilityTarget),
      step: 0.001,
    },
    {
      key: 'min_volume',
      label: t('Minimum sample volume'),
      path: ['min_volume'],
      defaultValue: 50,
      min: 0,
      max: Number.MAX_SAFE_INTEGER,
      step: 1,
    },
    {
      key: 'top_k',
      label: t('Candidate limit'),
      path: ['top_k'],
      defaultValue: 3,
      min: 1,
      max: Number.MAX_SAFE_INTEGER,
      step: 1,
    },
    {
      key: 'max_ejected_pct',
      label: t('Maximum ejected percent'),
      path: ['max_ejected_pct'],
      defaultValue: 50,
      min: 0,
      max: 100,
      step: 1,
    },
    {
      key: 'exploration_basis_points',
      label: t('Exploration basis points'),
      path: ['exploration_basis_points'],
      defaultValue: 100,
      min: 0,
      max: 300,
      step: 1,
    },
    {
      key: 'canary.slow_start.minimum_factor',
      label: t('Slow-start minimum factor'),
      path: ['canary', 'slow_start', 'minimum_factor'],
      defaultValue: 0.1,
      min: 0.01,
      max: 0.5,
      step: 0.01,
    },
    {
      key: 'canary.slow_start.ramp_seconds',
      label: t('Slow-start ramp seconds'),
      path: ['canary', 'slow_start', 'ramp_seconds'],
      defaultValue: 300,
      min: 30,
      max: Math.max(30, Math.min(3_600, slowStartStateTTL)),
      step: 1,
    },
  ]
  const weights = policyWeightKeys.map((key) =>
    numberAtPath(props.pool.policy, [key], defaults[key])
  )
  const weightTotal = weights.reduce((total, weight) => total + weight, 0)
  const enterpriseHedgeEnabled = booleanAtPath(
    props.pool.policy,
    ['enterprise', 'hedge', 'enabled'],
    false
  )
  const switchFields = [
    {
      key: 'require_known_cost',
      path: ['require_known_cost'],
      label: t('Require known cost'),
      description: t('Exclude candidates whose platform cost is unknown.'),
      fallback: false,
    },
    {
      key: 'allow_soft_failure_fallback',
      path: ['allow_soft_failure_fallback'],
      label: t('Allow soft-failure fallback'),
      description: t(
        'Permit a bounded fallback when every candidate is soft-failed.'
      ),
      fallback: true,
    },
    {
      key: 'enforce_business_tier_cascade',
      path: ['enforce_business_tier_cascade'],
      label: t('Enforce business-tier cascade'),
      description: t(
        'Keep automatic selection inside the best available priority tier.'
      ),
      fallback: false,
    },
  ]
  const enterpriseInputTPM = numberAtPath(
    props.pool.policy,
    ['enterprise', 'capacity', 'input_tpm'],
    1_000_000
  )
  const enterpriseOutputTPM = numberAtPath(
    props.pool.policy,
    ['enterprise', 'capacity', 'output_tpm'],
    250_000
  )
  const enterpriseTotalTPM = numberAtPath(
    props.pool.policy,
    ['enterprise', 'capacity', 'total_tpm'],
    1_250_000
  )
  const enterpriseCapacityFields: NumericPolicyField[] = [
    {
      key: 'enterprise.capacity.rpm',
      label: t('Requests per minute'),
      path: ['enterprise', 'capacity', 'rpm'],
      defaultValue: 600,
      min: 1,
      max: 1_000_000_000_000,
      step: 1,
    },
    {
      key: 'enterprise.capacity.input_tpm',
      label: t('Input tokens per minute'),
      path: ['enterprise', 'capacity', 'input_tpm'],
      defaultValue: 1_000_000,
      min: 1,
      max: Math.min(1_000_000_000_000, Math.max(1, enterpriseTotalTPM)),
      step: 1,
    },
    {
      key: 'enterprise.capacity.output_tpm',
      label: t('Output tokens per minute'),
      path: ['enterprise', 'capacity', 'output_tpm'],
      defaultValue: 250_000,
      min: 1,
      max: Math.min(1_000_000_000_000, Math.max(1, enterpriseTotalTPM)),
      step: 1,
    },
    {
      key: 'enterprise.capacity.total_tpm',
      label: t('Total tokens per minute'),
      path: ['enterprise', 'capacity', 'total_tpm'],
      defaultValue: 1_250_000,
      min: Math.min(
        1_000_000_000_000,
        Math.max(1, enterpriseInputTPM, enterpriseOutputTPM)
      ),
      max: 1_000_000_000_000,
      step: 1,
    },
    {
      key: 'enterprise.capacity.inflight',
      label: t('Inflight limit'),
      path: ['enterprise', 'capacity', 'inflight'],
      defaultValue: 32,
      min: 1,
      max: 1_000_000_000_000,
      step: 1,
    },
    {
      key: 'enterprise.capacity.cost_nano_usd',
      label: t('Cost limit in nanoUSD'),
      path: ['enterprise', 'capacity', 'cost_nano_usd'],
      defaultValue: 0,
      min: 0,
      max: 1_000_000_000_000,
      step: 1,
    },
  ]

  return (
    <>
      <section
        className='space-y-3 border-t pt-4'
        aria-labelledby='visual-scoring-heading'
      >
        <div className='flex flex-wrap items-end justify-between gap-2'>
          <div>
            <h3 id='visual-scoring-heading' className='text-sm font-semibold'>
              {t('Scoring and safety')}
            </h3>
            <p className='text-muted-foreground mt-0.5 text-xs'>
              {t('Weights are normalized by the routing engine before use.')}
            </p>
          </div>
          <Badge variant='outline'>
            {t('Normalized')}:{' '}
            {weights
              .map((weight) =>
                format.percent(weightTotal > 0 ? weight / weightTotal : 0)
              )
              .join(' / ')}
          </Badge>
        </div>
        {weightTotal <= 0 ? (
          <p className='text-destructive text-xs' role='alert'>
            {t('At least one routing weight must be greater than zero')}
          </p>
        ) : null}
        <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
          {numericFields.map((field) => {
            const value = numberAtPath(
              props.pool.policy,
              field.path,
              field.defaultValue
            )
            return (
              <div key={field.key} className='space-y-1.5'>
                <Label htmlFor={`policy-${props.pool.pool_id}-${field.key}`}>
                  {field.label}
                </Label>
                <Input
                  key={`${props.pool.pool_id}:${field.key}:${value}`}
                  id={`policy-${props.pool.pool_id}-${field.key}`}
                  type='number'
                  min={field.min}
                  max={field.max}
                  step={field.step}
                  defaultValue={value}
                  disabled={props.readOnly}
                  aria-invalid={fieldErrors[field.key] ? true : undefined}
                  onBlur={(event) =>
                    commitPolicyNumber(field, event.currentTarget)
                  }
                />
                {fieldErrors[field.key] ? (
                  <p className='text-destructive text-xs' role='alert'>
                    {fieldErrors[field.key]}
                  </p>
                ) : null}
              </div>
            )
          })}
        </div>
        <div className='grid gap-3 sm:grid-cols-2'>
          {switchFields.map((field) => {
            const checked = booleanAtPath(
              props.pool.policy,
              field.path,
              field.fallback
            )
            const id = `policy-${props.pool.pool_id}-${field.key}`
            return (
              <div
                key={field.key}
                className='flex min-h-14 items-start gap-3 rounded-lg border p-3'
              >
                <Checkbox
                  id={id}
                  checked={checked}
                  disabled={props.readOnly}
                  onCheckedChange={(next) =>
                    props.onUpdatePath(field.path, next === true)
                  }
                />
                <div className='min-w-0'>
                  <Label htmlFor={id}>{field.label}</Label>
                  <p className='text-muted-foreground mt-0.5 text-xs'>
                    {field.description}
                  </p>
                </div>
              </div>
            )
          })}
        </div>
      </section>

      {props.pool.policy_profile === 'enterprise_slo' ? (
        <section
          className='space-y-3 border-t pt-4'
          aria-labelledby='visual-enterprise-heading'
        >
          <div>
            <h3
              id='visual-enterprise-heading'
              className='text-sm font-semibold'
            >
              {t('Enterprise capacity')}
            </h3>
            <p className='text-muted-foreground mt-0.5 text-xs'>
              {t(
                'Strict shared capacity remains fail-closed when cost or lease evidence is unavailable.'
              )}
            </p>
          </div>
          <div className='flex min-h-14 items-start gap-3 rounded-lg border p-3'>
            <Checkbox
              id={`policy-${props.pool.pool_id}-enterprise-hedge-enabled`}
              checked={enterpriseHedgeEnabled}
              disabled={props.readOnly}
              onCheckedChange={(next) =>
                props.onUpdatePath(
                  ['enterprise', 'hedge', 'enabled'],
                  next === true
                )
              }
            />
            <div className='min-w-0'>
              <Label
                htmlFor={`policy-${props.pool.pool_id}-enterprise-hedge-enabled`}
              >
                {t('Hedge')}
              </Label>
              <p className='text-muted-foreground mt-0.5 text-xs'>
                {t(
                  'Allow independent duplicate attempts when all Hedge guards pass.'
                )}
              </p>
            </div>
          </div>
          <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
            {enterpriseCapacityFields.map((field) => {
              const value = numberAtPath(
                props.pool.policy,
                field.path,
                field.defaultValue
              )
              return (
                <div key={field.key} className='space-y-1.5'>
                  <Label htmlFor={`policy-${props.pool.pool_id}-${field.key}`}>
                    {field.label}
                  </Label>
                  <Input
                    key={`${props.pool.pool_id}:${field.key}:${value}`}
                    id={`policy-${props.pool.pool_id}-${field.key}`}
                    type='number'
                    min={field.min}
                    max={field.max}
                    step={field.step}
                    defaultValue={value}
                    disabled={props.readOnly}
                    aria-invalid={fieldErrors[field.key] ? true : undefined}
                    onBlur={(event) =>
                      commitPolicyNumber(field, event.currentTarget)
                    }
                  />
                  {fieldErrors[field.key] ? (
                    <p className='text-destructive text-xs' role='alert'>
                      {fieldErrors[field.key]}
                    </p>
                  ) : null}
                </div>
              )
            })}
          </div>
        </section>
      ) : null}
    </>
  )
}
