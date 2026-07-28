> **中文** | [English](./install-windows.md)

# Windows 安装回退

若 `irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.ps1 | iex` 在 Windows 上失败（issues #249、#199、#72），手动设置插件技能激活。这**不**安装独立 hooks 或状态行——安装后运行统一 Node 安装器：`npx -y github:JuliusBrussee/caveman -- --only claude`（或从克隆 `node bin/install.js --only claude`）。

```powershell
$ClaudeDir = if ($env:CLAUDE_CONFIG_DIR) { $env:CLAUDE_CONFIG_DIR } else { Join-Path $HOME ".claude" }
$PluginSkillDir = Join-Path $ClaudeDir ".agents\plugins\caveman\skills\caveman"
$MarketplaceDir = Join-Path $ClaudeDir ".agents\plugins"
$MarketplaceFile = Join-Path $MarketplaceDir "marketplace.json"

# 将 SKILL.md 复制到插件路径（从仓库克隆运行）
New-Item -ItemType Directory -Path $PluginSkillDir -Force | Out-Null
Copy-Item ".\skills\caveman\SKILL.md" "$PluginSkillDir\SKILL.md" -Force

# 创建或更新 marketplace.json 含穴居条目
New-Item -ItemType Directory -Path $MarketplaceDir -Force | Out-Null
if (Test-Path $MarketplaceFile) {
  $marketplace = Get-Content $MarketplaceFile -Raw | ConvertFrom-Json
} else {
  $marketplace = [pscustomobject]@{}
}
if (-not ($marketplace.PSObject.Properties.Name -contains "plugins")) {
  $marketplace | Add-Member -NotePropertyName plugins -NotePropertyValue ([pscustomobject]@{})
}
$plugins = [ordered]@{}
foreach ($p in $marketplace.plugins.PSObject.Properties) { $plugins[$p.Name] = $p.Value }
$plugins["caveman"] = [ordered]@{ name = "caveman"; source = "JuliusBrussee/caveman"; version = "main" }
$marketplace.plugins = [pscustomobject]$plugins
$marketplace | ConvertTo-Json -Depth 10 | Set-Content -Path $MarketplaceFile -Encoding UTF8
```

验证：`Test-Path "$PluginSkillDir\SKILL.md"` 应打印 `True`。重启 Claude Code，运行 `/caveman` 确认技能加载。

## Windows 上的 Codex

1. 先启用符号链接：`git config --global core.symlinks true`（需开发者模式或管理员权限）。
2. 克隆仓库 → 打开 VS Code → Codex Settings → Plugins → 本地 marketplace 中找到"Caveman"→ Install → Reload Window。
3. Codex hooks 目前在 Windows 上禁用，每会话用 `$caveman` 手动启动模式。

## `npx skills` 符号链接回退

`npx skills` 默认使用符号链接。若符号链接失败，加 `--copy`：

```powershell
npx skills add JuliusBrussee/caveman --copy
```

## 想始终启用（任意代理）？

将其粘贴到代理的系统提示或规则文件中：

```
Terse like caveman. Technical substance exact. Only fluff die.
Drop: articles, filler (just/really/basically), pleasantries, hedging.
Fragments OK. Short synonyms. Code unchanged.
Pattern: [thing] [action] [reason]. [next step].
ACTIVE EVERY RESPONSE. No revert after many turns. No filler drift.
Code/commits/PRs: normal. Off: "stop caveman" / "normal mode".
```
