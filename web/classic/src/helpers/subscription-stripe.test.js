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

import {
  hasLegacyStripePriceMapping,
  isValidLegacyStripePriceID,
  LEGACY_STRIPE_PRICE_ID_PURPOSE,
  normalizeLegacyStripePriceID,
} from './subscription-stripe.js';

describe('classic legacy Stripe plan mapping', () => {
  test('is visible only for records explicitly marked as legacy inventory', () => {
    assert.equal(
      hasLegacyStripePriceMapping({
        stripe_price_id_purpose: LEGACY_STRIPE_PRICE_ID_PURPOSE,
        plan: { stripe_price_id: ' price_legacy ' },
      }),
      true,
    );
    assert.equal(
      hasLegacyStripePriceMapping({
        plan: { stripe_price_id: 'price_current' },
      }),
      false,
    );
    assert.equal(
      hasLegacyStripePriceMapping({
        stripe_price_id_purpose: LEGACY_STRIPE_PRICE_ID_PURPOSE,
        plan: { stripe_price_id: '   ' },
      }),
      false,
    );
  });

  test('normalizes and validates Stripe Price IDs', () => {
    assert.equal(
      normalizeLegacyStripePriceID(' price_legacy '),
      'price_legacy',
    );
    assert.equal(isValidLegacyStripePriceID(''), true);
    assert.equal(isValidLegacyStripePriceID('price_legacy'), true);
    assert.equal(isValidLegacyStripePriceID('prod_not_a_price'), false);
    assert.equal(isValidLegacyStripePriceID(`price_${'x'.repeat(122)}`), true);
    assert.equal(isValidLegacyStripePriceID(`price_${'x'.repeat(123)}`), false);
  });
});
