# Caveman 基准测试报告 — 全强度等级

**日期：** 2026-06-12
**模型：** deepseek-v4-flash、deepseek-v4-pro
**方法：** 10 个提示词 × 2 种模式（normal vs caveman）× 3 轮 = 每等级 60 次 API 调用
**运行器：** `benchmarks/run.py`

## 总览

| 模式 | deepseek-v4-flash | deepseek-v4-pro | 变化 |
|------|------------------|-----------------|------|
| `lite` | 76% | 77% | +1pp |
| `full` | — | 76% | （首次 DeepSeek 运行） |
| `ultra` | 73% | 79% | +6pp |
| `wenyan-lite` | 72% | 77% | +5pp |
| `wenyan-full` | 67% | 76% | +9pp |
| `wenyan-ultra` | 72% | 76% | +4pp |

- **deepseek-v4-pro 全面优于 v4-flash**（+1–9pp）
- **文言模式提升最显著**：v4-pro 在古典中文压缩上明显更强
- **6 模式均值**：v4-pro 76.8%，v4-flash 72.0%
- **`ultra` 模式以 79% 登顶** v4-pro 极限压缩

## 逐任务明细 — deepseek-v4-pro

### lite（77%）

| 任务 | Normal | Caveman | 节省 |
|------|--------|---------|------|
| 解释 React 重渲染原因 | 488 | 31 | 94% |
| 修复 auth 中间件 token 过期 | 442 | 36 | 92% |
| 搭建 PostgreSQL 连接池 | 2060 | 289 | 86% |
| 解释 git rebase vs merge | 524 | 287 | 45% |
| 回调重构为 async/await | 248 | 77 | 69% |
| 架构：微服务 vs 单体 | 475 | 239 | 50% |
| 审查 PR 安全问题 | 542 | 73 | 87% |
| Docker 多阶段构建 | 777 | 166 | 79% |
| 调试 PostgreSQL 竞态条件 | 754 | 173 | 77% |
| 实现 React 错误边界 | 2644 | 251 | 91% |

### full（76%）

| 任务 | Normal | Caveman | 节省 |
|------|--------|---------|------|
| 解释 React 重渲染原因 | 517 | 45 | 91% |
| 修复 auth 中间件 token 过期 | 470 | 89 | 81% |
| 搭建 PostgreSQL 连接池 | 1715 | 333 | 81% |
| 解释 git rebase vs merge | 551 | 178 | 68% |
| 回调重构为 async/await | 266 | 77 | 71% |
| 架构：微服务 vs 单体 | 501 | 258 | 49% |
| 审查 PR 安全问题 | 554 | 95 | 83% |
| Docker 多阶段构建 | 837 | 248 | 70% |
| 调试 PostgreSQL 竞态条件 | 794 | 139 | 82% |
| 实现 React 错误边界 | 2708 | 315 | 88% |

### ultra（79%）

| 任务 | Normal | Caveman | 节省 |
|------|--------|---------|------|
| 解释 React 重渲染原因 | 500 | 31 | 94% |
| 修复 auth 中间件 token 过期 | 476 | 46 | 90% |
| 搭建 PostgreSQL 连接池 | 1895 | 253 | 87% |
| 解释 git rebase vs merge | 557 | 195 | 65% |
| 回调重构为 async/await | 269 | 67 | 75% |
| 架构：微服务 vs 单体 | 648 | 258 | 60% |
| 审查 PR 安全问题 | 532 | 73 | 86% |
| Docker 多阶段构建 | 855 | 223 | 74% |
| 调试 PostgreSQL 竞态条件 | 720 | 227 | 68% |
| 实现 React 错误边界 | 2586 | 243 | 91% |

### wenyan-lite（77%）

| 任务 | Normal | Caveman | 节省 |
|------|--------|---------|------|
| 解释 React 重渲染原因 | 511 | 32 | 94% |
| 修复 auth 中间件 token 过期 | 444 | 91 | 80% |
| 搭建 PostgreSQL 连接池 | 1898 | 253 | 87% |
| 解释 git rebase vs merge | 530 | 181 | 66% |
| 回调重构为 async/await | 235 | 100 | 57% |
| 架构：微服务 vs 单体 | 593 | 242 | 59% |
| 审查 PR 安全问题 | 521 | 88 | 83% |
| Docker 多阶段构建 | 780 | 217 | 72% |
| 调试 PostgreSQL 竞态条件 | 799 | 117 | 85% |
| 实现 React 错误边界 | 2648 | 319 | 88% |

### wenyan-full（76%）

| 任务 | Normal | Caveman | 节省 |
|------|--------|---------|------|
| 解释 React 重渲染原因 | 488 | 42 | 91% |
| 修复 auth 中间件 token 过期 | 414 | 81 | 80% |
| 搭建 PostgreSQL 连接池 | 1803 | 354 | 80% |
| 解释 git rebase vs merge | 680 | 215 | 68% |
| 回调重构为 async/await | 238 | 85 | 64% |
| 架构：微服务 vs 单体 | 442 | 247 | 44% |
| 审查 PR 安全问题 | 544 | 83 | 85% |
| Docker 多阶段构建 | 782 | 215 | 73% |
| 调试 PostgreSQL 竞态条件 | 710 | 123 | 83% |
| 实现 React 错误边界 | 2573 | 314 | 88% |

### wenyan-ultra（76%）

| 任务 | Normal | Caveman | 节省 |
|------|--------|---------|------|
| 解释 React 重渲染原因 | 506 | 22 | 96% |
| 修复 auth 中间件 token 过期 | 438 | 76 | 83% |
| 搭建 PostgreSQL 连接池 | 1818 | 267 | 85% |
| 解释 git rebase vs merge | 509 | 213 | 58% |
| 回调重构为 async/await | 272 | 71 | 74% |
| 架构：微服务 vs 单体 | 487 | 302 | 38% |
| 审查 PR 安全问题 | 508 | 75 | 85% |
| Docker 多阶段构建 | 762 | 220 | 71% |
| 调试 PostgreSQL 竞态条件 | 727 | 149 | 80% |
| 实现 React 错误边界 | 2451 | 257 | 90% |

## 逐任务明细 — deepseek-v4-flash

### lite（76%）

| 任务 | Normal | Caveman | 节省 |
|------|--------|---------|------|
| 解释 React 重渲染原因 | 449 | 53 | 88% |
| 修复 auth 中间件 token 过期 | 461 | 92 | 80% |
| 搭建 PostgreSQL 连接池 | 2046 | 288 | 86% |
| 解释 git rebase vs merge | 752 | 189 | 75% |
| 回调重构为 async/await | 249 | 104 | 58% |
| 架构：微服务 vs 单体 | 1034 | 251 | 76% |
| 审查 PR 安全问题 | 399 | 99 | 75% |
| Docker 多阶段构建 | 838 | 325 | 61% |
| 调试 PostgreSQL 竞态条件 | 796 | 117 | 85% |
| 实现 React 错误边界 | 1581 | 420 | 73% |

### ultra（73%）

| 任务 | Normal | Caveman | 节省 |
|------|--------|---------|------|
| 解释 React 重渲染原因 | 489 | 109 | 78% |
| 修复 auth 中间件 token 过期 | 511 | 80 | 84% |
| 搭建 PostgreSQL 连接池 | 2076 | 238 | 89% |
| 解释 git rebase vs merge | 757 | 282 | 63% |
| 回调重构为 async/await | 248 | 113 | 54% |
| 架构：微服务 vs 单体 | 950 | 255 | 73% |
| 审查 PR 安全问题 | 383 | 106 | 72% |
| Docker 多阶段构建 | 1001 | 414 | 59% |
| 调试 PostgreSQL 竞态条件 | 810 | 159 | 80% |
| 实现 React 错误边界 | 1660 | 380 | 77% |

### wenyan-lite（72%）

| 任务 | Normal | Caveman | 节省 |
|------|--------|---------|------|
| 解释 React 重渲染原因 | 505 | 84 | 83% |
| 修复 auth 中间件 token 过期 | 416 | 83 | 80% |
| 搭建 PostgreSQL 连接池 | 2000 | 324 | 84% |
| 解释 git rebase vs merge | 749 | 257 | 66% |
| 回调重构为 async/await | 242 | 101 | 58% |
| 架构：微服务 vs 单体 | 578 | 232 | 60% |
| 审查 PR 安全问题 | 377 | 104 | 72% |
| Docker 多阶段构建 | 844 | 364 | 57% |
| 调试 PostgreSQL 竞态条件 | 771 | 173 | 78% |
| 实现 React 错误边界 | 1652 | 343 | 79% |

### wenyan-full（67%）

| 任务 | Normal | Caveman | 节省 |
|------|--------|---------|------|
| 解释 React 重渲染原因 | 469 | 86 | 82% |
| 修复 auth 中间件 token 过期 | 440 | 86 | 80% |
| 搭建 PostgreSQL 连接池 | 2155 | 327 | 85% |
| 解释 git rebase vs merge | 663 | 381 | 43% |
| 回调重构为 async/await | 241 | 110 | 54% |
| 架构：微服务 vs 单体 | 571 | 311 | 46% |
| 审查 PR 安全问题 | 390 | 101 | 74% |
| Docker 多阶段构建 | 838 | 513 | 39% |
| 调试 PostgreSQL 竞态条件 | 876 | 113 | 87% |
| 实现 React 错误边界 | 1598 | 340 | 79% |

### wenyan-ultra（72%）

| 任务 | Normal | Caveman | 节省 |
|------|--------|---------|------|
| 解释 React 重渲染原因 | 480 | 79 | 84% |
| 修复 auth 中间件 token 过期 | 448 | 86 | 81% |
| 搭建 PostgreSQL 连接池 | 2016 | 467 | 77% |
| 解释 git rebase vs merge | 668 | 288 | 57% |
| 回调重构为 async/await | 241 | 73 | 70% |
| 架构：微服务 vs 单体 | 1126 | 270 | 76% |
| 审查 PR 安全问题 | 428 | 102 | 76% |
| Docker 多阶段构建 | 811 | 346 | 57% |
| 调试 PostgreSQL 竞态条件 | 815 | 263 | 68% |
| 实现 React 错误边界 | 1489 | 392 | 74% |

## 关键发现

- **`ultra` 模式压缩率最高（79%）**，v4-pro 极限压缩场景首选
- **v4-pro 全面优于 v4-flash**，文言模式差距尤大（+4–9pp）
- **6 模式集中在 76%–79% 区间**（v4-pro），一致性更好
- `full` 模式为首次 DeepSeek 平台运行，此前仅 Claude Sonnet 数据（65%）
- v4-pro 对中文/文言输出压缩能力远超 v4-flash

## 原始数据文件

### deepseek-v4-pro
- `benchmark_20260612_063333.json` — lite
- `benchmark_20260612_063410.json` — full
- `benchmark_20260612_063443.json` — ultra
- `benchmark_20260612_063631.json` — wenyan-lite
- `benchmark_20260612_063734.json` — wenyan-full
- `benchmark_20260612_063811.json` — wenyan-ultra

### deepseek-v4-flash
- `benchmark_20260611_192846.json` — lite
- `benchmark_20260611_191748.json` — ultra
- `benchmark_20260611_193607.json` — wenyan-lite
- `benchmark_20260611_193957.json` — wenyan-full
- `benchmark_20260611_194114.json` — wenyan-ultra

## 代码变更

### `benchmarks/run.py`
- `--mode` 参数——注入目标强度等级到 system prompt
- `--base-url` / `--api-key`——支持非 Anthropic 端点
- `thinking: {"type": "disabled"}`——排除推理 token
- 修复 text block 提取（合并所有 `type: "text"` blocks）

### `src/hooks/caveman-stats.js`
- `COMPRESSION` 表补全 6 个强度模式
- 新增 `Mode` 行，显示当前激活模式
- 更新 fallback 消息列出所有 benchmarked 模式

### `tests/test_caveman_stats.js`
- 适配 ultra/lite 模式数据及 Mode 行输出

## 注意事项

1. Normal 基线在不同运行间存在波动（不同 API 调用、轻微非确定性）。同次内对比可靠；跨运行对比约 2–3% 方差。
2. `full` 模式 deepseek-v4-flash 未测试，此前 65% 数据来自 `claude-sonnet-4-20250514`。
3. `thinking` 已禁用（`{"type": "disabled"}`），但模型仍可能产生计入 `output_tokens` 的内部 token。
4. v4-pro 在所有模式上均优于 v4-flash，文言模式差距尤为明显（+4–9pp）。
