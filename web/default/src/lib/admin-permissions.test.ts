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

import type { AuthUser } from '@/stores/auth-store'

import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  canAccessBillingReviews,
  canManageSystemSettings,
  hasAnyPermission,
  hasPermission,
} from './admin-permissions'
import { ROLE } from './roles'

function makeUser(user: Partial<AuthUser>): AuthUser {
  return {
    id: 1,
    username: 'admin',
    role: ROLE.ADMIN,
    ...user,
  }
}

describe('admin permission helpers', () => {
  test('allows root to manage system settings', () => {
    assert.equal(
      canManageSystemSettings(makeUser({ role: ROLE.SUPER_ADMIN })),
      true
    )
  })

  test('requires explicit system setting grant for admins', () => {
    assert.equal(canManageSystemSettings(makeUser({ role: ROLE.ADMIN })), false)
    assert.equal(
      canManageSystemSettings(
        makeUser({
          role: ROLE.ADMIN,
          permissions: {
            admin_permissions: {
              system_setting: {
                manage: true,
              },
            },
          },
        })
      ),
      true
    )
  })

  test('keeps channel routing grants independent from channel grants', () => {
    const channelAdmin = makeUser({
      permissions: {
        admin_permissions: {
          channel: { read: true, operate: true, write: true },
        },
      },
    })

    assert.equal(
      hasPermission(
        channelAdmin,
        ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
        ADMIN_PERMISSION_ACTIONS.READ
      ),
      false
    )

    const routingAdmin = makeUser({
      permissions: {
        admin_permissions: {
          channel_routing: {
            read: true,
            operate: true,
            write: true,
            deploy: true,
            sensitive_write: true,
            audit_export: true,
          },
        },
      },
    })

    for (const action of [
      ADMIN_PERMISSION_ACTIONS.READ,
      ADMIN_PERMISSION_ACTIONS.OPERATE,
      ADMIN_PERMISSION_ACTIONS.WRITE,
      ADMIN_PERMISSION_ACTIONS.DEPLOY,
      ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE,
      ADMIN_PERMISSION_ACTIONS.AUDIT_EXPORT,
    ]) {
      assert.equal(
        hasPermission(
          routingAdmin,
          ADMIN_PERMISSION_RESOURCES.CHANNEL_ROUTING,
          action
        ),
        true
      )
    }
  })

  test('keeps billing review read and resolve grants explicit', () => {
    const reviewer = makeUser({
      permissions: {
        admin_permissions: {
          billing_review: { read: true, resolve: false },
        },
      },
    })

    assert.equal(
      hasPermission(
        reviewer,
        ADMIN_PERMISSION_RESOURCES.BILLING_REVIEW,
        ADMIN_PERMISSION_ACTIONS.READ
      ),
      true
    )
    assert.equal(
      hasPermission(
        reviewer,
        ADMIN_PERMISSION_RESOURCES.BILLING_REVIEW,
        ADMIN_PERMISSION_ACTIONS.RESOLVE
      ),
      false
    )
  })

  test('matches the admin-auth boundary for direct billing review access', () => {
    const billingGrant = {
      admin_permissions: {
        billing_review: { read: true, resolve: false },
      },
    }

    assert.equal(
      canAccessBillingReviews(
        makeUser({ role: ROLE.USER, permissions: billingGrant })
      ),
      false
    )
    assert.equal(
      canAccessBillingReviews(
        makeUser({ role: ROLE.ADMIN, permissions: billingGrant })
      ),
      true
    )
    assert.equal(
      canAccessBillingReviews(makeUser({ role: ROLE.SUPER_ADMIN })),
      true
    )
  })

  test('allows billing operations access through either read grant', () => {
    const projectionGrant = {
      admin_permissions: {
        billing_projection_ops: {
          read: true,
          requeue: false,
          resolve: false,
        },
      },
    }
    const projectionAdmin = makeUser({
      role: ROLE.ADMIN,
      permissions: projectionGrant,
    })

    assert.equal(canAccessBillingReviews(projectionAdmin), true)
    assert.equal(
      hasAnyPermission(projectionAdmin, [
        {
          resource: ADMIN_PERMISSION_RESOURCES.BILLING_REVIEW,
          action: ADMIN_PERMISSION_ACTIONS.READ,
        },
        {
          resource: ADMIN_PERMISSION_RESOURCES.BILLING_PROJECTION_OPS,
          action: ADMIN_PERMISSION_ACTIONS.READ,
        },
      ]),
      true
    )
    assert.equal(
      canAccessBillingReviews(
        makeUser({ role: ROLE.USER, permissions: projectionGrant })
      ),
      false
    )
  })

  test('keeps projection read, requeue and resolve grants independent', () => {
    const operator = makeUser({
      permissions: {
        admin_permissions: {
          billing_projection_ops: {
            read: true,
            requeue: true,
            resolve: false,
          },
        },
      },
    })

    assert.equal(
      hasPermission(
        operator,
        ADMIN_PERMISSION_RESOURCES.BILLING_PROJECTION_OPS,
        ADMIN_PERMISSION_ACTIONS.READ
      ),
      true
    )
    assert.equal(
      hasPermission(
        operator,
        ADMIN_PERMISSION_RESOURCES.BILLING_PROJECTION_OPS,
        ADMIN_PERMISSION_ACTIONS.REQUEUE
      ),
      true
    )
    assert.equal(
      hasPermission(
        operator,
        ADMIN_PERMISSION_RESOURCES.BILLING_PROJECTION_OPS,
        ADMIN_PERMISSION_ACTIONS.RESOLVE
      ),
      false
    )
  })
})
