# `create-caveman-agent`

Release status: implemented and pack-tested in this repository, but not yet
published to npm. Command below becomes public path when package publishes.

Atomic initializer for `@caveman/agent`.

```bash
npm create caveman-agent@latest my-agent
cd my-agent
npm run doctor
npm run dev
```

Initializer creates one required source file, starter eval, strict build config,
provider choice, and installs dependencies. Exactly one detected provider
credential selects silently. Zero or multiple credentials prompt once. Secrets
are never printed or written.

Noninteractive use:

```bash
npm create caveman-agent@latest my-agent -- --provider anthropic
```

Supported providers: `anthropic`, `openai`, `google`.

Skip dependency installation when another tool owns it:

```bash
npm create caveman-agent@latest my-agent -- --provider openai --no-install
```

Generated eval starts unapproved. Review expected behavior, set
`approved: true`, then run `npm run build`. Local evidence remains `inferred`;
verified savings remain `$0` until active production traffic earns them.
After a locked build, run `npm run check` before deployment. Doctor makes no
provider call; if signed runtime artifacts are absent, follow its single
`caveman setup --install` action.
