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

export const ADMIN_PERMISSION_RESOURCES = {
  PAYMENT_GATEWAY: 'payment_gateway',
  PAYMENT_OPERATIONS: 'payment_operations',
  SYSTEM_SETTING: 'system_setting',
};

const MANAGE_ACTION = 'manage';

export const hasAdminPermission = (permissions, resource, action) =>
  permissions?.admin_permissions?.[resource]?.[action] === true;

export const canManagePaymentGatewaySettings = (permissions) =>
  hasAdminPermission(
    permissions,
    ADMIN_PERMISSION_RESOURCES.SYSTEM_SETTING,
    MANAGE_ACTION,
  ) &&
  hasAdminPermission(
    permissions,
    ADMIN_PERMISSION_RESOURCES.PAYMENT_GATEWAY,
    MANAGE_ACTION,
  );

export const canManagePaymentOperations = (permissions, role = 0) =>
  role >= 100 ||
  hasAdminPermission(
    permissions,
    ADMIN_PERMISSION_RESOURCES.PAYMENT_OPERATIONS,
    MANAGE_ACTION,
  );
