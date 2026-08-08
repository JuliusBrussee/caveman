<p align="center">
  <img src="docs/assets/caveman-logo-banner.png" alt="Caveman" width="720">
</p>

<p align="center">
  <strong>多言何用，少言足矣</strong>
</p>

<p align="center">
  让你的 AI 编程助手像穴居人一样说话。<br>
  同答，<strong>减65%出词</strong>。脑仍大。嘴小。
</p>

<p align="center">
  <a href="https://github.com/JuliusBrussee/caveman/stargazers"><img src="https://img.shields.io/github/stars/JuliusBrussee/caveman?style=flat&color=yellow" alt="Stars"></a>
  <a href="./INSTALL.zh-CN.md"><img src="https://img.shields.io/badge/works_with-30%2B_agents-orange?style=flat" alt="30+ agents"></a>
  <a href="https://github.com/JuliusBrussee/caveman/commits/main"><img src="https://img.shields.io/github/last-commit/JuliusBrussee/caveman?style=flat" alt="Last commit"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/JuliusBrussee/caveman?style=flat" alt="License"></a>
  <a href="./README.md"><img src="https://img.shields.io/badge/lang-English-blue?style=flat" alt="English"></a>
</p>

<p align="center">
  <a href="#before--after">效果速览</a> ·
  <a href="#install">安装</a> ·
  <a href="#pick-your-grunt">强度级别</a> ·
  <a href="#what-you-get">功能列表</a> ·
  <a href="#benchmarks">基准测试</a> ·
  <a href="#the-whole-cave">生态</a> ·
  <a href="#caveman-2">Caveman 2</a>
</p>

> **中文** | [English](./README.md)

---

穴居是 [Claude Code](https://docs.anthropic.com/en/docs/claude-code)、Codex、Gemini、Cursor、Windsurf、Cline、Copilot 等 30+ 代理的技能/插件。一装永逸。代理去掉废话，以紧凑穴居语回复——代码、命令、错误逐字节保留。每次回复节省输出 token，永久有效。

## Before / After

<table>
<tr>
<th width="50%">🗣️ 普通代理 — 69 token</th>
<th width="50%"><img src="docs/assets/dancing-rock.svg" width="18" height="18" alt=""> 穴居代理 — 19 token</th>
</tr>
<tr>
<td valign="top">

> The reason your React component is re-rendering is likely because you're creating a new object reference on each render cycle. When you pass an inline object as a prop, React's shallow comparison sees it as a different object every time, which triggers a re-render. I'd recommend using useMemo to memoize the object.

</td>
<td valign="top">

> New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`.

</td>
</tr>
<tr>
<td valign="top">

> Sure! I'd be happy to help you with that. The issue you're experiencing is most likely caused by your authentication middleware not properly validating the token expiry. Let me take a look and suggest a fix.

</td>
<td valign="top">

> Bug in auth middleware. Token expiry check use `<` not `<=`. Fix:

</td>
</tr>
</table>

同解法。三分一词量。技术细节全保留。

```
┌────────────────────────────────────────────┐
│   输出 token 节省      █████████       65% │
│   输入 token 节省      ░░░░░░░░░         0% │
│   技术准确度           █████████      100% │
│   气场                 █████████      爆表 │
└────────────────────────────────────────────┘
```

穴居不减脑。穴居缩*嘴*。减代理**所说**，不减代理所知。

## Install

**一命令。搜遍本机全部Agent。逐一安装。**

```bash
# macOS · Linux · WSL · Git Bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash
```

```powershell
# Windows · PowerShell 5.1+
irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.ps1 | iex
```

约30秒。需 Node ≥18。未装Agent自动跳过。可安全重复运行。

> [!TIP]
> **开启：** 输入 `/caveman` 或说 *"talk like caveman"*。**关闭：** 说 *"normal mode"*。Claude Code、Codex、Gemini 上对话首条即生效，无需命令。

<details>
<summary><strong>单 Agent 安装，或任意 30+ 种之一</strong></summary>

<br>

每Agent有自有路径（插件、扩展、规则文件或 `npx skills add`）。完整矩阵、全部标志、预演、卸载详见 **[INSTALL.zh-CN.md](./INSTALL.zh-CN.md)**。几个常见方式：

```bash
# Claude Code 插件
claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman

# Gemini CLI 扩展
gemini extensions install https://github.com/JuliusBrussee/caveman

# Cursor / Windsurf / Cline / Codex / 30+ 更多，通过 skills 注册表
npx skills add JuliusBrussee/caveman -a cursor
```

**安装失败？** 在本仓库打开你的 Agent，说：*"Read CLAUDE.md and INSTALL.md, install caveman for me."* 代理读仓库，代理修己脑。蛇吞尾。

</details>

## Pick your grunt

六档强度。随时 `/caveman <level>` 切换。级别保持到更改或会话结束。

| 级别 | 同句话，压缩版 |
|---|---|
| *普通代理* | 你应该把对象包装在 `useMemo` 里，因为每次渲染都会创建新的引用。 |
| `lite` | 对象用`useMemo`包装。每次渲染建新引用。 |
| `full` *(默认)* | 每次渲染建新引用。对象用`useMemo`。 |
| `ultra` | 每渲新引用。`useMemo`之。 |
| `wenyan` | 每渲染出新引用，故宜以`useMemo`裹之——文言文呈现，更为精短。 |

> [!NOTE]
> **保持母语。** 穴居不换语种。你写中文，穴居用中文回复。你写英文，穴居英文回复。压*风格*，不翻译。`wenyan` 模式例外：文言文每 token 承载最多含义。

## What you get

| 命令 | 功能 |
|---|---|
| `/caveman [lite\|full\|ultra\|wenyan]` | 压缩每次回复。级别持续整个会话。 |
| `/caveman-commit` | Conventional Commits 格式，≤50 字主题。说原因，不重复内容。 |
| `/caveman-review` | 一行 PR 评论：`L42: 🔴 bug: user 为空。加守卫。` |
| `/caveman-stats` | 真实会话 token 用量，累计节省，美元。`--share` 生成可分享文本。 |
| `/caveman-compress <file>` | 将记忆文件（如 `CLAUDE.md`）重写为穴居语。**此后每次会话**省约 46% 输入 token。代码、URL、路径逐字节保留。 |
| `caveman-shrink` | MCP 中间件。包裹任意 MCP 服务器，压缩工具描述。 [npm](https://www.npmjs.com/package/caveman-shrink)。 |
| `cavecrew-*` | 穴居子代理（调查员、构建员、审查员）。比普通版省约 60% token，主上下文更持久。 |

> [!TIP]
> Claude Code 状态行显示 `[CAVEMAN] ⛏ 12.4k` —— 你的累计 token 节省，每次 `/caveman-stats` 更新。设 `CAVEMAN_STATUSLINE_SAVINGS=0` 关闭。

## Benchmarks

Claude API 实测 token 数。10 个任务平均 **65% 输出减少**（范围 22–87%），对比默认冗长回复。仅输出 token，可复现数据在 [`benchmarks/`](./benchmarks/) 和 [`evals/`](./evals/)。

<!-- BENCHMARK-TABLE-START -->
| 任务 | 普通 | 穴居 | 节省 |
|------|-------:|--------:|------:|
| 解释 React 重渲染 bug | 1180 | 159 | 87% |
| 修复认证中间件 token 过期 | 704 | 121 | 83% |
| 设置 PostgreSQL 连接池 | 2347 | 380 | 84% |
| 解释 git rebase vs merge | 702 | 292 | 58% |
| 重构回调为 async/await | 387 | 301 | 22% |
| 微服务 vs 单体架构 | 446 | 310 | 30% |
| 审查 PR 安全问题 | 678 | 398 | 41% |
| Docker 多阶段构建 | 1042 | 290 | 72% |
| 调试 PostgreSQL 竞态条件 | 1200 | 232 | 81% |
| 实现 React 错误边界 | 3454 | 456 | 87% |
| **平均** | **1214** | **294** | **65%** |
<!-- BENCHMARK-TABLE-END -->

> [!IMPORTANT]
> **诚实数字警告。** 穴居仅压缩**输出** token。输入和推理 token 不变，技能本身每轮约增加 1-1.5k 输入 token。因此全会话节省小于输出数字，对本身已简洁的任务可能为净负。真正的收益在**可读性和速度**。成本节省是附加好处。穴居何时赚、何时亏、如何自己测量：**[docs/HONEST-NUMBERS.zh-CN.md](./docs/HONEST-NUMBERS.zh-CN.md)**。

话短不只省钱。2026年3月论文 [*Brevity Constraints Reverse Performance Hierarchies in Language Models*](https://arxiv.org/abs/2604.00025) 测试了 31 个模型，发现限制大模型简短回答**在某些基准上提高准确度约 26 个百分点**。有时字少 = 更对。

<details>
<summary><strong>caveman-compress 实测</strong> — 真实记忆文件，永久减少输入 token</summary>

<br>

| 文件 | 原始 | 压缩 | 节省 |
|---|---:|---:|---:|
| `claude-md-preferences.md` | 706 | 285 | **59.6%** |
| `project-notes.md` | 1145 | 535 | **53.3%** |
| `claude-md-project.md` | 1122 | 636 | **43.3%** |
| `todo-list.md` | 627 | 388 | **38.1%** |
| `mixed-with-code.md` | 888 | 560 | **36.9%** |
| **平均** | **898** | **481** | **46%** |

之后每次会话，该文件加载缩小约 46%。输入 token 永久节省，不止一次回复。

</details>

## The whole cave

<table>
<tr><td>

### <img src="docs/assets/dancing-rock.svg" width="20" height="20" alt=""> 想要整只代理，不止嘴巴？ → caveman-code

本技能缩代理所**说**。**[caveman-code](https://github.com/JuliusBrussee/caveman-code)** 缩**全部**——完整终端编码代理，穴居从头到脚。同任务比 Codex **少用约 2× token**。20+ 供应商，计划模式，自动驾驶目标循环，MIT。

```bash
npm install -g @juliusbrussee/caveman-code
```

[**▶ 试用 caveman-code →**](https://github.com/JuliusBrussee/caveman-code)

</td></tr>
</table>

五个工具，一个理念：**代理做更多，用更少。**

| 仓库 | 压缩对象 |
|------|------|
| [**caveman**](https://github.com/JuliusBrussee/caveman) *(你在此)* | 代理所**说** |
| [**caveman-code**](https://github.com/JuliusBrussee/caveman-code) | **整只代理**，端到端 |
| [**cavemem**](https://github.com/JuliusBrussee/cavemem) | 代理**记住**的内容，跨会话 |
| [**cavekit**](https://github.com/JuliusBrussee/cavekit) | **构建循环**——规约驱动，不猜 |
| [**cavegemma**](https://github.com/JuliusBrussee/finetune-caveman) | 压缩**烘焙入权重**（Gemma 微调） |

<details>
<summary><strong>另附：五个兄弟技能，一次安装</strong></summary>

<br>

[**JuliusBrussee/skills**](https://github.com/JuliusBrussee/skills) — 可用于 Claude Code、Cursor、Gemini、Cline、Copilot、40+ 代理：

| 技能 | 功能 |
|------|------|
| [**caveman**](https://github.com/JuliusBrussee/skills/tree/main/skills/caveman) | 即此。少说，多说。 |
| [**grill-me**](https://github.com/JuliusBrussee/skills/tree/main/skills/grill-me) | Agent 在你建错之前质询你的方案。 |
| [**interface-kit**](https://github.com/JuliusBrussee/skills/tree/main/skills/interface-kit) | 构建好看、快速、人人可用的 UI。 |
| [**junior-to-senior**](https://github.com/JuliusBrussee/skills/tree/main/skills/junior-to-senior) | 对抗式审查。初级进，高级出。 |
| [**loop-factory**](https://github.com/JuliusBrussee/skills/tree/main/skills/loop-factory) | 规约驱动任务循环 — 收件箱 → 活跃 → 归档。 |

```bash
npx skills@latest add JuliusBrussee/skills
```

</details>

<details>
<summary><strong>🦞 教龙虾学简洁 — OpenClaw 集成</strong></summary>

<br>

[**OpenClaw**](https://openclaw.ai) 是自托管网关：一箱多代理，接入 Slack / Discord / iMessage / Telegram。龙虾强。龙虾聪明。龙虾话也多。

同安装脚本，限定一个代理：

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash -s -- --only openclaw
```

仅做两件事，不多：一个穴居技能放入工作区，一个微小标记块追加到 `SOUL.md`（OpenClaw 每轮注入，使龙虾从第一条消息就简洁——无需每会话 `/caveman`）。自定义路径？`OPENCLAW_WORKSPACE=/your/path`。卸载用同一行加 `--uninstall`；其他工作区内容不变。龙虾钳仍锋利。龙虾嘴变小。

</details>

## Caveman 2

**穴居缩词。Caveman 2 使之_可证明_。**

当前节省数字（包括 `/caveman-stats`）为本地估算。Caveman 2 跨团队测量验证——真收据、真仪表盘、真证据证明 token 降了。正在开发中。

[**加入候补名单 → caveman.so**](https://caveman.so)

## How it works

1. 安装将技能文件放入你的代理。
2. 技能告诉代理：去废话，留实质，用片段——但绝不碰代码、命令或错误。
3. Claude Code 上，hook 每次会话写入一个小标志文件，代理从第一条消息就以穴居语对话，无需 `/caveman`。
4. `/caveman-stats` 读取会话日志，统计节省 token，写入状态行数字。
5. `/caveman-compress` 重写记忆文件（如 `CLAUDE.md`）使每次后续会话以更小上下文开始。永久省 token，不止一次。

Hook 架构、文件归属和 CI 同步写于 [CLAUDE.md](./CLAUDE.md)（英文维护者文档）。

## Privacy

穴居无联网。无遥测、无分析、无账户、无后端。安装后零网络调用——技能是提示词，hooks 是本地脚本，`/caveman-stats` 读取已在你磁盘上的日志。安装时的网络请求（GitHub 及各代理自有注册表）详见 [SECURITY.zh-CN.md](./SECURITY.zh-CN.md#privacy--telemetry)。

## Sponsors

穴居永免。赞助者磨石锋利。

<p align="center">
  <a href="https://www.atlascloud.ai">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="docs/assets/atlas-cloud-dark.svg">
      <img src="docs/assets/atlas-cloud.svg" alt="Atlas Cloud" height="32">
    </picture>
  </a>
</p>

<p align="center">
  <a href="https://www.atlascloud.ai"><strong>Atlas Cloud</strong></a> — 全模态 AI 推理平台，一个 API。
</p>

<p align="center">
  <a href="https://github.com/sponsors/JuliusBrussee"><strong>想在此留下你的石头？→ 赞助 caveman</strong></a>
</p>

## Star this repo

穴居省你 token，省你钱。星标无费。公道交易。⭐

[![Star History Chart](https://api.star-history.com/svg?repos=JuliusBrussee/caveman&type=Date)](https://star-history.com/#JuliusBrussee/caveman&Date)

---

<sub>
<strong>文档：</strong>
<a href="./INSTALL.zh-CN.md">安装矩阵</a> ·
<a href="./docs/HONEST-NUMBERS.zh-CN.md">诚实数字</a> ·
<a href="./CONTRIBUTING.zh-CN.md">贡献指南</a> ·
<a href="./CLAUDE.md">维护者指南（英文）</a> ·
<a href="https://github.com/JuliusBrussee/caveman/issues">Issues</a>
<br>
<strong>Julius Brussee 另作：</strong>
<a href="https://github.com/JuliusBrussee/revu-swift">Revu</a> — 本地优先 macOS 学习 App，FSRS 间隔重复（<a href="https://revu.cards">revu.cards</a>）
<br><br>
MIT — 如平原巨象，自由自在。
</sub>

---
<sub>翻译同步至英文版 commit: `0d95a81` (2026-07-28)</sub>
