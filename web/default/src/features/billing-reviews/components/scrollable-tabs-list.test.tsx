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

import { renderToStaticMarkup } from 'react-dom/server'

import '@/i18n/config'
import { Tabs, TabsTrigger } from '@/components/ui/tabs'

import { BillingScrollableTabsList } from './scrollable-tabs-list'

describe('billing scrollable tabs list', () => {
  test('preserves a 44px mobile touch target for tab triggers', () => {
    const html = renderToStaticMarkup(
      <Tabs defaultValue='reviews'>
        <BillingScrollableTabsList activeValue='reviews'>
          <TabsTrigger value='reviews'>Reviews</TabsTrigger>
        </BillingScrollableTabsList>
      </Tabs>
    )

    assert.match(html, /max-lg:min-h-11/)
    assert.match(html, /max-lg:\[&amp;_\[data-slot=tabs-trigger\]\]:min-h-11/)
  })
})
