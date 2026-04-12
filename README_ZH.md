<p align="center">
  <img src="https://em-content.zobj.net/source/apple/391/rock_1faa8.png" width="120" />
</p>

<h1 align="center">caveman (原始人)</h1>

<p align="center">
  <strong>言简意赅，何必繁文缛节</strong>
</p>

<p align="center">
  <a href="https://github.com/JuliusBrussee/caveman/stargazers"><img src="https://img.shields.io/github/stars/JuliusBrussee/caveman?style=flat&color=yellow" alt="Stars"></a>
  <a href="https://github.com/JuliusBrussee/caveman/commits/main"><img src="https://img.shields.io/github/last-commit/JuliusBrussee/caveman?style=flat" alt="最近提交"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/JuliusBrussee/caveman?style=flat" alt="许可证"></a>
</p>

<p align="center">
  <a href="#对比">对比</a> •
  <a href="#安装">安装</a> •
  <a href="#强度级别">级别</a> •
  <a href="#caveman-技能">技能</a> •
  <a href="#基准测试">基准</a> •
  <a href="#评估">评估</a>
</p>

---

这是一个适用于 [Claude Code](https://docs.anthropic.com/en/docs/claude-code) 和 Codex 的插件，能让 AI 像“原始人”一样说话 —— 在保持 100% 技术准确性的同时，减少 **~75% 的输出 Token**。现已支持 [文言文模式](#文言文-wenyan-mode)、[极简提交信息](#caveman-commit)、[单行代码审查](#caveman-review) 以及可减少 **~46% 输入 Token** 的 [压缩工具](#caveman-compress)。

基于一个热门观察：模仿原始人的说话方式能大幅度减少 LLM 的 Token 消耗，且不丢失任何技术实质内容。因此，我们将其制作成了“一键安装”的形式。

## 对比 (Before / After)

<table>
<tr>
<td width="50%">

### 🗣️ 普通 Claude (69 Tokens)

> "你的 React 组件之所以重新渲染，很可能是因为你在每个渲染周期中都创建了一个新的对象引用。当你将行内对象作为 prop 传递时，React 的浅层比较每次都会将其视为不同的对象，从而触发重新渲染。建议你使用 useMemo 来记忆化该对象。"

</td>
<td width="50%">

### 🪨 原始人 Claude (19 Tokens)

> "每渲染生新引。行内对象引异致重绘。用 `useMemo`。"

</td>
</tr>
<tr>
<td>

### 🗣️ 普通 Claude

> "当然！我很乐意为你提供帮助。你遇到的问题很可能是由于你的身份验证中间件未能正确验证 Token 的过期时间。让我看看并提供修复建议。"

</td>
<td>

### 🪨 原始人 Claude

> "鉴权中间件有误。过期检查用 `<` 非 `<=`。修复："

</td>
</tr>
</table>

**修复效果一致。省词 75%。头脑依旧灵活。**

**选择你的“咕噜”级别：**

<table>
<tr>
<td width="25%">

#### 🪶 Lite

> "由于每渲染生命新引，致重绘。行内对像浅比异。用 `useMemo`。"

</td>
<td width="25%">

#### 🪨 Full

> "每渲染生新引。行内对像引异致重绘。用 `useMemo`。"

</td>
<td width="25%">

#### 🔥 Ultra

> "行内对像引异→重绘。`useMemo`。"

</td>
<td width="25%">

#### 📜 文言文

> "物出新參照，致重繪。useMemo Wrap之。"

</td>
</tr>
</table>

**答案一致。字数由你定。**

```
┌─────────────────────────────────────┐
│  TOKEN 节省            ████████ 75% │
│  技术准确度           ████████ 100%│
│  速度提升             ████████ ~3x │
│  感觉 (VIBES)         ████████ OOG │
└─────────────────────────────────────┘
```

- **响应更快** —— 生成 Token 越少，速度越快。
- **易于阅读** —— 拒绝长篇大论，直抵核心。
- **准确无误** —— 保留所有技术细节，仅剔除冗余。
- **更省钱** —— 减少约 71% 输出 Token = 降低成本。
- **有趣** —— 让代码审查变得像脱口秀。

## 安装

选择你的 Agent。一条命令。搞定。

| Agent | 安装方式 |
|-------|---------|
| **Claude Code** | `claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman` |
| **Codex** | 克隆仓库 → `/plugins` → 搜索 "Caveman" → 安装 |
| **Gemini CLI** | `gemini extensions install https://github.com/JuliusBrussee/caveman` |
| **Cursor** | `npx skills add JuliusBrussee/caveman -a cursor` |
| **Windsurf** | `npx skills add JuliusBrussee/caveman -a windsurf` |
| **Copilot** | `npx skills add JuliusBrussee/caveman -a github-copilot` |
| **Cline** | `npx skills add JuliusBrussee/caveman -a cline` |

安装一次。终身受益。一块石头，足矣。

## 文言文 (Wenyan) 模式

古典文学压缩法 —— 技术准确性不变，但使用的是人类历史上 Token 效率最高的书面语言。

| 级别 | 触发词 | 说明 |
|-------|---------|------------|
| **文言-浅** | `/caveman wenyan-lite` | 半文言。保留基本语法，去除冗余词汇。 |
| **文言-全** | `/caveman wenyan` | 全文言。极致精炼。 |
| **文言-极** | `/caveman wenyan-ultra` | 狂野。预算不足的古代学者风。 |

## 许可证

MIT —— 像旷野上的猛犸象一样自由。
