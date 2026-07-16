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
  FileAddIcon,
  HistoryIcon,
  SourceCodeIcon,
  TextAlignLeftIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import type {
  ChangeEventHandler,
  FocusEventHandler,
  MutableRefObject,
  ReactEventHandler,
} from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { FormControl, FormDescription } from '@/components/ui/form'
import { Textarea } from '@/components/ui/textarea'

import { useChannelRoutingFormatters } from '../lib/format'
import {
  formatPolicyDocumentPath,
  type PolicyDocumentIssue,
} from '../lib/policy-document'

function syntaxIssueDetail(
  detail: string | undefined,
  translate: (key: string) => string
): string {
  switch (detail) {
    case 'PropertyNameExpected':
      return translate('A property name is required.')
    case 'ValueExpected':
      return translate('A JSON value is required.')
    case 'ColonExpected':
      return translate('A colon is required after the property name.')
    case 'CommaExpected':
      return translate('A comma is required between values.')
    case 'CloseBraceExpected':
      return translate('A closing brace is required.')
    case 'CloseBracketExpected':
      return translate('A closing bracket is required.')
    case 'EndOfFileExpected':
      return translate('Unexpected content follows the JSON document.')
    case 'InvalidCommentToken':
      return translate('Comments are not allowed in policy JSON.')
    default:
      return translate('The JSON token is invalid or incomplete.')
  }
}

function policyIssueMessage(
  issue: PolicyDocumentIssue,
  translate: (key: string, options?: Record<string, unknown>) => string
): string {
  switch (issue.code) {
    case 'json_syntax':
      return translate('JSON syntax error: {{detail}}', {
        detail: syntaxIssueDetail(issue.detail, translate),
      })
    case 'expected_object':
      return translate('Must be a JSON object.')
    case 'expected_array':
      return translate('Must be a JSON array.')
    case 'expected_boolean':
      return translate('Must be true or false.')
    case 'expected_integer':
      return translate('Must be a safe integer.')
    case 'expected_nonnegative_integer':
      return translate('Must be a non-negative safe integer.')
    case 'expected_positive_integer':
      return translate('Must be a positive safe integer.')
    case 'required_string':
      return translate('Must be a string and cannot be missing.')
    case 'string_too_long':
      return translate('Must be {{limit}} characters or fewer.', {
        limit: issue.limit,
      })
    case 'unsupported_schema_version':
      return translate('Only policy schema version 1 is supported.')
    case 'invalid_deployment_stage':
      return translate('Use observe, shadow, canary, or active.')
    case 'invalid_policy_profile':
      return translate(
        'Use balanced, reliability_first, cost_aware, enterprise_slo, or custom.'
      )
    case 'duplicate_value':
      return translate('This value must be unique in its policy scope.')
    case 'too_many_items':
      return translate('Contains more than {{limit}} allowed items.', {
        limit: issue.limit,
      })
    case 'document_too_large':
      return translate(
        'The policy document exceeds the {{limit}} byte limit.',
        {
          limit: issue.limit,
        }
      )
  }
}

export function PolicyJsonEditorToolbar(props: {
  hasSyntaxIssues: boolean
  hasCurrentDocument: boolean
  onFormat: () => void
  onUseStarterTemplate: () => void
  onUseCurrentPolicy: () => void
}) {
  const { t } = useTranslation()

  return (
    <div className='flex flex-wrap gap-1.5'>
      <Button
        type='button'
        size='sm'
        variant='outline'
        disabled={props.hasSyntaxIssues}
        onClick={props.onFormat}
      >
        <HugeiconsIcon
          icon={TextAlignLeftIcon}
          data-icon='inline-start'
          strokeWidth={2}
          aria-hidden='true'
        />
        {t('Format JSON')}
      </Button>
      <Button
        type='button'
        size='sm'
        variant='outline'
        onClick={props.onUseStarterTemplate}
      >
        <HugeiconsIcon
          icon={FileAddIcon}
          data-icon='inline-start'
          strokeWidth={2}
          aria-hidden='true'
        />
        {t('Starter template')}
      </Button>
      {props.hasCurrentDocument ? (
        <Button
          type='button'
          size='sm'
          variant='outline'
          onClick={props.onUseCurrentPolicy}
        >
          <HugeiconsIcon
            icon={HistoryIcon}
            data-icon='inline-start'
            strokeWidth={2}
            aria-hidden='true'
          />
          {t('Current policy')}
        </Button>
      ) : null}
    </div>
  )
}

export function PolicyJsonEditor(props: {
  value: string
  name: string
  readOnly: boolean
  visualSwitchBlocked: boolean
  cursor: { line: number; column: number }
  bytes: number
  issues: PolicyDocumentIssue[]
  issuesTruncated: boolean
  editorRef: MutableRefObject<HTMLTextAreaElement | null>
  fieldRef: (node: HTMLTextAreaElement | null) => void
  onBlur: FocusEventHandler<HTMLTextAreaElement>
  onChange: ChangeEventHandler<HTMLTextAreaElement>
  onSelect: ReactEventHandler<HTMLTextAreaElement>
  onFocusIssue: (issue: PolicyDocumentIssue) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()

  return (
    <div className='space-y-3'>
      {props.visualSwitchBlocked ? (
        <Alert role='alert' className='border-destructive/30 bg-destructive/5'>
          <HugeiconsIcon
            icon={SourceCodeIcon}
            strokeWidth={2}
            aria-hidden='true'
          />
          <AlertTitle>{t('Visual editor is not available yet')}</AlertTitle>
          <AlertDescription>
            {t(
              'Fix the policy JSON issues below before returning to the visual editor.'
            )}
          </AlertDescription>
        </Alert>
      ) : null}

      <FormControl>
        <Textarea
          ref={(node) => {
            props.fieldRef(node)
            props.editorRef.current = node
          }}
          className='min-h-96 resize-y font-mono text-xs leading-relaxed'
          spellCheck={false}
          readOnly={props.readOnly}
          name={props.name}
          value={props.value}
          onBlur={props.onBlur}
          onChange={props.onChange}
          onSelect={props.onSelect}
        />
      </FormControl>
      <FormDescription className='flex flex-wrap items-center justify-between gap-2 text-xs'>
        <span>{t('Line {{line}}, column {{column}}', props.cursor)}</span>
        <span>
          {t('{{count}} bytes', { count: format.number(props.bytes) })}
        </span>
      </FormDescription>

      {props.issues.length > 0 ? (
        <div
          className='border-destructive/30 bg-destructive/5 overflow-hidden rounded-lg border'
          role='alert'
        >
          <div className='border-destructive/20 flex items-center justify-between gap-3 border-b px-3 py-2'>
            <span className='text-destructive text-sm font-medium'>
              {t('Policy document issues')}
            </span>
            <span className='text-destructive/80 text-xs tabular-nums'>
              {props.issues.length}
            </span>
          </div>
          <ul className='max-h-48 divide-y overflow-auto'>
            {props.issues.map((issue) => (
              <li
                key={`${issue.kind}-${issue.code}-${issue.offset}-${issue.diagnosticId}`}
              >
                <button
                  type='button'
                  className='hover:bg-destructive/5 focus-visible:ring-ring flex w-full min-w-0 items-start gap-3 px-3 py-2 text-left outline-none focus-visible:ring-2 focus-visible:ring-inset'
                  onClick={() => props.onFocusIssue(issue)}
                >
                  <span className='text-destructive shrink-0 font-mono text-xs'>
                    {formatPolicyDocumentPath(issue.path)}
                  </span>
                  <span className='min-w-0 flex-1 text-xs'>
                    <span className='block'>
                      {policyIssueMessage(issue, t)}
                    </span>
                    <span className='text-muted-foreground mt-0.5 block'>
                      {t('Line {{line}}, column {{column}}', {
                        line: issue.line,
                        column: issue.column,
                      })}
                    </span>
                  </span>
                </button>
              </li>
            ))}
          </ul>
          {props.issuesTruncated ? (
            <p className='text-destructive border-destructive/20 border-t px-3 py-2 text-xs'>
              {t(
                'Additional issues are hidden until the listed errors are fixed.'
              )}
            </p>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}
