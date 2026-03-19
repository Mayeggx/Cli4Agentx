# cli-agentx

一个纯本地运行的 AI Agent CLI 工具，基于 [agent-clip](../agent-clip) 改造而来，去除了所有前端和 Pinix 平台依赖，专注于命令行交互与完整的 LLM 调用日志能力。

---

## 项目背景与设计思路

### 为什么从 agent-clip 改造？

`agent-clip` 是一个功能完整的 AI Agent，但它的架构依赖 [Pinix](https://github.com/epiral/pinix) 平台才能运行——包括 Clip 协议通信、Web 前端、浏览器远程调用等。这使得它在纯本地环境下显得过重。

`cli-agentx` 的目标是**去掉平台依赖，保留核心 Agent 能力**，让它成为一个可以直接在任意机器上运行的纯 Go 二进制程序。

### 核心设计原则

**1. 单一工具哲学**

LLM 只有一个工具 `run(command, stdin?)`，所有能力都是这个工具的子命令。这避免了 function calling 的组合爆炸问题，让 LLM 专注于"写命令"而不是"选工具"。

```
run(command="memory search 上次做了什么")
run(command="ls && cat notes.md")
run(command="write report.md" stdin="# 报告内容...")
```

**2. Agentic Loop**

每次对话触发一个 Run，进入最多 20 轮的 tool-call 循环：

```
用户消息
  → BuildContext（系统提示 + 历史 + 记忆召回 + 环境信息）
  → RunLoop
      ├─ CallLLM（流式 SSE）
      ├─ 有 tool_calls → 执行命令 → 追加 tool_result → 继续
      ├─ 检查 inbox（支持异步注入消息）
      └─ 无 tool_calls → 输出最终回复 → 结束
```

**3. 三层记忆系统**

- **Facts**：手动存储的持久化事实（`memory store`）
- **Summaries**：每次 Run 结束后由 LLM 自动生成摘要 + 向量嵌入
- **语义搜索**：新消息自动召回相关历史摘要（余弦相似度 > 0.5）

**4. LLM 调用全量日志**

每次调用 LLM 都会在 `logs/{topicID}/` 下写入一个 JSON 文件，完整记录请求体和响应体，便于调试和分析。

---

## 项目结构

```
cli-agentx/
├── cmd/agent/              # CLI 入口（cobra）
│   ├── main.go             # 注册所有子命令
│   ├── cli_helpers.go      # 工具函数（buildRegistry / spawnDetached）
│   ├── send_cmd.go         # send 命令（同步/异步 agentic loop）
│   ├── topic_cmd.go        # topic/run CRUD 命令
│   ├── config_cmd.go       # config 查看/修改命令
│   ├── skill_cmd.go        # skill 管理命令
│   └── worker_cmd.go       # 内部 worker（异步 run、memory）
│
├── internal/               # 核心 Go 包
│   ├── loop.go             # Agentic loop 引擎
│   ├── llm.go              # LLM 调用（OpenAI 流式协议）
│   ├── llm_anthropic.go    # Anthropic 协议支持
│   ├── logger.go           # LLM 调用日志（每次调用写 JSON 文件）
│   ├── tools.go            # 命令注册表 + 内置命令
│   ├── chain.go            # 命令链解析（&& ; | ||）
│   ├── config.go           # YAML 配置读写
│   ├── db.go               # SQLite 操作（Topics/Messages/Runs）
│   ├── run.go              # Run 生命周期
│   ├── context.go          # LLM 上下文构建
│   ├── memory.go           # 记忆系统（摘要/Facts/语义搜索）
│   ├── embed.go            # 向量嵌入 API
│   ├── fs.go               # 文件 I/O 命令（ls/cat/write/stat/rm...）
│   ├── skill.go            # Skill CRUD
│   ├── output.go           # 输出接口（CLI raw / JSONL）
│   ├── sanitize.go         # 消息内容提取
│   ├── media.go            # 图片 MIME 类型
│   └── upload.go           # 本地文件附件处理
│
├── seed/
│   ├── schema.sql          # SQLite 表结构
│   ├── config.yaml         # 默认配置模板
│   └── skills/             # 内置 skill 文件
│
├── data/                   # 运行时数据（gitignore）
│   ├── agent.db            # SQLite 数据库
│   ├── config.yaml         # 实际运行配置
│   ├── skills/             # skill 文件
│   ├── topics/             # 话题文件目录
│   └── runs/               # 异步 run 输出目录
│
└── logs/                   # LLM 调用日志（gitignore）
    └── {topicID}/
        ├── {runID}_call_001_{timestamp}.json
        └── {runID}_call_002_{timestamp}.json
```

---

## 快速开始

### 1. 构建

```bash
cd cli-agentx
go build -o agent ./cmd/agent/
```

### 2. 配置

编辑 `data/config.yaml`（首次运行会自动从 `seed/config.yaml` 创建）：

```yaml
name: pi

providers:
  dashscope:
    base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
    api_key: sk-your-key-here

llm_provider: dashscope
llm_model: qwen-plus

# 可选：配置向量嵌入（启用语义记忆搜索）
embedding_provider: dashscope
embedding_model: text-embedding-v4

system_prompt: |
  你是一个高效的 AI 助手...
```

支持的 LLM 协议：
- **OpenAI 兼容**（默认）：DashScope、OpenRouter、DeepSeek 等
- **Anthropic**：设置 `protocol: anthropic`

### 3. 使用

```bash
# 发送消息（自动创建话题）
./agent send -p "帮我分析一下当前目录的文件结构"

# 在指定话题中继续对话
./agent send -t <topic-id> -p "继续上面的任务"

# 异步执行（后台运行）
./agent send -p "执行一个耗时任务" --async

# 查看话题列表
./agent list-topics

# 查看某话题的消息记录
./agent get-topic <topic-id>

# 查看/修改配置
./agent config
./agent config set providers.dashscope.api_key sk-xxx
```

---

## LLM 调用日志

每次调用 LLM，都会在 `logs/{topicID}/` 下生成一个 JSON 日志文件：

**文件名格式：** `{runID}_call_{N:03d}_{timestamp}.json`

**日志内容示例：**

```json
{
  "session_id": "a1b2c3d4",
  "run_id": "e5f6g7h8",
  "call_index": 1,
  "timestamp": "2026-03-17T12:00:00+08:00",
  "duration_ms": 2341,
  "provider": "https://dashscope.aliyuncs.com/compatible-mode/v1",
  "model": "qwen-plus",
  "request": {
    "model": "qwen-plus",
    "messages": [...],
    "tools": [...],
    "stream": true,
    "max_tokens": 16384
  },
  "response": {
    "content": "好的，我来查看一下...",
    "reasoning": "",
    "tool_calls": [
      {
        "id": "call_xxx",
        "type": "function",
        "function": {
          "name": "run",
          "arguments": "{\"command\":\"ls\"}"
        }
      }
    ]
  }
}
```

同一会话（topicID）的所有 Run 的日志都存放在同一个子文件夹下，方便按对话维度查阅完整交互历史。

---

## 可用命令（Agent 内置）

LLM 可以通过 `run` 工具调用以下命令：

| 类别 | 命令 | 说明 |
|------|------|------|
| **文件** | `ls`, `cat`, `write`, `stat`, `rm`, `cp`, `mv`, `mkdir` | 文件 I/O，路径限定在话题目录内 |
| **文本** | `grep`, `head`, `tail`, `wc`, `echo` | 文本处理工具 |
| **记忆** | `memory search/recent/store/facts/forget` | 语义搜索 + 持久化事实 |
| **话题** | `topic list/info/runs/run/rename/search` | 会话管理 |
| **Skill** | `skill list/load/search/create/update/delete` | 可复用操作指南 |
| **配置** | `config set/delete` | 运行时修改配置 |
| **系统** | `time`, `help` | 获取时间、查看帮助 |

命令支持链式调用：

```bash
# && 前成功才执行后者
run(command="ls && cat notes.md")

# | 管道
run(command="cat data.txt | grep error | wc -l")

# ; 顺序执行
run(command="write a.txt hello ; write b.txt world")
```

---

## 与 agent-clip 的区别

| 特性 | agent-clip | cli-agentx |
|------|-----------|------------|
| 运行方式 | 需要 Pinix 平台 | 纯本地，单二进制 |
| 前端 | React Web UI | 无（命令行） |
| Clip 通信 | Connect-RPC | 已移除 |
| 浏览器控制 | bb-browser HTTP | 已移除 |
| 定时事件 | 支持 | 已移除 |
| LLM 日志 | 无 | **每次调用完整记录** |
| 依赖 | connectrpc + pinix | 仅标准库 + sqlite + cobra |

---

## 技术栈

- **语言**：Go 1.22+
- **CLI 框架**：[cobra](https://github.com/spf13/cobra)
- **数据库**：[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite)（CGO-free SQLite，含 FTS5 全文搜索）
- **配置**：YAML（[gopkg.in/yaml.v3](https://pkg.go.dev/gopkg.in/yaml.v3)）
- **LLM 协议**：OpenAI SSE 流式 + Anthropic 原生 API
