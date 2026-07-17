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

import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { CostCatalogHierarchyLayout } from './cost-catalog-layout'

describe('cost catalog responsive hierarchy', () => {
  test('uses one shared content tree for desktop and mobile layouts', () => {
    const html = renderToStaticMarkup(
      createElement(CostCatalogHierarchyLayout, {
        pools: createElement('section', null, 'pool-level-marker'),
        members: createElement('section', null, 'member-level-marker'),
        models: createElement('section', null, 'model-level-marker'),
        detail: createElement('section', null, 'detail-level-marker'),
      })
    )

    for (const marker of [
      'pool-level-marker',
      'member-level-marker',
      'model-level-marker',
      'detail-level-marker',
    ]) {
      assert.equal(html.split(marker).length - 1, 1)
    }
    assert.match(html, /xl:grid/)
    assert.doesNotMatch(html, /md:hidden|hidden md:/)
  })
})
