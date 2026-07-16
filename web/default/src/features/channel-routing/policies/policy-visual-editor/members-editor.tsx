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
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { ChannelRoutingEmptyState } from '../../components/page-state'
import { ChannelRoutingPagination } from '../../components/pagination-bar'
import { ChannelRoutingStatusBadge } from '../../components/status-badge'
import {
  parsePolicyCredentialIds,
  parsePolicyOverrides,
} from '../../lib/policy-visual-editor'
import type { PolicyMemberDocument, PolicyPoolDocument } from '../../types'

export function PolicyMembersEditor(props: {
  pool: PolicyPoolDocument
  readOnly: boolean
  onUpdateMember: (
    memberIndex: number,
    patch: Partial<PolicyMemberDocument>
  ) => void
}) {
  const { t } = useTranslation()
  const [memberPage, setMemberPage] = useState(1)
  const [memberPageSize, setMemberPageSize] = useState(20)
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const memberStart = Math.min(
    (memberPage - 1) * memberPageSize,
    props.pool.members.length
  )
  const visibleMembers = props.pool.members
    .slice(memberStart, memberStart + memberPageSize)
    .map((member, offset) => ({ member, index: memberStart + offset }))

  useEffect(() => {
    setMemberPage(1)
    setFieldErrors({})
  }, [props.pool.pool_id])

  useEffect(() => {
    const totalPages = Math.max(
      1,
      Math.ceil(props.pool.members.length / memberPageSize)
    )
    if (memberPage > totalPages) setMemberPage(totalPages)
  }, [memberPage, memberPageSize, props.pool.members.length])

  const setError = (key: string, message?: string) => {
    setFieldErrors((current) => {
      const next = { ...current }
      if (message) next[key] = message
      else delete next[key]
      return next
    })
  }

  return (
    <section
      className='space-y-3 border-t pt-4'
      aria-labelledby='visual-members-heading'
    >
      <div className='flex flex-wrap items-end justify-between gap-2'>
        <div>
          <h3 id='visual-members-heading' className='text-sm font-semibold'>
            {t('Pool members')}
          </h3>
          <p className='text-muted-foreground mt-0.5 text-xs'>
            {t('{{count}} members', { count: props.pool.members.length })}
          </p>
        </div>
        {props.pool.members.some((member) => member.weight === 0) ? (
          <ChannelRoutingStatusBadge
            status='paused'
            label={t('Zero weight pauses automatic traffic')}
          />
        ) : null}
      </div>

      {visibleMembers.length === 0 ? (
        <ChannelRoutingEmptyState
          title={t('No pool members')}
          description={t('Use advanced JSON to add members to this pool.')}
        />
      ) : (
        <div className='divide-y rounded-lg border'>
          {visibleMembers.map(({ member, index: memberIndex }) => {
            const credentialErrorKey = `member.${member.member_id}.credentials`
            const overrideErrorKey = `member.${member.member_id}.overrides`
            const enabledId = `policy-member-${member.member_id}-enabled`
            return (
              <article key={member.member_id} className='space-y-3 p-3 sm:p-4'>
                <div className='flex flex-wrap items-start justify-between gap-3'>
                  <div className='min-w-0'>
                    <h4 className='text-sm font-semibold'>
                      {t('Member #{{member}}', { member: member.member_id })}
                    </h4>
                    <p className='text-muted-foreground mt-0.5 text-xs'>
                      {t('Channel #{{channel}}', {
                        channel: member.channel_id,
                      })}
                    </p>
                  </div>
                  <div className='flex items-center gap-2'>
                    {member.weight === 0 ? (
                      <ChannelRoutingStatusBadge
                        status='paused'
                        label={t('Paused for automatic traffic')}
                      />
                    ) : null}
                    <Checkbox
                      id={enabledId}
                      checked={member.enabled}
                      disabled={props.readOnly}
                      onCheckedChange={(checked) =>
                        props.onUpdateMember(memberIndex, {
                          enabled: checked === true,
                        })
                      }
                    />
                    <Label htmlFor={enabledId}>{t('Enabled')}</Label>
                  </div>
                </div>

                <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-4'>
                  <div className='space-y-1.5'>
                    <Label htmlFor={`member-${member.member_id}-priority`}>
                      {t('Priority')}
                    </Label>
                    <Input
                      key={`${member.member_id}:priority:${member.priority}`}
                      id={`member-${member.member_id}-priority`}
                      type='number'
                      step={1}
                      defaultValue={member.priority}
                      disabled={props.readOnly}
                      onBlur={(event) => {
                        const value = event.currentTarget.valueAsNumber
                        if (Number.isSafeInteger(value)) {
                          props.onUpdateMember(memberIndex, { priority: value })
                        }
                      }}
                    />
                  </div>
                  <div className='space-y-1.5'>
                    <Label htmlFor={`member-${member.member_id}-weight`}>
                      {t('Weight')}
                    </Label>
                    <Input
                      key={`${member.member_id}:weight:${member.weight}`}
                      id={`member-${member.member_id}-weight`}
                      type='number'
                      min={0}
                      step={1}
                      defaultValue={member.weight}
                      disabled={props.readOnly}
                      onBlur={(event) => {
                        const value = event.currentTarget.valueAsNumber
                        if (Number.isSafeInteger(value) && value >= 0) {
                          props.onUpdateMember(memberIndex, { weight: value })
                        }
                      }}
                    />
                  </div>
                  <div className='space-y-1.5 sm:col-span-2'>
                    <Label htmlFor={`member-${member.member_id}-credentials`}>
                      {t('Credential scope')}
                    </Label>
                    <Input
                      key={`${member.member_id}:credentials:${member.credential_ids.join(',')}`}
                      id={`member-${member.member_id}-credentials`}
                      defaultValue={member.credential_ids.join(', ')}
                      placeholder={t('All active channel credentials')}
                      disabled={props.readOnly}
                      aria-invalid={
                        fieldErrors[credentialErrorKey] ? true : undefined
                      }
                      onBlur={(event) => {
                        const input = event.currentTarget
                        const parsed = parsePolicyCredentialIds(input.value)
                        if (!parsed.ok) {
                          const message = t(
                            'Enter unique positive credential IDs separated by commas.'
                          )
                          input.setCustomValidity(message)
                          setError(credentialErrorKey, message)
                          return
                        }
                        input.setCustomValidity('')
                        setError(credentialErrorKey)
                        props.onUpdateMember(memberIndex, {
                          credential_ids: parsed.value,
                        })
                      }}
                    />
                    {fieldErrors[credentialErrorKey] ? (
                      <p className='text-destructive text-xs' role='alert'>
                        {fieldErrors[credentialErrorKey]}
                      </p>
                    ) : (
                      <p className='text-muted-foreground text-xs'>
                        {t(
                          'Leave empty to use every active credential on the channel.'
                        )}
                      </p>
                    )}
                  </div>
                </div>

                <details>
                  <summary className='text-primary flex min-h-11 cursor-pointer items-center text-xs font-medium'>
                    {t('Member overrides')}
                  </summary>
                  <div className='space-y-1.5 pt-2'>
                    <Label
                      htmlFor={`member-${member.member_id}-overrides`}
                      className='sr-only'
                    >
                      {t('Member overrides')}
                    </Label>
                    <Textarea
                      key={`${member.member_id}:overrides:${JSON.stringify(member.overrides)}`}
                      id={`member-${member.member_id}-overrides`}
                      className='min-h-28 font-mono text-xs'
                      defaultValue={JSON.stringify(member.overrides, null, 2)}
                      readOnly={props.readOnly}
                      aria-invalid={
                        fieldErrors[overrideErrorKey] ? true : undefined
                      }
                      onBlur={(event) => {
                        const input = event.currentTarget
                        const parsed = parsePolicyOverrides(input.value)
                        if (!parsed.ok) {
                          const message = t(
                            'Member overrides must be a JSON object.'
                          )
                          input.setCustomValidity(message)
                          setError(overrideErrorKey, message)
                          return
                        }
                        input.setCustomValidity('')
                        setError(overrideErrorKey)
                        props.onUpdateMember(memberIndex, {
                          overrides: parsed.value,
                        })
                      }}
                    />
                    {fieldErrors[overrideErrorKey] ? (
                      <p className='text-destructive text-xs' role='alert'>
                        {fieldErrors[overrideErrorKey]}
                      </p>
                    ) : (
                      <p className='text-muted-foreground text-xs'>
                        {t(
                          'Unknown override fields are preserved when this object is saved.'
                        )}
                      </p>
                    )}
                  </div>
                </details>
              </article>
            )
          })}
        </div>
      )}

      <ChannelRoutingPagination
        page={memberPage}
        pageSize={memberPageSize}
        total={props.pool.members.length}
        onPageChange={setMemberPage}
        onPageSizeChange={(size) => {
          setMemberPageSize(size)
          setMemberPage(1)
        }}
      />
    </section>
  )
}
