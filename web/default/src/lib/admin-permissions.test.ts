import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import type { AuthUser } from '@/stores/auth-store'

import { canManageSystemSettings } from './admin-permissions'
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
})
