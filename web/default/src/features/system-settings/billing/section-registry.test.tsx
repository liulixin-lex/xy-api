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

import type { TFunction } from 'i18next'

import { ROLE } from '@/lib/roles'
import type { AuthUser } from '@/stores/auth-store'

import { canAccessBillingSection } from './permissions'
import { getBillingSectionNavItems } from './section-registry'

const t = ((key: string) => key) as TFunction

function user(
  role: number,
  permissions: {
    paymentGateway?: boolean
    paymentOperations?: boolean
    systemSettings?: boolean
  } = {}
): AuthUser {
  return {
    id: 1,
    username: 'admin',
    role,
    permissions:
      permissions.paymentGateway ||
      permissions.paymentOperations ||
      permissions.systemSettings
        ? {
            admin_permissions: {
              ...(permissions.paymentGateway
                ? { payment_gateway: { manage: true } }
                : {}),
              ...(permissions.paymentOperations
                ? { payment_operations: { manage: true } }
                : {}),
              ...(permissions.systemSettings
                ? { system_setting: { manage: true } }
                : {}),
            },
          }
        : undefined,
  }
}

function hasPaymentOperations(userValue: AuthUser): boolean {
  return getBillingSectionNavItems(t, userValue).some((item) =>
    item.url.endsWith('/payment-operations')
  )
}

describe('billing section permission visibility', () => {
  test('hides payment operations without its dedicated capability', () => {
    assert.equal(hasPaymentOperations(user(ROLE.ADMIN)), false)
  })

  test('shows payment operations to root and explicitly delegated admins', () => {
    assert.equal(hasPaymentOperations(user(ROLE.SUPER_ADMIN)), true)
    assert.equal(
      hasPaymentOperations(user(ROLE.ADMIN, { paymentOperations: true })),
      true
    )
  })

  test('returns only sections authorized by the delegated capabilities', () => {
    const paymentOnly = getBillingSectionNavItems(
      t,
      user(ROLE.ADMIN, { paymentOperations: true })
    )
    assert.deepEqual(
      paymentOnly.map((item) => item.url),
      ['/system-settings/billing/payment-operations']
    )

    const settingsOnly = getBillingSectionNavItems(
      t,
      user(ROLE.ADMIN, { systemSettings: true })
    )
    assert.equal(
      settingsOnly.some((item) => item.url.endsWith('/payment-operations')),
      false
    )
    assert.equal(
      settingsOnly.some((item) => item.url.endsWith('/payment')),
      false
    )
    assert.equal(settingsOnly.length, 6)
    assert.deepEqual(getBillingSectionNavItems(t, user(ROLE.ADMIN)), [])
  })

  test('requires both system settings and payment gateway grants for gateway configuration', () => {
    assert.equal(
      canAccessBillingSection(
        'payment',
        user(ROLE.ADMIN, { systemSettings: true })
      ),
      false
    )
    assert.equal(
      canAccessBillingSection(
        'payment',
        user(ROLE.ADMIN, { paymentGateway: true })
      ),
      false
    )
    const delegatedUser = user(ROLE.ADMIN, {
      paymentGateway: true,
      systemSettings: true,
    })
    assert.equal(canAccessBillingSection('payment', delegatedUser), true)
    assert.equal(
      getBillingSectionNavItems(t, delegatedUser).some((item) =>
        item.url.endsWith('/payment')
      ),
      true
    )
  })
})
