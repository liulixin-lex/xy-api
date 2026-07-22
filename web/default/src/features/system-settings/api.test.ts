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

import { api } from '@/lib/api'

import {
  getPaymentSettingsErrorField,
  getPaymentSettingsErrorParams,
  getSystemOptions,
} from './api'

describe('payment settings API error contract', () => {
  test('reads the field from direct and Axios-wrapped responses', () => {
    assert.equal(
      getPaymentSettingsErrorField({
        success: false,
        code: 'payment_settings_field_invalid',
        params: { field: 'XorPayCurrency' },
      }),
      'XorPayCurrency'
    )
    assert.equal(
      getPaymentSettingsErrorField({
        response: {
          data: {
            success: false,
            params: { field: 'payment_setting.amount_options' },
          },
        },
      }),
      'payment_setting.amount_options'
    )
  })

  test('does not treat malformed field metadata as a form target', () => {
    assert.equal(
      getPaymentSettingsErrorParams({ params: { field: 42 } })?.field,
      42
    )
    assert.equal(getPaymentSettingsErrorField({ params: { field: 42 } }), null)
    assert.equal(
      getPaymentSettingsErrorField({ params: { field: '  ' } }),
      null
    )
  })

  test('lets settings pages own load failures without a duplicate global toast', async () => {
    const originalGet = api.get
    let requestConfig: Record<string, unknown> | undefined
    try {
      api.get = (async (_url: string, config?: Record<string, unknown>) => {
        requestConfig = config
        return { data: { success: true, data: [] } }
      }) as typeof api.get

      await getSystemOptions()
    } finally {
      api.get = originalGet
    }

    assert.equal(requestConfig?.skipErrorHandler, true)
  })
})
