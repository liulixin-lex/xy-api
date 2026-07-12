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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlignLeft, FilePlus2, History, Save } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
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
import { Textarea } from '@/components/ui/textarea'

import {
  createChannelRoutingPolicyDraft,
  getChannelRoutingPolicyDraft,
  updateChannelRoutingPolicyDraft,
} from '../api/client'
import { channelRoutingQueryKeys } from '../api/query-keys'
import {
  ChannelRoutingErrorState,
  ChannelRoutingLoadingState,
} from '../components/page-state'
import { ChannelRoutingStatusBadge } from '../components/status-badge'
import { useChannelRoutingFormatters } from '../lib/format'
import {
  analyzePolicyDocument,
  formatPolicyDocumentPath,
  formatPolicyDocumentText,
  policyDocumentPositionAtOffset,
  policyDocumentText,
  starterPolicyDocumentText,
  type PolicyDocumentIssue,
} from '../lib/policy-document'
import type { PolicyDocument, PolicyDraftSummary } from '../types'

type PolicyDraftFormValues = {
  baseRevision: number
  document: string
}

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

function distributionLabel(
  values: Record<string, number>,
  translate: (key: string) => string
): string {
  const labels: Record<string, string> = {
    active: 'Active',
    balanced: 'Balanced',
    canary: 'Canary',
    cost_aware: 'Cost aware',
    custom: 'Custom',
    enterprise_slo: 'Enterprise SLO',
    observe: 'Observe',
    reliability_first: 'Reliability first',
    shadow: 'Shadow',
  }
  const entries = Object.entries(values).sort(([left], [right]) =>
    left.localeCompare(right)
  )
  if (entries.length === 0) return translate('None')
  return entries
    .map(([value, count]) => `${translate(labels[value] ?? value)} ${count}`)
    .join(' · ')
}

export function ChannelRoutingPolicyDraftSheet(props: {
  draft: PolicyDraftSummary | null
  baseRevision: number
  currentDocument?: PolicyDocument
  canWrite: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const format = useChannelRoutingFormatters()
  const queryClient = useQueryClient()
  const editorRef = useRef<HTMLTextAreaElement | null>(null)
  const [cursor, setCursor] = useState({ line: 1, column: 1 })
  const isEditing = props.draft != null
  const detailQuery = useQuery({
    queryKey: channelRoutingQueryKeys.policyDraft(props.draft?.id ?? 0),
    queryFn: () => getChannelRoutingPolicyDraft(props.draft?.id ?? 0),
    enabled: props.open && isEditing,
  })
  const schema = useMemo(
    () =>
      z.object({
        baseRevision: z
          .number({ error: t('Base revision is required') })
          .int(t('Base revision must be an integer'))
          .min(0, t('Base revision cannot be negative')),
        document: z
          .string()
          .min(2, t('Policy document is required'))
          .superRefine((value, context) => {
            if (!analyzePolicyDocument(value).valid) {
              context.addIssue({
                code: 'custom',
                message: t('Review the policy document issues below.'),
              })
            }
          }),
      }),
    [t]
  )
  const form = useForm<PolicyDraftFormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      baseRevision: props.baseRevision,
      document: props.currentDocument
        ? policyDocumentText(props.currentDocument)
        : starterPolicyDocumentText(),
    },
  })
  const documentValue =
    useWatch({ control: form.control, name: 'document' }) ?? ''
  const analysis = useMemo(
    () => analyzePolicyDocument(documentValue),
    [documentValue]
  )
  const saveDraft = useMutation({
    mutationFn: async (values: PolicyDraftFormValues) => {
      const analyzed = analyzePolicyDocument(values.document)
      if (!analyzed.document) {
        throw new Error('invalid channel routing policy document')
      }
      if (props.draft) {
        return updateChannelRoutingPolicyDraft(props.draft, analyzed.document)
      }
      return createChannelRoutingPolicyDraft({
        base_revision: values.baseRevision,
        document: analyzed.document,
      })
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.all,
      })
      const message = isEditing
        ? t('Policy draft updated')
        : t('Policy draft created')
      toast.success(message)
      props.onOpenChange(false)
    },
  })
  const resetSaveDraft = saveDraft.reset

  useEffect(() => {
    if (!props.open) return
    if (!props.draft) {
      form.reset({
        baseRevision: props.baseRevision,
        document: props.currentDocument
          ? policyDocumentText(props.currentDocument)
          : starterPolicyDocumentText(),
      })
      setCursor({ line: 1, column: 1 })
      resetSaveDraft()
      return
    }
    if (detailQuery.data) {
      form.reset({
        baseRevision: detailQuery.data.base_revision,
        document: policyDocumentText(detailQuery.data.document),
      })
      setCursor({ line: 1, column: 1 })
      resetSaveDraft()
    }
  }, [
    detailQuery.data,
    form,
    props.baseRevision,
    props.currentDocument,
    props.draft,
    props.open,
    resetSaveDraft,
  ])

  const immutable = props.draft?.status === 'published'
  const writable = props.canWrite && !immutable
  const replaceDocument = (value: string) => {
    form.setValue('document', value, {
      shouldDirty: true,
      shouldTouch: true,
      shouldValidate: true,
    })
    setCursor({ line: 1, column: 1 })
    window.requestAnimationFrame(() => editorRef.current?.focus())
  }
  const focusIssue = (issue: PolicyDocumentIssue) => {
    const editor = editorRef.current
    if (!editor) return
    editor.focus()
    const valueLength = editor.value.length
    const start = Math.max(0, Math.min(valueLength, issue.offset))
    const end = Math.max(start, Math.min(valueLength, start + issue.length))
    editor.setSelectionRange(start, end)
    const lineHeight = Number.parseFloat(
      window.getComputedStyle(editor).lineHeight
    )
    if (Number.isFinite(lineHeight)) {
      editor.scrollTop = Math.max(0, (issue.line - 3) * lineHeight)
    }
    setCursor({ line: issue.line, column: issue.column })
  }
  const formatDocument = () => {
    const formatted = formatPolicyDocumentText(form.getValues('document'))
    if (!formatted) {
      void form.trigger('document')
      const firstIssue = analyzePolicyDocument(form.getValues('document'))
        .issues[0]
      if (firstIssue) focusIssue(firstIssue)
      return
    }
    replaceDocument(formatted)
  }
  const updateCursor = (editor: HTMLTextAreaElement) => {
    setCursor(
      policyDocumentPositionAtOffset(editor.value, editor.selectionStart)
    )
  }
  const summaryItems = [
    [t('Pools'), format.number(analysis.summary.poolCount)],
    [t('Members'), format.number(analysis.summary.memberCount)],
    [t('Enabled members'), format.number(analysis.summary.enabledMemberCount)],
    [t('Stages'), distributionLabel(analysis.summary.stages, t)],
    [t('Profiles'), distributionLabel(analysis.summary.profiles, t)],
    [
      t('Document size'),
      t('{{count}} bytes', { count: format.number(analysis.summary.bytes) }),
    ],
  ]
  const hasSyntaxIssues = analysis.issues.some(
    (issue) => issue.kind === 'syntax'
  )
  const currentDocument = props.currentDocument

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent
        className={sideDrawerContentClassName(
          'max-w-none max-lg:[&_button]:min-h-11 max-lg:[&_button]:min-w-11 sm:!max-w-4xl'
        )}
      >
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle>
            {isEditing
              ? t('Policy draft #{{id}}', { id: props.draft?.id })
              : t('Create policy draft')}
          </SheetTitle>
          <SheetDescription>
            {isEditing
              ? t('Revision {{version}} · {{status}}', {
                  version: props.draft?.version,
                  status: props.draft?.status,
                })
              : t(
                  'Create an immutable policy change candidate from a base revision.'
                )}
          </SheetDescription>
        </SheetHeader>

        {detailQuery.isLoading ? (
          <div className='px-4'>
            <ChannelRoutingLoadingState rows={6} />
          </div>
        ) : null}
        {detailQuery.isError ? (
          <div className='px-4'>
            <ChannelRoutingErrorState
              error={detailQuery.error}
              onRetry={() => void detailQuery.refetch()}
            />
          </div>
        ) : null}
        {!detailQuery.isLoading && !detailQuery.isError ? (
          <Form {...form}>
            <form
              id='channel-routing-policy-draft-form'
              className={sideDrawerFormClassName('gap-5')}
              onSubmit={form.handleSubmit((values) => saveDraft.mutate(values))}
            >
              <FormField
                control={form.control}
                name='baseRevision'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Base revision')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0}
                        disabled={isEditing || !writable}
                        value={field.value}
                        onChange={(event) =>
                          field.onChange(event.target.valueAsNumber)
                        }
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='document'
                render={({ field }) => (
                  <FormItem className='min-h-0 flex-1'>
                    <div className='flex flex-wrap items-center justify-between gap-2'>
                      <div className='flex items-center gap-2'>
                        <FormLabel>{t('Policy document')}</FormLabel>
                        <ChannelRoutingStatusBadge
                          status={analysis.valid ? 'validated' : 'failed'}
                          label={
                            analysis.valid
                              ? t('Schema valid')
                              : t('{{count}} issues', {
                                  count: analysis.issues.length,
                                })
                          }
                        />
                      </div>
                      {writable ? (
                        <div className='flex flex-wrap gap-1.5'>
                          <Button
                            type='button'
                            size='sm'
                            variant='outline'
                            disabled={hasSyntaxIssues}
                            onClick={formatDocument}
                          >
                            <AlignLeft aria-hidden='true' />
                            {t('Format JSON')}
                          </Button>
                          <Button
                            type='button'
                            size='sm'
                            variant='outline'
                            onClick={() =>
                              replaceDocument(starterPolicyDocumentText())
                            }
                          >
                            <FilePlus2 aria-hidden='true' />
                            {t('Starter template')}
                          </Button>
                          {currentDocument ? (
                            <Button
                              type='button'
                              size='sm'
                              variant='outline'
                              onClick={() =>
                                replaceDocument(
                                  policyDocumentText(currentDocument)
                                )
                              }
                            >
                              <History aria-hidden='true' />
                              {t('Current policy')}
                            </Button>
                          ) : null}
                        </div>
                      ) : null}
                    </div>

                    <dl className='bg-border grid grid-cols-2 gap-px overflow-hidden rounded-lg border lg:grid-cols-3'>
                      {summaryItems.map(([label, value]) => (
                        <div
                          key={String(label)}
                          className='bg-background min-w-0 p-2.5'
                        >
                          <dt className='text-muted-foreground text-xs'>
                            {label}
                          </dt>
                          <dd
                            className='mt-0.5 truncate text-sm font-medium'
                            title={String(value)}
                          >
                            {value}
                          </dd>
                        </div>
                      ))}
                    </dl>

                    <FormControl>
                      <Textarea
                        ref={(node) => {
                          field.ref(node)
                          editorRef.current = node
                        }}
                        className='min-h-96 resize-y font-mono text-xs leading-relaxed'
                        spellCheck={false}
                        readOnly={!writable}
                        name={field.name}
                        value={field.value}
                        onBlur={field.onBlur}
                        onChange={(event) => {
                          field.onChange(event)
                          updateCursor(event.currentTarget)
                        }}
                        onSelect={(event) => updateCursor(event.currentTarget)}
                      />
                    </FormControl>
                    <FormDescription className='flex flex-wrap items-center justify-between gap-2 text-xs'>
                      <span>
                        {t('Line {{line}}, column {{column}}', cursor)}
                      </span>
                      <span>
                        {t('{{count}} bytes', {
                          count: format.number(analysis.summary.bytes),
                        })}
                      </span>
                    </FormDescription>
                    <FormMessage />

                    {analysis.issues.length > 0 ? (
                      <div
                        className='border-destructive/30 bg-destructive/5 overflow-hidden rounded-lg border'
                        role='alert'
                      >
                        <div className='border-destructive/20 flex items-center justify-between gap-3 border-b px-3 py-2'>
                          <span className='text-destructive text-sm font-medium'>
                            {t('Policy document issues')}
                          </span>
                          <span className='text-destructive/80 text-xs tabular-nums'>
                            {analysis.issues.length}
                          </span>
                        </div>
                        <ul className='max-h-48 divide-y overflow-auto'>
                          {analysis.issues.map((issue) => (
                            <li
                              key={`${issue.kind}-${issue.code}-${issue.offset}-${issue.diagnosticId}`}
                            >
                              <button
                                type='button'
                                className='hover:bg-destructive/5 focus-visible:ring-ring flex w-full min-w-0 items-start gap-3 px-3 py-2 text-left outline-none focus-visible:ring-2 focus-visible:ring-inset'
                                onClick={() => focusIssue(issue)}
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
                        {analysis.issuesTruncated ? (
                          <p className='text-destructive border-destructive/20 border-t px-3 py-2 text-xs'>
                            {t(
                              'Additional issues are hidden until the listed errors are fixed.'
                            )}
                          </p>
                        ) : null}
                      </div>
                    ) : null}
                  </FormItem>
                )}
              />

              {saveDraft.isError ? (
                <div className='border-destructive/30 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm'>
                  {t(
                    'Could not save the policy draft. Refresh it if another operator changed the revision.'
                  )}
                </div>
              ) : null}
            </form>
          </Form>
        ) : null}

        {writable && !detailQuery.isLoading && !detailQuery.isError ? (
          <SheetFooter className={sideDrawerFooterClassName()}>
            <Button
              type='submit'
              form='channel-routing-policy-draft-form'
              disabled={saveDraft.isPending || !analysis.valid}
            >
              <Save aria-hidden='true' />
              {saveDraft.isPending ? t('Saving') : t('Save draft')}
            </Button>
          </SheetFooter>
        ) : null}
      </SheetContent>
    </Sheet>
  )
}
