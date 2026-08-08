> **中文** | [English](./SECURITY.md)

# 安全策略

## 受支持版本

仅最新稳定 release 构建获得安全补丁。

## 报告漏洞

如果你在 caveman 中发现安全漏洞（如任意 shell 执行、工作区文件夹逃逸、通过提示词劫持 token/凭据、或扩展设置中的恶意 JSON 解析缺陷），请**不要**公开提交 issue。

请通过邮件或 [GitHub 私密漏洞报告](https://github.com/JuliusBrussee/caveman/security/advisories/new) 私下报告漏洞。

## 隐私与遥测

**穴居无遥测。零。** 无分析、无崩溃报告、无回传、无账户、不收集 API 密钥。没有 caveman 后端——没有可发送数据的地方。

### 安装后：零网络调用

安装后，穴居中没有任何内容触碰网络。对照代码已验证（可自行审计——全部文件在此仓库）：

- **技能本身**（`skills/caveman/SKILL.md`）是一个 markdown 提示词。不含代码。
- **Hooks**（`src/hooks/*.js`、状态行脚本）是本地 Node/shell 脚本。仅读写本地文件（标志文件、会话日志、状态行节省文件）。其中无 `http`/`https`/`fetch`。
- **`/caveman-stats`** 从本地磁盘读取 Claude Code 的会话 JSONL 并打印计数。美元数字来自脚本中硬编码的价格常量。无数据离开你的机器。
- **`caveman-shrink`**（MCP 中间件）启动*你*配置的 MCP 服务器，本地运行，进程中压缩其输出。它自身不发网络请求；任何网络活动属于你包裹的服务器。
- **`/caveman-compress`** 重写你指定的本地文件并保存 `.original.md` 备份在其旁。仅本地文件 I/O。

### 安装时：仅以下网络请求，无其他

- `curl … install.sh | bash`（或 `irm … install.ps1 | iex`）从 raw.githubusercontent.com 获取薄壳，后者委托给 `npx -y github:JuliusBrussee/caveman` —— npm 从 GitHub 获取此仓库。
- 安装器调用各代理 CLI，后者从自有注册表获取：`claude plugin marketplace add` / `claude plugin install`（Anthropic/GitHub）、`gemini extensions install`、`npm view caveman-shrink`、`npx -y skills add`（npm）。
- **罕见回退：** 若安装器脱离开 repo checkout 运行，会从 raw.githubusercontent.com 下载 hook 文件，**锁定于不可变 release tag**，并在接入前对每个文件用已发布的 SHA-256 清单验证（不匹配即中止）。从正常克隆或 npx 运行时，文件为本地复制——离线安装可用。

以上任何步骤均不上传数据。详情及完整写入路径列表：[INSTALL.zh-CN.md → 隐私](./INSTALL.zh-CN.md#隐私)。

### 什么保留在你的机器上

全部。你代理配置目录中的技能/规则文件，`~/.claude/`（或 `$CLAUDE_CONFIG_DIR`）下的模式标志文件和合并后 `settings.json`，累计节省状态行文件，以及 `/caveman-compress` 的 `.original.md` 备份。卸载移除安装器写入的内容：`npx -y github:JuliusBrussee/caveman -- --uninstall`。

### 企业 / 隔离网环境

穴居安装后自包含，完全离线可用。无许可证服务器、无外部后端、除上述安装时网络获取外无数据流需审计。隔离网环境中，内部克隆仓库并从克隆运行安装器——无需网络。

## 关于扫描器警告

- **Windows Defender / SmartScreen 对 `install.ps1`（#383）：** 将互联网脚本管道进 `iex` 并写入代理配置目录符合通用投放器启发式，安全工具可能警告。脚本短小可读，在此仓库中；安装的 hook 文件对比锁定的 release manifest 做 SHA-256 校验。若不愿管道到 shell，克隆仓库并运行 `node bin/install.js`——结果相同，全程可检查。
- **Snyk 对 `caveman-compress` 的"高风险"评级（#28）：** 压缩技能指示代理读取你指定的文件，原地重写，保存备份。原地文件重写正是通用风险评分标记的行为。这是真实能力，非隐藏——但无网络访问、无超出 [`skills/caveman-compress/`](./skills/caveman-compress/) 记录的 shell 执行、绝不碰你未指定的文件。

---
<sub>翻译同步至英文版 commit: `0d95a81` (2026-07-28)</sub>
