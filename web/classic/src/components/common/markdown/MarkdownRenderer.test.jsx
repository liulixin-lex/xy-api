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

import { describe, expect, test } from 'bun:test';
import { MERMAID_CONFIG } from './mermaid-config';
import { HTML_PREVIEW_SANDBOX } from './markdown-security';

describe('Mermaid rendering security', () => {
  test('keeps untrusted diagrams in strict mode', () => {
    expect(MERMAID_CONFIG.securityLevel).toBe('strict');
    expect(MERMAID_CONFIG.startOnLoad).toBe(false);
    expect(Object.isFrozen(MERMAID_CONFIG)).toBe(true);
  });
});

describe('HTML preview security', () => {
  test('keeps generated HTML scripts, forms, and navigation sandboxed', () => {
    const permissions = HTML_PREVIEW_SANDBOX.split(/\s+/).filter(Boolean);

    expect(permissions).toEqual(['allow-same-origin']);
    expect(permissions).not.toContain('allow-scripts');
    expect(permissions).not.toContain('allow-forms');
    expect(permissions).not.toContain('allow-top-navigation');
    expect(permissions).not.toContain('allow-popups');
  });
});
