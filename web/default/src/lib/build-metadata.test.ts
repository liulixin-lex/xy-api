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
import { afterEach, describe, test } from 'node:test'

import { getBuildRevision } from './build-metadata'

const originalBuildVersion = process.env.VITE_REACT_APP_VERSION

afterEach(() => {
  if (originalBuildVersion === undefined) {
    delete process.env.VITE_REACT_APP_VERSION
  } else {
    process.env.VITE_REACT_APP_VERSION = originalBuildVersion
  }
})

describe('build metadata revision', () => {
  test('uses the public build version and preserves the fallback', () => {
    process.env.VITE_REACT_APP_VERSION = 'v0.1.11'
    assert.equal(getBuildRevision(), 'rv.v0.1.11.2k6e8r7p')

    delete process.env.VITE_REACT_APP_VERSION
    assert.equal(getBuildRevision(), 'rv.0000.2k6e8r7p')
  })
})
