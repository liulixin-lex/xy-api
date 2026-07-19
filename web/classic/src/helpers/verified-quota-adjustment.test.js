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

import { executeVerifiedQuotaAdjustment } from './verified-quota-adjustment.js';

const verification = {
  title: 'Security verification',
  description: 'Confirm identity',
};

describe('classic verified quota adjustment', () => {
  test('keeps the adjustment and success effect inside the verified operation', async () => {
    let successCalls = 0;
    const result = await executeVerifiedQuotaAdjustment({
      withVerification: async (operation, options) => {
        assert.deepEqual(options, verification);
        return operation();
      },
      request: async () => ({ success: true }),
      onSuccess: () => {
        successCalls += 1;
      },
      failureMessage: 'Operation failed',
      verification,
    });

    assert.equal(result.success, true);
    assert.equal(successCalls, 1);
  });

  test('does not apply success effects while verification is pending', async () => {
    let requestCalls = 0;
    let successCalls = 0;
    const result = await executeVerifiedQuotaAdjustment({
      withVerification: async () => null,
      request: async () => {
        requestCalls += 1;
        return { success: true };
      },
      onSuccess: () => {
        successCalls += 1;
      },
      failureMessage: 'Operation failed',
      verification,
    });

    assert.equal(result, null);
    assert.equal(requestCalls, 0);
    assert.equal(successCalls, 0);
  });

  test('rejects unsuccessful responses without success effects', async () => {
    let successCalls = 0;
    await assert.rejects(
      executeVerifiedQuotaAdjustment({
        withVerification: async (operation) => operation(),
        request: async () => ({ success: false, message: 'permission denied' }),
        onSuccess: () => {
          successCalls += 1;
        },
        failureMessage: 'Operation failed',
        verification,
      }),
      /permission denied/,
    );
    assert.equal(successCalls, 0);
  });
});
