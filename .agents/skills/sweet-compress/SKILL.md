---
name: sweet-compress
description: >
  자연어 메모리 파일(CLAUDE.md, 할 일, 설정 메모)을 압축해서 입력 토큰을 줄인다.
  기술적 내용, 코드, URL, 구조는 전부 보존한다. 압축본이 원본을 덮어쓰고,
  사람이 읽을 수 있는 백업은 FILE.original.md 로 저장한다. 한국어 파일과 영어 파일 모두 처리한다.
  트리거: /sweet-compress 파일경로, 또는 "메모리 파일 압축해줘"
---

# Sweet Compress

## Purpose

Compress natural language files (CLAUDE.md, todos, preferences) into sweet-speak to reduce input tokens. Compressed version overwrites original. Human-readable backup saved as `<filename>.original.md`.

## Trigger

`/sweet-compress <filepath>` or when user asks to compress a memory file.

## Process

1. The compression scripts live in `scripts/` (adjacent to this SKILL.md). If the path is not immediately available, search for `scripts/__main__.py` next to this SKILL.md.

2. From the directory containing this SKILL.md, run:

python3 -m scripts <absolute_filepath>

3. The CLI will:
- detect file type (no tokens)
- call Claude to compress
- validate output (no tokens)
- if errors: cherry-pick fix with Claude (targeted fixes only, no recompression)
- retry up to 2 times
- if still failing after 2 retries: report error to user, leave original file untouched

4. Return result to user

## Compression Rules

### Remove
- Articles: a, an, the
- Filler: just, really, basically, actually, simply, essentially, generally
- Pleasantries: "sure", "certainly", "of course", "happy to", "I'd recommend"
- Hedging: "it might be worth", "you could consider", "it would be good to"
- Redundant phrasing: "in order to" → "to", "make sure to" → "ensure", "the reason is because" → "because"
- Connective fluff: "however", "furthermore", "additionally", "in addition"

### Preserve EXACTLY (never modify)
- Code blocks (fenced ``` and indented)
- Inline code (`backtick content`)
- URLs and links (full URLs, markdown links)
- File paths (`/src/components/...`, `./config.yaml`)
- Commands (`npm install`, `git commit`, `docker build`)
- Technical terms (library names, API names, protocols, algorithms)
- Proper nouns (project names, people, companies)
- Dates, version numbers, numeric values
- Environment variables (`$HOME`, `NODE_ENV`)

### Preserve MEANING (rules files are instructions, not prose)

A compressed `CLAUDE.md` is what the agent obeys next session. These three
classes of word change what the agent DOES, so they survive compression even
when they cost tokens:

- **Polarity** — `never`, `don't`, `avoid`, `no`, 금지, 절대, 마라. "Never mock
  the DB in integration tests" may become "No DB mocking in integration tests";
  it may not become "Mock the DB in integration tests".
- **Conditions and scope** — `unless`, `only`, `except`, `if`, `when`, ~할 때만,
  ~인 경우. "Avoid `any` unless unavoidable" keeps the escape hatch.
- **Order and obligation** — `before`, `after`, `first`, `must`, `always`,
  `required` vs `optional`, 먼저, 반드시, 필수/선택. "Run migration before
  deploy" is not "Run migration, deploy".

Drop the *rationale* only when the rule survives intact. When a rule and its
reason both fit in one short sentence, keep both — a rule with a reason gets
followed more reliably than a bare imperative.

`scripts/validate.py` enforces the polarity half automatically: if a section
carried a prohibition and the compressed section carries no negation at all,
validation fails and the fix pass runs. It cannot check conditions or ordering
— that is on you.

### Preserve Structure
- All markdown headings (keep exact heading text, compress body below)
- Bullet point hierarchy (keep nesting level)
- Numbered lists (keep numbering)
- Tables (compress cell text, keep structure)
- Frontmatter/YAML headers in markdown files

### Compress
- Use short synonyms: "big" not "extensive", "fix" not "implement a solution for", "use" not "utilize"
- Fragments OK: "Run tests before commit" not "You should always run tests before committing"
- Drop "you should", "make sure to", "remember to" — just state the action
- Merge redundant bullets that say the same thing differently
- Keep one example where multiple examples show the same pattern

CRITICAL RULE:
Anything inside ``` ... ``` must be copied EXACTLY.
Do not:
- remove comments
- remove spacing
- reorder lines
- shorten commands
- simplify anything

Inline code (`...`) must be preserved EXACTLY.
Do not modify anything inside backticks.

If file contains code blocks:
- Treat code blocks as read-only regions
- Only compress text outside them
- Do not merge sections around code

## Korean files (한국어 파일)

Detect the file's dominant language and compress in that language. Never translate — a Korean CLAUDE.md stays Korean, an English one stays English.

### Remove (한국어)
- 조사: 은/는/이/가/을/를/에서/으로 — only when meaning stays unambiguous
- 군더더기: 그냥, 진짜, 사실, 약간, 좀, 일단, 아무튼
- 상투어: "~하시면 됩니다", "~해주세요", "참고로", "덧붙이면"
- 완충 표현: 아마, ~인 것 같습니다, ~일 수도 있습니다, ~하는 것이 좋을 것 같습니다
- 접속 군더더기: 그러나, 또한, 게다가, 그리고 나서

### Compress (한국어)
- 개조식 종결: "테스트 실행" not "테스트를 실행해야 합니다"
- 명사형으로 끝내도 OK
- 짧은 동의어: "고치기" not "수정 작업 진행", "쓰기" not "활용하기"
- "~해야 합니다", "반드시 ~하세요", "잊지 마세요" 는 버리고 행동만 남긴다
- 새 약어를 만들지 않는다 (설정→셋, 구현→임플). 토큰 절약 0이고 가독성만 손해

### Never touch (한국어 파일에서도 동일)
Code blocks, inline code, URLs, file paths, commands, English technical terms, API names, environment variables, version numbers. These stay byte-identical regardless of the file's language.

## Pattern

Original (English):
> You should always make sure to run the test suite before pushing any changes to the main branch. This is important because it helps catch bugs early and prevents broken builds from being deployed to production.

Compressed:
> Run tests before push to main. Catch bugs early, prevent broken prod deploys.

Original:
> The application uses a microservices architecture with the following components. The API gateway handles all incoming requests and routes them to the appropriate service. The authentication service is responsible for managing user sessions and JWT tokens.

Compressed:
> Microservices architecture. API gateway route all requests to services. Auth service manage user sessions + JWT tokens.

Original (한국어):
> main 브랜치에 변경사항을 푸시하기 전에는 반드시 테스트 스위트를 실행하셔야 합니다. 이것은 버그를 조기에 발견하고 깨진 빌드가 프로덕션에 배포되는 것을 방지하는 데 도움이 되기 때문에 중요합니다.

Compressed:
> main 에 push 전 테스트 실행. 버그 조기 발견, 깨진 빌드 프로덕션 배포 방지.

Original (한국어):
> 이 애플리케이션은 다음과 같은 컴포넌트로 구성된 마이크로서비스 아키텍처를 사용하고 있습니다. API 게이트웨이가 들어오는 모든 요청을 처리하여 적절한 서비스로 라우팅합니다. 인증 서비스는 사용자 세션과 JWT 토큰을 관리하는 역할을 담당합니다.

Compressed:
> 마이크로서비스 아키텍처. API 게이트웨이가 모든 요청 받아 서비스로 라우팅. 인증 서비스가 사용자 세션 + JWT 토큰 관리.

## Boundaries

- ONLY compress natural language files (.md, .txt, .typ, .typst, .tex, extensionless)
- NEVER modify: .py, .js, .ts, .json, .yaml, .yml, .toml, .env, .lock, .css, .html, .xml, .sql, .sh
- If file has mixed content (prose + code), compress ONLY the prose sections
- If unsure whether something is code or prose, leave it unchanged
- Original file is backed up as FILE.original.md before overwriting
- Never compress FILE.original.md (skip it)
