# cli-agentx

一个纯本地运行的 AI Agent CLI。

它把 LLM、工具调用、话题管理、运行日志和文件系统记忆整合成一个单二进制工作流：
- 用 `agent send` 发起或继续对话
- 用 `run(command="...")` 让模型调用本地命令
- 用 `data/memory/` 保存可读、可检索、可压缩的本地记忆
- 用 `logs/` 保留完整 LLM 请求/响应日志

---

## 特性

- **单工具模型**：LLM 只有一个工具 `run(command, stdin?)`，所有能力都走命令行子命令
- **本地优先**：运行、存储、记忆、日志都在本地目录，便于调试和审计
- **文件系统记忆**：记忆完全基于 `data/memory/`，不依赖向量数据库
- **Agentic Loop**：支持多轮工具调用、异步运行、运行中注入消息
- **事件流运行时**：agent_start / turn / tool / message 等阶段都会输出结构化事件
- **分层消息**：运行态消息和发给 LLM 的消息分层管理，减少上下文污染
- **完整日志**：每次 LLM 调用都会写入 JSON 日志，方便复盘问题
- **树状会话**：每个 topic 现在保留 run 分支树，可回看和切换历史分支
- **钩子机制**：主循环预留 context / before_tool / after_tool / before_finish 拦截点
- **会话隔离**：每个话题有独立的文件目录和运行历史
- **Agent Teams**：支持按 `depends_on` 自动切并行 stage，把多个 researcher 并发执行后再进入 implementer，并提供 team 内通信命令

## 工作方式

一次同步对话的大致流程如下：

```text
用户消息
  → BuildContext（系统提示 + 分支历史 + recall + 环境信息）
  → Hooks.TransformContext
  → RunLoop（事件流）
      ├─ agent_start / turn_start
      ├─ CallLLM（流式）
      ├─ 如果有 tool_calls
      │    ├─ before_tool hook
      │    ├─ 执行 run(command="...")
      │    ├─ after_tool hook
      │    └─ 追加 tool_result 再继续推理
      └─ 没有 tool_calls
           ├─ before_finish hook
           └─ 输出最终回复
  → 保存消息并推进 topic 当前 leaf
  → 异步整理本地记忆并回写节点摘要
```

## 记忆系统

当前版本的记忆系统是**纯文件系统方案**。

同时，会话主干已经升级为 **topic 内的 run 树**：
- topic 保留一个当前 `leaf`
- 每次成功 run 都会挂成一个新节点
- 可以切回旧节点继续，形成分支

### 设计原则

- **Facts**：显式写入的稳定事实，由 `memory store` 维护
- **Summaries**：每次 Run 完成后自动生成摘要，写入本地记忆文件
- **Recall**：新消息到来时，从本地分层记忆中召回相关内容注入 `<recall>`
- **Compact**：旧的运行笔记可归档到 `archive/`，只保留近期热数据

### 目录结构

主要目录：`cli-agentx/data/memory/`

```text
data/memory/
├── .abstract
├── MEMORY.md
├── SESSION-STATE.md
├── insights/
│   ├── .abstract
│   └── YYYY-MM.md
├── lessons/
│   ├── .abstract
│   └── operational-lessons.jsonl
├── runs/
│   └── YYYY-MM-DD/*.md
└── archive/
    └── runs/
        └── YYYY-MM-DD/*.md
```

### 分层含义

- `P0`：稳定事实，主要体现在 `lessons/operational-lessons.jsonl`
- `P1`：提炼后的长期上下文，主要体现在 `MEMORY.md`
- `P2`：最近的热工作区，主要体现在 `SESSION-STATE.md`
- `L0/L1/L2`：导航、浓缩、细节三层文件视图

### 检索方式

`memory search` 和自动 recall 都只依赖本地文件：
- 扫描关键 memory 文件
- 对文本块做轻量 lexical 匹配和排序
- 过滤 topic / keyword
- 返回分层命中结果

这让记忆系统更容易：
- 直接查看
- 手动校验
- 排查为什么命中或没命中

---

## 项目结构

```text
cli-agentx/
├── cmd/agent/
│   ├── main.go             # CLI 入口
│   ├── cli_helpers.go      # 输出/子进程/注册表辅助
│   ├── send_cmd.go         # send 命令
│   ├── teams_cmd.go        # agent teams 命令
│   ├── topic_cmd.go        # topic/run 相关命令
│   ├── config_cmd.go       # 配置命令
│   ├── skill_cmd.go        # skill 命令
│   └── worker_cmd.go       # 异步 worker / 记忆整理 worker
│
├── internal/
│   ├── loop.go             # Agentic loop
│   ├── llm.go              # OpenAI 兼容流式调用
│   ├── llm_anthropic.go    # Anthropic 调用
│   ├── logger.go           # LLM 调用日志
│   ├── tools.go            # run 工具注册表与内置命令
│   ├── chain.go            # 命令链解析（&& ; || |）
│   ├── config.go           # YAML 配置读写
│   ├── db.go               # topics/messages/runs 存储
│   ├── run.go              # Run 生命周期
│   ├── context.go          # 上下文构建与 recall 注入
│   ├── memory.go           # 记忆检索/摘要/facts
│   ├── memory_store.go     # 本地记忆文件刷新与 compact
│   ├── fs.go               # 文件命令
│   ├── skill.go            # skill CRUD
│   ├── output.go           # 输出格式
│   ├── sanitize.go         # 用户内容/think 提取
│   ├── media.go            # 图片 MIME 处理
│   ├── teams.go            # team 配置 / 状态
│   └── upload.go           # 附件上传
│
├── seed/
│   ├── schema.sql          # 初始 SQLite 表结构
│   ├── config.yaml         # 默认配置模板
│   ├── skills/             # 内置 skills
│   └── teams/              # 内置 team 模板与角色 prompts
│
├── data/
│   ├── agent.db            # topics/messages/runs 数据库
│   ├── config.yaml         # 实际运行配置
│   ├── memory/             # 本地记忆目录
│   ├── skills/             # skills
│   ├── topics/             # 每个 topic 的文件目录
│   └── runs/               # 异步运行输出
│
└── logs/
    └── {topicID}/
        └── {runID}_call_*.json
```

---

## 快速开始

### 1. 构建

```bash
cd cli-agentx
go build -o agent ./cmd/agent
```

### 2. 配置

首次运行前，准备 `data/config.yaml`。
如果文件不存在，通常可从 `seed/config.yaml` 复制一份。

示例：

```yaml
name: pi

providers:
  dashscope:
    base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
    api_key: sk-your-key-here

llm_provider: dashscope
llm_model: qwen-plus

system_prompt: |
  你是一个高效的 AI 助手。
  简洁直接，优先完成任务。
```

支持的模型协议：
- **OpenAI 兼容**：如 DashScope、OpenRouter、DeepSeek 等
- **Anthropic**：给 provider 设置 `protocol: anthropic`

### 3. 发送消息

```bash
# 新建 topic 并发送
./agent send -p "帮我分析一下当前目录的文件结构"

# 在指定 topic 中继续
./agent send -t <topic-id> -p "继续上面的任务"

# 异步运行
./agent send -t <topic-id> -p "执行一个耗时任务" --async
```

### 4. 查看 topic 和 run

```bash
# 创建 topic
./agent create-topic -n "排查 Redis 延迟"

# 列出 topics
./agent list-topics

# 查看 topic 消息
./agent get-topic <topic-id>

# 查看 run 状态/输出
./agent get-run <run-id>

# 取消运行中的 run
./agent cancel-run <run-id>
```

### 5. 修改配置

```bash
# 查看配置（敏感字段已脱敏）
./agent config

# 设置配置项
./agent config set providers.dashscope.api_key sk-xxx

# 删除配置项
./agent config delete browser
```

### 6. 管理 skills

```bash
# 列出 skills
./agent skill list

# 查看 skill
./agent skill get daily-journal

# 删除 skill
./agent skill delete daily-journal
```

`skill save` 使用 stdin JSON：

```bash
printf '%s' '{
  "name": "daily-journal",
  "description": "记录每日总结",
  "content": "先收集今天完成的事项，再按要点输出。"
}' | ./agent skill save
```

### 7. 上传附件

`upload` 使用 stdin JSON：

```bash
printf '%s' '{
  "topic_id": "<topic-id>",
  "path": "/absolute/path/to/file.png"
}' | ./agent upload
```

上传后的附件可在 `send` 的 JSON 输入里通过 `attachments` 传入。

### 8. 运行 agent teams

内置了一个 `default` team 模板，按 `planner -> (researcher-codebase || researcher-risks) -> implementer -> tester` 执行。

```bash
# 查看可用 team
./agent teams list

# 查看默认 team 配置
./agent teams show default

# 直接运行默认 team
./agent teams run default -p "为当前任务制定计划并产出验证报告"

# 使用 tmux 运行，让每个角色在独立 tmux window 中执行
./agent teams run default -p "整理一个多角色工作流" --runner tmux

# 查看 team run 状态
./agent teams status <team-run-id>

# 把默认模板复制到 data/teams/my-team/ 里进行自定义
./agent teams init my-team
```

team 会创建一个协调 topic，把共享工件写到该 topic 目录中：

- `task.md`：原始任务
- `manifest.md`：角色、topic、artifact 对照
- `plan.md` / `research-codebase.md` / `research-risks.md` / `implementation.md` / `test-report.md`：默认角色产物
- `team-messages.md`：team 内部消息日志

并发 stage 中，运行中的角色可通过唯一工具里的 `team` 子命令互相通信：

```bash
run(command="team status")
run(command="team send researcher-risks \"请补充登录失败路径的边界条件\"")
run(command="team broadcast \"我已经确认当前模块没有现成测试\"")
run(command="team messages 20")
```

默认会把通信范围限制在**当前并发 stage 且正在运行的其他角色**，这样可以减少对下游 stage 的提前污染。

当 team 角色需要读取当前仓库源码而不是 coordinator topic 工件时，应使用只读仓库命令：

```bash
run(command="repo-ls internal/")
run(command="repo-cat cmd/agent/teams_runtime.go | head -n 120")
run(command="repo-grep SendTeamMessages internal/")
```

约定上：

- `cat /<topic-id>/...` 用于读取 team 共享工件
- `write /<topic-id>/<file>` 用于写回 team 工件
- `repo-*` 只用于读取仓库源码，不会写入仓库文件

---

## CLI 命令

顶层 Cobra 命令：

- `send`
- `create-topic`
- `list-topics`
- `get-topic`
- `get-run`
- `cancel-run`
- `config`
- `skill`
- `upload`
- `teams`

所有命令都支持：
- `--output raw`
- `--output jsonl`

---

## Agent 内置命令

模型并不直接调用 Go 函数，而是统一通过：

```text
run(command="...")
```

可用子命令大致分为：

| 类别 | 命令 | 说明 |
|------|------|------|
| 文件 | `ls`, `cat`, `write`, `stat`, `rm`, `cp`, `mv`, `mkdir`, `see` | 操作 topic 目录内文件 |
| 仓库只读 | `repo-ls`, `repo-cat`, `repo-grep` | 从仓库根目录只读浏览源码，适合 team 角色调研代码 |
| 文本 | `grep`, `head`, `tail`, `wc`, `echo` | 文本处理 |
| 记忆 | `memory search/recent/compact/store/facts/forget` | 本地记忆检索与管理 |
| 话题 | `topic list/info/runs/run/tree/checkout/rename/search` | topic / run 浏览与分支切换 |
| Skill | `skill list/load/search/create/update/delete` | 经验与指南复用 |
| 配置 | `config set/delete` | 修改运行配置 |
| 系统 | `time`, `help` | 通用辅助 |

支持链式命令：

```bash
run(command="ls && cat notes.md")
run(command="cat data.txt | grep error | wc -l")
run(command="write a.txt hello ; write b.txt world")
run(command="memory compact 7")
```

---

## LLM 调用日志

每次调用 LLM，都会在 `logs/{topicID}/` 下生成一个 JSON 文件。

文件名格式：

```text
{runID}_call_{N:03d}_{timestamp}.json
```

日志中包含：
- `session_id`
- `run_id`
- `call_index`
- `timestamp`
- `duration_ms`
- `provider`
- `model`
- `request`
- `response`

这对排查下面几类问题特别有用：
- 为什么模型没调工具
- 为什么 tool arguments 不对
- 为什么某轮输出中断
- 为什么某次 recall 没起作用

---

## 数据说明

### SQLite 中保留什么

当前数据库主要保存运行态和会话态信息：
- `topics`
- `messages`
- `runs`
- 其他运行辅助表

### 文件系统中保存什么

以下内容主要在文件系统中维护：
- 记忆
- facts
- run 摘要
- 月度 insights
- 归档后的旧 run notes
- topic 文件目录
- LLM 调用日志

也就是说：
- **数据库**更像运行索引和会话存档
- **文件系统**是记忆的唯一来源

---

## 适合的场景

`cli-agentx` 适合：
- 本地运行的个人 agent
- 需要完整日志和可审计记忆的实验环境
- 想用单二进制管理 topic、文件、skills、memory 的工作流
- 更关注可读、可调试的记忆，而不是复杂的向量检索系统

---

## 技术栈

- **语言**：Go 1.22+
- **CLI 框架**：`cobra`
- **数据库**：`modernc.org/sqlite`
- **配置**：`gopkg.in/yaml.v3`
- **LLM 协议**：OpenAI SSE 流式 + Anthropic 原生 API

---

## 当前状态

当前版本重点是：
- 单工具 `run(command)` 的 agent 交互模型
- 本地文件系统记忆
- 可回放的 LLM 调用日志
- 基于 topic / run 的会话组织方式

如果你准备继续扩展，建议优先看这些文件：
- `cli-agentx/internal/runtime.go`（分层消息、事件、hooks）
- `cli-agentx/internal/context.go`（按当前分支构建上下文）
- `cli-agentx/internal/db.go`（session node / leaf 管理）
- `cli-agentx/internal/loop.go`（事件化 agent loop）
- `cli-agentx/internal/memory.go`
- `cli-agentx/internal/memory_store.go`
- `cli-agentx/internal/tools.go`
- `cli-agentx/cmd/agent/send_cmd.go`
