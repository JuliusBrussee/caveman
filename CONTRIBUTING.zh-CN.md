> **中文** | [English](./CONTRIBUTING.md)

# 贡献 caveman

感谢考虑贡献。穴居是一项多代理技能，让 30+ AI 编程代理以压缩穴居语说话。大多数贡献属于三类之一：

1. **编辑技能文案** — 改变穴居说话方式、强度级别行为、斜杠命令触发逻辑。
2. **添加新代理** — 将新编辑器/CLI/IDE 接入统一安装器。
3. **修复 hooks 或安装器** — Claude Code hooks、Node 安装器、仓库初始化脚本。

穴居喜简洁。小而专注 PR > 大面积重写。

---

## 快速定位

本仓库通过不同分发机制将一项核心技能（caveman）外加若干子技能（caveman-commit、caveman-review、caveman-compress、cavecrew-*）分发给众多代理（Claude Code 插件、Codex 插件、Gemini 扩展、Cursor/Windsurf/Cline 规则文件、长尾用 `npx skills`）。单个 Node 安装器 `bin/install.js` 检测用户机器上装了哪些代理并为每个安装正确的组件。

单一数据源位于仓库**顶层**。代理特定副本位于 `plugins/caveman/` 及类似镜像目录——这些由 **CI 重建**，编辑会被覆盖。

---

## 编辑什么（单一数据源）

| 我想改... | 编辑这个文件 |
|---|---|
| 穴居行为（强度级别、语调、规则） | `skills/caveman/SKILL.md` |
| 穴居提交消息格式 | `skills/caveman-commit/SKILL.md` |
| 穴居代码审查格式 | `skills/caveman-review/SKILL.md` |
| 穴居压缩逻辑 | `skills/caveman-compress/SKILL.md` 和 `skills/caveman-compress/scripts/` |
| 穴居快速参考卡 | `skills/caveman-help/SKILL.md` |
| Cavecrew 决策指南（何时委派子代理） | `skills/cavecrew/SKILL.md` |
| cavecrew 子代理定义 | `agents/cavecrew-investigator.md`、`agents/cavecrew-builder.md`、`agents/cavecrew-reviewer.md` |
| 自动激活规则体（Cursor/Windsurf/Cline/Copilot） | `src/rules/caveman-activate.md` |
| 添加新代理支持 | `bin/install.js`（PROVIDERS 数组） |
| 仓库初始化脚本（将规则文件放入用户仓库） | `src/tools/caveman-init.js` |
| Claude Code hooks | `src/hooks/caveman-activate.js`、`src/hooks/caveman-mode-tracker.js`、`src/hooks/caveman-config.js`、`src/hooks/caveman-statusline.sh`、`src/hooks/caveman-statusline.ps1` |
| settings.json 读写辅助 | `bin/lib/settings.js` |
| MCP shrink 服务器 | `src/mcp-servers/caveman-shrink/` |

就这些。路径中含 `SKILL.md` 的其他 markdown 文件都是副本。

---

## 不要编辑什么（CI 生成的镜像）

对这些文件的编辑会被下次 CI 运行抹掉。`.github/workflows/sync-skill.yml` 作业在每次推送到 `main` 时从上述数据源重建它们。

| 路径 | 重建来源 |
|------|---------|
| `plugins/caveman/skills/caveman/SKILL.md` | `skills/caveman/SKILL.md` |
| `plugins/caveman/skills/caveman-compress/{SKILL.md, scripts/}` | `skills/caveman-compress/{SKILL.md, scripts/}` |
| `plugins/caveman/skills/cavecrew/SKILL.md` | `skills/cavecrew/SKILL.md` |
| `plugins/caveman/agents/cavecrew-*.md` | `agents/cavecrew-*.md` |
| `dist/caveman.skill` | `skills/caveman/` 的 ZIP（gitignored；每次推 `main` CI 重建） |

`caveman-commit`、`caveman-review`、`caveman-help` 和 `caveman-stats` CI **不**镜像到 `plugins/caveman/skills/`。Claude Code 通过独立 hook + skill 安装路径访问它们，`npx skills` 将其传给其他代理。如果你看到 `plugins/caveman/skills/caveman-stats/` 已入库，将其视为遗留手工副本——`.github/workflows/sync-skill.yml` 工作流不碰它。

有疑问时：文件在 `plugins/`、`dist/` 或任何代理 dotdir 镜像下就是构建产物。改顶层源文件。

---

## 添加新代理

统一 Node 安装器 `bin/install.js` 是受支持代理列表的**唯一数据源**。README 和 `INSTALL.md` 的安装表手工镜像——仓库根目录的 bash 和 PowerShell 薄壳仅转发到它。

1. 确认代理有分发路径。以下之一：
   - 在上游 [vercel-labs/skills](https://github.com/vercel-labs/skills) 中有 profile slug（最常见），或
   - 有我们可以对接的原生插件/扩展/规则文件机制。
2. 向 `bin/install.js` 的 `PROVIDERS` 数组追加一行。每行需要：
   - `id` — 短 kebab-case 标识符（如 `windsurf`）
   - `label` — 人类可读名称（如 `Windsurf`）
   - `mech` — 分发机制（`plugin`、`extension`、`rules-file`、`skills-cli` 等）
   - `detect` — 子句规格如 `command:foo||dir:$HOME/x` 描述如何检测代理
   - `profile` — vercel-labs/skills 的 slug（如适用）
   - `soft: true` — 检测仅基于配置目录时设置（尽力而为）
3. 运行 `node bin/install.js --list` 确认新行渲染正确。Soft probe 应显示为 `(soft)`。
4. 在 `README.md` 和 `INSTALL.md` 的安装表中添加一行。
5. 无需 CI 改动 — 工作流自动重读 `bin/install.js`。

Slug 错了？`npx skills add` 在安装**运行时**失败，而非安装脚本加载时。合并前务必对照 vercel-labs/skills README 验证 slug。

---

## 添加新技能

1. 创建 `skills/<name>/SKILL.md` 带 frontmatter：
   ```yaml
   ---
   name: <name>
   description: <一句描述，现在时>
   ---
   ```
2. 创建 `skills/<name>/README.md` — 面向用户摘要、安装提示、示例。
3. 若技能附带辅助脚本，添加 `skills/<name>/scripts/`（Python 或 Node）。
4. 若技能应放在 Claude Code 插件中，在 `.github/workflows/sync-skill.yml` 中添加同步步骤以便 CI 镜像到 `plugins/caveman/skills/<name>/`。
5. 若用户可通过斜杠命令调用，在 `README.md` 和 `INSTALL.md` 的斜杠命令表中添加一行。
6. 若想让评估工具链评分，在 `evals/prompts/en.txt` 中添加评估提示词。

---

## 运行测试

```bash
# 安装器单元 + e2e 测试（Node）
npm test

# 压缩技能安全测试（Python）
python3 -m unittest tests.test_compress_safety

# 仓库初始化测试
node tests/test_caveman_init.js

# 标志文件符号链接安全测试
node tests/test_symlink_flag.js
```

CI 在每个 PR 上运行以上全部。若任何测试依赖网络或外部 SDK，在依赖缺失时必须干净跳过——不要让可选凭据阻塞整个测试套件。

---

## 运行基准测试和评估

基准测试调用真实 Claude API 并记录原始 token 数：

```bash
uv run python benchmarks/run.py     # 需要 ANTHROPIC_API_KEY 在 .env.local 中
```

评估是三臂离线工具链（`__baseline__`、`__terse__`、每个技能）：

```bash
python evals/llm_run.py             # 重新生成 evals/snapshots/results.json
python evals/measure.py             # 读取快照，打印 token 差值
```

快照提交到 git。仅在 `SKILL.md` 或 `evals/prompts/en.txt` 变更时重新生成。`README.md` 和所有文档中的数字来自真实运行——绝不编造或四舍五入。

---

## PR 指南

- **Conventional Commits** 作为提交主题格式。格式见 `skills/caveman-commit/SKILL.md`。
- **每个 PR 一个关注点。** README 措辞修改和安装器修复应分开 PR。
- **更新 `package.json` `files`** 如果你添加了安装器需要发布到 npm 的新顶层目录。数组外的文件不会被发布。
- **任何 `SKILL.md` 的文案变更**请展示 before/after。一句话说明新措辞为什么更好。
- **提及 CI 同步。** 如果你编辑了单一数据源文件，注明："CI 将在合并时重同步 `plugins/caveman/skills/...`。"

PR 描述不用长。穴居风即可。就说改了什么，为什么。

---

## 代码风格

几条踩过坑的不变量。守住。

- **Hooks 必须在文件系统错误上静默失败。** `try/catch` 吞掉错误在这里是对的。抛出异常的 hook 会阻塞 Claude Code 会话启动——那是面向用户的故障。现有模式见 `src/hooks/caveman-activate.js`。
- **settings.json 读写通过 `bin/lib/settings.js`。** 它能容忍 JSONC 注释。直接 `JSON.parse` 用户 `settings.json` 会在一个 `// 注释` 上崩溃。
- **写入前校验 hook 条目。** 使用 `bin/lib/settings.js` 中的 `validateHookFields()`。Claude Code 的 Zod schema 在单个错误 hook 条目上会静默丢弃**整个** `settings.json`——一次格式异常的写入破坏用户全部配置。
- **通过 `src/hooks/caveman-config.js` 的 `safeWriteFlag()` 进行符号链接安全的标志写入。** 标志文件位于 `$CLAUDE_CONFIG_DIR/` 下的可预测路径；没有 `O_NOFOLLOW` 和父目录符号链接检查，本地攻击者可覆盖用户能写的任意文件。
- **尊重 `CLAUDE_CONFIG_DIR`。** Hooks、安装器和状态行脚本必须按此处理——绝不硬编码 `~/.claude`。
- **仓库根目录的 `install.sh` 和 `install.ps1` 是 30 行的薄壳**，转发到 `bin/install.js`。不要重新添加按 OS 的安装逻辑。那是引号 bug 的来源。

---

## 想法

入门任务见 [标记为 `good first issue` 的 issues](../../issues?q=label%3A%22good+first+issue%22)。
或在 `src/hooks/`、`bin/`、`src/tools/` 中 grep `TODO` / `FIXME` —— 每个都是有效线索。

穴居喜贡献。你带石来，穴居垒之。石堆渐大。脑仍大。

---
<sub>翻译同步至英文版 commit: `0d95a81` (2026-07-28)</sub>
