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
import { readFile } from 'node:fs/promises'
import { afterEach, describe, test } from 'node:test'

import type { ConfigParams, RsbuildConfig } from '@rsbuild/core'

import configDefinition from './rsbuild.config'

const versionKey = 'import.meta.env.VITE_REACT_APP_VERSION'
const originalBuildVersion = process.env.VITE_REACT_APP_VERSION

async function resolveConfig(): Promise<RsbuildConfig> {
  if (typeof configDefinition !== 'function') {
    throw new TypeError('Expected a functional Rsbuild configuration')
  }
  return configDefinition({
    env: 'web',
    command: 'build',
    envMode: 'production',
  } satisfies ConfigParams)
}

afterEach(() => {
  if (originalBuildVersion === undefined) {
    delete process.env.VITE_REACT_APP_VERSION
  } else {
    process.env.VITE_REACT_APP_VERSION = originalBuildVersion
  }
})

describe('Rsbuild public build version', () => {
  test('defines only the safely encoded public version key', async () => {
    const version = 'v0.1.11";globalThis.injected=true;//'
    process.env.VITE_REACT_APP_VERSION = version

    const config = await resolveConfig()

    assert.deepEqual(config.source?.define, {
      [versionKey]: JSON.stringify(version),
    })
  })

  test('defines an empty value when no build version is supplied', async () => {
    delete process.env.VITE_REACT_APP_VERSION

    const config = await resolveConfig()

    assert.equal(config.source?.define?.[versionKey], JSON.stringify(''))
  })

  test('keeps the runtime reader statically addressable by the single-key define', async () => {
    const source = await readFile(
      new URL('./src/lib/build-metadata.ts', import.meta.url),
      'utf8'
    )

    assert.match(source, /import\.meta\.env\.VITE_REACT_APP_VERSION/)
    assert.doesNotMatch(
      source,
      /(?:const|let|var)\s+\w+\s*=\s*import\.meta\.env(?!\.)[\s\S]{0,200}?\.VITE_REACT_APP_VERSION/
    )
  })
})
