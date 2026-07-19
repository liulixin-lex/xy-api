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

import { isSecurePaymentCallbackOrigin } from './payment-callback-origin'

describe('managed payment callback origin validation', () => {
  test('accepts public HTTPS origins and explicit local HTTP development origins', () => {
    for (const value of [
      'https://payments.example.com',
      'https://8.8.8.8:8443',
      'https://[2606:4700:4700::1111]',
      'http://localhost:3000',
      'http://dev.localhost:3000',
      'http://127.0.0.1:3000',
      'http://[::1]:3000',
    ]) {
      assert.equal(isSecurePaymentCallbackOrigin(value), true, value)
    }
  })

  test('rejects HTTPS local development hosts and non-public literal addresses', () => {
    for (const value of [
      'https://localhost',
      'https://dev.localhost',
      'https://127.0.0.1',
      'https://2130706433',
      'https://0x7f000001',
      'https://017700000001',
      'https://127.1',
      'https://0177.0.0.1',
      'https://0x7f.0.0.1',
      'https://134744072',
      'https://0x08080808',
      'https://010.010.010.010',
      'https://8.8.8',
      'https://[::1]',
      'https://10.0.0.1',
      'https://100.64.0.1',
      'https://169.254.1.1',
      'https://172.16.0.1',
      'https://192.0.0.1',
      'https://192.0.2.1',
      'https://192.168.1.1',
      'https://198.18.0.1',
      'https://198.51.100.1',
      'https://203.0.113.1',
      'https://224.0.0.1',
      'https://240.0.0.1',
      'https://255.255.255.255',
      'https://[::]',
      'https://[::ffff:127.0.0.1]',
      'https://[64:ff9b::1]',
      'https://[100::1]',
      'https://[2001:100::1]',
      'https://[2001:db8::1]',
      'https://[fc00::1]',
      'https://[fe80::1]',
      'https://[ff02::1]',
    ]) {
      assert.equal(isSecurePaymentCallbackOrigin(value), false, value)
    }
  })

  test('rejects malformed or non-origin callback values', () => {
    for (const value of [
      '',
      'http://payments.example.com',
      'ftp://payments.example.com',
      'https:payments.example.com',
      'https:\\payments.example.com',
      'https://user:pass@payments.example.com',
      'https://payments.example.com/callback',
      'https://payments.example.com?token=1',
      'https://payments.example.com#fragment',
      `https://${'a'.repeat(2049)}.example.com`,
    ]) {
      assert.equal(isSecurePaymentCallbackOrigin(value), false, value)
    }
  })
})
