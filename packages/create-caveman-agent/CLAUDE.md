# packages/create-caveman-agent

> **Repository routing:** do not continue initializer product work here. Source
> of truth is `JuliusBrussee/caveman-agent-sdk`, local checkout
> `/Users/julb/Desktop/GitHub/caveman-agent-sdk`. This directory is a historical
> consumer copy; edit only for pinned integration, migration/removal, or an
> explicit cross-repo sync.

Zero-runtime-dependency npm initializer for `@caveman-ai/agent`. `src/index.ts`
parses provider/install flags, writes scaffold into temporary directory,
installs dependencies by default, then atomically renames into target.

Keep generated project at one required source file. Never print or persist
provider secrets. Ambiguous noninteractive provider selection fails without
partial target. `--no-install` supports callers that manage dependencies.

Run `pnpm --dir public/create-caveman-agent test`.
