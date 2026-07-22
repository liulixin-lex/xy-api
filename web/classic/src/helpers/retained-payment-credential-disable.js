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
import {
  isEmergencyCredentialRevocationReasonValid,
  normalizeEmergencyCredentialRevocationReason,
} from './payment-credential-revocation.js';

export const RETAINED_PAYMENT_PROVIDERS = ['creem', 'waffo', 'waffo_pancake'];

export const buildRetainedCredentialDisablePreviewParams = (provider) => ({
  provider,
  mode: 'all_active',
});

export const buildRetainedCredentialDisablePayload = (
  provider,
  reason,
  expectedVersion,
) => {
  if (!RETAINED_PAYMENT_PROVIDERS.includes(provider)) {
    throw new RangeError('retained payment provider is invalid');
  }
  const normalizedReason = normalizeEmergencyCredentialRevocationReason(reason);
  if (!isEmergencyCredentialRevocationReasonValid(normalizedReason)) {
    throw new RangeError('emergency credential disable reason is invalid');
  }
  if (!Number.isSafeInteger(expectedVersion) || expectedVersion <= 0) {
    throw new RangeError('payment configuration version is invalid');
  }
  return {
    options: {},
    disable_current_credentials: [provider],
    reason: normalizedReason,
    expected_version: expectedVersion,
  };
};
