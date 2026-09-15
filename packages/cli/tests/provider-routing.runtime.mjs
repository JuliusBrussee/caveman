import assert from 'node:assert/strict';
import test from 'node:test';
import { verifiedProviderRoute, publishedUpstreamsOf, unforwardedProviderHeaders, publishedForwardHeadersOf } from '../dist/provider-routing.js';
const gw = 'http://127.0.0.1:8787/w/host';
const native = { openai: 'https://api.openai.com', anthropic: 'https://api.anthropic.com', gemini: 'https://generativelanguage.googleapis.com' };
test('provider-specific header forwarding requires a contract for the exact mount', () => {
  const headers = { 'X-API-Tenant': 'secret-tenant', 'CF-AIG-Authorization': 'secret-key', 'OpenAI-Organization': 'org-id' };
  const names = ['CF-AIG-Authorization', 'X-API-Tenant'];
  assert.deepEqual(unforwardedProviderHeaders('openai-completions', 'relay', headers), names);
  const published = { compat_upstreams: { relay: 'https://relay.example' }, compat_forward_headers: { other: names } };
  assert.deepEqual(unforwardedProviderHeaders('openai-completions', 'relay', headers, published), names);
  published.compat_forward_headers = publishedForwardHeadersOf({ relay: names, ignored: 'unsafe', bad: [1, null] });
  assert.deepEqual(unforwardedProviderHeaders('openai-completions', 'relay', headers, published), []);
  assert.deepEqual(unforwardedProviderHeaders('openai-completions', 'relay', { ...headers, 'X-Unrelated': 'secret' }, published), ['X-Unrelated']);
  assert.deepEqual(unforwardedProviderHeaders('openai-responses', 'relay', { Authorization: 'Bearer key', 'X-Optional': undefined }), []);
});
for (const [api, base, route] of [
  ['openai-completions', 'https://api.openai.com/v1', '/openai/v1'],
  ['openai-responses', 'https://api.openai.com/v1/', '/openai/v1'],
  ['anthropic-messages', 'https://api.anthropic.com', '/anthropic'],
  ['google-generative-ai', 'https://generativelanguage.googleapis.com/v1beta', '/gemini/v1beta'],
  ['google-generative-ai', 'https://generativelanguage.googleapis.com/v1', '/gemini/v1'],
]) test(`native ${api} preserves ${base}`, () => {
  assert.equal(verifiedProviderRoute(gw, api, 'selected', base, { provider_upstreams: native }), gw + route);
  assert.equal(verifiedProviderRoute(gw, api, 'selected', base), undefined, 'no assumed native destination');
});
for (const original of ['https://relay.example/tenant-a/v1', 'http://relay.example/tenant-b/v1', 'https://relay.example:9443/tenant-b/v1', 'https://other.example/tenant-b/v1', 'https://relay.example/tenant-b/v1?key=secret', 'https://user:secret@relay.example/tenant-b/v1', 'https://relay.example/tenant-b/v1#fragment']) {
  test(`compat refuses changed destination ${original.replace(/secret/g, 'redacted')}`, () => {
    assert.equal(verifiedProviderRoute(gw, 'openai-completions', 'relay', original, { compat_upstreams: { relay: 'https://relay.example/tenant-b' } }), undefined);
  });
}
for (const [base, original] of [
  ['https://relay.example/tenant', 'https://relay.example/tenant/v1'],
  ['https://relay.example/tenant/v1', 'https://relay.example/tenant/v1'],
]) test(`compat overlap follows Go path join for ${base}`, () => {
  assert.equal(verifiedProviderRoute(gw, 'openai-responses', 'relay', original, { compat_upstreams: { relay: base } }), gw + '/compat/relay/v1');
});
test('internal double slashes do not collapse into another tenant endpoint', () => {
  assert.equal(verifiedProviderRoute(gw, 'openai-responses', 'relay', 'https://relay.example/tenant/sub/v1', { compat_upstreams: { relay: 'https://relay.example/tenant//sub' } }), undefined);
});
test('native configured prefix must match the actual adapter and may not use compat overlap', () => {
  const provider_upstreams = { openai: 'https://relay.example/tenant' };
  assert.equal(verifiedProviderRoute(gw, 'openai-responses', 'relay', 'https://relay.example/tenant/v1', { provider_upstreams }), gw + '/openai/v1');
  assert.equal(verifiedProviderRoute(gw, 'openai-responses', 'relay', 'https://relay.example/tenant/v1', { provider_upstreams: { openai: 'https://relay.example/tenant/v1' } }), undefined);
});
test('invalid mounts remain unavailable instead of falling back to native', () => {
  for (const raw of ['https://user:secret@api.openai.com', 'https://api.openai.com?key=secret', '', null, 7]) {
    const compat_upstreams = publishedUpstreamsOf({ openai: raw });
    assert.equal(compat_upstreams.openai, '');
    assert.equal(verifiedProviderRoute(gw, 'openai-responses', 'openai', 'https://api.openai.com/v1', { provider_upstreams: native, compat_upstreams }), undefined);
  }
  assert.equal(verifiedProviderRoute(gw, 'openai-responses', 'constructor', 'https://other.example/v1', { compat_upstreams: {} }), undefined);
});

test('SDK repeated trailing separators are not collapsed into a verified route', () => {
  assert.equal(verifiedProviderRoute(gw, 'openai-completions', 'relay', 'https://relay.example/tenant/v1//', { provider_upstreams: { openai: 'https://relay.example/tenant' } }), undefined);
});

test('empty query/fragment and whitespace cannot change SDK request-path joining', () => {
  for (const tail of ['?', '#', ' ', '\n', '\t']) {
    assert.equal(verifiedProviderRoute(gw, 'openai-responses', 'openai', 'https://api.openai.com/v1' + tail, { provider_upstreams: native }), undefined);
  }
});
