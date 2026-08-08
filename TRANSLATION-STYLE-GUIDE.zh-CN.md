# Caveman 中文化翻译风格指南

本文档定义 caveman 项目文档中文化的规则、词汇表和审查清单。仅供维护者参考，非用户文档。

---

## 核心原则：电报体

中文 caveman 读感应如电报或文言短句——以最少字数传达最大信息量。

**"保留全部技术细节。压缩连接词。像一个很聪明但偏要用 2-3 字短句说话的人。"**

压缩目标：在保证技术准确的前提下，中文字符减少约 50-65%。

---

## 六条变形规则

| 规则 | 英文机制 | 中文等价操作 | 示例 |
|------|---------|-------------|------|
| R1 | 去掉冠词 (a, an, the) | 去掉量词：个、种、次、条、些 | 一个问题 → 一问题 |
| R2 | 使用片段 | 去掉结构助词"的/地/得"（语义清晰时） | 很大的问题 → 很大问题 |
| R3 | 省略主语代词 | 上下文明确时省略主语 | 你应该使用 → 使用 |
| R4 | 短同义词 | 用单字文言词替代双字词 | 但是→但, 可以→可, 因为→因, 如果→若, 没有→无, 已经→已, 这个→此, 那个→彼 |
| R5 | 电报式 | 去掉助动词/能愿动词 | 可以帮助→可帮, 应该使用→应用, 需要安装→需装 |
| R6 | **绝不压缩** | 代码、URL、命令、技术术语、英文专有名词——逐字节保留 | `useMemo`, `git commit`, `/caveman-stats` |

### R6 详细说明：以下内容绝不触碰

- 代码块（\`\`\` 包围内容）
- 内联代码（\` 包围内容）
- 所有 URL 和文件路径
- 命令行示例
- 标志名（`--all`, `--with-init` 等）
- 技术术语（`useMemo`, `token`, `MCP`, `JSONC` 等）
- 英文专有名词（`Claude Code`, `GitHub`, `npm` 等）
- HTML 标签和属性
- emoji 和图片 alt 文本
- 表格中的数据列

---

## 签名短语翻译对照表

以下短语贯穿多个文件，体现品牌调性，必须保持一致翻译。

| 英文原文 | 中文翻译 | 备注 |
|---------|---------|------|
| "why use many token when few do trick" | 多言何用，少言足矣 | 文言风格，8字。标语。 |
| "Brain still big. Mouth small." | 脑仍大。嘴小。 | 电报体，5字 |
| "Caveman no phone home" | 穴居无联网 | 4字，清脆 |
| "Rock sharp" | 石锋利 | 3字 |
| "Same answers, 65% fewer output tokens" | 同答，减65%出词 | 电报体 |
| "Caveman make token small" | 穴居缩词 | 4字 |
| "cost go down forever" | 花费永降 | 4字 |
| "free like mass mammoth on open plain" | 如平原巨象，自由自在 | 文言语感，8字 |
| "one install, work for every agent" | 一装通用 | 4字 |
| "snake eat tail" | 蛇吞尾 | 文言比喻，3字 |
| "you bring rock, caveman put rock in pile" | 你带石来，穴居垒之 | 文言语感，7字 |
| "pile get bigger" | 石堆渐大 | 4字 |
| "one rock, that it" | 一石足矣 | 4字 |
| "Caveman free forever" | 穴居永免 | 4字 |
| "Sponsors keep the rock sharp" | 赞助者磨石锋利 | 5字 |
| "Star cost zero. Fair trade." | 星标无费。公道交易。 | 电报体 |
| "Caveman no make brain smaller" | 穴居不减脑 | 5字 |
| "Caveman make *mouth* smaller" | 穴居缩*嘴* | 4字 |

---

## Before/After 示例

### 示例 1：React 重渲染解释

**英文正常版（69 token）：**
> The reason your React component is re-rendering is likely because you're creating a new object reference on each render cycle. When you pass an inline object as a prop, React's shallow comparison sees it as a different object every time, which triggers a re-render. I'd recommend using useMemo to memoize the object.

**英文 caveman 版（19 token）：**
> New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`.

**中文正常翻译：**
> 你的 React 组件重新渲染的原因，很可能是因为你在每个渲染周期中都创建了新的对象引用。当你把内联对象作为 prop 传入时，React 的浅比较每次都将其视为不同的对象，从而触发重新渲染。我建议使用 useMemo 来缓存该对象。

**中文 caveman 版（应用 R1-R6）：**
> 每次渲染建新对象引用。内联prop=新引用=重渲染。用`useMemo`。

### 示例 2：隐私声明

**英文 caveman：**
> Caveman no phone home. No telemetry, no analytics, no accounts, no backend.

**中文 caveman：**
> 穴居无联网。无遥测、无分析、无账户、无后端。

### 示例 3：安装宣传语

**英文 caveman：**
> One command. Finds every agent on your machine. Installs for each.

**中文 caveman：**
> 一命令。搜遍本机全部Agent。逐一安装。

---

## 审查清单

翻译完成后逐项检查：

- [ ] 语言横幅在文件顶部且正确链接
- [ ] 代码块内容与英文原版逐字节一致
- [ ] 所有 URL 未变化
- [ ] 所有命令和标志名未变化
- [ ] 签名短语使用词汇表中的统一翻译
- [ ] 表格结构、标题层级未变化
- [ ] 中文读起来像穴居人——简洁、直接、略随意，不像标准技术文档
- [ ] 内部交叉引用指向 `.zh-CN.md` 版本（未翻译文件除外）
- [ ] 同步页脚包含英文版 commit hash 和日期
