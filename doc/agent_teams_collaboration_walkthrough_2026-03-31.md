# Agent Teams 协作机制解剖报告（基于 run `520541fe`）

## 1. 这份报告回答什么

这份报告不是单纯复述测试是否通过，而是把这轮真实 `agent teams` 运行中：

1. Team 是怎么被创建和调度的
2. 每个成员实际读了什么、做了什么、说了什么
3. 并行 researcher 是如何通信的
4. implementer 和 tester 是如何接收上游结果的
5. 代码层面为什么会形成这种协作方式

完整串起来，帮助理解 `teams` 的工作原理。

本报告基于以下真实运行记录整理：

- Team Run：`520541fe`
- Coordinator Topic：`1290a6e9`
- Team 状态文件：`data/teams/runs/520541fe.json`
- 共享清单：`data/topics/1290a6e9/manifest.md`
- Team 通信日志：`data/topics/1290a6e9/team-messages.md`
- 各角色异步输出：`data/runs/62b817bf/output`、`data/runs/e4d5c313/output`、`data/runs/ef87c0d5/output`、`data/runs/23bca712/output`、`data/runs/db620c08/output`

---

## 2. 整体原理：它其实是“一个总控 + 多个角色进程 + 一个共享 topic”

从实现上看，`agent teams` 不是让一个模型在脑内扮演 5 个角色，而是：

1. 先根据 team 配置创建一轮 team run
2. 为每个角色创建独立 topic
3. 再按依赖关系计算 stage
4. 每个 stage 启动一个或多个独立角色进程
5. 角色通过共享工件和 team 消息协作
6. 当前 stage 全部完成后，才进入下一 stage

关键代码位置：

- CLI 入口：`cmd/agent/teams_cmd.go`
- 总调度器：`cmd/agent/teams_runtime.go`
- team 状态与消息：`internal/teams.go`
- team 工具命令：`internal/team_tools.go`
- 运行时消息注入：`internal/run.go`

### 2.1 配置如何定义团队

默认 team 模板在 `seed/teams/default/team.yaml`：

- `planner`
- `researcher-codebase`
- `researcher-risks`
- `implementer`
- `tester`

依赖关系决定 stage：

- `planner` 无依赖，所以是 stage 0
- 两个 researcher 都依赖 `planner`，所以同属 stage 1，可并行
- `implementer` 依赖 `planner + 两个 researcher`，所以是 stage 2
- `tester` 依赖 `implementer`，所以是 stage 3

这套 stage 计算来自 `internal/teams.go` 里的 `ComputeTeamStages()`。

### 2.2 总控如何启动 team

`runTeam()` 位于 `cmd/agent/teams_runtime.go`。它做了 4 件关键事：

1. 创建 coordinator topic
2. 给每个角色创建独立 topic
3. 生成 team run state，并写入 `data/teams/runs/<id>.json`
4. 按 stage 启动角色，并等待整 stage 完成

所以你可以把 coordinator topic 理解成“团队共享工作台”，把角色 topic 理解成“每个成员自己的私有工作空间”。

### 2.3 为什么工件都写进同一个 topic

虽然每个角色有独立 topic，但共享交付物统一写进 coordinator topic，例如这轮是 `1290a6e9`：

- `plan.md`
- `research-codebase.md`
- `research-risks.md`
- `implementation.md`
- `test-report.md`
- `team-messages.md`

这样下游角色只要读 coordinator topic，就能消费上游结果。

### 2.4 为什么 researcher 能互相发消息

team 角色会额外挂载 `team` 工具，定义在 `internal/team_tools.go`，支持：

- `team status`
- `team send <role> <message>`
- `team broadcast <message>`
- `team messages [limit]`
- `team artifact <role>`

消息发送最终走 `SendTeamMessages()`，实现位于 `internal/teams.go`。

它做两层持久化：

1. 把消息写进 team run state 的 `Messages` 数组
2. 追加写入 coordinator topic 下的 `team-messages.md`

如果目标角色当前正在运行，还会调用 `InjectMessage()` 把消息塞进目标 run 的 inbox，让对方在运行中收到注入消息。

### 2.5 为什么只能同 stage 通信

`internal/teams.go` 的 `isEligibleTeamMessageTarget()` 明确限制：

- 目标角色必须是 `running`
- 且要和发送者处于同一个 stage

所以：

- stage 1 的 `researcher-codebase` 可以和 `researcher-risks` 发消息
- 但它们不能直接给 stage 2 的 `implementer` 发消息
- 跨 stage 传递信息主要依赖共享工件，而不是即时消息

这就是该系统的核心协作哲学：

- 同阶段靠消息协作
- 跨阶段靠工件交接

---

## 3. 这轮运行的真实结构

根据 `data/topics/1290a6e9/manifest.md`，本轮团队结构如下：

- `planner`：topic `84ee438e`，输出 `plan.md`
- `researcher-codebase`：topic `23a4e90f`，输出 `research-codebase.md`
- `researcher-risks`：topic `6669951e`，输出 `research-risks.md`
- `implementer`：topic `eae7601c`，输出 `implementation.md`
- `tester`：topic `8b35aca4`，输出 `test-report.md`

根据 `data/teams/runs/520541fe.json`，最终所有角色都完成，team run 状态为 `done`。

---

## 4. 每个成员到底做了什么

下面这部分是最接近“现场回放”的内容，按成员拆解。

### 4.1 `planner`：先把任务转成团队执行计划

来源记录：`data/runs/62b817bf/output`

#### 它的输入

`planner` 开始时先读：

- `task.md`
- `manifest.md`

它一开始还误用过 `repo-cat /1290a6e9/task.md` 和 `repo-cat /1290a6e9/manifest.md`，发现这是错误路径，因为：

- `repo-cat` 用于读仓库源码
- `/1290a6e9/...` 是 team 工件路径，不是仓库源码路径

随后它改用：

- `ls /1290a6e9/`
- `cat /1290a6e9/task.md`
- `cat /1290a6e9/manifest.md`

这很能说明 `teams` 的一个关键设计：

- 仓库源码和 team 工件是两套路径体系
- 角色必须区分“代码证据”与“团队交付物”

#### 它的工作内容

`planner` 还读取了与 team 机制相关的仓库代码，主要是：

- `internal/team_tools.go`
- `internal/teams.go`
- `cmd/agent/teams_runtime.go`

然后写出了 `data/topics/1290a6e9/plan.md`。

#### 它的产出作用

这个计划文件定义了：

- 5 个角色的职责
- 每个角色的输出文件
- 需要验证的要点
- 研究员之间必须互发消息
- tester 报告必须包含的结构

等于说，`planner` 并不做事实验证，它做的是把原始任务转换成“团队分工协议”。

---

### 4.2 `researcher-codebase`：研究实现细节

来源记录：`data/runs/e4d5c313/output`

#### 它的输入

它先读：

- `task.md`
- `manifest.md`
- `plan.md`

之后重点读代码：

- `cmd/agent/teams_runtime.go`
- `cmd/agent/teams_cmd.go`
- `internal/teams.go`
- `internal/team_tools.go`
- `seed/teams/default/team.yaml`

#### 它的真实行为特点

它也出现过几次工具使用偏差：

- 误用 `repo-cat /1290a6e9/...`
- 误写 `repo-grep` 参数
- 误尝试 `sed`

但它最终还是收敛到了正确路径，并成功读到了：

- `SendTeamMessages()`
- `resolveTeamMessageTargets()`
- `isEligibleTeamMessageTarget()`
- `SaveTeamRunState()`
- `UpdateTeamRunState()`

#### 它收到的消息

在运行中，它被注入了一条来自 `researcher-risks` 的 team message：

- 风险点同步：消息投递依赖目标角色 `running`、`team-messages.md` 可能有并发风险、跨 stage 通信受限

这是 `InjectMessage()` 发挥作用的真实证据：对方发消息后，当前角色不需要手工轮询文件，就在运行流里看到了注入消息。

#### 它回复了什么

它给 `researcher-risks` 回复了两条消息：

1. 简短确认：`isEligibleTeamMessageTarget` 要求目标状态是 `running`
2. 完整确认：
   - 必须同 stage
   - `pending/done` 角色不能接收
   - `RunID` 为空不会真正注入
   - `team-messages.md` 追加写与 state 原子落盘不同步

#### 它的产出作用

它写了 `data/topics/1290a6e9/research-codebase.md`，核心是“代码证据层”的说明：

- team 消息是怎么发的
- stage 是怎么推进的
- 状态如何持久化
- 为什么 researcher 能互相沟通

你可以把它理解成“技术实现研究员”。

---

### 4.3 `researcher-risks`：研究失败路径和边界

来源记录：`data/runs/ef87c0d5/output`

#### 它的输入

它先读：

- `task.md`
- `manifest.md`
- `plan.md`

然后读取：

- `team status`
- `team messages 10`
- `cmd/agent/teams_runtime.go`
- `internal/team_tools.go`
- `internal/teams.go`

#### 它的策略

和 codebase researcher 不同，它不是去梳理全貌，而是专盯失败风险：

- 消息在什么时序下可能投递失败
- 并发写 `team-messages.md` 是否安全
- 跨 stage 限制是否会影响协作
- 一个角色失败后整 stage 会怎样

#### 它先主动发起对话

它在 stage 1 的早期先发送了第一条 team message 给 `researcher-codebase`：

- 请对方确认三类风险点是否有遗漏

这一点很关键：

- 它不是等对方找自己
- 而是主动把“风险假设”抛给另一个研究员去验证

这正是 `teams` 的预期协作模式：

- 并行研究
- 中途互相校验
- 再各自写工件

#### 它收到回复后做了什么

之后它收到 `researcher-codebase` 的两条确认消息，并再发送一条回执：

- 已收到确认
- R1/R2/R4 与对方代码分析一致
- 会在自己的工件中标注这些已确认项

#### 它的产出作用

它写了 `data/topics/1290a6e9/research-risks.md`，核心不是“系统怎么工作”，而是：

- 哪些地方容易出问题
- 哪些边界条件有防护
- 哪些地方本轮没测到
- implementer 和 tester 该重点关注什么

你可以把它理解成“故障与验证研究员”。

---

### 4.4 `implementer`：不改代码，只整合验证过程

来源记录：`data/runs/23bca712/output`

#### 它的输入

它按依赖读取：

- `plan.md`
- `research-codebase.md`
- `research-risks.md`
- `team messages 20`
- `team status`

此时 stage 1 已全部完成，所以它拿到的是两个 researcher 已收敛后的结果，而不是中间态。

#### 它实际做的事情

它没有改代码，而是做“实施汇总”：

- 把 4 条 team 消息整理成时间线
- 用 `team status` 验证 stage 顺序
- 把 codebase 结论与 risk 结论拼起来
- 检查这轮是否发生了 `pending/queued` 残留或 stage 阻塞

#### 它的产出作用

它写了 `data/topics/1290a6e9/implementation.md`。

这个文件相当于“项目经理/执行汇总”的角色：

- 不重新研究代码
- 不亲自跑边界测试
- 而是基于研究员结论，汇总本轮实际执行情况

这说明 `implementer` 在该默认 team 中，并不一定等于“改代码的人”，而是“主交付整合者”。

---

### 4.5 `tester`：站在最后一棒做验收

来源记录：`data/runs/db620c08/output`

#### 它的输入

它读取：

- `implementation.md`
- `task.md`
- `manifest.md`
- `team-messages.md`
- `team status`
- `team messages 20`
- `research-codebase.md`
- `research-risks.md`
- `plan.md`

#### 它的验证方式

tester 并没有重新深入代码，而是做“验收校对”：

1. 检查 team 是否按 stage 顺序推进
2. 检查 researcher 是否确实双向通信
3. 检查消息是否都落在 `team-messages.md`
4. 检查上游工件是否齐全
5. 检查最终报告结构是否满足原任务要求

#### 它的小插曲

它一开始尝试直接把 Markdown 内容拼进 `write` 命令里，导致 `unknown command: ##` 报错；随后改为：

- `write /1290a6e9/test-report.md` + `stdin`

这说明 team 角色虽然是独立 agent，但仍严格受限于当前工具模型，不能随意像 shell 一样执行 heredoc。

#### 它的产出作用

tester 最终写出 `data/topics/1290a6e9/test-report.md`，把这轮 team 测试定性为“通过”。

在协作链上，tester 的作用是：

- 不生产新事实
- 而是验证“上游所有环节是否形成闭环”

---

## 5. 真实对话时间线

本轮 researcher 并行阶段的真实 team 对话如下，来自 `data/topics/1290a6e9/team-messages.md`：

1. `15:12:36` `researcher-risks -> researcher-codebase`
   - 同步 3 个风险点：状态依赖、并发写、跨 stage 限制
2. `15:13:03` `researcher-codebase -> researcher-risks`
   - 简短确认：目标必须是 `running`
3. `15:13:07` `researcher-codebase -> researcher-risks`
   - 补充更完整的边界实现
4. `15:13:12` `researcher-risks -> researcher-codebase`
   - 确认已收到并将写入风险工件

这个对话很像一个小型工程团队：

- 风险研究员先抛问题
- 代码研究员给出实现证据
- 风险研究员确认并收敛

它不是“闲聊”，而是围绕共享任务的结构化协作。

---

## 6. 为什么这个流程能跑通

### 6.1 因为角色之间不是靠上下文硬记，而是靠工件与状态机协作

team 不是把所有角色混在一个上下文里，而是：

- 每个角色有自己的 topic 和 run
- 共享文件放到 coordinator topic
- team run state 单独落盘

这样好处是：

- 阶段边界清晰
- 可恢复、可观察
- 可以单独看某个角色的输出

### 6.2 因为 stage 是硬调度，不是“靠模型自觉”

`runTeam()` 会：

- 先启动本 stage 全部角色
- 然后等待它们都完成
- 有错误则整 stage 失败
- 全部通过后才进下一 stage

所以 implementer 不可能抢先开始，因为调度器根本不会启动它。

### 6.3 因为消息限制在并行 stage，避免污染后续阶段

只允许同 stage 的 `running` 角色互发消息，带来的效果是：

- researcher 之间可以同步
- 但不会提前影响 implementer/tester
- 下游只读最终工件，不读并行阶段的噪声中间态

这是一个很像流水线的设计。

### 6.4 因为状态持久化是文件化的，便于复盘

本轮你能回看全过程，正是因为几乎所有关键对象都落盘了：

- team run state：`data/teams/runs/520541fe.json`
- 角色输出：`data/topics/1290a6e9/*.md`
- 通信日志：`data/topics/1290a6e9/team-messages.md`
- 每个角色的异步输出：`data/runs/<run-id>/output`

这对调试多 agent 协作非常重要。

---

## 7. 这套协作模型的优点和代价

### 优点

1. 角色职责清晰
2. 并行 researcher 能缩短探索时间
3. 下游只消费收敛后的工件
4. 所有关键过程都可审计
5. 某个角色失败时，问题定位比较直接

### 代价

1. 工具使用稍复杂，角色必须理解工件路径与仓库路径的区别
2. 即时通信能力被故意限制，跨 stage 不能直接 message
3. 高并发和极端时序仍可能有边界问题
4. 如果角色提示不够强，模型会误用工具或做多余探索

这也是为什么 `handoff.md` 里专门强调：

- 要限制探索轮次
- 要区分 `repo-*` 和 team artifact 路径
- 要用 `write /<topic-id>/<file>` 写工件

---

## 8. 用一句话总结 `teams` 是怎么协作的

一句话说，`agent teams` 的协作方式是：

**用依赖关系把角色分成 stage；同 stage 角色通过 team message 做轻量同步；跨 stage 角色通过共享 artifact 接力；总调度器负责启动、等待、落盘和收敛。**

---

## 9. 结合这轮 run 的最终理解

基于 run `520541fe`，可以把 5 个角色理解成一个最小工程小组：

- `planner`：把模糊需求拆成执行协议
- `researcher-codebase`：查实现细节
- `researcher-risks`：找失败路径和验证点
- `implementer`：汇总执行结果，形成主交付
- `tester`：最后验收并形成结论

其中最关键的协作接口只有两个：

1. `team message`：用于同 stage 运行中的即时交流
2. `artifact`：用于跨 stage 的正式交接

这也是你理解整个 `agent teams` 设计时，最值得抓住的主线。
