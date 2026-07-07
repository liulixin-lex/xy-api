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
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const currentDir = dirname(fileURLToPath(import.meta.url))
const walletSource = readFileSync(join(currentDir, 'index.tsx'), 'utf8')

describe('wallet referral rewards surface', () => {
  test('renders the referral reward card and transfer dialog from the wallet page', () => {
    assert.match(
      walletSource,
      /import \{ AffiliateRewardsCard \} from '\.\/components\/affiliate-rewards-card'/
    )
    assert.match(
      walletSource,
      /import \{ TransferDialog \} from '\.\/components\/dialogs\/transfer-dialog'/
    )
    assert.match(walletSource, /useAffiliate\(\)/)
    assert.match(walletSource, /<AffiliateRewardsCard[\s\S]*affiliateLink=/)
    assert.match(walletSource, /<TransferDialog[\s\S]*availableQuota=/)
  })
})
