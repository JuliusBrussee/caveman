// Child process for behavior.test.mjs. Its working directory holds a fake application node_modules whose framework
// metadata is out of range; openai/anthropic report the version constant their own module exports, so a resolve hook
// fakes those. Prints one JSON result per adapter.
import { registerHooks } from 'node:module';

const fake = JSON.parse(process.env.CAVEMAN_FAKE_VERSIONS);
registerHooks({ resolve(specifier, context, next) {
  const name = { 'openai/version': 'openai', '@anthropic-ai/sdk/version': '@anthropic-ai/sdk' }[specifier];
  return name && fake[name] ? { url: `data:text/javascript,export const VERSION=${JSON.stringify(fake[name])};`, shortCircuit: true } : next(specifier, context);
} });
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
