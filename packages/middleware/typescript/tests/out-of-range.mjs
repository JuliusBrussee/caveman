// Child process for behavior.test.mjs. Its working directory holds fake framework metadata that is out of range. The
// gate reads the copy this package imports (resolved from dist/versions.js, never from the working directory) and, for
// openai/anthropic, the client's own User-Agent: point the first at the fake metadata and patch the second.
// Prints one JSON result per adapter.
import { registerHooks } from 'node:module';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

const fake = JSON.parse(process.env.CAVEMAN_FAKE_VERSIONS);
registerHooks({ resolve(specifier, context, next) {
  const name = Object.keys(fake).find(name => specifier === name || specifier.startsWith(`${name}/`));
  return name && context.parentURL?.endsWith('/dist/versions.js')
    ? { url: pathToFileURL(join(process.cwd(), 'node_modules', name, 'index.js')).href, shortCircuit: true } : next(specifier, context);
} });
for (const name of ['openai', '@anthropic-ai/sdk']) {
  const { default: Client } = await import(name), userAgent = Client.prototype.getUserAgent;
  Client.prototype.getUserAgent = function () { return userAgent.call(this).replace(/\/JS \S+$/, `/JS ${fake[name]}`); };
}
const warnings = [];
console.warn = (...args) => warnings.push(args.join(' '));
const { drivers } = await import('./drivers.mjs');
const { runtimeFixture } = await import('./runtime-fixture.mjs');
const results = {};
for (const name of JSON.parse(process.env.CAVEMAN_ADAPTERS)) {
  const skip = runtimeFixture(), strict = runtimeFixture({ strict: true }), accepted = runtimeFixture();
  try {
    const skipped = await drivers[name].run(skip.runtime);
    const strictRun = await drivers[name].run(strict.runtime).then(result => result.seen, error => `threw ${error?.code ?? error}`);
    const ready = await strict.runtime.ready().then(() => 'ready', error => error?.code ?? String(error));
    const acceptedRun = await drivers[name].run(accepted.runtime, { acceptFrameworkVersion: true });
    results[name] = { skipped, requests: skip.requests.length, reasons: skip.reports.map(report => report.reason), strictRun, ready, accepted: acceptedRun };
  } catch (error) { results[name] = { error: String(error?.stack ?? error) }; }
  finally { for (const f of [skip, strict, accepted]) f.runtime.close(); }
}
process.stdout.write(JSON.stringify({ results, warnings }));
