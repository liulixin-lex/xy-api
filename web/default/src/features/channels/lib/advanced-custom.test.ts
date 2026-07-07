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

import {
  getAdvancedCustomConverterOptions,
  getAdvancedCustomStats,
} from './advanced-custom'

describe('advanced custom route helpers', () => {
  test('limits converter options to the selected incoming path', () => {
    assert.deepEqual(
      getAdvancedCustomConverterOptions('/v1/messages').map(
        (option) => option.value
      ),
      ['none', 'anthropic_messages_to_openai_chat_completions']
    )

    assert.deepEqual(
      getAdvancedCustomConverterOptions('/v1/responses').map(
        (option) => option.value
      ),
      ['none', 'openai_responses_to_openai_chat_completions']
    )
  })

  test('summarizes configured route types without duplicates', () => {
    const stats = getAdvancedCustomStats(
      JSON.stringify({
        advanced_routes: [
          {
            incoming_path: '/v1/chat/completions',
            upstream_path: '/v1/chat/completions',
            converter: 'none',
          },
          {
            incoming_path: '/v1/chat/completions',
            upstream_path: '/v1beta/models/{model}:generateContent',
            converter: 'openai_chat_completions_to_gemini_generate_content',
          },
          {
            incoming_path: '/v1/responses',
            upstream_path: '/v1/responses',
            converter: 'none',
          },
        ],
      })
    )

    assert.equal(stats.routeCount, 3)
    assert.deepEqual(stats.routeTypeLabels, [
      'OpenAI Chat',
      'OpenAI Responses',
    ])
  })
})
