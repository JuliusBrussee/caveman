import assert from 'node:assert/strict';
import test from 'node:test';
import { unforwardedProviderHeaders } from '../dist/provider-routing.js';

// OpenAI SDK 6.40.0 sends defaultHeaders.Authorization verbatim, even with
// apiKey configured. The standalone credential mapper supports Bearer, so an
// existing custom provider using another scheme must retain its direct route.
const published = { compat_upstreams: { relay: 'https://relay.example/v1' } };

for (const api of ['openai-completions', 'openai-responses', 'anthropic-messages']) {
  test(`${api} does not certify a custom non-Bearer Authorization override`, () => {
    for (const value of ['Basic local-fixture', 'Token local-fixture', 'Digest local-fixture', '', null, 'Bearer', 'Bearer ']) {
      assert.deepEqual(unforwardedProviderHeaders(api, 'relay', { Authorization: value }, published), ['Authorization']);
    }
    assert.deepEqual(unforwardedProviderHeaders(api, 'relay', { Authorization: 'Bearer local-fixture' }, published), []);
  });
}

test('explicitly removed native key headers do not certify an auth-changing fallback', () => {
  for (const [api, header] of [['anthropic-messages', 'X-API-Key'], ['google-generative-ai', 'X-Goog-API-Key']]) {
    for (const value of ['', null]) {
      assert.deepEqual(unforwardedProviderHeaders(api, 'relay', { [header]: value }, published), [header]);
    }
    assert.deepEqual(unforwardedProviderHeaders(api, 'selected', { [header]: 'local-fixture-key' }), []);
  }
});
