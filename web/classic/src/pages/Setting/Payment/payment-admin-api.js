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

import { API } from '../../../helpers';
import { createPaymentAdminError } from '../../../helpers/payment-admin-errors';

const paymentAdminRequestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
};

const unwrapPaymentAdminResponse = (response, fallbackMessage) => {
  if (!response?.success || response.data === undefined) {
    throw createPaymentAdminError(response, fallbackMessage);
  }
  return response.data;
};

export const getPaymentOperationsOverview = async () => {
  const response = await API.get(
    '/api/option/payment/overview',
    paymentAdminRequestConfig,
  );
  return unwrapPaymentAdminResponse(
    response.data,
    'Failed to load payment overview',
  );
};

export const listPaymentLimits = async () => {
  const response = await API.get(
    '/api/option/payment/limits',
    paymentAdminRequestConfig,
  );
  return unwrapPaymentAdminResponse(
    response.data,
    'Failed to load payment limits',
  );
};

export const updatePaymentLimit = async (request) => {
  const response = await API.put(
    '/api/option/payment/limits',
    request,
    paymentAdminRequestConfig,
  );
  return unwrapPaymentAdminResponse(
    response.data,
    'Failed to save payment limit',
  );
};

export const listStripeLegacyInventory = async ({
  page = 1,
  pageSize = 20,
} = {}) => {
  const response = await API.get('/api/subscription/admin/stripe/inventory', {
    ...paymentAdminRequestConfig,
    params: {
      p: page,
      page_size: pageSize,
    },
  });
  return unwrapPaymentAdminResponse(
    response.data,
    'Failed to load Stripe subscription inventory',
  );
};

export const syncStripeLegacyInventory = async () => {
  const response = await API.post(
    '/api/subscription/admin/stripe/inventory/sync',
    {},
    paymentAdminRequestConfig,
  );
  return unwrapPaymentAdminResponse(
    response.data,
    'Failed to sync Stripe subscription inventory',
  );
};
