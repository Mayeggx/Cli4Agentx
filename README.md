# cli-agentx

一个纯本地运行的 AI Agent CLI。

它把 LLM、工具调用、话题管理、运行日志和文件系统记忆整合成一个单二进制工作流：
- 用 `agent send --worktree <name>` 在隔离 Git worktree 中发起或继续对话
- 用受审批、受沙箱保护的 `shell <program> [arg...]` 调用外部程序
- 用 `data/checkpoints/` 保存可恢复的 Agent 执行检查点
- 用 `data/memory/` 保存可读、可检索、可压缩的本地记忆
- 用 `logs/` 保留完整 LLM 请求/响应日志

---

## 特性

- **单工具模型**：LLM 通过 `run(command, stdin?)` 调用内置子命令；外部程序仅能经受控 `shell` 子命令执行
- **隔离执行**：每个 Agent run 在受管理 Git worktree 中运行，主仓库不能作为外部命令的写入目录
- **命令审批**：默认逐条确认外部命令；`--yes` 只对当前一次 `agent send` 有效
- **操作系统沙箱**：默认要求 macOS `sandbox-exec` 或 Linux `bwrap`，默认禁用网络
- **可恢复运行**：在模型调用前、工具调用后和结束时自动保存 checkpoint
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
      ├─ 保存 `before_llm` checkpoint
      ├─ CallLLM（流式）
      ├─ 如果有 tool_calls
      │    ├─ before_tool hook
      │    ├─ 执行内置命令或受控 `shell`
      │    ├─ 外部命令：审批 → OS 沙箱 → 审计
      │    ├─ after_tool hook
      │    ├─ 保存 `after_tool` checkpoint
      │    └─ 追加 tool_result 再继续推理
      └─ 没有 tool_calls
           ├─ 保存 `completed` checkpoint
           ├─ before_finish hook
           └─ 输出最终回复
  → 保存消息并推进 topic 当前 leaf
  → 异步整理本地记忆并回写节点摘要
```

## 隔离执行、审批与恢复

### 受管理 Git worktree

外部命令仅可在受管理 Git worktree 中执行；主仓库和任意未受管理目录都会被拒绝。`agent send --worktree <name>` 会基于配置的 `base_branch`（默认 `main`）创建或复用 `agentx/<name>` 分支及其隔离目录。

```bash
# 创建隔离 worktree 并在其中执行 Agent。
./agent send --worktree feature-a -p "实现当前需求"

# 也可单独管理 worktree。
./agent worktree create feature-a
./agent worktree list
./agent worktree remove feature-a
```

默认 worktree 根目录是 `.agentx/worktrees/`。创建时会写入仅作用于该目录的 `.gitignore`，避免 worktree 容器出现在主仓库 Git 状态中。`remove` 默认拒绝删除存在未提交修改的 worktree；仅显式传入 `--force` 时才会强制删除。

### 命令审批与 OS 沙箱

Agent 的内置文件、记忆和话题命令仍由运行时处理。调用外部程序时必须使用 `shell <program> [arg...]`，并具有以下约束：

- 不启动 Shell，禁止 `sh`、`bash`、`zsh`、管道、重定向和命令替换；
- 默认每条外部命令均需要确认，输入 `y` 或 `yes` 才会运行；
- `agent send --yes` 仅自动批准当前一次 run，不能持久化更改审批策略；
- macOS 使用 `sandbox-exec`，Linux 使用 `bwrap`；默认禁网、超时 120 秒、输出上限 4 MiB；
- 审批事件写入 `data/audit/command-approvals.jsonl`，包含敏感名称的参数会被脱敏；
- 受控 `git` 命令仅额外访问 linked worktree 所需的 Git 元数据目录。

默认策略会在缺少 OS 沙箱时失败关闭。只有可信的本地开发环境，才应由用户在配置中显式设为 `runtime.sandbox.mode: disabled`。

### Checkpoint 恢复

每个 run 在模型请求前、工具结果写入后以及完成时都会保存 checkpoint。检查点存放在 `data/checkpoints/<run-id>/`，采用仅当前用户可读写的文件权限。

```bash
./agent checkpoint list --run <run-id>
./agent checkpoint show --run <run-id> <checkpoint-id>
./agent checkpoint delete --run <run-id> <checkpoint-id>

# 恢复时会验证并自动回到 checkpoint 原先记录的受管理 worktree。
./agent send --resume-run <run-id> --checkpoint <checkpoint-id>
```

恢复 checkpoint 时不能额外传入 `--worktree`，也不能切换到其他目录，以避免消息上下文与实际修改目录不一致。

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
│   └── main.go             # send、worktree、checkpoint CLI 入口
│
├── internal/
│   ├── loop.go             # Agentic loop 与 checkpoint 保存时机
│   ├── checkpoint.go       # 私有 checkpoint 持久化与恢复校验
│   ├── secure_execution.go # 命令审批、审计、超时与 OS 沙箱执行器
│   ├── secure_commands.go  # 受控 shell 子命令注册
│   ├── worktree.go         # 受管理 Git worktree 生命周期
│   ├── llm.go              # OpenAI 兼容流式调用
│   ├── llm_anthropic.go    # Anthropic 调用
│   ├── logger.go           # LLM 调用日志
│   ├── tools.go            # run 工具注册表与内置命令
│   ├── chain.go            # 内置命令链解析（&& ; || |）
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
│   ├── checkpoints/        # 每次 run 的私有恢复点
│   ├── audit/              # 外部命令审批审计日志
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
  openrouter:
    base_url: https://openrouter.ai/api/v1
    api_key: ""              # 不在配置文件中保存密钥

llm_provider: openrouter
llm_model: anthropic/claude-3.5-haiku

runtime:
  sandbox:
    mode: required          # `required` 或仅可信环境下的 `disabled`
    allow_network: false
    timeout_seconds: 120
  command_approval:
    mode: always            # `always`、`dangerous`、`never`
    audit: true
  worktree:
    base_branch: main
    root_dir: .agentx/worktrees

system_prompt: |
  你是一个高效的 AI 助手。
  简洁直接，优先完成任务。
```

配置 OpenRouter 时，请在启动前提供 API Key：

```bash
export OPENROUTER_API_KEY='...'
```

不要将 API Key 写入 `data/config.yaml`、worktree、prompt、checkpoint 或审计日志。

支持的模型协议：
- **OpenAI 兼容**：如 DashScope、OpenRouter、DeepSeek 等
- **Anthropic**：给 provider 设置 `protocol: anthropic`

### 3. 发送消息

```bash
# 创建隔离 worktree 并在其中执行；外部命令只可在此目录内运行。
./agent send --worktree analyze-repo -p "帮我分析一下当前目录的文件结构"

# 复用已有隔离 worktree，在指定 topic 中继续。
./agent send --worktree analyze-repo -t <topic-id> -p "继续上面的任务"

# 仅在可信任务中为当前 run 自动批准外部命令。
./agent send --worktree analyze-repo --yes -p "运行已确认的测试命令"
```

### 4. 管理 worktree 和 checkpoint

```bash
# 显式创建、列出和删除受管理 worktree。
./agent worktree create feature-a
./agent worktree list
./agent worktree remove feature-a

# 查看某个 run 的可恢复状态。
./agent checkpoint list --run <run-id>
./agent checkpoint show --run <run-id> <checkpoint-id>

# 恢复时自动回到 checkpoint 的原始 worktree。
./agent send --resume-run <run-id> --checkpoint <checkpoint-id>
```

### 5. 修改配置

`data/config.yaml` 是用户持有的安全策略文件。请在启动 Agent 前手动编辑它；运行中的 Agent 只能查看配置，不能通过工具修改 `sandbox`、`command_approval` 或 `worktree`。API Key 等敏感值仅应通过环境变量提供，勿写入配置文件、worktree、prompt、checkpoint 或审计日志。

## CLI 命令

| 命令 | 说明 |
|---|---|
| `agent send --worktree <name> -p <prompt>` | 创建或复用隔离 worktree 后运行 Agent |
| `agent send --worktree <name> --yes -p <prompt>` | 仅为当前 run 自动批准外部命令 |
| `agent send --resume-run <run-id> --checkpoint <id>` | 在 checkpoint 的原 worktree 中恢复 |
| `agent worktree create/list/remove` | 管理 `agentx/<name>` 隔离分支和目录 |
| `agent checkpoint list/show/delete --run <run-id>` | 查看、读取或清理恢复点 |

`agent send` 支持 `--format raw`（默认）和 `--format jsonl`。`worktree remove` 默认拒绝删除未提交修改，只有传入 `--force` 才会强制移除。

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
| 配置 | `config` | 只读查看当前配置；Agent 运行中不能修改安全策略 |
| 外部程序 | `shell <program> [arg...]` | 经人工审批、审计、超时和 OS 沙箱执行单一程序 |
| 系统 | `time`, `help` | 通用辅助 |

内置命令可使用既有命令链解析；但 `shell` 不是 Shell：它只接受一个程序与显式参数，不能使用 `|`、`&&`、`;`、重定向、命令替换或脚本解释器。

```bash
# 合法：执行一个已审批的程序及参数。
run(command="shell go test ./...")

# 非法：不会启动 Shell，也不会解释管道或命令替换。
run(command="shell sh -c 'go test ./... | tee test.log'")
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
- 私有 checkpoint 与命令审批审计日志

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
- 内置 `run(command)` 与受控 `shell <program> [arg...]` 外部执行入口
- 受管理 Git worktree 隔离、逐命令审批和 OS 沙箱
- 自动 checkpoint 与可验证的原 worktree 恢复
- 本地文件系统记忆、可回放的 LLM 调用日志
- 基于 topic / run 的会话组织方式

如果你准备继续扩展，建议优先看这些文件：
- `internal/runtime.go`（分层消息、事件、hooks）
- `internal/loop.go`（事件化 Agent loop 与 checkpoint 时机）
- `internal/checkpoint.go`（检查点持久化与恢复校验）
- `internal/secure_execution.go`（审批、审计和 OS 沙箱）
- `internal/worktree.go`（受管理 worktree 生命周期）
- `internal/tools.go`（内置命令注册表）
- `cmd/agent/main.go`（CLI 入口与交互式审批）
