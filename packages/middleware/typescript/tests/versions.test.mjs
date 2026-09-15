import assert from 'node:assert/strict';
import test from 'node:test';
import { inRange, matchesFramework } from '../dist/versions.js';

test('release ranges accept the tested band and refuse the next major', () => {
  assert.equal(inRange('7.0.94', '7.0.94', '8'), true);
  assert.equal(inRange('7.0.95', '7.0.94', '8'), true);
  assert.equal(inRange('7.14.0', '7.0.94', '8'), true, 'a later minor stays inside the major');
  assert.equal(inRange('7.0.93', '7.0.94', '8'), false, 'below the tested floor');
  assert.equal(inRange('8.0.0', '7.0.94', '8'), false, 'the next major is out');
  assert.equal(inRange('8.0.0-beta.1', '7.0.94', '8'), false, 'a prerelease compares as its release');
  assert.equal(inRange('7', '7.0.94', '8'), false, 'a short version is not silently padded upward');
  assert.equal(inRange('0.124.0', '0.124', '1'), true, 'zero-major bands compare segment by segment');
  assert.equal(inRange('0.123.9', '0.124', '1'), false);
  assert.equal(inRange('1.0.0', '0.124', '1'), false);
});

test('a missing or unparseable version is never in range', () => {
  assert.equal(inRange(null, '1.0', '2'), false);
  assert.equal(inRange('', '1.0', '2'), false);
  assert.equal(inRange('latest', '1.0', '2'), false);
  assert.equal(matchesFramework('@caveman-ai/no-such-framework', '1.0', '2'), false);
});
