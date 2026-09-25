import assert from 'node:assert/strict';
import test from 'node:test';
import { MiddlewareRuntime } from '../dist/middleware/index.js';

const replacement = { transform_id: 'caveman.engine.log.v1', reused: false };
const outcome = (status, reason, replacements = []) => ({ status, reason, replacements,
  plan: null, request: null, cacheContinuity: 'unavailable' });

test('native call reports distinguish five outcomes without input capture or I/O', async () => {
  const received = [], runtime = new MiddlewareRuntime({ onReport: value => { received.push(value); },
    fetch() { throw new Error('report must not access transport'); } });
  assert.equal(runtime.lastReport, null);
  runtime.report(outcome('optimized', 'eligible', [replacement]), { adapter: 'native-test', logicalCallId: 'logical-1', attemptId: 'attempt-1' });
  runtime.report(outcome('optimized', 'eligible', [{ ...replacement, reused: true }]));
  runtime.report(null, { reason: 'unsupported_shape' });
  runtime.report(outcome('record', 'record'));
  const disabled = new MiddlewareRuntime({ mode: 'off', onReport: value => { received.push(value); },
    fetch() { throw new Error('off report must not access transport'); } });
  disabled.report(outcome('optimized', 'eligible', [replacement]));
  assert.deepEqual(received.map(value => value.status), ['applied', 'reused', 'skipped', 'recorded', 'disabled']);
  assert.deepEqual(received.map(value => value.transform_ids), [['caveman.engine.log.v1'], ['caveman.engine.log.v1'], [], [], []]);
  assert.deepEqual(received.map(value => value.replacement_count), [1, 1, 0, 0, 0]);
  assert.deepEqual(received.map(value => value.reused_count), [0, 1, 0, 0, 0]);
  assert.equal(received[0].attempt_id, 'attempt-1');
  assert.equal(received[0].logical_call_id, 'logical-1');
  assert.equal(disabled.lastReport.reason, 'disabled');
  assert.equal(runtime.lastReport, received[3]);
  assert.ok(received.every(value => Object.isFrozen(value) && Object.isFrozen(value.transform_ids)));
  assert.throws(() => { received[0].transform_ids.push('forged'); }, TypeError);
  assert.throws(() => { received[0].status = 'forged'; }, TypeError);
  runtime.close(); disabled.close();
});

test('report sinks cannot alter native calls or create content-shaped diagnostics', async () => {
  const secret = 'source text with whitespace and api-key=must-not-log';
  const runtime = new MiddlewareRuntime({ onReport: () => { throw new Error(secret); } });
  const report = runtime.report(null, { reason: secret, adapter: secret, attemptId: secret, logicalCallId: secret });
  assert.equal(report.reason, 'unknown_reason');
  assert.deepEqual([report.adapter, report.attempt_id, report.logical_call_id], [null, null, null]);
  assert.ok(!JSON.stringify(report).includes('must-not-log'));
  const asyncSink = new MiddlewareRuntime({ onReport: async () => { throw new Error(secret); } });
  assert.equal(asyncSink.report().status, 'skipped');
  await new Promise(resolve => setImmediate(resolve));
  runtime.close(); asyncSink.close();
});

test('prepared SDK plans never claim their native patches were applied', async () => {
  const events = [], runtime = new MiddlewareRuntime({ mode: 'off', onReport: value => { events.push(value); } });
  const prepared = await runtime.optimize({ scope: {}, adapter: {}, candidates: [], manifest: [] });
  assert.equal(prepared.status, 'off');
  assert.deepEqual(events, []);
  runtime.report(prepared);
  assert.equal(events.length, 1);
  assert.equal(events[0].status, 'disabled');
  runtime.close();
});
