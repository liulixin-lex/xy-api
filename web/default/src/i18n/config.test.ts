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
import { test } from 'node:test'

test('keeps the page language aligned with initial and runtime i18n languages', async (t) => {
  const originalDocument = Object.getOwnPropertyDescriptor(
    globalThis,
    'document'
  )
  const originalNavigator = Object.getOwnPropertyDescriptor(
    globalThis,
    'navigator'
  )
  const documentElement = { lang: '' }

  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    value: { documentElement },
  })
  Object.defineProperty(globalThis, 'navigator', {
    configurable: true,
    value: { language: 'en', languages: ['en'] },
  })
  t.after(() => {
    if (originalDocument) {
      Object.defineProperty(globalThis, 'document', originalDocument)
    } else {
      Reflect.deleteProperty(globalThis, 'document')
    }
    if (originalNavigator) {
      Object.defineProperty(globalThis, 'navigator', originalNavigator)
    } else {
      Reflect.deleteProperty(globalThis, 'navigator')
    }
  })

  const { default: i18n } = await import('./config')
  if (!i18n.isInitialized) {
    await new Promise<void>((resolve) => {
      const handleInitialized = () => {
        i18n.off('initialized', handleInitialized)
        resolve()
      }
      i18n.on('initialized', handleInitialized)
    })
  }

  assert.equal(documentElement.lang, 'en')
  for (const [language, expected] of [
    ['ru', 'ru'],
    ['fr', 'fr'],
    ['zhTW', 'zh-TW'],
  ] as const) {
    await i18n.changeLanguage(language)
    assert.equal(documentElement.lang, expected)
  }

  Reflect.deleteProperty(globalThis, 'document')
  await assert.doesNotReject(i18n.changeLanguage('en'))
})
