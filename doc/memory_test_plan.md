# cli-agentx 记忆系统测试计划

这份计划专门用于测试 `cli-agentx` 当前这套记忆系统，而不是只做普通功能冒烟。

重点验证的不是“能不能回答”，而是：
- 记忆写到哪里
- 记忆如何分层
- 召回为什么命中
- lexical fallback 是否真的在起作用
- `memory compact` 是否体现了记忆生命周期管理

---

## 1. 测试目标

验证以下能力是否真实存在并可被观察到：

1. **分层记忆结构**
   - `L0/L1/L2` 是否体现在本地 memory 文件中
   - `P0/P1/P2` 是否体现在稳定事实、摘要总览、热状态中

2. **双通道记忆存储**
   - 数据库记忆（`facts` / `summaries` / FTS）是否正常工作
   - 本地文件记忆（`data/memory/`）是否同步更新

3. **综合召回机制**
   - recall 是否不再只依赖 embedding
   - 是否能结合 semantic、FTS、lexical、本地文件搜索进行召回

4. **lexical 匹配能力**
   - 对于明显的词面查询，是否可以在 embedding 不理想时仍然命中
   - 是否能从不同层级文件中找回结果

5. **记忆生命周期管理**
   - `memory compact [days]` 是否能归档旧的 run note
   - 是否开始形成“热记忆 vs 归档记忆”的区别

6. **日志可验证性**
   - `logs/` 中是否能证明 recall 注入、工具调用和真实执行过程

---

## 2. 测试范围

本次测试覆盖：
- 启动 `cli-agentx`
- 进行多轮简单对话
- 触发稳定事实写入、近期状态更新、细节记录
- 触发 `memory search`、`memory recent`、`memory compact`
- 检查 `data/memory/`、数据库结果侧证、`logs/` 文件

本次测试不覆盖：
- 更换模型后的横向对比
- 大规模压力测试
- archive 之后的长期检索策略优化
- 文本形式假工具调用的自动恢复实现（目前仍是已知问题）

---

## 3. 测试前提

执行测试前，应确认：

1. `cli-agentx` 可正常构建运行
2. `cli-agentx/data/config.yaml` 中已有可用模型配置
3. 当前环境允许真实调用 LLM
4. 当前仓库已有 `cli-agentx/data/memory/` 目录，或程序可自动生成
5. 若要验证 `memory compact` 的真实归档效果，最好已有多个不同日期的 `runs/YYYY-MM-DD/` 目录

---

## 4. 需要重点观察的文件

### 本地 memory 文件

- `cli-agentx/data/memory/.abstract`
- `cli-agentx/data/memory/MEMORY.md`
- `cli-agentx/data/memory/SESSION-STATE.md`
- `cli-agentx/data/memory/insights/.abstract`
- `cli-agentx/data/memory/insights/YYYY-MM.md`
- `cli-agentx/data/memory/lessons/.abstract`
- `cli-agentx/data/memory/lessons/operational-lessons.jsonl`
- `cli-agentx/data/memory/runs/YYYY-MM-DD/*.md`
- `cli-agentx/data/memory/archive/runs/YYYY-MM-DD/*.md`

### 日志文件

- `cli-agentx/logs/{topicID}/*.json`

### 数据库侧证

- `facts` 表
- `summaries` 表
- `summaries_fts`

---

## 5. 核心概念与预期映射

### `L0/L1/L2`

- `L0`：导航层
  - 典型文件：`.abstract`
  - 作用：告诉系统有哪些记忆区块可读

- `L1`：浓缩层
  - 典型文件：`insights/YYYY-MM.md`
  - 作用：提供月度级别浓缩摘要

- `L2`：细节层
  - 典型文件：`runs/YYYY-MM-DD/*.md`
  - 作用：保留更原始的 run 细节与证据

### `P0/P1/P2`

- `P0`：稳定事实
  - 典型来源：`facts` 表、`lessons/operational-lessons.jsonl`

- `P1`：提炼上下文
  - 典型来源：`MEMORY.md`

- `P2`：热工作区
  - 典型来源：`SESSION-STATE.md`

测试过程中，应观察这些概念是否真的能在文件、命令结果和日志中对应起来。

---

## 6. 测试阶段

## 阶段 A：基线快照

### 目的

在测试开始前记录当前状态，方便后面对比。

### 操作

记录以下内容：
- `data/memory/` 当前文件列表
- `data/memory/runs/` 当前日期目录
- `data/memory/archive/runs/` 当前日期目录
- `logs/` 当前文件列表
- `MEMORY.md`、`SESSION-STATE.md`、当月 `insights` 的内容快照

### 预期

- 能明确看到测试前已有或不存在的 memory 文件
- 为后续判断“哪些文件被刷新或新增”提供依据

---

## 阶段 B：P0 稳定事实测试

### 目的

验证显式记忆写入是否进入稳定事实层。

### 建议对话

- “请记住：我叫李航，我的职业是分布式系统工程师，我喜欢乌龙茶。”

### 观察点

1. 程序回答是否正常
2. 是否触发真实工具调用 `memory store`
3. `facts` 表是否新增记录
4. `lessons/operational-lessons.jsonl` 是否更新
5. `MEMORY.md` 的稳定事实部分是否更新
6. 对应 `logs/...json` 中是否存在 `tool_calls`

### 预期

- 该信息应更偏向 `P0` 稳定事实
- 即使后续对话很多，这条信息也应属于长期保留内容

### 风险记录

- 如果模型把 `run(command="memory store ...")` 作为纯文本输出，而不是正式工具调用，应记录为“模型工具调用问题”，不是记忆存储逻辑本身失效

---

## 阶段 C：P2 热状态测试

### 目的

验证近期状态是否进入热工作区。

### 建议对话

连续发送 2～3 轮：
- “我今天在排查 Redis 延迟尖刺问题。”
- “刚刚发现主要原因可能是连接池配置太小。”
- “请总结一下我最近在处理什么问题。”

### 观察点

1. `SESSION-STATE.md` 是否刷新
2. `memory recent` 是否优先展示近期状态
3. 最近 summaries 是否能反映这些新内容
4. 对应 run note 是否生成到 `runs/YYYY-MM-DD/*.md`

### 预期

- 这些内容应更偏向 `P2`
- `SESSION-STATE.md` 中应能看到近期问题与状态

---

## 阶段 D：P1 提炼上下文测试

### 目的

验证 `MEMORY.md` 是否承担长期提炼视图，而不是简单复制原始对话。

### 操作

在完成若干轮对话后，检查：
- `cli-agentx/data/memory/MEMORY.md`

### 观察点

1. 是否存在 `P0 Stable Facts`
2. 是否存在 `P1 Distilled Context`
3. 是否存在 `P2 Working Buffer` 的阅读指引
4. 内容是否为“提炼视图”，而非大段原始对话复制

### 预期

- `MEMORY.md` 像一个总览入口
- 既能看到长期事实，也能看到近期摘要入口

---

## 阶段 E：L2 细节层测试

### 目的

验证原始 run 细节是否落到 `L2` 层。

### 建议对话

- “请记住这个排障细节：P99 从 220ms 降到 18ms，关键改动是把连接池从 16 调到 64，并开启批量 pipeline。”

### 观察点

1. 对应 run note 是否生成
2. 文件路径是否在 `runs/YYYY-MM-DD/*.md`
3. 文件内容中是否包含：
   - topic
   - run
   - time
   - Summary
   - User Intent
4. 后续搜索“连接池 64”“P99”“pipeline”时能否命中这类细节

### 预期

- 这类具体参数和细节更容易落到 `L2`
- 它们应能作为后续检索的详细证据来源

---

## 阶段 F：L1 浓缩层测试

### 目的

验证月度浓缩层是否存在并参与记忆体系。

### 操作

完成多轮对话后检查：
- `cli-agentx/data/memory/insights/YYYY-MM.md`

### 观察点

1. 文件是否存在
2. 是否包含最近摘要的月度浓缩结果
3. 内容是否明显比 `runs/*.md` 更短、更概括

### 预期

- 它应更像月度 condensed view，而不是原始明细

---

## 阶段 G：L0 导航层测试

### 目的

验证 `.abstract` 文件是否承担导航角色。

### 操作

检查：
- `cli-agentx/data/memory/.abstract`
- `cli-agentx/data/memory/insights/.abstract`
- `cli-agentx/data/memory/lessons/.abstract`

然后执行若干查询，例如：
- `memory search 最近记忆总览`
- `memory search 月度 insight`
- `memory search lessons`

### 观察点

1. `.abstract` 文件是否存在并描述各目录作用
2. 搜索导航类关键词时，是否可能命中 `L0` / `L1`
3. 结果是否不再只来自 DB

### 预期

- 导航类查询应有机会命中 `.abstract` 或浓缩文件
- 能体现“本地文件记忆不仅仅是展示用，还参与 recall”

---

## 阶段 H：lexical 匹配专项测试

### 目的

验证 lexical fallback 与本地文件匹配是否真的生效。

### 建议查询设计

#### A. 第一人称问句
- “我叫什么名字？”
- “我是做什么工作的？”

#### B. 明确关键词
- “乌龙茶”
- “连接池 64”
- “pipeline”

#### C. 接近自然语言的问法
- “我最近优化延迟时用了什么办法？”
- “我喜欢喝什么？”

### 观察点

1. `memory search <query>` 是否有结果
2. 返回结果的 `Layer` 是否合理，例如：
   - `P0`
   - `P1`
   - `P2`
   - `L0`
   - `L1`
   - `L2`
   - `DB`
   - `DB-lexical`
3. 对明显词面匹配强的查询，是否能稳定召回
4. 结果来源是否同时覆盖本地文件与 DB

### 预期

- 在 semantic 不是特别强时，lexical fallback 仍然应能命中明显词面信息
- “乌龙茶”“连接池 64”这类查询应有较高命中概率
- 结果中应能观察到不同层的来源差异

---

## 阶段 I：自动 recall 综合测试

### 目的

验证 `buildRecall()` 当前使用的是综合召回，而不是 embedding-only。

### 操作

新开一个 topic，然后提问：
- “我叫什么名字？”
- “我最近在处理什么技术问题？”
- “我喜欢喝什么？”

### 观察点

1. `logs/...json` 的 request 中是否出现 `<recall>`
2. `<recall>` 中是否包含 memory layer 标签
3. recall 内容是否覆盖：
   - 稳定事实
   - 近期状态
   - 细节参数
4. 即使问题是第一人称问句，是否也能有一定召回结果

### 预期

- `<recall>` 应该出现在请求中
- recall 不应只来自 semantic 命中
- 应能体现综合召回效果

---

## 阶段 J：`memory search` 分层观察测试

### 目的

验证不同问题会命中不同层级。

### 建议查询

- `memory search 名字`
- `memory search 乌龙茶`
- `memory search 连接池`
- `memory search 月度总结`
- `memory search 最近记忆总览`

### 观察点

记录每次结果中的：
- `Layer`
- `Source`
- 命中文本
- 结果排序

### 预期

- 稳定偏好类信息更容易命中 `P0 / P1`
- 最近状态类信息更容易命中 `P2`
- 参数细节类信息更容易命中 `L2`
- 导航或总览类查询更容易命中 `L0 / L1`

---

## 阶段 K：`memory compact` 生命周期测试

### 目的

验证记忆系统开始区分“热区”和“归档区”。

### 前提

- `data/memory/runs/` 下最好已有多个不同日期目录
- 至少有一部分目录日期早于保留窗口

### 操作

执行：
- `memory compact`
- 或 `memory compact 7`

### 观察点

1. 是否触发真实工具调用
2. 工具执行返回是：
   - `archived N run note(s)...`
   - 或 `memory already compact...`
3. `data/memory/runs/` 目录数量是否减少
4. `data/memory/archive/runs/` 是否增加对应旧目录
5. `MEMORY.md`、`SESSION-STATE.md`、`lessons`、`insights` 是否仍保留并可读
6. `memory search`、`memory recent` 是否仍能正常工作

### 预期

- 热区保留最近若干天 run note
- 旧 run note 被挪入归档区
- 系统开始体现轻量记忆生命周期管理

### 备注

- 当前实现只归档旧的 `L2` 原始 run note
- 还不会做更深度的 archive digest 或 dry-run

---

## 阶段 L：日志验证

### 目的

确保所有测试结论都有日志证据，而不是只看表面回答。

### 每个关键步骤都应检查

1. `logs/{topicID}/*.json` 是否新增
2. request 中是否包含 `<recall>`
3. response 中是否包含 `tool_calls`
4. 若执行 `memory store` / `memory compact`，是否真的由 tool call 触发
5. 响应文字是否与工具执行结果一致

### 重点区分

- **真实执行**：日志中有 `tool_calls`，并有后续 tool result
- **口头执行**：模型只输出 `run(command="...")` 纯文本，但没有实际工具调用

---

## 7. 建议测试对话样例

### 样例 1：稳定事实
- “请记住：我叫李航，是分布式系统工程师，我喜欢乌龙茶。”

### 样例 2：近期状态
- “我今天在排查 Redis 延迟尖刺问题。”
- “刚刚发现主要原因可能是连接池配置太小。”

### 样例 3：细节参数
- “请记住这个排障细节：P99 从 220ms 降到 18ms，关键改动是连接池从 16 调到 64，并开启批量 pipeline。”

### 样例 4：自然 recall 问句
- “我叫什么名字？”
- “我最近在处理什么技术问题？”
- “我喜欢喝什么？”

### 样例 5：关键词查询
- `memory search 乌龙茶`
- `memory search 连接池 64`
- `memory search pipeline`

### 样例 6：分层观察查询
- `memory search 月度总结`
- `memory search 最近记忆总览`
- `memory search lessons`

### 样例 7：生命周期命令
- “请执行 `memory compact 7`，并告诉我结果。”

---

## 8. 测试结果记录模板

每个测试步骤建议记录：

- 测试编号
- 输入内容
- 模型回答
- 是否出现真实 `tool_calls`
- 对应日志文件路径
- 对应 memory 文件变化
- 命中层级（如适用）
- 结论：通过 / 部分通过 / 失败
- 备注：如果失败，是功能问题还是模型工具调用问题

---

## 9. 通过标准

### 通过

满足以下大部分条件：
- 本地 memory 文件按预期刷新
- `P0/P1/P2` 和 `L0/L1/L2` 能从结果中观察出来
- lexical 查询能命中明显的词面信息
- 自动 recall 能在日志中看到 `<recall>` 注入
- `memory compact` 能运行，并产生合理的文件侧变化或无变化说明
- 日志可证明工具真实执行

### 部分通过

- 记忆文件与 recall 机制正常
- 但模型偶发不触发正式 tool call，导致个别用例受阻

### 失败

出现以下任一情况：
- 只有回答，没有底层文件或日志证据
- 各层 memory 文件没有明显更新
- lexical 查询完全不能工作
- `memory compact` 无法执行或文件变化明显不符合预期

---

## 10. 当前已知风险

1. 模型可能把 `run(command="...")` 输出成纯文本，而不是正式工具调用
2. 若缺少旧日期的 `runs/` 目录，`memory compact` 只能验证“命令能跑”，不能验证“真实归档行为”
3. semantic recall 仍可能受模型 embedding 表现波动影响
4. 当前 topic 过滤对本地文件命中仍较弱，测试时要注意区分“召回成功”与“过滤精度一般”

---

## 11. 最终测试产出

测试完成后，建议输出两份结果：

1. **测试记录**
   - 每一步输入、输出、日志、文件变化、层级观察结果

2. **总结报告**
   - 哪些能力通过
   - 哪些能力部分通过
   - 哪些问题是实现问题
   - 哪些问题是模型工具调用问题
   - 下一步最值得改进的点

---

## 12. 一句话总结

这份计划的核心不是验证“AI 有没有记住”，而是验证：

**`cli-agentx` 是否已经具备一套可观察、可解释、可分层、可压缩的本地记忆系统。**
