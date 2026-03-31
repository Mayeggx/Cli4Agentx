# Handoff

## 背景

本轮工作围绕 `teams` 模式的真实回归问题展开，目标是修复默认 team 模板在端到端执行中的两个主要失败点：

1. team 角色无法读取仓库源码，只能访问 topic 目录，导致 `researcher-*` 角色缺乏真实代码证据。
2. team 角色经常误用 `write`，或在探索阶段消耗过多轮次，触发 `agentic loop exceeded 20 iterations`。

## 已完成改动

### 1. `internal/fs.go`

新增只读仓库浏览命令：

- `repo-ls [dir]`
- `repo-cat <path>`
- `repo-grep [-i] <pattern> [dir]`

这些命令以仓库根目录为只读范围，不写入任何文件，供 team 角色在保持 topic 沙箱写隔离的前提下读取真实源码。

### 2. `cmd/agent/teams_runtime.go`

#### team 角色系统提示增强

新增并强化了以下约束：

- 明确区分 team 工件路径 `/<topic-id>/...` 与仓库源码路径。
- 强制工件写入使用 `write /<topic-id>/<file>`，禁止 heredoc、shell 重定向等伪 shell 写法。
- 指导 team 角色使用 `repo-ls` / `repo-cat` / `repo-grep` 读取源码。
- 增加“避免无节制探索”的提示，要求优先读最相关的 3-6 个文件。
- 为 `researcher-codebase`、`researcher-risks`、`tester` 添加了更具体的角色级指导。

#### team 角色完成时序调整

`executeTeamRole()` 中把：

- `markRoleFinished(teamRunID, roleIndex, "done", ...)`

移动到：

- `internal.ProcessMemory(...)`

之前执行，避免 memory 后处理阻塞 stage 推进。

#### stage 等待逻辑收敛

`waitForTeamRoleCompletion()` 现在在 `run.Status != "running"` 后会额外等待 team state 收敛：

- 若子进程仍存活，则继续短轮询。
- 若 run 已结束但 team role state 还未落盘，则给出宽限窗口再判定。

这避免了早期版本中 “run 已 done，但 role 仍显示 running” 被父调度器误判失败。

## 真实回归结果

### 已验证通过

最新一轮具有代表性的真实回归是：

- team run: `867a9776`
- coordinator topic: `aa02e8e9`

在这一轮中，以下行为已经被真实验证：

- `planner` 成功完成并进入 stage 1。
- `researcher-codebase` 成功完成，不再触发 20 轮上限。
- `researcher-codebase` 成功产出 `aa02e8e9/research-codebase.md`。
- team 通信链路已打通，`researcher-codebase -> researcher-risks` 的消息被持久化到：
  - team run state `data/teams/runs/867a9776.json`
  - `data/topics/aa02e8e9/team-messages.md`

### 仍未闭环的问题

在同一轮回归中：

- `researcher-risks` 最终没有成功收尾。
- `implementer` 和 `tester` 因上游未全部完成而未进入执行。

当前掌握的信息：

- `researcher-risks` 仍可能在探索阶段耗尽轮次或异常退出。
- `data/teams/runs/867a9776.json` 最终 `status` 为 `error`，且 `researcher-risks` 状态与 run 结束状态存在不一致现象。
- 我已经继续把 `researcher-risks` 的角色级提示收紧为：
  - 只看少量关键片段
  - 前 2-3 次工具调用内先发消息
  - 拿到 3-5 个风险点后立即写工件

但这最后一版还没有完成一次新的全链路验证。

## 当前代码状态

本轮实际改动文件：

- `internal/fs.go`
- `cmd/agent/teams_runtime.go`
- `README.md`
- `handoff.md`

编译与静态检查状态：

- `go build -o agent ./cmd/agent` 通过
- 已修改文件无新的 linter 报错

## 建议的下一步

建议按这个顺序继续：

1. 用最新二进制重新跑一轮完整 `teams run default`。
2. 重点观察 `researcher-risks` 是否还能触发 20 轮上限。
3. 如果 `researcher-risks` 仍失败，优先读取其 `get-run --output raw` 结果，确认是：
   - 读太多文件
   - `team send` / `team messages` 交互循环
   - 还是写工件前耗尽轮次
4. 一旦 stage 1 两个 researcher 都通过，再继续验证：
   - `implementer` 是否成功读取两个 research 工件
   - `tester` 是否产出包含“通过项 / 风险项 / 未验证项”的 `test-report.md`
5. 若需要进一步降低探索轮次，可继续在 `buildTeamRoleSystemPrompt()` 中对 `researcher-risks` 增加更硬的工具预算或文件白名单。

## 参考文件

- `cmd/agent/teams_runtime.go`
- `internal/fs.go`
- `internal/team_tools.go`
- `internal/teams.go`
- `data/teams/runs/867a9776.json`
- `data/topics/aa02e8e9/research-codebase.md`
- `data/topics/aa02e8e9/team-messages.md`
