# Agent Teams 真实回归测试报告（2026-03-31）

## 1. 测试目标

根据 `handoff.md` 的交接说明，本次测试重点验证 `agent teams` 是否已经满足以下预期：

1. 默认 `default` team 能完整跑通 `planner -> researcher-codebase / researcher-risks -> implementer -> tester`。
2. 并行 stage 中的 team 消息通信链路可正常工作。
3. 各角色能正确产出共享工件，并被下游角色消费。
4. 最终 `tester` 能生成结构完整的 `test-report.md`。
5. 本轮重点观察此前未闭环的 `researcher-risks` 是否仍会卡死、超轮次或导致整轮失败。

## 2. 测试前置

- 仓库路径：`/Users/mayegg/cursor/cli-agentx`
- 参考交接文档：`cli-agentx/handoff.md`
- 编译命令：`go build -o agent ./cmd/agent`
- Team 模板：`seed/teams/default/team.yaml`
- 运行方式：`process`

编译结果：通过。

## 3. 测试输入

实际执行命令：

```bash
./agent teams run default -p '请对当前 cli-agentx 仓库的 agent teams 功能做一次真实回归测试。要求：1）planner 先制定测试计划；2）researcher-codebase 与 researcher-risks 并行执行，并且二者之间至少互发 1 条 team 消息；3）implementer 不修改仓库代码，只汇总验证过程与结果，写 implementation.md；4）tester 必须读取前序工件与 team-messages.md，产出 test-report.md，结构至少包含：测试目标、测试对话摘要、通过项、失败项、风险项、未验证项、最终结论；5）若发现不符合预期，明确写出具体原因。整个过程使用真实 team artifacts 与 team messages 完成。'
```

## 4. 实际运行结果

- Team Run ID：`520541fe`
- Coordinator Topic：`1290a6e9`
- 最终状态：`done`
- 当前 stage（结束时）：`3`

状态文件：`data/teams/runs/520541fe.json`

各角色结果：

- `planner`：`done`，topic `84ee438e`，run `62b817bf`
- `researcher-codebase`：`done`，topic `23a4e90f`，run `e4d5c313`
- `researcher-risks`：`done`，topic `6669951e`，run `ef87c0d5`
- `implementer`：`done`，topic `eae7601c`，run `23bca712`
- `tester`：`done`，topic `8b35aca4`，run `db620c08`

这说明交接文档里提到的未闭环问题，在本轮真实回归中没有复现。

## 5. 测试对话与通信验证

本次并行 researcher 阶段发生了 4 条有效 team 消息，均已成功投递并持久化：

消息文件：`data/topics/1290a6e9/team-messages.md`

对话摘要：

1. `researcher-risks -> researcher-codebase`
   - 同步风险点：目标角色必须 `running`、`team-messages.md` 可能有并发写入风险、跨 stage 通信受限。
2. `researcher-codebase -> researcher-risks`
   - 第一次回复，确认目标角色状态限制。
3. `researcher-codebase -> researcher-risks`
   - 第二次回复，补充完整边界条件：`pending/done` 角色不能接收、`RunID` 为空仅会 queued、日志追加与状态落盘不同步。
4. `researcher-risks -> researcher-codebase`
   - 确认已收到验证结果，并说明会写入 `research-risks.md`。

验证结论：

- researcher 间双向通信正常。
- 消息状态均为 `delivered`。
- 消息不仅出现在 team run state，也落到了 `team-messages.md`。
- 通信发生在并行 stage 内，符合默认 team 的设计约束。

## 6. 工件验证

本轮团队成功产出以下共享工件：

- `data/topics/1290a6e9/plan.md`
- `data/topics/1290a6e9/research-codebase.md`
- `data/topics/1290a6e9/research-risks.md`
- `data/topics/1290a6e9/implementation.md`
- `data/topics/1290a6e9/test-report.md`
- `data/topics/1290a6e9/team-messages.md`

其中，`tester` 的最终报告已经按要求生成，且内容包含：

- 测试目标
- 测试对话摘要
- 通过项
- 失败项
- 风险项
- 未验证项
- 最终结论

对应文件：`data/topics/1290a6e9/test-report.md`

## 7. 与预期比对

### 通过项

1. `default` team 全链路跑通。
2. `researcher-risks` 本轮成功完成，没有再次出现整轮阻塞。
3. stage 顺序正确：先 `planner`，再两个 researcher 并行，然后 `implementer`，最后 `tester`。
4. team 消息功能正常，且实现了真实对话往返。
5. 下游角色成功消费上游工件。
6. 最终测试报告文件由 team 内 `tester` 角色自动生成。

### 未发现的问题

- 未出现 `agentic loop exceeded 20 iterations`。
- 未出现 run 已完成但 role state 未收敛导致的整轮报错。
- 未出现 `researcher-risks` 卡住、异常退出或导致 stage 2 无法启动。

### 仍需注意的风险

虽然本轮通过，但 team 自身产出的研究结果仍指出以下潜在风险尚未被压力验证：

1. 消息投递依赖目标角色处于 `running` 状态，极端时序下可能出现 queued/delivered 差异。
2. `team-messages.md` 的追加写在高并发下可能存在竞争窗口。
3. 跨 stage 的协作仍主要依赖 artifact，而不是消息。
4. 更极端的异常路径和压力场景尚未覆盖。

## 8. 最终结论

本次真实回归测试结果为：**通过**。

结论如下：

1. `agent teams` 在当前代码状态下已经可以完整跑通默认 team 模板。
2. 交接文档中提到的核心能力——源码读取、并行 researcher 协作、team 消息通信、stage 推进、最终 tester 收尾——本轮均符合预期。
3. 此前重点担心的 `researcher-risks` 未闭环问题，在本轮未复现。
4. 当前实现已经满足“可用且行为基本正确”的目标，但仍建议后续补充高并发和异常时序专项测试。

## 9. 关键证据文件

- 交接文档：`handoff.md`
- Team 运行状态：`data/teams/runs/520541fe.json`
- Team 通信记录：`data/topics/1290a6e9/team-messages.md`
- Team 实施报告：`data/topics/1290a6e9/implementation.md`
- Team 最终测试报告：`data/topics/1290a6e9/test-report.md`
