> **中文** | [English](./INSTALL.md)

# 安装 caveman

一装永逸。适用于你机器上所有 AI 编程代理。

只想让它跑起来？跑一行命令。想了解具体动了什么？往下翻。

## 一行安装

**macOS / Linux / WSL / Git Bash**

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash
```

**Windows (PowerShell 5.1+)**

```powershell
irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.ps1 | iex
```

> 把脚本直接管道进 shell 意味着未审阅就运行。若想先读再跑：`curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh -o install.sh`（审查它）`&& bash install.sh`。安装器从固定的 release tag 下载 hook 文件，并在写入前用提交过的 SHA-256 清单校验。

安装器做了什么：

- 自动检测你机器上所有支持的代理（Claude Code、Cursor、Codex 等）。
- 对每个代理运行其原生安装路径（插件 / 扩展 / 规则文件 / `npx skills add`）。
- 额外接入 Claude Code hooks 和状态行徽章。（`caveman-shrink` MCP 中间件需主动勾选 `--with-mcp-shrink` — 见下方标志表。）
- 跳过未安装的代理。可安全重复运行。端到端约 30 秒。

想在安装前预览？用 `--dry-run`：

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash -s -- --dry-run
```

## 按代理安装

若只想为一个代理安装（或想确知底层具体执行了什么命令），用下表。每行也可作为 `--only <id>` 传给统一安装器。

| Agent | Install command | Auto-activates? |
|---|---|:-:|
| **Claude Code** | `claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman` | 是 |
| **Gemini CLI** | `gemini extensions install https://github.com/JuliusBrussee/caveman` | 是 |
| **opencode** | `node bin/install.js --only opencode` *(或 `npx -y github:JuliusBrussee/caveman -- --only opencode`)* | 是 (plugin + AGENTS.md) |
| **OpenClaw** | `npx -y github:JuliusBrussee/caveman -- --only openclaw` | 是 (workspace skill + SOUL.md) |
| **Hermes Agent** | `npx -y github:JuliusBrussee/caveman -- --only hermes` *(或从克隆 `node bin/install.js --only hermes`)* | 是 (原生技能，加载即启用) |
| **Codex CLI** | `npx skills add JuliusBrussee/caveman -a codex` | 每会话: `/caveman` |
| **Cursor** | `npx skills add JuliusBrussee/caveman -a cursor` | 默认每会话；`--with-init` 写入始终启用规则文件 |
| **Windsurf** | `npx skills add JuliusBrussee/caveman -a windsurf` | 默认每会话；`--with-init` 写入始终启用规则文件 |
| **Cline** | `npx skills add JuliusBrussee/caveman -a cline` | 默认每会话；`--with-init` 写入始终启用规则文件 |
| **GitHub Copilot** *(soft probe)* | `npx -y github:JuliusBrussee/caveman -- --only copilot --with-init` | 通过 `--with-init` 仓库级指令 |
| **Continue** | `npx skills add JuliusBrussee/caveman -a continue` | 否 — 说 `/caveman` |
| **Kilo Code** | `npx skills add JuliusBrussee/caveman -a kilo` | 否 |
| **Roo Code** | `npx skills add JuliusBrussee/caveman -a roo` | 否 |
| **Augment Code** | `npx skills add JuliusBrussee/caveman -a augment` | 否 |
| **Aider Desk** | `npx skills add JuliusBrussee/caveman -a aider-desk` | 否 |
| **Sourcegraph Amp** | `npx skills add JuliusBrussee/caveman -a amp` | 否 |
| **IBM Bob** | `npx skills add JuliusBrussee/caveman -a bob` | 否 |
| **Crush** | `npx skills add JuliusBrussee/caveman -a crush` | 否 |
| **Devin (终端)** | `npx skills add JuliusBrussee/caveman -a devin` | 否 |
| **Droid (Factory)** | `npx skills add JuliusBrussee/caveman -a droid` | 否 |
| **ForgeCode** | `npx skills add JuliusBrussee/caveman -a forgecode` | 否 |
| **Block Goose** | `npx skills add JuliusBrussee/caveman -a goose` | 否 |
| **iFlow CLI** | `npx skills add JuliusBrussee/caveman -a iflow-cli` | 否 |
| **Kiro CLI** | `npx skills add JuliusBrussee/caveman -a kiro-cli` | 否 |
| **Mistral Vibe** | `npx skills add JuliusBrussee/caveman -a mistral-vibe` | 否 |
| **OpenHands** | `npx skills add JuliusBrussee/caveman -a openhands` | 否 |
| **Qwen Code** | `npx skills add JuliusBrussee/caveman -a qwen-code` | 否 |
| **Atlassian Rovo Dev** | `npx skills add JuliusBrussee/caveman -a rovodev` | 否 |
| **Tabnine CLI** | `npx skills add JuliusBrussee/caveman -a tabnine-cli` | 否 |
| **Trae** | `npx skills add JuliusBrussee/caveman -a trae` | 否 |
| **Warp** | `npx skills add JuliusBrussee/caveman -a warp` | 否 |
| **Replit Agent** | `npx skills add JuliusBrussee/caveman -a replit` | 否 |
| **JetBrains Junie** *(soft probe)* | `npx skills add JuliusBrussee/caveman -a junie` | 否 |
| **Qoder** *(soft probe)* | `npx skills add JuliusBrussee/caveman -a qoder` | 否 |
| **Google Antigravity** *(soft probe)* | `npx skills add JuliusBrussee/caveman -a antigravity` | 否 |

"Soft probe" = 没有 `--only <id>` 标志时安装器不会自动检测这些代理，因为缺乏可靠的始终在线信号（Copilot 订阅状态受认证控制；其余无 CLI / 仅配置目录）。需要时传标志。

对于"Auto-activates? 否"的代理，每会话输入一次 `/caveman`（或使用自然语言触发如 "talk like caveman"、"caveman mode"）。

**查找 `npx skills add ... -a <profile>` 的 profile slug？** 既可查阅上表，也可从安装器打印实时矩阵：

```bash
# 以下任一均可（install.sh / install.ps1 是薄封装壳，
# 转发全部标志到 bin/install.js）：
bash install.sh --list             # macOS / Linux / WSL，本地克隆
pwsh install.ps1 --list            # Windows / PowerShell，本地克隆
node bin/install.js --list         # 任意平台，本地克隆
npx -y github:JuliusBrussee/caveman -- --list   # 无需克隆
```

每行打印 agent id、profile slug（如适用）以及是否在你的机器上检测到。完整 agent 矩阵（含检测规则）定义在 `bin/install.js` 的 `PROVIDERS` 数组中。

## 手动安装（不用 `curl | bash`）

若想确切了解每一步执行内容：

```bash
# 克隆仓库
git clone https://github.com/JuliusBrussee/caveman.git
cd caveman

# 预览安装器将执行的每条命令
node bin/install.js --dry-run --all

# 查看 agent 矩阵
node bin/install.js --list

# 为所有检测到的代理安装
node bin/install.js --all
```

常用标志：

| 标志 | 说明 |
|---|---|
| `--all` | 插件 + hooks + 状态行 + 当前目录的仓库规则文件。（MCP shrink 需主动勾选 — 见下方 `--with-mcp-shrink`。） |
| `--minimal` | 仅插件/扩展。无 hooks，无 MCP shrink，无仓库规则。 |
| `--only <id>` | 仅一个代理。可重复：`--only claude --only cursor`。 |
| `--dry-run` | 打印每条命令。不写入任何内容。 |
| `--with-init` | 将始终启用规则文件放入当前仓库（`.cursor/`、`.windsurf/`、`.clinerules/`、`.github/copilot-instructions.md`、`.opencode/AGENTS.md`、`AGENTS.md`），若 OpenClaw 在本机则追加引导块到 `~/.openclaw/workspace/SOUL.md`。 |
| `--with-mcp-shrink="<upstream cmd>"` | 注册 `caveman-shrink` MCP 代理包裹给定的上游 MCP 服务器。**默认关闭。** 需提供值——caveman-shrink 是代理，无值立即退出。示例：`--with-mcp-shrink="npx @modelcontextprotocol/server-filesystem /tmp"`。值按空格分割；路径含空格时通过克隆 `node bin/install.js` 安装或 stub 安装后编辑 `~/.claude.json`。 |
| `--no-mcp-shrink` | 跳过 MCP-shrink 注册。（默认。） |
| `--with-hooks` / `--no-hooks` | 强制开启或关闭 Claude Code hook 安装器。（默认：开。） |
| `--skip-skills` | 无其他匹配时不运行 npx-skills 自动检测回退。 |
| `--config-dir <path>` | Claude Code hook 文件 + `settings.json` 的配置目录。**不作用于** `claude plugin install`、`gemini extensions install`、opencode（`XDG_CONFIG_HOME`）或 openclaw（`OPENCLAW_WORKSPACE`）——这些使用自有路径。默认：`$CLAUDE_CONFIG_DIR` 或 `~/.claude`。`~` 展开。 |
| `--non-interactive` | 从不提示；使用默认值。（stdin 非 TTY 时自动启用。） |
| `--no-color` | 禁用 ANSI 颜色。 |
| `--list` | 打印完整 agent 矩阵后退出。 |
| `--force` | 即使已安装也重新运行。 |
| `--uninstall` | 移除全部内容。见下方。 |

## 始终启用的规则

对于没有 hook 系统的代理（Cursor、Windsurf、Cline、Copilot 等），始终启用的方式是静态规则文件。两种方式：

```bash
# 将规则文件放入当前仓库
node bin/install.js --with-init

# 或直接拉取规则内容（手动）
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/src/rules/caveman-activate.md \
  > .cursor/rules/caveman.mdc   # 或 .windsurf/rules/caveman.md、.clinerules/caveman.md、.github/copilot-instructions.md
```

`--with-init` 将规则写入每个它能检测到的受支持目录（`.cursor/rules/`、`.windsurf/rules/`、`.clinerules/`、`.github/copilot-instructions.md`、`.opencode/AGENTS.md`、`AGENTS.md`）。当 `~/.openclaw/workspace/` 存在时还会安装 OpenClaw 工作区引导（skill 文件夹 + SOUL.md 标记块）。单一来源：[`src/rules/caveman-activate.md`](src/rules/caveman-activate.md)。

## 验证

安装后，三项快速检查：

**1. 查看已安装内容。**

```bash
node bin/install.js --list
```

应看到约 30 行。已检测到的代理有标记。你想要的但未标记 → 未检测到（大概率二进制不在 `PATH` 上）。

**2. 和 Claude Code 对话。**

打开 Claude Code，输入 `/caveman`。回复应为紧凑片段——"Got it. Caveman mode on." 之类。试一个真实问题："What is closures in JS?"——回答应省略冠词，读起来像嘟囔。

**3. 检查标志文件。**

```bash
cat "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.caveman-active"
# 预期输出：full
```

若文件缺失或为空，SessionStart hook 未触发。见下方故障排除。

状态行应在 Claude Code 底部显示 `[CAVEMAN]`（橙色）。首次运行 `/caveman-stats` 后会追加节省计数器如 `[CAVEMAN] ⛏ 12.4k`。

## 卸载

```bash
npx -y github:JuliusBrussee/caveman -- --uninstall
```

移除内容：

- `$CLAUDE_CONFIG_DIR/settings.json`（默认 `~/.claude/`；通过子字符串 `caveman` 匹配）中的 caveman hook 条目。
- `$CLAUDE_CONFIG_DIR/hooks/` 中的 hook 文件（`caveman-activate.js`、`caveman-mode-tracker.js`、`caveman-stats.js`、`caveman-config.js`、`caveman-statusline.{sh,ps1}`，以及该目录的 `package.json` 标记）。
- Claude Code 插件和 Gemini CLI 扩展（若已安装）。
- opencode 原生插件（`~/.config/opencode/plugins/caveman/`，`opencode.json` 中 `plugin` 和 `mcp.caveman-shrink` 条目，我们的 skill/agent/command 文件，`AGENTS.md` 中 caveman 块，以及 opencode 标志文件）。
- OpenClaw 工作区 skill 文件夹及 `~/.openclaw/workspace/SOUL.md` 中标记包围的块（若存在）。
- `.caveman-active` 标志文件。

**不**移除的内容：

- 通过 `npx skills add` 安装的 skill —— `skills` CLI 自行管理。运行 `npx skills remove caveman`（或使用你的 IDE 的 skill 管理器）。
- `--with-init` 写入的仓库规则文件（`.cursor/rules/`、`.windsurf/rules/`、`.clinerules/`、`.github/copilot-instructions.md`、`.opencode/AGENTS.md`、`AGENTS.md`）。如需删除请手动操作。

## 故障排除

**"安装脚本挂了。怎么办？"**

在本仓库中打开你的代理，说：

> "Read CLAUDE.md and INSTALL.md. Install caveman for me."

代理读仓库。代理跑安装。穴居让代理少说话——代理首个任务是安装穴居来说更少话。蛇吞尾。

仍未解决？[提交 issue](https://github.com/JuliusBrussee/caveman/issues)。

**"我跑安装器了但 Claude Code 不按穴居语说话。"**

1. 运行 `node bin/install.js --list` — 确认 `claude` 在检测列表中。若不在，`claude` 不在 `PATH` 上。先修那个。
2. 打开 `$CLAUDE_CONFIG_DIR/settings.json`（默认 `~/.claude/settings.json`），查找包含 `caveman-activate.js` 和 `caveman-mode-tracker.js` 的 `"hooks"`。若缺失，用 `--force` 重跑。
3. 检查 `$CLAUDE_CONFIG_DIR/.caveman-active` 是否含内容 `full`。若缺失，SessionStart hook 静默失败了——检查 `$CLAUDE_CONFIG_DIR/hooks/` 中的 JS 文件，试 `node $CLAUDE_CONFIG_DIR/hooks/caveman-activate.js < /dev/null` 看是否报错。
4. 重启 Claude Code。SessionStart hook 仅在会话启动时触发，非会话中。

**"Windows 上 hooks 失败。"**

- 用 `install.ps1`，不要用 `install.sh`。Git Bash 可以跑 shell 版，但 hook 侧连接的是 PowerShell 对应文件（`caveman-statusline.ps1`）。
- PowerShell 5.1 最低要求。用 `$PSVersionTable.PSVersion` 检查。
- 若 `irm | iex` 因执行策略被阻止：`Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass` 针对安装会话，然后重跑。
- 长期问题：参见仓库中 `docs/install-windows.zh-CN.md` 的手动回退方案。

**"我的 `settings.json` 被损坏了。"**

安装器使用 JSONC 兼容解析器（`bin/lib/settings.js`），因此注释和尾逗号不会导致合并崩溃。每次写入前还运行 `validateHookFields()`，防止格式异常的 hook 污染文件。若仍有问题：

1. 检查 `$CLAUDE_CONFIG_DIR/settings.json.bak` 备份（安装器在每次合并前写入）。
2. 若无备份，从 shell 历史或版本控制恢复。
3. 提交 issue 附带损坏的 `settings.json` 内容（脱敏）——通过了校验却破坏了 Claude Code 的文件是我们想修的 bug。

**"我在托管环境中无法安装 hooks。"**

用纯规则文件路径。Hooks 是 Claude Code 专用；其他全部通过静态规则文件工作：

```bash
# 仅为一个代理安装，无 Claude hooks
node bin/install.js --only cursor

# 或仅写入仓库规则文件（无全局状态）
node bin/install.js --with-init --only cursor --only windsurf
```

这会在你的仓库中放入 `.cursor/rules/caveman.mdc`（及其同类）。无 hooks，无全局配置，仓库外无任何文件。

**"`npx skills add` 在 profile slug 上报错。"**

Profile slug 必须存在于 [vercel-labs/skills](https://github.com/vercel-labs/skills)。若上表某行 404，上游 profile 已重命名或移除——提交 issue，我们更新。

## 隐私

安装器不打电话回家。写入位置：

- `$CLAUDE_CONFIG_DIR`（默认 `~/.claude/`）—— hooks、标志文件、`settings.json` 合并。
- 各代理自有配置位置——Cursor 的 `.cursor/rules/`、Windsurf 的 `.windsurf/rules/`、opencode 的 `~/.config/opencode/` 等。
- 当前工作目录（仅配合 `--with-init`）——仓库本地规则文件。
- `~/.openclaw/workspace/`（仅配合 `--only openclaw` 或当检测到 OpenClaw 时 `--with-init`）——`--with-init` 唯一的工作目录外副作用。

无遥测。无分析。无论从克隆还是通过 npx 运行，安装器自身代码不发网络请求——文件均为本地复制。唯一例外：脱离任何 checkout 运行时（罕见的 curl 回退路径），会从 raw.githubusercontent.com 下载固定于不可变 release tag 的 hook 文件，并在接入前对每个文件用 SHA-256 清单校验。网络请求还间接通过安装器调用的各代理 CLI 发生——`claude plugin marketplace add`、`claude plugin install`、`gemini extensions install`、`npm view caveman-shrink` 和 `npx -y skills add`。各自从其自有注册表获取（Anthropic / GitHub / npm）。来源：[`bin/install.js`](bin/install.js)。安装后：零网络调用，永远——完整声明见 [SECURITY.zh-CN.md](./SECURITY.zh-CN.md#privacy--telemetry)。

---

卡住了？提交 issue：<https://github.com/JuliusBrussee/caveman/issues>

---
<sub>翻译同步至英文版 commit: `0d95a81` (2026-07-28)</sub>
