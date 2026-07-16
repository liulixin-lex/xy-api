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
import { useBlocker } from '@tanstack/react-router'
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'

export function RuntimeSettingsNavigationGuard(props: { when: boolean }) {
  const { t } = useTranslation()
  const blocker = useBlocker({ condition: props.when })

  useEffect(() => {
    if (!props.when) return
    const handleBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', handleBeforeUnload)
    return () => window.removeEventListener('beforeunload', handleBeforeUnload)
  }, [props.when])

  return (
    <ConfirmDialog
      open={blocker.status === 'blocked'}
      onOpenChange={(nextOpen) => {
        if (!nextOpen) blocker.reset?.()
      }}
      title={t('Unsaved runtime settings')}
      desc={t(
        'Your runtime settings draft has not been saved. Leave and discard it?'
      )}
      confirmText={t('Leave and discard')}
      cancelBtnText={t('Keep editing')}
      destructive
      handleConfirm={() => {
        blocker.proceed?.()
      }}
    />
  )
}
