# cli-agentx 记忆系统测试报告

**测试时间**：2026-03-19 20:27–20:31 CST  
**测试环境**：本地 CLI + DashScope `qwen3.5-plus`  
**测试对象**：`cli-agentx` 新版记忆系统（本地 memory store / layered recall / `memory compact`）

---

## 1. 测试说明

本次测试按 `doc/memory_test_plan.md` 执行，重点不是单纯看“AI 有没有回答”，而是验证：

- 记忆是否同时写入 DB 和本地文件
- `L0/L1/L2`、`P0/P1/P2` 是否能从结果中体现
- `memory search` 是否能返回分层命中
- lexical fallback 是否真的在起作用
- `memory compact 7` 是否真的归档旧 run note
- `logs/` 中是否有 tool call 与 recall 注入证据

**重要说明**：测试开始后发现仓库里的 `agent` 二进制是旧版本，所以先重新构建了最新二进制，再用新版本重跑关键用例。旧二进制产生的结果不作为最终结论依据。

---

## 2. 测试话题

### 2.1 主测试话题

- topic id：`ae7109ba`
- topic name：`memory-plan-test-20260319`

### 2.2 recall 验证话题

- topic id：`394b4dc5`
- topic name：`memory-recall-check-20260319`

---

## 3. 基线状态

测试前看到的 memory 结构：

- `data/memory/.abstract`
- `data/memory/MEMORY.md`
- `data/memory/SESSION-STATE.md`
- `data/memory/insights/.abstract`
- `data/memory/insights/2026-03.md`
- `data/memory/lessons/.abstract`
- `data/memory/lessons/operational-lessons.jsonl`
- `data/memory/runs/2026-03-19/`
- `data/memory/archive/`

说明本地 memory store 已存在并可供后续对比。

---

## 4. 测试执行记录

## 4.1 P0 稳定事实写入

### 输入

在 topic `ae7109ba` 中发送：

> 请记住：我叫李航，我的职业是分布式系统工程师，我喜欢乌龙茶。

### 结果

模型触发了真实工具调用：

- `run({"command": "memory store 用户叫李航，职业是分布式系统工程师，喜欢乌龙茶"})`
- 工具返回：`fact stored`

最终回复：

> 已记住：李航，分布式系统工程师，喜欢乌龙茶。

### 证据

- 日志请求/工具调用：`cli-agentx/logs/ae7109ba/fc5fb7b5_call_001_20260319-202729.json`
- 日志工具结果/最终回复：`cli-agentx/logs/ae7109ba/fc5fb7b5_call_002_20260319-202730.json`
- 稳定事实文件：`cli-agentx/data/memory/lessons/operational-lessons.jsonl`
- 长期总览：`cli-agentx/data/memory/MEMORY.md`

### 判定

**PASS**

### 备注

这一步证明：
- `P0` 稳定事实写入有效
- DB 与本地 memory store 同步有效
- `memory store` 工具调用链真实执行

---

## 4.2 P2 热状态与 L2 细节生成

### 输入

在 topic `ae7109ba` 中连续发送：

1. 我今天在排查 Redis 延迟尖刺问题。  
2. 刚刚发现主要原因可能是连接池配置太小。  
3. 请记住这个排障细节：P99 从 220ms 降到 18ms，关键改动是把连接池从 16 调到 64，并开启批量 pipeline。  
4. 请总结一下我最近在处理什么问题。

### 结果

出现了以下行为：

- 第 1 条触发了 `daily-journal` skill，并创建/更新了日记文件
- 第 3 条触发了 `memory store`，把 Redis 优化细节存为长期事实
- 多次 run 结束后，新的 run note 被写入 `data/memory/runs/YYYY-MM-DD/*.md`

### 证据

- 稳定事实：`cli-agentx/data/memory/MEMORY.md`
- 热状态：`cli-agentx/data/memory/SESSION-STATE.md`
- 细节 run note 示例：`cli-agentx/data/memory/runs/2026-03-19/203023_ae7109ba_376fcd20.md`
- facts 导出：`cli-agentx/data/memory/lessons/operational-lessons.jsonl`

### 观察

`MEMORY.md` 已出现：

- `P0 Stable Facts`
  - `Redis 延迟优化：连接池 16→64 + pipeline 批量，P99 从 220ms 降至 18ms`
  - `用户叫李航，职业是分布式系统工程师，喜欢乌龙茶`

`SESSION-STATE.md` 已出现本轮 03-19 的多条近期记录，说明 `P2` 有刷新。

`runs/2026-03-19/*.md` 中存在包含 `Summary` 和 `User Intent` 的 run note，说明 `L2` 已生成。

### 判定

**PASS**

### 备注

这一步证明：
- `P0` 稳定事实
- `P2` 热状态
- `L2` 原始 run note

三者都已在文件层面可观察。

---

## 4.3 `memory search 乌龙茶`：lexical / P0-P1-P2 命中

### 输入

在 topic `ae7109ba` 中发送：

> 请执行 memory search 乌龙茶，并原样展示结果。

### 新版二进制下的结果

工具返回：

```text
Found 1 memory hits:
  [03-19 20:28] DB-lexical db:topic=ae7109ba run=fc5fb7b5
    用户李航意图让系统记录其姓名、职业（分布式系统工程师）及喜好（乌龙茶）。系统执行 `memory store` 命令将该信息存入长期记忆。最终信息成功保存，系统确认已记住该内容。
```

之后在压缩测试后再次检索，返回变为：

```text
Found 5 memory hits:
  [03-19 20:31] P2 memory/SESSION-STATE.md
  [03-19 20:31] P2 memory/SESSION-STATE.md
  [03-19 20:31] P2 memory/SESSION-STATE.md
  [03-19 20:31] P1 memory/MEMORY.md
  [03-19 20:31] P1 memory/MEMORY.md
```

### 证据

- 运行日志：`cli-agentx/logs/ae7109ba/376fcd20_call_001_20260319-202923.json`
- L2 run note：`cli-agentx/data/memory/runs/2026-03-19/203023_ae7109ba_376fcd20.md`
- 最终文件态：`cli-agentx/data/memory/MEMORY.md`
- 最终文件态：`cli-agentx/data/memory/SESSION-STATE.md`

### 判定

**PASS**

### 说明

这一步能明显看出 lexical 和 layered memory 的特点：

- 首次命中表现为 `DB-lexical`
- 随着更多总结写入后，检索开始更多命中 `P2` 和 `P1`
- “乌龙茶”这种词面很强的查询可以不依赖 embedding 也命中

---

## 4.4 `memory search 连接池 64`：分层命中验证

### 输入

在 topic `ae7109ba` 中发送：

> 请执行 memory search 连接池 64，并原样展示结果。

### 结果

新版二进制下工具返回：

```text
Found 5 memory hits:
  [03-19 15:35] P2 memory/SESSION-STATE.md
  [03-19 15:35] P1 memory/MEMORY.md
  [03-19 15:35] L1 memory/insights/2026-03.md
  [03-19 20:29] DB db:topic=ae7109ba run=9435364b
  [03-19 15:35] P2 memory/SESSION-STATE.md
```

### 证据

- 请求和 tool call：`cli-agentx/logs/ae7109ba/14247b5f_call_001_20260319-202947.json`
- 工具输出：`cli-agentx/logs/ae7109ba/14247b5f_call_002_20260319-202957.json`

### 判定

**PARTIAL PASS**

### 说明

优点：
- 明确看到了 `P2 / P1 / L1 / DB` 的分层命中
- 说明 `memory search` 确实在查本地 layered memory + DB

问题：
- 返回结果并没有优先命中本次 Redis 优化事实本身
- 结果更偏向历史中“连接池”相关的旧摘要
- 存在明显的词面误召回/泛召回现象

这说明：
- 分层检索机制已生效
- 但 lexical 排序和 topic 过滤仍然比较粗糙

---

## 4.5 自动 recall：新话题问“我叫什么名字？”

### 输入

在新 topic `394b4dc5` 中发送：

> 我叫什么名字？

### 结果

模型直接回答：

> 李航。  
> 分布式系统工程师，喜欢乌龙茶。

### 关键证据

日志请求中明确出现了：

- `Known Facts`
- `<recall>`
- 且 `<recall>` 中带了 layer 标签，如 `[P2]`、`[P1]`、`[L1]`

相关日志：

- `cli-agentx/logs/394b4dc5/1d37a08e_call_001_20260319-203016.json`

### 判定

**PASS（但有风险）**

### 说明

通过点：
- 证明 `buildRecall()` 已经把 `<recall>` 注入到请求中
- 证明 recall 结果包含 layer 信息
- 说明不再是纯 embedding-only 路线

风险点：
- `<recall>` 中同时混入了“张伟”的历史信息
- 最终能答对，主要依赖 `Known Facts` 中的李航事实压过了 recall 噪音

这说明：
- 综合 recall 已生效
- 但跨 topic / 多身份场景仍可能有污染

---

## 4.6 `memory recent 5`：P2 工作缓冲验证

### 输入

在 topic `ae7109ba` 中发送：

> 请执行 memory recent 5，并原样展示结果。

### 结果

工具输出包含：

- `# Session State`
- 一段历史工作缓冲内容
- 以及之后追加的一批近期 summaries

### 判定

**PARTIAL PASS**

### 说明

优点：
- 命令可执行
- 明确展示了 `SESSION-STATE.md` 的内容
- 后面也追加了数据库里的近期 summaries

问题：
- 输出比“热工作缓冲”预期更长
- 并非严格只展示当前最热状态
- 读起来更像“热缓冲 + 一批 recent summaries 拼接结果”

这和当前实现一致，但也说明 `P2` 还不是特别“hot-only”。

---

## 4.7 `memory compact 7`：记忆生命周期验证

### 测试前置处理

为了验证真实归档行为，先人工放入了一个旧日期样本文件：

- `data/memory/runs/2026-03-01/090000_seed_old_run.md`

说明：这是为了制造“可归档样本”，不代表正常运行自动生成的真实旧 run。

### 输入

在 topic `ae7109ba` 中发送：

> 请执行 memory compact 7，并原样展示结果。

### 结果

工具返回：

```text
archived 1 run note(s) from 1 day folder(s); kept last 7 day(s) hot
```

### 文件侧变化

压缩前：
- `data/memory/runs/2026-03-01/090000_seed_old_run.md`
- `data/memory/runs/2026-03-19/...`

压缩后：
- `data/memory/runs/2026-03-01/` 消失
- `data/memory/archive/runs/2026-03-01/090000_seed_old_run.md` 出现
- `data/memory/runs/2026-03-19/` 保留

### 证据

- 请求/tool call：`cli-agentx/logs/ae7109ba/4fef2a26_call_001_20260319-203104.json`
- 工具结果/最终回复：`cli-agentx/logs/ae7109ba/4fef2a26_call_002_20260319-203106.json`
- 归档结果：`cli-agentx/data/memory/archive/runs/2026-03-01/090000_seed_old_run.md`

### 判定

**PASS**

### 说明

这一步证明：
- `memory compact 7` 已真正接入工具系统
- 它不只是“回答说做了”，而是真的移动了文件
- 现在已经开始形成“热区 runs / 归档区 archive/runs” 的生命周期区分

---

## 5. 分层与特性结论

## 5.1 `P0 / P1 / P2`

### `P0`

**PASS**

体现为：
- `facts` 导出到 `lessons/operational-lessons.jsonl`
- `MEMORY.md` 中 `P0 Stable Facts` 出现李航和 Redis 优化事实

### `P1`

**PASS**

体现为：
- `MEMORY.md` 中 `P1 Distilled Context` 持续写入摘要化上下文
- `memory search` 能命中 `P1 memory/MEMORY.md`

### `P2`

**PASS / PARTIAL**

体现为：
- `SESSION-STATE.md` 确实承载近期工作缓冲
- `memory search` 能命中 `P2 memory/SESSION-STATE.md`

不足：
- `memory recent` 输出仍偏长，不够“严格热区”

---

## 5.2 `L0 / L1 / L2`

### `L0`

**未充分验证**

原因：
- 本轮测试没有拿到一个明确的 `.abstract` 命中结果作为成功样本
- 只能确认 `.abstract` 文件存在，但没有得到足够强的命中证据

### `L1`

**PASS**

体现为：
- `memory search 连接池 64` 返回中出现 `L1 memory/insights/2026-03.md`

### `L2`

**PASS**

体现为：
- `data/memory/runs/2026-03-19/*.md` 持续生成
- run note 中包含 `Summary` 和 `User Intent`
- `memory compact` 归档的对象就是 `L2` 原始 run note

---

## 5.3 lexical 匹配

**PASS / PARTIAL**

通过点：
- “乌龙茶”这类词面很强的查询稳定命中
- 可以命中 `DB-lexical`，也可以命中 `P1/P2`

不足：
- “连接池 64” 的检索结果出现明显偏题召回
- 当前 lexical 排序对 topic 和语义意图的控制还比较弱

---

## 6. 发现的问题

## 问题 1：旧二进制未自动更新

开始测试时，仓库根下 `agent` 是旧构建产物，导致最开始的 `memory search` 仍然表现为旧输出格式。后续重新 `go build -o agent ./cmd/agent` 后问题解决。

**影响**：测试时必须确保二进制和源码同步。

---

## 问题 2：身份混淆 / 跨 topic recall 污染

在新 topic 的“我叫什么名字？”问题里，`<recall>` 中出现了“张伟”的历史信息，但最终回答靠 `Known Facts` 纠正成了李航。

**影响**：
- 多身份、多 topic 时 recall 可能带入不相关历史
- topic 过滤和身份隔离仍需要加强

---

## 问题 3：lexical 检索排序仍较粗糙

“连接池 64” 本应优先命中 Redis 优化细节，但实际优先命中了旧的 HTTP 服务优化摘要等泛相关内容。

**影响**：
- 分层检索已能工作
- 但排序与 topic 过滤还不够精准

---

## 问题 4：`memory recent` 还不够 hot-only

`memory recent 5` 虽然会展示工作缓冲，但输出仍偏长，更像“工作缓冲 + recent summaries”的组合结果。

**影响**：
- 作为“热状态查看器”仍有提升空间

---

## 7. 总体结论

### 通过项

- `memory store` 真实执行并写入长期事实
- DB 与本地 memory store 同步更新
- `P0 / P1 / P2` 基本都能在文件和输出中观察到
- `L1 / L2` 已有明确命中证据
- `memory search` 已能返回分层结果
- lexical fallback 对强词面查询有效
- 自动 `<recall>` 注入已生效，并携带 layer 标签
- `memory compact 7` 已真实归档旧 run note

### 部分通过项

- `L0` 本轮没有拿到足够强的命中样本
- `memory recent` 能用，但还不够“热缓冲化”
- `memory search` 的排序与 topic 过滤仍然偏粗糙

### 总评

**总体结果：PASS（带若干已知局限）**

可以确认当前 `cli-agentx` 已经不只是“数据库摘要 + embedding 检索”，而是具备了一套：

- **本地文件可观察**
- **分层结构可解释**
- **lexical fallback 可用**
- **原始记忆可归档**

的记忆系统。

但如果要继续提升可用性，下一步最值得做的是：

1. 强化 topic / 身份过滤  
2. 优化 lexical 排序  
3. 把 `memory recent` 收紧成真正的 hot-only buffer  
4. 补一轮专门针对 `L0 .abstract` 命中的测试和 UX 调整
