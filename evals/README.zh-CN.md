> **中文** | [English](./README.md)

# Evals

通过在三种条件下通过 Claude Code 运行相同提示词并比较生成的输出 token 数，测量穴居技能的真实 token 压缩效果。

## 三臂

| 臂 | 系统提示 |
|---|---------|
| `__baseline__` | 无 |
| `__terse__` | `Answer concisely.` |
| `<skill>` | `Answer concisely.\n\n{SKILL.md}` |

任何技能的诚实差值 = **`<skill>` vs `__terse__`** — 即技能本身在单纯"请简洁"指令之外增加了多少效果。将技能与无系统提示基线比较会混淆技能与通用简洁要求，这是此前版本工具链的做法，也是其数字虚高的原因。

## 为什么这样设计

- **真实 LLM 输出**，非手工示例（无循环论证）。
- **同样 Claude Code**，技能目标一致 — 无需单独 API 密钥。
- **快照提交到 git**，CI 运行确定且免费，任何数字变化可在 diff 中审查。
- **控制臂**隔离技能贡献与通用"请简洁"效果。

## 文件

- `prompts/en.txt` — 固定开发问题列表，每行一个。
- `llm_run.py` — 每（提示词，臂）运行 `claude -p --system-prompt …`，捕获真实 LLM 输出，写入 `snapshots/results.json` 及元数据（模型、CLI 版本、生成时间戳）。
- `measure.py` — 读取快照，用 tiktoken `o200k_base` 计 token，打印含中位数/均值/最小值/最大值/标准差的 markdown 表。
- `snapshots/results.json` — 已提交的单一数据源，仅在 SKILL.md 文件或提示词变更时重新生成。

## 刷新快照（需要 `claude` CLI 已登录）

```bash
uv run python evals/llm_run.py
```

每个提示词 ×（N 个技能 + 2 个控制臂）各调用一次 Claude。用小模型降低成本：

```bash
CAVEMAN_EVAL_MODEL=claude-haiku-4-5 uv run python evals/llm_run.py
```

## 读取快照（无需 LLM，无需 API 密钥，CI 中运行）

```bash
uv run --with tiktoken python evals/measure.py
```

## 添加提示词

追加一行到 `prompts/en.txt`，然后刷新快照。

## 添加技能

放入 `skills/<name>/SKILL.md`，然后刷新快照。`llm_run.py` 自动拾取每个技能目录。

## 这不测量的东西

- **保真度** — 压缩回答是否保留技术主张？对一切回复 `k` 的技能会得 −99% 并"获胜"。未来 v2 可添加评判模型评分标准。
- **延迟或成本** — 超出范围。注意技能每次调用增加输入 token，输出节省不是完整经济图景。
- **跨模型行为** — 仅测量生成快照所用的模型。
- **精确 Claude token** — `tiktoken o200k_base` 是 OpenAI 的 BPE，仅是对 Claude 分词器的近似。臂间比值有意义；绝对数字是近似的。
- **统计显著性** — 默认温度下每（提示词，臂）单次运行。min/max/stdev 列让你目测数字是否扎实或有噪声，但这不是有统计功效的实验。
