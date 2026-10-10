// caveman — opencode 2.x entry point.
//
// opencode 2 dropped the 1.x plugin API: it loads this directory's
// `server.js` and wants a default export `{ id, setup(ctx) }`. opencode 1.x
// never loads this file: the opencode.json entry names this directory, which
// 1.x imports through package.json `main` (plugin.js).
//
// No second implementation: this adapts plugin.js's 1.x hooks to the 2.x API
// (https://opencode.ai/v2/docs/build/plugins/migrate-v1):
//   event                               → ctx.event.subscribe()
//   chat.message                        → ctx.session.hook('prompt')
//   experimental.chat.system.transform  → ctx.session.hook('context')

import { CavemanPlugin } from './plugin.js';

export default {
  id: 'caveman',
  async setup(ctx) {
    const hooks = await CavemanPlugin(ctx);

    const controller = new AbortController();
    (async () => {
      for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
        await hooks.event({ event });
      }
    })().catch(() => {});

    // The prompt text is mutable; plugin.js rewrites it for /caveman status.
    await ctx.session.hook('prompt', async (event) => {
      if (!event.prompt || typeof event.prompt.text !== 'string') return;
      const part = { type: 'text', text: event.prompt.text };
      await hooks['chat.message']({}, { parts: [part] });
      event.prompt.text = part.text;
    });

    await ctx.session.hook('context', async (event) => {
      const system = [];
      await hooks['experimental.chat.system.transform']({}, { system });
      for (const text of system) event.system.push({ type: 'text', text });
    });

    return () => controller.abort();
  },
};
