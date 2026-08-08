> **中文** | [English](./README.md)

# Caveman Hooks

这些 hooks **捆绑在 caveman 插件中**，插件安装后自动激活。无需手动设置。

如果你独立安装了 caveman（无插件），统一 Node 安装器 `bin/install.js` 会为你将它们接入 `settings.json`——从克隆运行 `node bin/install.js --only claude`，或用 curl 管道路径 `npx -y github:JuliusBrussee/caveman -- --only claude`。

## 包含内容

### `caveman-activate.js` — SessionStart hook

- Claude Code 启动时运行一次
- 通过符号链接安全的 `safeWriteFlag` 辅助函数写入 `full` 到 `$CLAUDE_CONFIG_DIR/.caveman-active`（默认 `~/.claude/.caveman-active`）
- 以隐藏 SessionStart 上下文形式发出穴居规则
- 检测缺失的状态行配置并发出设置提示（Claude 将提供设置帮助）

### `caveman-mode-tracker.js` — UserPromptSubmit hook

- 每次用户提示触发，检测 `/caveman` 命令和自然语言激活/停用短语（"talk like caveman"、"stop caveman"、"normal mode"）
- 检测到穴居命令时将活跃模式写入标志文件；停用时删除
- 当标志设置为非独立模式（`lite`/`full`/`ultra`/`wenyan*`）时发出小型每轮强化提醒
- 支持：`lite`、`full`、`ultra`、`wenyan`、`wenyan-lite`、`wenyan-full`、`wenyan-ultra`、`commit`、`review`、`compress`

### `caveman-statusline.sh` / `caveman-statusline.ps1` — 状态行徽章脚本

- 读取 `$CLAUDE_CONFIG_DIR/.caveman-active`（默认 `~/.claude/.caveman-active`）并输出彩色徽章
- 显示 `[CAVEMAN]`、`[CAVEMAN:ULTRA]`、`[CAVEMAN:WENYAN]` 等
- 追加累计节省后缀 `⛏ 12.4k`（来自 `$CLAUDE_CONFIG_DIR/.caveman-statusline-suffix`，由 `caveman-stats.js` 在每次 `/caveman-stats` 运行时写入；首次运行前缺失，所以新安装不渲染虚假数字）。设 `CAVEMAN_STATUSLINE_SAVINGS=0` 关闭。

## 状态行徽章

状态行徽章直接在 Claude Code 状态栏显示当前穴居模式。

**插件用户：** 如果你尚未配置 `statusLine`，Claude 会在安装后首次会话检测到并提示设置。接受即完成。

如果你已有自定义状态行，穴居不覆盖，Claude 保持安静。将徽章片段添加到你的现有脚本。

**独立用户：** 统一安装器（`bin/install.js`，由仓库根目录 `install.sh` / `install.ps1` 薄壳调用）若你无自定义状态行则自动接入。若有，安装器会忽略并打印合并说明。

**手动设置：** 如需自行配置，添加如下到 `~/.claude/settings.json`：

```json
{
  "statusLine": {
    "type": "command",
    "command": "bash /path/to/caveman-statusline.sh"
  }
}
```

```json
{
  "statusLine": {
    "type": "command",
    "command": "powershell -ExecutionPolicy Bypass -File C:\\path\\to\\caveman-statusline.ps1"
  }
}
```

替换路径为实际脚本位置（如独立安装用 `~/.claude/hooks/`，插件安装用插件安装目录）。

**自定义状态行：** 如果你已有状态行脚本，添加此片段：

```bash
caveman_text=""
caveman_flag="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.caveman-active"
if [ -f "$caveman_flag" ]; then
  caveman_mode=$(cat "$caveman_flag" 2>/dev/null)
  if [ "$caveman_mode" = "full" ] || [ -z "$caveman_mode" ]; then
    caveman_text=$'\033[38;5;172m[CAVEMAN]\033[0m'
  else
    caveman_suffix=$(echo "$caveman_mode" | tr '[:lower:]' '[:upper:]')
    caveman_text=$'\033[38;5;172m[CAVEMAN:'"${caveman_suffix}"$']\033[0m'
  fi
fi
```

徽章示例：
- `/caveman` → `[CAVEMAN]`
- `/caveman ultra` → `[CAVEMAN:ULTRA]`
- `/caveman wenyan` → `[CAVEMAN:WENYAN]`
- `/caveman-commit` → `[CAVEMAN:COMMIT]`
- `/caveman-review` → `[CAVEMAN:REVIEW]`

## 工作原理

```
SessionStart hook ──写入"full"──▶ $CLAUDE_CONFIG_DIR/.caveman-active ◀──写入模式── UserPromptSubmit hook
                                              │
                                           读取
                                              ▼
                                     状态行脚本
                                    [CAVEMAN:ULTRA] │ ...
```

SessionStart stdout 作为隐藏系统上下文注入——Claude 可见，用户不可见。状态行作为独立进程运行。标志文件是桥梁。

## 卸载

通过插件安装：禁用插件——hooks 自动停用。

通过独立 Node 安装器安装：
```bash
npx -y github:JuliusBrussee/caveman -- --uninstall
# 或从克隆：
node bin/install.js --uninstall
```

或手动：
1. 从 `$CLAUDE_CONFIG_DIR/hooks/`（默认 `~/.claude/hooks/`）移除穴居 hook 文件：`caveman-activate.js`、`caveman-mode-tracker.js`、`caveman-stats.js`、`caveman-config.js` 和 `caveman-statusline.{sh,ps1}`。
2. 从 `$CLAUDE_CONFIG_DIR/settings.json` 移除 SessionStart、UserPromptSubmit 和 statusLine 条目。
3. 删除 `$CLAUDE_CONFIG_DIR/.caveman-active`（以及 `$CLAUDE_CONFIG_DIR/.caveman-statusline-suffix`，如果你运行过 `/caveman-stats`）。
