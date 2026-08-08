> **中文** | [English](./README.md)

# caveman-shrink

> MCP 中间件。包裹任意 MCP 服务器。减散文。留实质。

`caveman-shrink` 是 [Model Context Protocol](https://modelcontextprotocol.io) 的 stdio 代理。它位于 Claude（或任何 MCP 客户端）与上游 MCP 服务器之间，使用与 [caveman](../..) 技能相同的边界压缩散文字段（`description` 等）——保留代码、URL、路径和标识符，去除冠词、填充词、模糊用语和客套话。

结果：模型阅读工具目录消耗更少 token，工具语义不变。

## 安装

```bash
npm install -g caveman-shrink
# 或直接通过 npx 运行
npx caveman-shrink <upstream-command> [...args]
```

## 使用

在 Claude Code（或其他客户端）配置中包裹任意 MCP 服务器：

```jsonc
{
  "mcpServers": {
    "fs-shrunk": {
      "command": "npx",
      "args": [
        "caveman-shrink",
        "npx", "@modelcontextprotocol/server-filesystem", "/path/to/dir"
      ]
    }
  }
}
```

代理将上游作为子进程启动，拦截 `tools/list`、`prompts/list`、`resources/list` 响应，并重写 `description` 字段（以及 `CAVEMAN_SHRINK_FIELDS` 中列出的所有字段）。

## 不触碰的内容

按设计，v1 保守：

- **请求体**传递到上游不变。
- **工具调用响应**（`tools/call`）传递不变。我们不想冒险静默修改上游返回给模型的数据。
- **标识符、URL、路径和代码类 token**在任何散文中精确保留。与父穴居技能相同边界。

## 配置

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `CAVEMAN_SHRINK_FIELDS` | `description` | 逗号分隔的要压缩字段名列表 |
| `CAVEMAN_SHRINK_DEBUG` | `0` | 设 `1` 将每字段压缩差值记录到 stderr |

## 状态

Pre-1.0 — 压缩规则和字段集可能变化。本包属于 [caveman 生态](https://github.com/JuliusBrussee/caveman)；完整技能套件见父仓库（`caveman`、`cavemem`、`cavekit`、`cavecrew`、`caveman-stats`、`caveman-init`）。

## 许可证

MIT。
