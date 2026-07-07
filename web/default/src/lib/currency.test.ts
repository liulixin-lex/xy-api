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

import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { formatCurrencyFromUSD } from './currency'

describe('currency formatting', () => {
  test('can omit symbols for currency and custom displays', (t) => {
    const originalConfig = useSystemConfigStore.getState().config
    t.after(() => {
      useSystemConfigStore.setState({ config: originalConfig })
    })

    useSystemConfigStore.setState({
      config: {
        ...originalConfig,
        currency: {
          ...DEFAULT_CURRENCY_CONFIG,
          quotaDisplayType: 'USD',
        },
      },
    })
    assert.equal(formatCurrencyFromUSD(12.34, { showSymbol: false }), '12.34')

    useSystemConfigStore.setState({
      config: {
        ...originalConfig,
        currency: {
          ...DEFAULT_CURRENCY_CONFIG,
          quotaDisplayType: 'CUSTOM',
          customCurrencySymbol: '¤',
          customCurrencyExchangeRate: 2,
        },
      },
    })
    assert.equal(formatCurrencyFromUSD(3, { showSymbol: false }), '6')
  })
})
