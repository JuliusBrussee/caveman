# Caveman Licensing

Everything in this repository is licensed under the Apache License, Version 2.0,
starting with Caveman 3.0.0. That covers the skill, CLI, SDKs, middleware,
extension, and the whole runtime: Engine, Proxy, Browse, MCP server,
`shrink`, the cavemem Go core, and the shared Go platform.

Caveman Cloud, the hosted service, is separate commercial software. Its source
is not in this repository and this license does not cover it.

## Canonical Files

- Root `LICENSE` is the verbatim Apache License 2.0 text.
- Root `NOTICE` carries the copyright line and third-party attributions.
- Root `LICENSE-MIT` keeps the MIT License text that covered the MIT parts of
  the repository before 3.0.0 (see below).
- Every directory that ships its own `LICENSE` (npm and PyPI packages, the npm
  launchers, the Go modules) carries a byte-identical copy of the root `LICENSE`.

Copyright 2026 Julius Brussee.

## What changed in 3.0.0

Before 3.0.0 the repository was split-licensed. The skill and adoption surfaces
(skill, CLI, SDKs, middleware, contracts, provider catalog, extension shell,
cavemem clients) were MIT. The Engine-linked runtime (`engine/`, `proxy/`,
`rewriter/`, `browse/`, `mcp/`, `shrink/`, the `mem/` Go core,
`shared/platform/`, and the extension's bundled `engine.wasm`) was Business
Source License 1.1 with an Additional Use Grant.

From 3.0.0 on, all of it is Apache-2.0. There is no longer a hosted-service
restriction, Change Date, or commercial license requirement for the code in this
repository.

Releases published before 3.0.0 keep the terms they shipped with. The Apache
license applies to 3.0.0 and later.

Outside contributors wrote code in the formerly MIT parts under MIT terms.
That code stays available under the MIT License, whose notice must travel with
it, so the pre-3.0.0 MIT text is kept verbatim in `LICENSE-MIT` and referenced
from `NOTICE`.

## Contributions

Contributions are inbound=outbound: you license your change under Apache-2.0,
the same terms as the rest of the repository. See `CONTRIBUTING.md`.

## Third-party code

`engine/pixel/` is a Go port of pxpipe (MIT, Copyright (c) 2026
claude-image-proxy contributors) with embedded glyph atlases derived from the
Spleen 5x8 (BSD-2-Clause) and GNU Unifont (OFL-1.1 / GPLv2 with font-embedding
exception) fonts. The upstream MIT and font notices are preserved in
`engine/pixel/NOTICE` and `engine/pixel/assets/`.

`browse/` links MIT-licensed chromedp modules; see `browse/NOTICE`.

The runtime binaries and container image link third-party Go modules (MIT,
BSD-3-Clause, Apache-2.0); every binary release ships their license texts as
`THIRD_PARTY_GO_LICENSES.tar.gz`, and the image under `/licenses/third_party/`.
The CLI contains a port of part of Qwen Code (Apache-2.0) and bundles
MIT-licensed `@clack/prompts` and its dependencies; see `packages/cli/NOTICE`.
The extension ships the Geist fonts under the SIL Open Font License 1.1
(`extension/fonts/GEIST_LICENSE.txt`).

## Trademarks

"Caveman" and Caveman logos are trademarks of Julius Brussee. The Apache
License grants no trademark rights. Nominative use such as "Powered by Caveman"
or "Optimized by Caveman" is allowed when truthful. Naming a product, hosted
service, or fork in a way that implies Caveman sponsorship requires written
permission.

See `TRADEMARKS.md` for the full trademark policy.
