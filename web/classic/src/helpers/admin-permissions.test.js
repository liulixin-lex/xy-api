/*
Copyright (C) 2025 QuantumNous

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

import assert from 'node:assert/strict';
import { describe, test } from 'node:test';

import {
  canManagePaymentGatewaySettings,
  canManagePaymentOperations,
} from './admin-permissions.js';

const permissions = (systemSetting, paymentGateway) => ({
  admin_permissions: {
    system_setting: { manage: systemSetting },
    payment_gateway: { manage: paymentGateway },
  },
});

describe('classic payment operations permission helper', () => {
  test('allows root and explicitly delegated administrators only', () => {
    assert.equal(canManagePaymentOperations(null, 100), true);
    assert.equal(
      canManagePaymentOperations(permissions(false, false), 10),
      false,
    );
    assert.equal(
      canManagePaymentOperations(
        { admin_permissions: { payment_operations: { manage: true } } },
        10,
      ),
      true,
    );
  });

  test('keeps gateway and operations access independent', () => {
    const operationsOnly = {
      admin_permissions: { payment_operations: { manage: true } },
    };
    const gatewayOnly = permissions(true, true);

    assert.equal(canManagePaymentGatewaySettings(operationsOnly), false);
    assert.equal(canManagePaymentOperations(operationsOnly, 10), true);
    assert.equal(canManagePaymentGatewaySettings(gatewayOnly), true);
    assert.equal(canManagePaymentOperations(gatewayOnly, 10), false);
  });
});

describe('classic payment gateway permission helper', () => {
  test('requires both system settings and payment gateway grants', () => {
    assert.equal(
      canManagePaymentGatewaySettings(permissions(false, false)),
      false,
    );
    assert.equal(
      canManagePaymentGatewaySettings(permissions(true, false)),
      false,
    );
    assert.equal(
      canManagePaymentGatewaySettings(permissions(false, true)),
      false,
    );
    assert.equal(
      canManagePaymentGatewaySettings(permissions(true, true)),
      true,
    );
  });
});
