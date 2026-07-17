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
  CheckmarkCircle02Icon,
  Delete02Icon,
  EyeIcon,
  FlaskConicalIcon,
  RocketIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import type { PolicyDraftSummary } from '../types'

export function ChannelRoutingPolicyDraftActions(props: {
  draft: PolicyDraftSummary
  canWrite: boolean
  canDeploy: boolean
  validating: boolean
  deleting: boolean
  mutationsDisabled?: boolean
  onValidate: () => void
  onSimulate: () => void
  onPublish: () => void
  onDelete: () => void
  onView: () => void
}) {
  const { t } = useTranslation()
  const simulationAvailable =
    props.draft.workspace_state === 'working' &&
    props.draft.status === 'validated'

  return (
    <div className='flex flex-wrap items-center justify-end gap-1'>
      {props.canWrite && props.draft.can_validate ? (
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                size='icon-sm'
                variant='ghost'
                aria-label={t('Validate draft')}
                disabled={props.mutationsDisabled || props.validating}
                onClick={props.onValidate}
              />
            }
          >
            <HugeiconsIcon
              icon={CheckmarkCircle02Icon}
              data-icon='inline-start'
              strokeWidth={2}
              aria-hidden='true'
            />
          </TooltipTrigger>
          <TooltipContent>{t('Validate draft')}</TooltipContent>
        </Tooltip>
      ) : null}
      {props.canWrite && simulationAvailable ? (
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                size='icon-sm'
                variant='ghost'
                aria-label={t('Simulate policy')}
                disabled={props.mutationsDisabled}
                onClick={props.onSimulate}
              />
            }
          >
            <HugeiconsIcon
              icon={FlaskConicalIcon}
              data-icon='inline-start'
              strokeWidth={2}
              aria-hidden='true'
            />
          </TooltipTrigger>
          <TooltipContent>{t('Simulate policy')}</TooltipContent>
        </Tooltip>
      ) : null}
      {props.canDeploy && props.draft.can_publish ? (
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                size='icon-sm'
                variant='ghost'
                aria-label={t('Publish policy')}
                disabled={props.mutationsDisabled}
                onClick={props.onPublish}
              />
            }
          >
            <HugeiconsIcon
              icon={RocketIcon}
              data-icon='inline-start'
              strokeWidth={2}
              aria-hidden='true'
            />
          </TooltipTrigger>
          <TooltipContent>{t('Publish policy')}</TooltipContent>
        </Tooltip>
      ) : null}
      {props.canWrite && props.draft.can_delete ? (
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                size='icon-sm'
                variant='ghost'
                aria-label={t('Permanently delete draft')}
                disabled={props.mutationsDisabled || props.deleting}
                onClick={props.onDelete}
              />
            }
          >
            <HugeiconsIcon
              icon={Delete02Icon}
              data-icon='inline-start'
              strokeWidth={2}
              aria-hidden='true'
            />
          </TooltipTrigger>
          <TooltipContent>{t('Permanently delete draft')}</TooltipContent>
        </Tooltip>
      ) : null}
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              size='icon-sm'
              variant='ghost'
              aria-label={t('View policy draft')}
              onClick={props.onView}
            />
          }
        >
          <HugeiconsIcon
            icon={EyeIcon}
            data-icon='inline-start'
            strokeWidth={2}
            aria-hidden='true'
          />
        </TooltipTrigger>
        <TooltipContent>{t('View policy draft')}</TooltipContent>
      </Tooltip>
    </div>
  )
}
