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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

import { ChannelRoutingEmptyState } from '../components/page-state'
import {
  updatePolicyMemberDocument,
  updatePolicyPoolDocument,
  updatePolicyPoolPath,
  updatePolicyPoolProfile,
} from '../lib/policy-visual-editor'
import type {
  PolicyDocument,
  PolicyMemberDocument,
  PolicyPoolDocument,
} from '../types'
import { PolicyMembersEditor } from './policy-visual-editor/members-editor'
import { PolicyPoolNavigation } from './policy-visual-editor/pool-navigation'
import { PolicyPoolSettings } from './policy-visual-editor/pool-settings'
import { PolicyScoringSettings } from './policy-visual-editor/scoring-settings'

export function ChannelRoutingPolicyVisualEditor(props: {
  document: PolicyDocument
  readOnly: boolean
  onChange: (document: PolicyDocument) => void
}) {
  const { t } = useTranslation()
  const [selectedPoolId, setSelectedPoolId] = useState(
    props.document.pools[0]?.pool_id ?? 0
  )
  const selectedPoolIndex = props.document.pools.findIndex(
    (pool) => pool.pool_id === selectedPoolId
  )
  const selectedPool = props.document.pools[selectedPoolIndex]

  useEffect(() => {
    if (selectedPoolIndex >= 0) return
    setSelectedPoolId(props.document.pools[0]?.pool_id ?? 0)
  }, [props.document.pools, selectedPoolIndex])

  const updatePool = (patch: Partial<PolicyPoolDocument>) => {
    if (selectedPoolIndex < 0) return
    props.onChange(
      updatePolicyPoolDocument(props.document, selectedPoolIndex, patch)
    )
  }
  const updatePolicyPath = (path: string[], value: unknown) => {
    if (selectedPoolIndex < 0) return
    props.onChange(
      updatePolicyPoolPath(props.document, selectedPoolIndex, path, value)
    )
  }
  const updateProfile = (profile: PolicyPoolDocument['policy_profile']) => {
    if (selectedPoolIndex < 0) return
    props.onChange(
      updatePolicyPoolProfile(props.document, selectedPoolIndex, profile)
    )
  }
  const updateMember = (
    memberIndex: number,
    patch: Partial<PolicyMemberDocument>
  ) => {
    if (selectedPoolIndex < 0) return
    props.onChange(
      updatePolicyMemberDocument(
        props.document,
        selectedPoolIndex,
        memberIndex,
        patch
      )
    )
  }

  if (!selectedPool) {
    return (
      <ChannelRoutingEmptyState
        title={t('No policy pools')}
        description={t(
          'Use advanced JSON to add the first pool, then return to the visual editor.'
        )}
      />
    )
  }

  return (
    <div className='space-y-4'>
      <Alert role='note'>
        <HugeiconsIcon
          icon={InformationCircleIcon}
          strokeWidth={2}
          aria-hidden='true'
        />
        <AlertTitle>{t('Visual policy editor')}</AlertTitle>
        <AlertDescription>
          {t(
            'Core pool and member fields are editable here. Unknown extension fields are preserved. Use advanced JSON to add or remove pools and members.'
          )}
        </AlertDescription>
      </Alert>

      <div className='grid min-h-0 gap-4 lg:grid-cols-[15rem_minmax(0,1fr)]'>
        <PolicyPoolNavigation
          pools={props.document.pools}
          selectedPoolId={selectedPoolId}
          onSelectPool={setSelectedPoolId}
        />

        <div className='min-w-0 space-y-5'>
          <PolicyPoolSettings
            pool={selectedPool}
            pools={props.document.pools}
            readOnly={props.readOnly}
            onUpdate={updatePool}
            onProfileChange={updateProfile}
          />
          <PolicyScoringSettings
            pool={selectedPool}
            readOnly={props.readOnly}
            onUpdatePath={updatePolicyPath}
          />
          <PolicyMembersEditor
            pool={selectedPool}
            readOnly={props.readOnly}
            onUpdateMember={updateMember}
          />
        </div>
      </div>
    </div>
  )
}
