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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  routingControlAuditActorRoleLabel,
  routingControlAuditChanges,
  routingControlAuditEntries,
  routingControlAuditFieldLabel,
  routingControlAuditHumanizeKey,
} from './control-audits'

describe('routing control audit presentation', () => {
  test('accepts only structured change items', () => {
    assert.deepEqual(
      routingControlAuditChanges({
        items: [
          {
            scope: 'member',
            change: 'updated',
            field: 'weight_override',
            before: 100,
            after: 0,
          },
          { scope: 1, change: 'ignored' },
        ],
      }),
      [
        {
          scope: 'member',
          change: 'updated',
          field: 'weight_override',
          before: 100,
          after: 0,
          pool_id: undefined,
          group_name: undefined,
          member_id: undefined,
          routing_generation: undefined,
        },
      ]
    )
  })

  test('keeps meaningful false and zero values in structured sections', () => {
    assert.deepEqual(routingControlAuditEntries({ enabled: false, count: 0 }), [
      ['enabled', false],
      ['count', 0],
    ])
    assert.equal(
      routingControlAuditHumanizeKey('runtime_snapshot_rebuild'),
      'Runtime Snapshot Rebuild'
    )
    assert.equal(
      routingControlAuditFieldLabel('routing_identity'),
      'Routing identity'
    )
    assert.equal(
      routingControlAuditFieldLabel('runtime_snapshot_rebuild'),
      'Runtime snapshot rebuild'
    )
    assert.equal(routingControlAuditFieldLabel('provider_specific_field'), null)
  })

  test('uses system semantics for non-user control actors', () => {
    assert.equal(routingControlAuditActorRoleLabel(0, 0, 'system'), 'System')
    assert.equal(
      routingControlAuditActorRoleLabel(42, 10, 'migration'),
      'System'
    )
    assert.equal(
      routingControlAuditActorRoleLabel(42, 10, 'reconciler'),
      'System'
    )
    assert.equal(routingControlAuditActorRoleLabel(42, 10, 'admin'), 'Admin')
  })

  test('maps common policy impact fields to existing translated labels', () => {
    assert.equal(routingControlAuditFieldLabel('changed_pool_ids'), 'Pools')
    assert.equal(
      routingControlAuditFieldLabel('deterministic_validation_passed'),
      'Validated'
    )
    assert.equal(
      routingControlAuditFieldLabel('member_change_count'),
      'Changed members'
    )
    assert.equal(
      routingControlAuditFieldLabel('pool_change_count'),
      'Policy changes'
    )
    assert.equal(routingControlAuditFieldLabel('activation_id'), 'Activation')
  })
})
