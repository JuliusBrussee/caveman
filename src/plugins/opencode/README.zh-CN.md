> **中文** | [English](./README.md)

# caveman — opencode 插件

原生 opencode 插件。使用 opencode 的 `session.created` + `tui.prompt.append` 生命周期 hooks 镜像 Claude Code hook 架构。

## 包含内容

| 文件 | 角色 |
|---|---|
| `plugin.js` | ESM Bun 模块。默认导出 opencode `Plugin` 工厂。 |
| `package.json` | 标记目录为 ESM，使 Bun 正确加载 `plugin.js`。 |
| `commands/*.md` | 六个斜杠命令提示模板（`/caveman`、`/caveman-commit` 等）。 |

安装器（`bin/install.js --only opencode`）将这些连同 `src/hooks/caveman-config.js`（提供符号链接安全的标志写入辅助函数，重命名为 `caveman-config.cjs`，因为此目录是 `"type": "module"`）复制到 `~/.config/opencode/plugins/caveman/` 并用 `"plugin"` 数组条目修补 `opencode.json`。

## 做什么

- `session.created` → 通过 Claude Code 同款 `safeWriteFlag` 辅助函数将配置的默认模式写入 `~/.config/opencode/.caveman-active`（O_NOFOLLOW、原子 temp+rename、0600 权限、符号链接拒绝、所有权检查）。
- `tui.prompt.append` → 响应 `/caveman[ <level>]`、`/caveman-commit`、`/caveman-review`、`/caveman-compress` 和自然语言（"turn on caveman"、"stop caveman"、"normal mode"）翻转标志。非独立模式激活时，追加一行强化提示以保持穴居每轮在模型注意力中。

## 不做什么

- **无状态行徽章。** opencode 的 TUI 不暴露可被插件写入的状态行。标志文件在 `~/.config/opencode/.caveman-active`，若想在 shell 提示符中显示模式可用。
- **无 `session.created` 系统提示注入。** opencode 文档未暴露其返回形状。始终启用的穴居规则集来自 `~/.config/opencode/AGENTS.md`（也由安装器写入），即使插件运行时损坏规则也能加载。

## 为什么没有单独 npm 包

插件代码复用主仓库的 `caveman-config.js`。作为仓库内插件发布避免第二次发布节奏和与现有三方 `opencode-caveman` npm 包的名称冲突。
