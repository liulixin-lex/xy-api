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
  BadgeCheck,
  CheckCircle2,
  Eye,
  FlaskConical,
  Rocket,
} from 'lucide-react'
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
  mutationsDisabled?: boolean
  onValidate: () => void
  onSimulate: () => void
  onApprove: () => void
  onPublish: () => void
  onView: () => void
}) {
  const { t } = useTranslation()
  const simulationAvailable =
    props.draft.status === 'validated' || props.draft.status === 'published'

  return (
    <div className='flex flex-wrap items-center justify-end gap-1'>
      {props.canWrite && props.draft.status === 'editing' ? (
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
            <CheckCircle2 aria-hidden='true' />
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
            <FlaskConical aria-hidden='true' />
          </TooltipTrigger>
          <TooltipContent>{t('Simulate policy')}</TooltipContent>
        </Tooltip>
      ) : null}
      {props.canDeploy && props.draft.status === 'validated' ? (
        <>
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  size='icon-sm'
                  variant='ghost'
                  aria-label={t('Approve deployment')}
                  disabled={props.mutationsDisabled}
                  onClick={props.onApprove}
                />
              }
            >
              <BadgeCheck aria-hidden='true' />
            </TooltipTrigger>
            <TooltipContent>{t('Approve deployment')}</TooltipContent>
          </Tooltip>
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
              <Rocket aria-hidden='true' />
            </TooltipTrigger>
            <TooltipContent>{t('Publish policy')}</TooltipContent>
          </Tooltip>
        </>
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
          <Eye aria-hidden='true' />
        </TooltipTrigger>
        <TooltipContent>{t('View policy draft')}</TooltipContent>
      </Tooltip>
    </div>
  )
}
