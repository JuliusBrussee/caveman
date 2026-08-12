# packages/create-caveman-agent

Zero-runtime-dependency npm initializer for `@caveman-ai/agent`. `src/index.ts`
parses provider/install flags, writes scaffold into temporary directory,
installs dependencies by default, then atomically renames into target.

Keep generated project at one required source file. Never print or persist
provider secrets. Ambiguous noninteractive provider selection fails without
partial target. `--no-install` supports callers that manage dependencies.

Run `pnpm --dir public/create-caveman-agent test`.
