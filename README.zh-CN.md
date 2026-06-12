<p align="center">
  <img src="https://em-content.zobj.net/source/apple/391/rock_1faa8.png" width="120" />
</p>

<h1 align="center">caveman</h1>

<p align="center">
  <strong>能少说，何必多 token</strong>
</p>

<p align="center">
  <a href="README.md">English</a> •
  <strong>简体中文</strong>
</p>

<p align="center">
  <a href="https://github.com/JuliusBrussee/caveman/stargazers"><img src="https://img.shields.io/github/stars/JuliusBrussee/caveman?style=flat&color=yellow" alt="Stars"></a>
  <a href="https://github.com/JuliusBrussee/caveman/commits/main"><img src="https://img.shields.io/github/last-commit/JuliusBrussee/caveman?style=flat" alt="Last Commit"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/JuliusBrussee/caveman?style=flat" alt="License"></a>
</p>

<p align="center">
  <a href="#before-after">前后对比</a> •
  <a href="#install">安装</a> •
  <a href="#levels">强度等级</a> •
  <a href="#skills">技能</a> •
  <a href="#benchmarks">基准</a> •
  <a href="#evals">评测</a>
</p>

---

`caveman` 是 [Claude Code](https://docs.anthropic.com/en/docs/claude-code) skill/plugin，也是 Codex 插件。它让 agent 用穴居人风格说话，在保留完整技术准确性的前提下，把 **输出 token 砍掉约 75%**。现在还带 [文言文模式](#wenyan-mode)、[精简 commit](#caveman-commit)、[单行代码评审](#caveman-review)，以及一个 [压缩工具](#caveman-compress)，能在每次会话里把 **输入 token 再砍约 46%**。

起点很简单：大家发现 caveman 说话法能大幅减少 LLM token，又不丢技术信息。于是这仓库把它做成一行命令安装。

<a id="before-after"></a>
## 前后对比

<table>
<tr>
<td width="50%">

### 🗣️ 普通 Claude（69 tokens）

> "The reason your React component is re-rendering is likely because you're creating a new object reference on each render cycle. When you pass an inline object as a prop, React's shallow comparison sees it as a different object every time, which triggers a re-render. I'd recommend using useMemo to memoize the object."

</td>
<td width="50%">

### 🪨 Caveman Claude（19 tokens）

> "New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`."

</td>
</tr>
<tr>
<td>

### 🗣️ 普通 Claude

> "Sure! I'd be happy to help you with that. The issue you're experiencing is most likely caused by your authentication middleware not properly validating the token expiry. Let me take a look and suggest a fix."

</td>
<td>

### 🪨 Caveman Claude

> "Bug in auth middleware. Token expiry check use `<` not `<=`. Fix:"

</td>
</tr>
</table>

**同样修复。少 75% 废话。脑子没变小。**

**咕哝强度，你来挑：**

<table>
<tr>
<td width="25%">

#### 🪶 Lite

> "Your component re-renders because you create a new object reference each render. Inline object props fail shallow comparison every time. Wrap it in `useMemo`."

</td>
<td width="25%">

#### 🪨 Full

> "New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`."

</td>
<td width="25%">

#### 🔥 Ultra

> "Inline obj prop → new ref → re-render. `useMemo`."

</td>
<td width="25%">

#### 📜 文言文

> "物出新參照，致重繪。useMemo Wrap之。"

</td>
</tr>
</table>

**同样答案。多少词，你决定。**

```
┌─────────────────────────────────────┐
│  TOKENS SAVED          ████████ 75% │
│  TECHNICAL ACCURACY    ████████ 100%│
│  SPEED INCREASE        ████████ ~3x │
│  VIBES                 ████████ OOG │
└─────────────────────────────────────┘
```

- **响应更快**：要生成的 token 更少，速度自然更快
- **更好读**：没有大段空话，直接给答案
- **准确性不变**：技术信息都在，只是去掉废话（[论文也这么说](https://arxiv.org/abs/2604.00025)）
- **更省钱**：输出 token 平均少约 71%，成本也跟着降
- **更好玩**：每次 code review 都像冷笑话现场

<a id="install"></a>
## 安装

选你的 agent。一条命令。完事。

| Agent | 安装方式 |
|-------|---------|
| **Claude Code** | `claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman` |
| **Codex** | Clone repo → `/plugins` → 搜索 "Caveman" → Install |
| **Gemini CLI** | `gemini extensions install https://github.com/JuliusBrussee/caveman` |
| **Cursor** | `npx skills add JuliusBrussee/caveman -a cursor` |
| **Windsurf** | `npx skills add JuliusBrussee/caveman -a windsurf` |
| **Copilot** | `npx skills add JuliusBrussee/caveman -a github-copilot` |
| **Cline** | `npx skills add JuliusBrussee/caveman -a cline` |
| **其他任意 Agent** | `npx skills add JuliusBrussee/caveman` |

装一次。之后该安装目标里的每次会话都能用。一个石头。够了。

### 你会得到什么

Claude Code、Gemini CLI、以及下面这个仓库内的 Codex 配置都内建自动激活。对其他 agent，`npx skills add` 只会安装 skill，不会安装对应规则/指令文件，所以默认**不会**自动启动 Caveman，除非你再加下面的 always-on 片段。

| 功能 | Claude Code | Codex | Gemini CLI | Cursor | Windsurf | Cline | Copilot |
|---------|:-----------:|:-----:|:----------:|:------:|:--------:|:-----:|:-------:|
| Caveman 模式 | Y | Y | Y | Y | Y | Y | Y |
| 每次会话自动激活 | Y | Y¹ | Y | —² | —² | —² | —² |
| `/caveman` 命令 | Y | Y¹ | Y | — | — | — | — |
| 模式切换（lite/full/ultra） | Y | Y¹ | Y | Y³ | Y³ | — | — |
| 状态栏徽章 | Y⁴ | — | — | — | — | — | — |
| caveman-commit | Y | — | Y | Y | Y | Y | Y |
| caveman-review | Y | — | Y | Y | Y | Y | Y |
| caveman-compress | Y | Y | Y | Y | Y | Y | Y |
| caveman-help | Y | — | Y | Y | Y | Y | Y |

> [!NOTE]
> 各 agent 的自动激活方式不同：Claude Code 用 SessionStart hooks，这个仓库里的 Codex 自用配置走 `.codex/hooks.json`，Gemini 用上下文文件。Cursor/Windsurf/Cline/Copilot 也可以做到 always-on，但 `npx skills add` 只安装 skill，不会安装仓库规则/指令文件。
>
> ¹ Codex 用的是 `$caveman`，不是 `/caveman`。这个仓库自带 `.codex/hooks.json`，所以你在本仓库里跑 Codex 时会自动启动 caveman。安装后的插件本身会给你 `$caveman`；如果想在别的仓库也 always-on，把同样的 SessionStart hook 复制过去即可。`caveman-commit` 和 `caveman-review` 不在 Codex 插件包里，需要直接使用对应 `SKILL.md`。
> ² 如果你想让这些 agent 一开场就激活，把下面 “Want it always on?” 的片段加到系统提示词或规则文件里。
> ³ Cursor 和 Windsurf 会拿到完整 `SKILL.md`，包含全部强度等级。可按需切换模式，但没有 slash command。
> ⁴ Claude Code 支持状态栏徽章，但 plugin 安装只会提示设置。独立 `install.sh` / `install.ps1` 在没有自定义 `statusLine` 时会自动配置。

<details>
<summary><strong>Claude Code — 完整说明</strong></summary>

插件安装会带上 skills 和自动加载 hooks。如果你还没有自定义 `statusLine`，Caveman 会在首次会话时提示 Claude 帮你设置状态栏徽章。

```bash
claude plugin marketplace add JuliusBrussee/caveman
claude plugin install caveman@caveman
```

**独立 hooks 安装（不走 plugin）：** 如果你不想用插件系统：

```bash
# macOS / Linux / WSL
bash <(curl -s https://raw.githubusercontent.com/JuliusBrussee/caveman/main/hooks/install.sh)

# Windows (PowerShell)
irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/hooks/install.ps1 | iex
```

也可以在本地 clone 后执行：`bash hooks/install.sh` / `powershell -File hooks\install.ps1`

卸载：`bash hooks/uninstall.sh` 或 `powershell -File hooks\uninstall.ps1`

**状态栏徽章：** 会在 Claude Code 状态栏显示 `[CAVEMAN]`、`[CAVEMAN:ULTRA]` 等。

- **插件安装：** 如果你还没有自定义 `statusLine`，Claude 应该会在首次会话时提示你配置
- **独立安装：** `install.sh` / `install.ps1` 会自动配置，前提是你没有已有状态栏配置
- **已有自定义状态栏：** 安装器不会覆盖你的现有配置。合并方式看 [`hooks/README.md`](hooks/README.md)

</details>

<details>
<summary><strong>Codex — 完整说明</strong></summary>

**macOS / Linux：**
1. Clone 仓库 → 在仓库目录打开 Codex → `/plugins` → 搜索 "Caveman" → Install

**Windows：**
1. 先启用 symlink：`git config --global core.symlinks true`（需要 Developer Mode 或管理员权限）
2. Clone 仓库 → 打开 VS Code → Codex Settings → Plugins → 在本地 marketplace 里找到 "Caveman" → Install → Reload Window

本仓库也自带 `.codex/hooks.json`，所以你在这个仓库里运行 Codex 时会自动激活 caveman。安装后的插件会给你 `$caveman`；如果你想在别的仓库也 always-on，把同样的 SessionStart hook 加过去即可。

</details>

<details>
<summary><strong>Gemini CLI — 完整说明</strong></summary>

```bash
gemini extensions install https://github.com/JuliusBrussee/caveman
```

更新：`gemini extensions update caveman` · 卸载：`gemini extensions uninstall caveman`

它通过 `GEMINI.md` 上下文文件自动激活。还附带 Gemini 自定义命令：
- `/caveman`：切换强度等级（lite/full/ultra/wenyan）
- `/caveman-commit`：生成精简 commit message
- `/caveman-review`：生成单行 code review

</details>

<details>
<summary><strong>Cursor / Windsurf / Cline / Copilot — 完整说明</strong></summary>

`npx skills add` 只安装 skill 文件，不会安装 agent 的规则/指令文件，所以 caveman 默认不会自动启动。若想 always-on，把下面的 “Want it always on?” 片段加到规则或系统提示词里。

| Agent | 命令 | 不会安装的文件 | 模式切换 | Always-on 放置位置 |
|-------|---------|--------------|:--------------:|--------------------|
| Cursor | `npx skills add JuliusBrussee/caveman -a cursor` | `.cursor/rules/caveman.mdc` | Y | Cursor rules |
| Windsurf | `npx skills add JuliusBrussee/caveman -a windsurf` | `.windsurf/rules/caveman.md` | Y | Windsurf rules |
| Cline | `npx skills add JuliusBrussee/caveman -a cline` | `.clinerules/caveman.md` | — | Cline rules 或 system prompt |
| Copilot | `npx skills add JuliusBrussee/caveman -a github-copilot` | `.github/copilot-instructions.md` + `AGENTS.md` | — | Copilot custom instructions |

卸载：`npx skills remove caveman`

Copilot 支持 Chat、Edits、Coding Agent。

</details>

<details>
<summary><strong>其他 agent（opencode、Roo、Amp、Goose、Kiro，以及 40+ 更多）</strong></summary>

[npx skills](https://github.com/vercel-labs/skills) 支持 40+ agent：

```bash
npx skills add JuliusBrussee/caveman           # 自动检测 agent
npx skills add JuliusBrussee/caveman -a amp
npx skills add JuliusBrussee/caveman -a augment
npx skills add JuliusBrussee/caveman -a goose
npx skills add JuliusBrussee/caveman -a kiro-cli
npx skills add JuliusBrussee/caveman -a roo
# ... and many more
```

卸载：`npx skills remove caveman`

> **Windows 提示：** `npx skills` 默认用 symlink。若 symlink 失败，加 `--copy`：`npx skills add JuliusBrussee/caveman --copy`

**重要：** 这些 agent 没有 hook 系统，所以 caveman 不会自动启动。每次会话请说 `/caveman` 或 "talk like caveman" 来激活。

**Want it always on?** 把下面片段贴到 agent 的 system prompt 或 rules file 里，caveman 会从第一条消息开始生效，每次会话都开：

```
Terse like caveman. Technical substance exact. Only fluff die.
Drop: articles, filler (just/really/basically), pleasantries, hedging.
Fragments OK. Short synonyms. Code unchanged.
Pattern: [thing] [action] [reason]. [next step].
ACTIVE EVERY RESPONSE. No revert after many turns. No filler drift.
Code/commits/PRs: normal. Off: "stop caveman" / "normal mode".
```

建议放这里：

| Agent | 文件 |
|-------|------|
| opencode | `.config/opencode/AGENTS.md` |
| Roo | `.roo/rules/caveman.md` |
| Amp | 你的 workspace system prompt |
| 其他 | 该 agent 的 system prompt 或 rules file |

</details>

## 使用

这样触发：
- `/caveman` 或 Codex 的 `$caveman`
- "talk like caveman"
- "caveman mode"
- "less tokens please"

这样关闭：`stop caveman` 或 `normal mode`

<a id="levels"></a>
### 强度等级

| 等级 | 触发方式 | 作用 |
|-------|---------|------------|
| **Lite** | `/caveman lite` | 去掉废话，保留完整语法。更职业，更克制 |
| **Full** | `/caveman full` | 默认 caveman。去冠词、用短句、碎片化表达 |
| **Ultra** | `/caveman ultra` | 极限压缩。电报体。能缩就缩 |

<a id="wenyan-mode"></a>
### 文言文模式

古典汉语式压缩。技术准确性不变，但使用人类历史上最省 token 的书面风格之一。

| 等级 | 触发方式 | 作用 |
|-------|---------|------------|
| **Wenyan-Lite** | `/caveman wenyan-lite` | 半文半白。语法还完整，废话已去 |
| **Wenyan-Full** | `/caveman wenyan` | 完整文言文。极致古典压缩 |
| **Wenyan-Ultra** | `/caveman wenyan-ultra` | 更狠。预算有限的古代学者模式 |

模式会保持，直到你主动切换或会话结束。

<a id="skills"></a>
## Caveman 技能

| Skill | 作用 | 触发方式 |
|-------|-----------|---------|
| **caveman-commit** | 精简 commit message。Conventional Commits。标题 ≤50 字符。多写 why，少写 what。 | `/caveman-commit` |
| **caveman-review** | 单行 PR 评论：`L42: 🔴 bug: user null. Add guard.` 不绕圈子。 | `/caveman-review` |
| **caveman-help** | 快速参考卡。模式、技能、命令一页看完。 | `/caveman-help` |

<a id="caveman-compress"></a>
### caveman-compress

Caveman 让 Claude **说**得更省 token。**Compress** 让 Claude **读**得更省 token。

你的 `CLAUDE.md` 会在**每次会话启动**时加载。Caveman Compress 会把记忆文件改写成 caveman 风格，让 Claude 读更少 token，同时保留给人看的原文备份。

```
/caveman:compress CLAUDE.md
```

```
CLAUDE.md           ← 压缩版（Claude 每次会话都会读，token 更少）
CLAUDE.original.md  ← 人类可读备份（你看、你改）
```

| 文件 | 原始 | 压缩后 | 节省 |
|------|----------:|----------:|------:|
| `claude-md-preferences.md` | 706 | 285 | **59.6%** |
| `project-notes.md` | 1145 | 535 | **53.3%** |
| `claude-md-project.md` | 1122 | 636 | **43.3%** |
| `todo-list.md` | 627 | 388 | **38.1%** |
| `mixed-with-code.md` | 888 | 560 | **36.9%** |
| **平均** | **898** | **481** | **46%** |

代码块、URL、文件路径、命令、标题、日期、版本号这类技术内容会原样保留。只有 prose 会被压缩。更多细节见 [caveman-compress README](caveman-compress/README.md)。[安全说明](./caveman-compress/SECURITY.md)：Snyk 会把它标成 High Risk，因为它看到了 subprocess / file I/O 模式，但这是误报。

<a id="benchmarks"></a>
## 基准

这些数字来自真实 Claude API 调用（[你也可以自己复现](benchmarks/)）：

<!-- BENCHMARK-TABLE-START -->
| 任务 | 普通模式（tokens） | Caveman（tokens） | 节省 |
|------|---------------:|----------------:|------:|
| 解释 React 重渲染 bug | 1180 | 159 | 87% |
| 修复认证中间件 token 过期判断 | 704 | 121 | 83% |
| 配置 PostgreSQL 连接池 | 2347 | 380 | 84% |
| 解释 git rebase 与 merge | 702 | 292 | 58% |
| 把 callback 重构成 async/await | 387 | 301 | 22% |
| 架构讨论：微服务 vs 单体 | 446 | 310 | 30% |
| 审查 PR 的安全问题 | 678 | 398 | 41% |
| Docker 多阶段构建 | 1042 | 290 | 72% |
| 调试 PostgreSQL race condition | 1200 | 232 | 81% |
| 实现 React error boundary | 3454 | 456 | 87% |
| **平均** | **1214** | **294** | **65%** |

*范围：不同 prompt 间节省 22%–87%。*
<!-- BENCHMARK-TABLE-END -->

> [!IMPORTANT]
> Caveman 只影响输出 token，不影响 thinking/reasoning token。Caveman 不让脑子变小，只让嘴变小。最大收益是 **更快、更好读**，省钱是额外奖励。

2026 年 3 月论文 ["Brevity Constraints Reverse Performance Hierarchies in Language Models"](https://arxiv.org/abs/2604.00025) 发现：要求大模型简短作答，在某些基准上**能把准确率提高 26 个百分点**，甚至完全反转模型性能排序。话多不一定更强。有时少说，反而更准。

<a id="evals"></a>
## 评测

Caveman 不只是喊 75%。Caveman **真测**。

`evals/` 目录里有一个三臂评测框架，测的是真实 token 压缩效果，而且用了合理对照组。不是简单比“啰嗦 Claude vs skill”，而是比“普通简洁提示 vs skill”。因为如果拿 caveman 去和冗长回答比，会把“skill 效果”和“单纯要求简洁”的效果混在一起。那叫作弊。Caveman 不作弊。

```bash
# 运行评测（需要 claude CLI）
uv run python evals/llm_run.py

# 读取结果（不需要 API key，本地离线跑）
uv run --with tiktoken python evals/measure.py
```

## 给仓库点星

如果 caveman 帮你省了很多 token、很多钱，那就顺手点个 star。⭐

[![Star History Chart](https://api.star-history.com/svg?repos=JuliusBrussee/caveman&type=Date)](https://star-history.com/#JuliusBrussee/caveman&Date)

## Julius Brussee 的其他项目

- **[Cavekit](https://github.com/JuliusBrussee/cavekit)**：给 Claude Code 用的 specification-driven development。Caveman 语言 → specs → 并行构建 → 工作中的软件。
- **[Revu](https://github.com/JuliusBrussee/revu-swift)**：本地优先的 macOS 学习应用，带 FSRS 间隔重复、卡组、考试和学习指南。[revu.cards](https://revu.cards)

## 许可证

MIT。像平原上的猛犸一样自由。
