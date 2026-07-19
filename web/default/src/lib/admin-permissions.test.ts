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
  canManagePaymentGateway,
  canManagePaymentOperations,
  canManageSystemSettings,
  getSystemSettingsEntryUrl,
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

  test('keeps payment operations root-only unless explicitly delegated', () => {
    assert.equal(
      canManagePaymentOperations(makeUser({ role: ROLE.SUPER_ADMIN })),
      true
    )
    assert.equal(
      canManagePaymentOperations(makeUser({ role: ROLE.ADMIN })),
      false
    )
    assert.equal(
      canManagePaymentOperations(
        makeUser({
          role: ROLE.ADMIN,
          permissions: {
            admin_permissions: {
              payment_operations: {
                manage: true,
              },
            },
          },
        })
      ),
      true
    )
  })

  test('keeps payment gateway management root-only unless explicitly delegated', () => {
    assert.equal(
      canManagePaymentGateway(makeUser({ role: ROLE.SUPER_ADMIN })),
      true
    )
    assert.equal(canManagePaymentGateway(makeUser({ role: ROLE.ADMIN })), false)
    assert.equal(
      canManagePaymentGateway(
        makeUser({
          role: ROLE.ADMIN,
          permissions: {
            admin_permissions: {
              payment_gateway: {
                manage: true,
              },
            },
          },
        })
      ),
      true
    )
  })

  test('maps the system settings entry to the first authorized destination', () => {
    assert.equal(
      getSystemSettingsEntryUrl(makeUser({ role: ROLE.ADMIN })),
      null
    )
    assert.equal(
      getSystemSettingsEntryUrl(
        makeUser({
          permissions: {
            admin_permissions: {
              payment_operations: { manage: true },
            },
          },
        })
      ),
      '/system-settings/billing/payment-operations'
    )
    assert.equal(
      getSystemSettingsEntryUrl(
        makeUser({
          permissions: {
            admin_permissions: {
              system_setting: { manage: true },
            },
          },
        })
      ),
      '/system-settings/site'
    )
    assert.equal(
      getSystemSettingsEntryUrl(makeUser({ role: ROLE.SUPER_ADMIN })),
      '/system-settings/site'
    )
  })
})
