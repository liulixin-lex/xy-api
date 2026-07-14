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

import { useQueryClient, type QueryKey } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { SSE } from 'sse.js'

import { getCommonHeaders } from '@/lib/api'

import { channelRoutingQueryKeys } from '../api/query-keys'
import {
  channelRoutingEventNames,
  getChannelRoutingEventResources,
  getChannelRoutingReadyCursor,
  getChannelRoutingRetryDelayMs,
  inspectChannelRoutingEventSequence,
  parseChannelRoutingEvent,
  type ChannelRoutingEventCursor,
  type ChannelRoutingEventResource,
} from '../lib/events'

export type ChannelRoutingRealtimeStatus = 'connecting' | 'live' | 'polling'

const eventFlushDelayMs = 750
const fallbackPollingIntervalMs = 15_000
const defaultReconnectDelayMs = 3_000

function eventResourceQueryKey(
  resource: Exclude<ChannelRoutingEventResource, 'all'>
): QueryKey {
  switch (resource) {
    case 'overview':
      return channelRoutingQueryKeys.overview()
    case 'nodes':
      return channelRoutingQueryKeys.nodesRoot()
    case 'groups':
      return channelRoutingQueryKeys.groupsRoot()
    case 'channels':
      return channelRoutingQueryKeys.channelsRoot()
    case 'endpoints':
      return channelRoutingQueryKeys.endpointsRoot()
    case 'costs':
      return channelRoutingQueryKeys.costsRoot()
    case 'cost-bindings':
      return channelRoutingQueryKeys.costBindingsRoot()
    case 'probes':
      return channelRoutingQueryKeys.probesRoot()
    case 'decisions':
      return channelRoutingQueryKeys.decisionsRoot()
    case 'policy-drafts':
      return channelRoutingQueryKeys.policyDraftsRoot()
    case 'policies':
      return channelRoutingQueryKeys.policiesRoot()
    case 'operations':
      return channelRoutingQueryKeys.operationsRoot()
    case 'audit-exports':
      return channelRoutingQueryKeys.auditExportsRoot()
  }
}

export function useChannelRoutingEvents(): ChannelRoutingRealtimeStatus {
  const queryClient = useQueryClient()
  const [status, setStatus] =
    useState<ChannelRoutingRealtimeStatus>('connecting')

  useEffect(() => {
    let lastCursor: ChannelRoutingEventCursor | null = null
    let flushTimer: number | undefined
    let pollingTimer: number | undefined
    const pendingResources = new Set<ChannelRoutingEventResource>()
    const source = new SSE('/api/channel-routing/v2/events', {
      headers: getCommonHeaders(),
      withCredentials: true,
      autoReconnect: true,
      reconnectDelay: defaultReconnectDelayMs,
      useLastEventId: true,
      start: false,
    })

    const flush = () => {
      flushTimer = undefined
      const resources = [...pendingResources]
      pendingResources.clear()
      if (resources.includes('all')) {
        void queryClient.invalidateQueries({
          queryKey: channelRoutingQueryKeys.all,
          refetchType: 'active',
        })
        return
      }
      for (const resource of resources) {
        if (resource === 'all') {
          continue
        }
        void queryClient.invalidateQueries({
          queryKey: eventResourceQueryKey(resource),
          refetchType: 'active',
        })
      }
    }

    const enqueue = (resources: ChannelRoutingEventResource[]) => {
      if (resources.includes('all')) {
        pendingResources.clear()
        pendingResources.add('all')
      } else if (!pendingResources.has('all')) {
        for (const resource of resources) pendingResources.add(resource)
      }
      if (pendingResources.size > 0 && flushTimer == null) {
        flushTimer = window.setTimeout(flush, eventFlushDelayMs)
      }
    }

    const stopPolling = () => {
      if (pollingTimer != null) {
        window.clearInterval(pollingTimer)
        pollingTimer = undefined
      }
      setStatus('live')
    }

    const startPolling = () => {
      if (pollingTimer != null) return
      setStatus('polling')
      void queryClient.invalidateQueries({
        queryKey: channelRoutingQueryKeys.all,
        refetchType: 'active',
      })
      pollingTimer = window.setInterval(() => {
        void queryClient.invalidateQueries({
          queryKey: channelRoutingQueryKeys.all,
          refetchType: 'active',
        })
      }, fallbackPollingIntervalMs)
    }

    const handleEvent = (rawEvent: Event) => {
      const message = rawEvent as MessageEvent<string>
      const event = parseChannelRoutingEvent(message.data)
      if (!event) {
        enqueue(['all'])
        return
      }
      if (event.type === 'routing.ready') {
        const readyCursor = getChannelRoutingReadyCursor(event)
        if (!readyCursor) {
          enqueue(['all'])
          return
        }
        lastCursor = readyCursor
        source.lastEventId = `${readyCursor.nodeEpochId}:${readyCursor.sequence}`
        stopPolling()
        return
      }
      const sequence = inspectChannelRoutingEventSequence(lastCursor, event)
      lastCursor = sequence.cursor
      if (sequence.duplicate) return
      if (sequence.gap) enqueue(['all'])
      enqueue(getChannelRoutingEventResources(event.type))
    }

    const handleOpen = (rawEvent: Event) => {
      const event = rawEvent as Event & {
        responseCode?: number
        headers?: Record<string, string | string[] | undefined>
      }
      const responseCode = event.responseCode ?? 0
      if (responseCode >= 200 && responseCode < 300) {
        source.reconnectDelay = defaultReconnectDelayMs
        stopPolling()
        return
      }
      if (responseCode === 503 && event.headers) {
        source.reconnectDelay = getChannelRoutingRetryDelayMs(
          event.headers,
          defaultReconnectDelayMs
        )
      }
      startPolling()
    }

    source.addEventListener('open', handleOpen)
    source.addEventListener('error', startPolling)
    for (const eventName of channelRoutingEventNames) {
      source.addEventListener(eventName, handleEvent)
    }
    source.stream()

    return () => {
      source.close()
      source.removeEventListener('open', handleOpen)
      source.removeEventListener('error', startPolling)
      for (const eventName of channelRoutingEventNames) {
        source.removeEventListener(eventName, handleEvent)
      }
      if (flushTimer != null) window.clearTimeout(flushTimer)
      if (pollingTimer != null) window.clearInterval(pollingTimer)
    }
  }, [queryClient])

  return status
}
