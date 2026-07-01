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
import {
  combineBillingExpr,
  splitBillingExprAndRequestRules,
} from '@/features/pricing/lib/billing-expr'

import { safeJsonParse } from '../utils/json-parser'
import { formatPricingNumber } from './pricing-format'

export type ModelPricingSnapshotInput = {
  modelPrice: string
  modelRatio: string
  cacheRatio: string
  createCacheRatio: string
  completionRatio: string
  imageRatio: string
  audioRatio: string
  audioCompletionRatio: string
  billingMode: string
  billingExpr: string
}

export type ModelPricingSnapshot = {
  name: string
  price?: string
  ratio?: string
  cacheRatio?: string
  createCacheRatio?: string
  completionRatio?: string
  imageRatio?: string
  audioRatio?: string
  audioCompletionRatio?: string
  billingMode?: string
  billingExpr?: string
  requestRuleExpr?: string
  hasConflict: boolean
}

export type ModelPricingEntryDraft = {
  name: string
  price?: string
  ratio?: string
  cacheRatio?: string
  createCacheRatio?: string
  completionRatio?: string
  imageRatio?: string
  audioRatio?: string
  audioCompletionRatio?: string
  billingMode?: string
  billingExpr?: string
  requestRuleExpr?: string
}

export type ModelRow = ModelPricingSnapshot & {
  saved?: ModelPricingSnapshot
  draft?: ModelPricingSnapshot
  isDraftChanged: boolean
  isDraftDeleted: boolean
  isDraftNew: boolean
}

export const hasPricingValue = (value?: string) =>
  value !== undefined && value !== ''

const parseOptionRecord = <T>(value: string, context: string) =>
  safeJsonParse<Record<string, T>>(value, {
    fallback: {},
    context,
    silent: true,
  })

const stringifyOptionRecord = <T>(value: Record<string, T>) =>
  JSON.stringify(value, null, 2)

const setNumberIfPresent = (
  target: Record<string, number>,
  name: string,
  value: string | undefined
) => {
  if (!value || value === '') return
  const parsed = Number.parseFloat(value)
  if (Number.isFinite(parsed)) target[name] = parsed
}

export const removeModelPricingEntry = (
  input: ModelPricingSnapshotInput,
  name: string
): ModelPricingSnapshotInput => {
  const priceMap = parseOptionRecord<number>(input.modelPrice, 'model prices')
  const ratioMap = parseOptionRecord<number>(input.modelRatio, 'model ratios')
  const cacheMap = parseOptionRecord<number>(input.cacheRatio, 'cache ratios')
  const createCacheMap = parseOptionRecord<number>(
    input.createCacheRatio,
    'create cache ratios'
  )
  const completionMap = parseOptionRecord<number>(
    input.completionRatio,
    'completion ratios'
  )
  const imageMap = parseOptionRecord<number>(input.imageRatio, 'image ratios')
  const audioMap = parseOptionRecord<number>(input.audioRatio, 'audio ratios')
  const audioCompletionMap = parseOptionRecord<number>(
    input.audioCompletionRatio,
    'audio completion ratios'
  )
  const billingModeMap = parseOptionRecord<string>(
    input.billingMode,
    'billing mode'
  )
  const billingExprMap = parseOptionRecord<string>(
    input.billingExpr,
    'billing expression'
  )

  delete priceMap[name]
  delete ratioMap[name]
  delete cacheMap[name]
  delete createCacheMap[name]
  delete completionMap[name]
  delete imageMap[name]
  delete audioMap[name]
  delete audioCompletionMap[name]
  delete billingModeMap[name]
  delete billingExprMap[name]

  return {
    modelPrice: stringifyOptionRecord(priceMap),
    modelRatio: stringifyOptionRecord(ratioMap),
    cacheRatio: stringifyOptionRecord(cacheMap),
    createCacheRatio: stringifyOptionRecord(createCacheMap),
    completionRatio: stringifyOptionRecord(completionMap),
    imageRatio: stringifyOptionRecord(imageMap),
    audioRatio: stringifyOptionRecord(audioMap),
    audioCompletionRatio: stringifyOptionRecord(audioCompletionMap),
    billingMode: stringifyOptionRecord(billingModeMap),
    billingExpr: stringifyOptionRecord(billingExprMap),
  }
}

export const upsertModelPricingEntry = (
  input: ModelPricingSnapshotInput,
  data: ModelPricingEntryDraft,
  targetNames: string[] = [data.name]
): ModelPricingSnapshotInput => {
  let next = input

  targetNames.forEach((name) => {
    next = removeModelPricingEntry(next, name)

    const priceMap = parseOptionRecord<number>(next.modelPrice, 'model prices')
    const ratioMap = parseOptionRecord<number>(next.modelRatio, 'model ratios')
    const cacheMap = parseOptionRecord<number>(next.cacheRatio, 'cache ratios')
    const createCacheMap = parseOptionRecord<number>(
      next.createCacheRatio,
      'create cache ratios'
    )
    const completionMap = parseOptionRecord<number>(
      next.completionRatio,
      'completion ratios'
    )
    const imageMap = parseOptionRecord<number>(next.imageRatio, 'image ratios')
    const audioMap = parseOptionRecord<number>(next.audioRatio, 'audio ratios')
    const audioCompletionMap = parseOptionRecord<number>(
      next.audioCompletionRatio,
      'audio completion ratios'
    )
    const billingModeMap = parseOptionRecord<string>(
      next.billingMode,
      'billing mode'
    )
    const billingExprMap = parseOptionRecord<string>(
      next.billingExpr,
      'billing expression'
    )

    if (data.billingMode === 'tiered_expr') {
      const combined = combineBillingExpr(
        data.billingExpr || '',
        data.requestRuleExpr || ''
      )
      if (combined) {
        billingModeMap[name] = 'tiered_expr'
        billingExprMap[name] = combined
      }
    } else if (data.price && data.price !== '') {
      setNumberIfPresent(priceMap, name, data.price)
    } else {
      setNumberIfPresent(ratioMap, name, data.ratio)
      setNumberIfPresent(cacheMap, name, data.cacheRatio)
      setNumberIfPresent(createCacheMap, name, data.createCacheRatio)
      setNumberIfPresent(completionMap, name, data.completionRatio)
      setNumberIfPresent(imageMap, name, data.imageRatio)
      setNumberIfPresent(audioMap, name, data.audioRatio)
      setNumberIfPresent(audioCompletionMap, name, data.audioCompletionRatio)
    }

    next = {
      modelPrice: stringifyOptionRecord(priceMap),
      modelRatio: stringifyOptionRecord(ratioMap),
      cacheRatio: stringifyOptionRecord(cacheMap),
      createCacheRatio: stringifyOptionRecord(createCacheMap),
      completionRatio: stringifyOptionRecord(completionMap),
      imageRatio: stringifyOptionRecord(imageMap),
      audioRatio: stringifyOptionRecord(audioMap),
      audioCompletionRatio: stringifyOptionRecord(audioCompletionMap),
      billingMode: stringifyOptionRecord(billingModeMap),
      billingExpr: stringifyOptionRecord(billingExprMap),
    }
  })

  return next
}

const toNumberOrNull = (value?: string) => {
  if (!hasPricingValue(value)) return null
  const num = Number(value)
  return Number.isFinite(num) ? num : null
}

const ratioToPrice = (ratio?: string, denominator?: string) => {
  const ratioNumber = toNumberOrNull(ratio)
  const denominatorNumber = denominator ? toNumberOrNull(denominator) : 2
  if (ratioNumber === null || denominatorNumber === null) return ''
  return formatPricingNumber(ratioNumber * denominatorNumber)
}

export const getModeLabel = (mode?: string) => {
  if (mode === 'per-request') return 'Per-request'
  if (mode === 'tiered_expr') return 'Expression'
  return 'Per-token'
}

export const getModeVariant = (
  mode?: string
): 'warning' | 'info' | 'success' => {
  if (mode === 'per-request') return 'warning'
  if (mode === 'tiered_expr') return 'info'
  return 'success'
}

const getExpressionSummary = (
  row: ModelPricingSnapshot,
  t: (key: string) => string
) => {
  const tierCount = (row.billingExpr?.match(/tier\(/g) || []).length
  if (tierCount > 0) {
    return `${t('Tiered pricing')} · ${tierCount} ${t('tiers')}`
  }
  return t('Expression pricing')
}

export const getPriceSummary = (
  row: ModelPricingSnapshot,
  t: (key: string) => string
) => {
  if (row.billingMode === 'tiered_expr') {
    return getExpressionSummary(row, t)
  }
  if (row.billingMode === 'per-request') {
    return row.price ? `$${row.price} / ${t('request')}` : t('Unset price')
  }

  const inputPrice = ratioToPrice(row.ratio)
  if (!inputPrice) return t('Unset price')

  const extraCount = [
    row.completionRatio,
    row.cacheRatio,
    row.createCacheRatio,
    row.imageRatio,
    row.audioRatio,
    row.audioCompletionRatio,
  ].filter(hasPricingValue).length

  return extraCount > 0
    ? `${t('Input')} $${inputPrice} · ${extraCount} ${t('extras')}`
    : `${t('Input')} $${inputPrice}`
}

export const getPriceDetail = (
  row: ModelPricingSnapshot,
  t: (key: string) => string
) => {
  if (row.billingMode === 'tiered_expr') {
    return row.requestRuleExpr
      ? t('Includes request rules')
      : t('Expression based')
  }
  if (row.billingMode === 'per-request') {
    return t('Fixed request price')
  }

  const inputPrice = ratioToPrice(row.ratio)
  if (!inputPrice) return t('No base input price')

  const details = [
    row.completionRatio &&
      `${t('Output')} $${ratioToPrice(row.completionRatio, inputPrice)}`,
    row.cacheRatio &&
      `${t('Cache')} $${ratioToPrice(row.cacheRatio, inputPrice)}`,
    row.createCacheRatio &&
      `${t('Cache write')} $${ratioToPrice(row.createCacheRatio, inputPrice)}`,
  ]
    .filter(Boolean)
    .slice(0, 2)

  return details.length > 0 ? details.join(' · ') : t('Base input price only')
}

export const buildModelSnapshots = ({
  modelPrice,
  modelRatio,
  cacheRatio,
  createCacheRatio,
  completionRatio,
  imageRatio,
  audioRatio,
  audioCompletionRatio,
  billingMode,
  billingExpr,
}: ModelPricingSnapshotInput): ModelPricingSnapshot[] => {
  const priceMap = safeJsonParse<Record<string, number>>(modelPrice, {
    fallback: {},
    context: 'model prices',
  })
  const ratioMap = safeJsonParse<Record<string, number>>(modelRatio, {
    fallback: {},
    context: 'model ratios',
  })
  const cacheMap = safeJsonParse<Record<string, number>>(cacheRatio, {
    fallback: {},
    context: 'cache ratios',
  })
  const createCacheMap = safeJsonParse<Record<string, number>>(
    createCacheRatio,
    { fallback: {}, context: 'create cache ratios' }
  )
  const completionMap = safeJsonParse<Record<string, number>>(completionRatio, {
    fallback: {},
    context: 'completion ratios',
  })
  const imageMap = safeJsonParse<Record<string, number>>(imageRatio, {
    fallback: {},
    context: 'image ratios',
  })
  const audioMap = safeJsonParse<Record<string, number>>(audioRatio, {
    fallback: {},
    context: 'audio ratios',
  })
  const audioCompletionMap = safeJsonParse<Record<string, number>>(
    audioCompletionRatio,
    { fallback: {}, context: 'audio completion ratios' }
  )
  const billingModeMap = safeJsonParse<Record<string, string>>(billingMode, {
    fallback: {},
    context: 'billing mode',
  })
  const billingExprMap = safeJsonParse<Record<string, string>>(billingExpr, {
    fallback: {},
    context: 'billing expression',
  })

  const modelNames = new Set([
    ...Object.keys(priceMap),
    ...Object.keys(ratioMap),
    ...Object.keys(cacheMap),
    ...Object.keys(createCacheMap),
    ...Object.keys(completionMap),
    ...Object.keys(imageMap),
    ...Object.keys(audioMap),
    ...Object.keys(audioCompletionMap),
    ...Object.keys(billingModeMap),
    ...Object.keys(billingExprMap),
  ])

  return [...modelNames].map((name) => {
    const price = priceMap[name]?.toString() || ''
    const ratio = ratioMap[name]?.toString() || ''
    const cache = cacheMap[name]?.toString() || ''
    const createCache = createCacheMap[name]?.toString() || ''
    const completion = completionMap[name]?.toString() || ''
    const image = imageMap[name]?.toString() || ''
    const audio = audioMap[name]?.toString() || ''
    const audioCompletion = audioCompletionMap[name]?.toString() || ''

    const modeForModel = billingModeMap[name]
    if (modeForModel === 'tiered_expr') {
      const fullExpr = billingExprMap[name] || ''
      const { billingExpr: pureExpr, requestRuleExpr } =
        splitBillingExprAndRequestRules(fullExpr)
      return {
        name,
        billingMode: 'tiered_expr',
        billingExpr: pureExpr,
        requestRuleExpr,
        price,
        ratio,
        cacheRatio: cache,
        createCacheRatio: createCache,
        completionRatio: completion,
        imageRatio: image,
        audioRatio: audio,
        audioCompletionRatio: audioCompletion,
        hasConflict: false,
      }
    }

    return {
      name,
      price,
      ratio,
      cacheRatio: cache,
      createCacheRatio: createCache,
      completionRatio: completion,
      imageRatio: image,
      audioRatio: audio,
      audioCompletionRatio: audioCompletion,
      billingMode: price !== '' ? 'per-request' : 'per-token',
      hasConflict:
        price !== '' &&
        (ratio !== '' ||
          completion !== '' ||
          cache !== '' ||
          createCache !== '' ||
          image !== '' ||
          audio !== '' ||
          audioCompletion !== ''),
    }
  })
}

export const getSnapshotSignature = (snapshot?: ModelPricingSnapshot) => {
  if (!snapshot) return ''
  return JSON.stringify({
    price: snapshot.price || '',
    ratio: snapshot.ratio || '',
    cacheRatio: snapshot.cacheRatio || '',
    createCacheRatio: snapshot.createCacheRatio || '',
    completionRatio: snapshot.completionRatio || '',
    imageRatio: snapshot.imageRatio || '',
    audioRatio: snapshot.audioRatio || '',
    audioCompletionRatio: snapshot.audioCompletionRatio || '',
    billingMode: snapshot.billingMode || 'per-token',
    billingExpr: snapshot.billingExpr || '',
    requestRuleExpr: snapshot.requestRuleExpr || '',
  })
}
