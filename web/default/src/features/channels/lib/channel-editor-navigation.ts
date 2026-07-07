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
import { MODEL_FETCHABLE_TYPES } from '../constants'
import { hasConfiguredOverrideValue } from './channel-config-values'
import type { ChannelFormValues } from './channel-form'
import { hasAdvancedSettingsErrors } from './channel-form-errors'
import { parseModelsString } from './model-mapping-validation'

export type ChannelEditorSectionStatus =
  | 'complete'
  | 'configured'
  | 'error'
  | 'idle'

export type ChannelEditorNavChildItemState = {
  id: string
  titleKey: string
  configured: boolean
}

export type ChannelEditorNavItemState = {
  id: string
  titleKey: string
  statusLabelKey: string
  status: ChannelEditorSectionStatus
  configured?: boolean
  children?: ChannelEditorNavChildItemState[]
}

type ChannelEditorErrorMap = Record<string, unknown>

export const CHANNEL_EDITOR_SECTION_IDS = {
  identity: 'channel-section-identity',
  credentials: 'channel-section-credentials',
  models: 'channel-section-models',
  advanced: 'channel-section-advanced',
} as const

export const CHANNEL_EDITOR_MAIN_SECTION_IDS = [
  CHANNEL_EDITOR_SECTION_IDS.identity,
  CHANNEL_EDITOR_SECTION_IDS.credentials,
  CHANNEL_EDITOR_SECTION_IDS.models,
  CHANNEL_EDITOR_SECTION_IDS.advanced,
]

export const ADVANCED_SETTINGS_SECTION_IDS = {
  routingStrategy: 'channel-section-advanced-routing-strategy',
  internalNotes: 'channel-section-advanced-internal-notes',
  overrideRules: 'channel-section-advanced-override-rules',
  extraSettings: 'channel-section-advanced-extra-settings',
  fieldPassthrough: 'channel-section-advanced-field-passthrough',
  upstreamModelDetection: 'channel-section-advanced-upstream-model-detection',
} as const

export const ADVANCED_SETTINGS_CHILD_SECTION_IDS = Object.values(
  ADVANCED_SETTINGS_SECTION_IDS
)

function getCompletionStatus(
  hasErrors: boolean,
  isComplete: boolean
): ChannelEditorSectionStatus {
  if (hasErrors) return 'error'
  if (isComplete) return 'complete'
  return 'idle'
}

function getStatusLabelKey(status: ChannelEditorSectionStatus): string {
  if (status === 'error') return 'Error'
  if (status === 'complete' || status === 'configured') return 'Ready'
  return 'Incomplete'
}

function hasFieldError(
  errors: ChannelEditorErrorMap,
  fieldNames: Array<keyof ChannelFormValues>
): boolean {
  return fieldNames.some((fieldName) => Boolean(errors[fieldName]))
}

export function hasAdvancedChannelEditorValues(
  values: ChannelFormValues
): boolean {
  return Boolean(
    hasConfiguredOverrideValue(values.param_override) ||
    hasConfiguredOverrideValue(values.header_override) ||
    values.advanced_custom?.trim() ||
    hasConfiguredOverrideValue(values.status_code_mapping) ||
    values.tag?.trim() ||
    values.remark?.trim() ||
    values.priority ||
    values.weight ||
    values.proxy?.trim() ||
    values.system_prompt?.trim() ||
    values.force_format ||
    values.thinking_to_content ||
    values.pass_through_body_enabled ||
    values.disable_task_polling_sleep ||
    values.system_prompt_override ||
    values.allow_service_tier ||
    values.disable_store ||
    values.allow_safety_identifier ||
    values.allow_include_obfuscation ||
    values.allow_inference_geo ||
    values.allow_speed ||
    values.claude_beta_query ||
    values.upstream_model_update_check_enabled ||
    values.upstream_model_update_auto_sync_enabled ||
    values.upstream_model_update_ignored_models?.trim()
  )
}

export function buildChannelEditorNavigationState({
  values,
  errors = {},
  isEditing,
}: {
  values: ChannelFormValues
  errors?: ChannelEditorErrorMap
  isEditing: boolean
}) {
  const currentModelsArray = parseModelsString(values.models)
  const providerRequiresBaseUrl = [3, 8, 36, 45].includes(values.type)
  const providerRequiresOther = [3, 18, 21, 39, 41, 49].includes(values.type)

  const identityHasErrors = hasFieldError(errors, [
    'name',
    'type',
    'status',
    'openai_organization',
  ])
  const credentialsHaveErrors = hasFieldError(errors, [
    'key',
    'base_url',
    'other',
    'multi_key_mode',
    'multi_key_type',
    'key_mode',
    'vertex_key_type',
    'aws_key_type',
    'azure_responses_version',
  ])
  const modelsHaveErrors = hasFieldError(errors, [
    'models',
    'group',
    'model_mapping',
  ])
  const advancedHaveErrors =
    hasAdvancedSettingsErrors(errors) || Boolean(errors.advanced_custom)

  const identityComplete = Boolean(values.name?.trim() && values.type > 0)
  const credentialsComplete = Boolean(
    (isEditing || values.key?.trim()) &&
    (!providerRequiresBaseUrl || values.base_url?.trim()) &&
    (!providerRequiresOther || values.other?.trim())
  )
  const modelsComplete = Boolean(
    currentModelsArray.length > 0 && values.group?.length
  )
  const requiredCompletedCount = [
    identityComplete,
    credentialsComplete,
    modelsComplete,
  ].filter(Boolean).length

  const identityStatus = getCompletionStatus(
    identityHasErrors,
    identityComplete
  )
  const credentialsStatus = getCompletionStatus(
    credentialsHaveErrors,
    credentialsComplete
  )
  const modelsStatus = getCompletionStatus(modelsHaveErrors, modelsComplete)
  const advancedStatus: ChannelEditorSectionStatus = advancedHaveErrors
    ? 'error'
    : 'idle'

  const routingStrategyConfigured = Boolean(
    values.priority ||
    values.weight ||
    values.test_model?.trim() ||
    (values.auto_ban ?? 1) !== 1
  )
  const internalNotesConfigured = Boolean(
    values.tag?.trim() || values.remark?.trim()
  )
  const overrideRulesConfigured = Boolean(
    hasConfiguredOverrideValue(values.status_code_mapping) ||
    hasConfiguredOverrideValue(values.param_override) ||
    hasConfiguredOverrideValue(values.header_override)
  )
  const extraSettingsConfigured = Boolean(
    values.force_format ||
    values.thinking_to_content ||
    values.pass_through_body_enabled ||
    values.disable_task_polling_sleep ||
    values.proxy?.trim() ||
    values.system_prompt?.trim() ||
    values.system_prompt_override
  )

  let fieldPassthroughConfigured = false
  if (values.type === 1 || values.type === 57) {
    fieldPassthroughConfigured = Boolean(
      values.allow_service_tier ||
      values.disable_store ||
      values.allow_safety_identifier ||
      values.allow_include_obfuscation ||
      values.allow_inference_geo
    )
  } else if (values.type === 14) {
    fieldPassthroughConfigured = Boolean(
      values.allow_service_tier ||
      values.allow_inference_geo ||
      values.allow_speed ||
      values.claude_beta_query
    )
  }

  const upstreamModelDetectionConfigured = Boolean(
    values.upstream_model_update_check_enabled ||
    values.upstream_model_update_auto_sync_enabled ||
    values.upstream_model_update_ignored_models?.trim()
  )
  const advancedConfigured = Boolean(
    routingStrategyConfigured ||
    internalNotesConfigured ||
    overrideRulesConfigured ||
    extraSettingsConfigured ||
    fieldPassthroughConfigured ||
    upstreamModelDetectionConfigured
  )

  const advancedChildren: ChannelEditorNavChildItemState[] = [
    {
      id: ADVANCED_SETTINGS_SECTION_IDS.routingStrategy,
      titleKey: 'Routing Strategy',
      configured: routingStrategyConfigured,
    },
    {
      id: ADVANCED_SETTINGS_SECTION_IDS.internalNotes,
      titleKey: 'Internal Notes',
      configured: internalNotesConfigured,
    },
    {
      id: ADVANCED_SETTINGS_SECTION_IDS.overrideRules,
      titleKey: 'Override Rules',
      configured: overrideRulesConfigured,
    },
    {
      id: ADVANCED_SETTINGS_SECTION_IDS.extraSettings,
      titleKey: 'Channel Extra Settings',
      configured: extraSettingsConfigured,
    },
  ]

  if (values.type === 1 || values.type === 14 || values.type === 57) {
    advancedChildren.push({
      id: ADVANCED_SETTINGS_SECTION_IDS.fieldPassthrough,
      titleKey: 'Field passthrough controls',
      configured: fieldPassthroughConfigured,
    })
  }
  if (MODEL_FETCHABLE_TYPES.has(values.type)) {
    advancedChildren.push({
      id: ADVANCED_SETTINGS_SECTION_IDS.upstreamModelDetection,
      titleKey: 'Upstream Model Detection Settings',
      configured: upstreamModelDetectionConfigured,
    })
  }

  const items: ChannelEditorNavItemState[] = [
    {
      id: CHANNEL_EDITOR_SECTION_IDS.identity,
      titleKey: 'Basic Information',
      statusLabelKey: getStatusLabelKey(identityStatus),
      status: identityStatus,
    },
    {
      id: CHANNEL_EDITOR_SECTION_IDS.credentials,
      titleKey: 'Credentials',
      statusLabelKey: getStatusLabelKey(credentialsStatus),
      status: credentialsStatus,
    },
    {
      id: CHANNEL_EDITOR_SECTION_IDS.models,
      titleKey: 'Models & Groups',
      statusLabelKey: getStatusLabelKey(modelsStatus),
      status: modelsStatus,
    },
    {
      id: CHANNEL_EDITOR_SECTION_IDS.advanced,
      titleKey: 'Advanced Settings',
      statusLabelKey: advancedHaveErrors ? 'Error' : 'Advanced Settings',
      status: advancedStatus,
      configured: advancedConfigured,
      children: advancedChildren,
    },
  ]

  return {
    items,
    progressLabel: `${requiredCompletedCount}/3`,
    requiredCompletedCount,
    advancedConfigured,
  }
}
