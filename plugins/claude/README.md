# Caveman for Claude Code

Caveman adds concise communication skills, lifecycle hooks, and cavecrew
agent profiles to Claude Code. It shortens prose while preserving technical
details. Results depend on the model and task; shorter output does not by
itself establish equal answer quality or lower cost.

## Install

Run these commands in your terminal:

```sh
claude plugin marketplace add JuliusBrussee/caveman
claude plugin install caveman@caveman
```

Restart an existing Claude Code session after installation or an update.

## Use

Use `/caveman`, `/ultracave`, or `/megacave` to select a communication mode.
Use `/caveman off` to disable it.

The plugin also includes workflow skills, the cavecrew investigator, builder
and reviewer agents, and `/caveman-init` for adding activation rules to a
working repository.

## Requirements and boundaries

Lifecycle hooks require Node.js on PATH. They use Node built-in modules and
bundled files; this plugin does not require an npm dependency installation.

Some skills describe optional tools and services that have their own setup
requirements. The `caveman-compress` skill uses Python and a configured model
provider; invoking compression can send file contents to that provider.
Installing this plugin does not configure those optional services.

## Documentation

- [Installation and host-specific setup](https://github.com/JuliusBrussee/caveman/blob/main/INSTALL.md)
- [Skills and project overview](https://github.com/JuliusBrussee/caveman/blob/main/README.md)
- [Measurement limitations](https://github.com/JuliusBrussee/caveman/blob/main/docs/HONEST-NUMBERS.md)
- [Security policy](https://github.com/JuliusBrussee/caveman/blob/main/SECURITY.md)
- [Contribution guidelines](https://github.com/JuliusBrussee/caveman/blob/main/CONTRIBUTING.md)

## Packaging

This directory is generated from canonical repository sources. Its README
comes from `docs/claude-plugin.md`. Contributors should edit the sources and
run `node scripts/build-claude-plugin.mjs`, then include the generated output
in their PR.

See the bundled LICENSE and NOTICE files for licensing information.
