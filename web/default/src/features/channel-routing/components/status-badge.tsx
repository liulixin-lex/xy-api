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
  AlertCircle,
  CheckCircle2,
  CircleDashed,
  Clock3,
  XCircle,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

type ChannelRoutingStatusBadgeProps = {
  status: string | number
  label?: string
  className?: string
}

function statusPresentation(status: string | number) {
  const normalized = String(status).trim().toLowerCase()
  if (
    normalized === 'active' ||
    normalized === 'converged' ||
    normalized === 'passed' ||
    normalized === 'success' ||
    normalized === 'succeeded' ||
    normalized === 'enabled' ||
    normalized === '1' ||
    normalized === 'known' ||
    normalized === 'validated' ||
    normalized === 'published' ||
    normalized === 'ready' ||
    normalized === 'healthy' ||
    normalized === 'closed' ||
    normalized === 'exact' ||
    normalized === 'full' ||
    normalized === 'fresh' ||
    normalized === 'completed'
  ) {
    return {
      icon: CheckCircle2,
      className: 'border-success/40 bg-success/15 text-foreground',
    }
  }
  if (
    normalized === 'warning' ||
    normalized === 'partial' ||
    normalized === 'degraded' ||
    normalized === 'derived' ||
    normalized === 'group_only'
  ) {
    return {
      icon: AlertCircle,
      className: 'border-warning/40 bg-warning/15 text-foreground',
    }
  }
  if (
    normalized === 'canary' ||
    normalized === 'shadow' ||
    normalized === 'observe' ||
    normalized === 'lagging' ||
    normalized === 'ahead' ||
    normalized === 'stale' ||
    normalized === 'timeout' ||
    normalized === 'editing' ||
    normalized === 'pending' ||
    normalized === 'running' ||
    normalized === 'half_open'
  ) {
    return {
      icon: Clock3,
      className: 'border-warning/40 bg-warning/15 text-foreground',
    }
  }
  if (
    normalized === 'conflict' ||
    normalized === 'failed' ||
    normalized === 'failure' ||
    normalized === 'open' ||
    normalized === 'disabled' ||
    normalized === '2' ||
    normalized === '3' ||
    normalized === 'breached' ||
    normalized === 'local_error' ||
    normalized === 'critical' ||
    normalized === 'expired'
  ) {
    return {
      icon: XCircle,
      className: 'border-destructive/40 bg-destructive/10 text-foreground',
    }
  }
  if (
    normalized === 'initializing' ||
    normalized === 'inconclusive' ||
    normalized === 'unavailable' ||
    normalized === 'insufficient_data'
  ) {
    return {
      icon: CircleDashed,
      className: 'border-info/40 bg-info/15 text-foreground',
    }
  }
  return {
    icon: AlertCircle,
    className: 'border-border bg-muted/50 text-muted-foreground',
  }
}

function statusLabel(
  status: string | number,
  translate: (key: string) => string
) {
  const normalized = String(status).trim().toLowerCase()
  const labels: Record<string, string> = {
    active: 'Active',
    ahead: 'Ahead',
    breached: 'Breached',
    canary: 'Canary',
    conflict: 'Conflict',
    converged: 'Converged',
    critical: 'Critical',
    completed: 'Completed',
    degraded: 'Degraded',
    derived: 'Derived',
    disabled: 'Disabled',
    editing: 'Editing',
    enabled: 'Enabled',
    exact: 'Exact',
    expired: 'Expired',
    failed: 'Failed',
    failure: 'Failed',
    fresh: 'Fresh',
    full: 'Full',
    group_only: 'Group only',
    healthy: 'Healthy',
    closed: 'Closed',
    half_open: 'Half-open',
    inconclusive: 'Inconclusive',
    initializing: 'Initializing',
    insufficient_data: 'Insufficient data',
    known: 'Known',
    lagging: 'Lagging',
    local_error: 'Local error',
    observe: 'Observe',
    open: 'Open',
    passed: 'Passed',
    partial: 'Partial',
    pending: 'Pending',
    published: 'Published',
    ready: 'Ready',
    running: 'Running',
    shadow: 'Shadow',
    stale: 'Stale',
    succeeded: 'Succeeded',
    success: 'Success',
    superseded: 'Superseded',
    timeout: 'Timeout',
    unavailable: 'Unavailable',
    unknown: 'Unknown',
    validated: 'Validated',
    warning: 'Warning',
  }
  if (normalized === '1') return translate('Enabled')
  if (normalized === '2' || normalized === '3') return translate('Disabled')
  return translate(labels[normalized] ?? String(status))
}

export function ChannelRoutingStatusBadge(
  props: ChannelRoutingStatusBadgeProps
) {
  const { t } = useTranslation()
  const presentation = statusPresentation(props.status)
  const Icon = presentation.icon

  return (
    <Badge
      variant='outline'
      className={cn(presentation.className, props.className)}
    >
      <Icon aria-hidden='true' />
      {props.label ?? statusLabel(props.status, t)}
    </Badge>
  )
}
