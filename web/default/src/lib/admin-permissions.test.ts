import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import type { AuthUser } from '@/stores/auth-store'

import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  canManageSystemSettings,
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
})
