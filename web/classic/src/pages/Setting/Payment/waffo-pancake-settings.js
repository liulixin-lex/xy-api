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

export const normalizeWaffoPancakeTestMode = (value) =>
  value === true || value === 'true';

export const getWaffoPancakePricingError = (unitPrice, minTopUp) => {
  const normalizedUnitPrice = Number(unitPrice);
  if (
    !Number.isFinite(normalizedUnitPrice) ||
    normalizedUnitPrice <= 0 ||
    normalizedUnitPrice > 1000000
  ) {
    return 'unit_price';
  }
  const normalizedMinTopUp = Number(minTopUp);
  if (
    !Number.isInteger(normalizedMinTopUp) ||
    normalizedMinTopUp < 1 ||
    normalizedMinTopUp > 10000
  ) {
    return 'min_top_up';
  }
  return null;
};

export const buildWaffoPancakeSavePayload = ({
  merchantID,
  privateKey,
  returnURL,
  storeID,
  productID,
  unitPrice,
  minTopUp,
  testMode,
  expectedVersion,
}) => ({
  merchant_id: merchantID,
  private_key: privateKey,
  return_url: returnURL,
  store_id: storeID,
  product_id: productID,
  unit_price: Number(unitPrice),
  min_top_up: Number(minTopUp),
  test_mode: normalizeWaffoPancakeTestMode(testMode),
  expected_version: expectedVersion,
});
