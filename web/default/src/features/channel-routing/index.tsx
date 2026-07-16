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
import { getRouteApi } from '@tanstack/react-router'
import { lazy, Suspense } from 'react'

import { ChannelRoutingLoadingState } from './components/page-state'
import type { ChannelRoutingSection } from './types'

const route = getRouteApi('/_authenticated/channel-routing/$section')

const OverviewPage = lazy(() =>
  import('./overview').then((module) => ({
    default: module.ChannelRoutingOverviewPage,
  }))
)
const GroupsPage = lazy(() =>
  import('./groups').then((module) => ({
    default: module.ChannelRoutingGroupsPage,
  }))
)
const ChannelsPage = lazy(() =>
  import('./channels').then((module) => ({
    default: module.ChannelRoutingChannelsPage,
  }))
)
const DecisionsPage = lazy(() =>
  import('./decisions').then((module) => ({
    default: module.ChannelRoutingDecisionsPage,
  }))
)
const CostsPage = lazy(() =>
  import('./costs').then((module) => ({
    default: module.ChannelRoutingCostsPage,
  }))
)
const PoliciesPage = lazy(() =>
  import('./policies').then((module) => ({
    default: module.ChannelRoutingPoliciesPage,
  }))
)

const pages: Record<ChannelRoutingSection, React.ComponentType> = {
  overview: OverviewPage,
  groups: GroupsPage,
  channels: ChannelsPage,
  decisions: DecisionsPage,
  costs: CostsPage,
  policies: PoliciesPage,
}

export function ChannelRoutingSectionPage() {
  const params = route.useParams()
  const Page = pages[params.section as ChannelRoutingSection]

  return (
    <Suspense fallback={<ChannelRoutingLoadingState rows={8} />}>
      <Page />
    </Suspense>
  )
}
